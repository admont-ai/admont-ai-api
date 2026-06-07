package requesthandler

import (
	"fmt"
	"net/http"
	"strings"
	"sync"

	"github.com/christianfischer/md-wiki-server/internal/llm"
	"github.com/christianfischer/md-wiki-server/internal/permissions"
	"github.com/christianfischer/md-wiki-server/internal/pg_vector/backend"
	"github.com/christianfischer/md-wiki-server/internal/repo"
	"github.com/christianfischer/md-wiki-server/internal/store/ai_conversation"
	"github.com/christianfischer/md-wiki-server/internal/store/git_repo"
	"github.com/gin-gonic/gin"
	"github.com/go-fuego/fuego"
	log "github.com/sirupsen/logrus"
)

type ragSource struct {
	RepoSlug string  `json:"repo"`
	FilePath string  `json:"file_path"`
	Chunk    string  `json:"chunk"`
	Score    float64 `json:"score"`
}

type ragRequest struct {
	Repos          []searchRepoRef  `json:"repos" validate:"required"`
	Query          string           `json:"query" validate:"required"`
	Context        string           `json:"context,omitempty"`
	Model          string           `json:"model,omitempty"`
	TopK           int              `json:"top_k,omitempty"`
	Threshold      float64          `json:"threshold,omitempty"`
	ConversationID string           `json:"conversation_id,omitempty"`
	History        []llm.ChatMessage `json:"history,omitempty"`
}

type ragResponse struct {
	Answer         string         `json:"answer"`
	Sources        []ragSource    `json:"sources"`
	Usage          llm.TokenUsage `json:"usage"`
	ConversationID string         `json:"conversation_id,omitempty"`
	MessageID      string         `json:"message_id,omitempty"`
}

type RAGRequesthandler struct {
	mu            sync.RWMutex
	llmClient     *llm.Client
	backend       *backend.Holder
	backends      map[string]repo.RepoBackend
	repoConfigs   map[string]*git_repo.GitRepo
	permResolvers map[string]*permissions.Resolver
	convStore     *ai_conversation.Store
	summarizer    *llm.Summarizer
}

func NewRAGRequesthandler(llmClient *llm.Client, b *backend.Holder, backends map[string]repo.RepoBackend, repoConfigs map[string]*git_repo.GitRepo, permResolvers map[string]*permissions.Resolver) *RAGRequesthandler {
	return &RAGRequesthandler{llmClient: llmClient, backend: b, backends: backends, repoConfigs: repoConfigs, permResolvers: permResolvers}
}

func (h *RAGRequesthandler) SetConversationStore(store *ai_conversation.Store, summarizer *llm.Summarizer) {
	h.convStore = store
	h.summarizer = summarizer
}

// SetClient replaces the LLM client at runtime.
func (h *RAGRequesthandler) SetClient(client *llm.Client) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.llmClient = client
}

func (h *RAGRequesthandler) getClient() *llm.Client {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.llmClient
}

func (h *RAGRequesthandler) RAG(c fuego.ContextWithBody[ragRequest]) (ragResponse, error) {
	client := h.getClient()
	if client == nil || !client.HasProviders() {
		return ragResponse{}, fuego.HTTPError{Status: http.StatusNotImplemented, Detail: "LLM not configured"}
	}

	gc := c.Context().(*gin.Context)

	body, err := c.Body()
	if err != nil {
		return ragResponse{}, err
	}

	if body.Query == "" {
		return ragResponse{}, fuego.BadRequestError{Detail: "query is required"}
	}

	if body.Model != "" && !client.ValidModel(body.Model) {
		return ragResponse{}, fuego.BadRequestError{Detail: "invalid model"}
	}

	topK := body.TopK
	if topK <= 0 {
		topK = 10
	}
	if topK > 100 {
		topK = 100
	}

	threshold := body.Threshold
	if threshold < 0 {
		threshold = 0
	}

	// Filter repos by auth
	identity, _ := gc.Get("user_identity")
	userEmail, _ := identity.(string)

	var allowedRepos []string
	pathPrefixes := map[string]string{}

	for _, ref := range body.Repos {
		slug := h.resolveRepoSlug(ref.Name)
		if slug == "" {
			continue
		}
		rc, ok := h.repoConfigs[slug]
		if !ok || rc.SearchProviderID == nil {
			continue
		}
		if !h.canAccessRepo(slug, userEmail) {
			continue
		}
		allowedRepos = append(allowedRepos, slug)
		if ref.Path != "" {
			pathPrefixes[slug] = ref.Path
		}
	}

	var sources []ragSource
	var contextBuilder strings.Builder

	if len(allowedRepos) > 0 {
		pathPrefix := ""
		for _, p := range pathPrefixes {
			pathPrefix = p
			break
		}

		b := h.backend.Get()
		if b != nil {
			results, err := b.HybridSearch(gc.Request.Context(), allowedRepos, body.Query, pathPrefix, topK, threshold)
			if err != nil {
				log.WithError(err).Warn("RAG search failed")
			} else {
				sources = make([]ragSource, len(results))
				for i, r := range results {
					sources[i] = ragSource{
						RepoSlug: r.RepoSlug,
						FilePath: r.FilePath,
						Chunk:    r.Chunk,
						Score:    r.Score,
					}
				}

				const maxContextChars = 30_000
				for i, r := range results {
					entry := fmt.Sprintf("[%d] %s — %s\n%s\n\n", i+1, r.RepoSlug, r.FilePath, r.Chunk)
					if contextBuilder.Len()+len(entry) > maxContextChars {
						break
					}
					contextBuilder.WriteString(entry)
				}
			}
		}
	}

	if sources == nil {
		sources = []ragSource{}
	}

	hasDocs := contextBuilder.Len() > 0

	// Cap the page/selection context to avoid overly large prompts.
	const maxPageContextChars = 20_000
	pageContext := body.Context
	if len(pageContext) > maxPageContextChars {
		pageContext = pageContext[:maxPageContextChars]
	}
	hasPageContext := pageContext != ""

	var systemPrompt string
	switch {
	case hasDocs && hasPageContext:
		systemPrompt = "You are a knowledgeable assistant for a markdown wiki. " +
			"The user is viewing a specific page and has a question. " +
			"Use the current page content and the related document excerpts to answer. " +
			"Cite document sources by their number (e.g. [1], [2]) when referencing them. " +
			"Use markdown formatting."
	case hasDocs:
		systemPrompt = "You are a knowledgeable assistant for a markdown wiki. " +
			"Answer the user's question using ONLY the provided document excerpts. " +
			"Cite sources by their number (e.g. [1], [2]). " +
			"If the documents do not contain enough information, say so. " +
			"Use markdown formatting."
	case hasPageContext:
		systemPrompt = "You are a helpful assistant for a markdown wiki. " +
			"The user is viewing a specific page and has a question about it. " +
			"Answer based on the page content provided. " +
			"Use markdown formatting."
	default:
		systemPrompt = "You are a helpful assistant for a markdown wiki. " +
			"Answer the user's question clearly and concisely. " +
			"Use markdown formatting."
	}

	var userPrompt string
	switch {
	case hasDocs && hasPageContext:
		userPrompt = fmt.Sprintf("## Current page\n\n%s\n\n## Related documents\n\n%s\n## Question\n\n%s", pageContext, contextBuilder.String(), body.Query)
	case hasDocs:
		userPrompt = fmt.Sprintf("## Documents\n\n%s\n## Question\n\n%s", contextBuilder.String(), body.Query)
	case hasPageContext:
		userPrompt = fmt.Sprintf("## Current page\n\n%s\n\n## Question\n\n%s", pageContext, body.Query)
	default:
		userPrompt = body.Query
	}

	log.WithFields(log.Fields{"user": userEmail, "query": body.Query, "sources": len(sources), "model": body.Model, "history": len(body.History)}).Info("RAG request")

	var answer string
	var usage llm.TokenUsage

	if len(body.History) > 0 {
		messages := make([]llm.ChatMessage, 0, len(body.History)+1)
		messages = append(messages, body.History...)
		messages = append(messages, llm.ChatMessage{Role: "user", Content: userPrompt})
		answer, usage, err = client.DoChat(gc.Request.Context(), body.Model, systemPrompt, messages)
	} else {
		answer, usage, err = client.Do(gc.Request.Context(), body.Model, systemPrompt, userPrompt)
	}
	if err != nil {
		log.WithError(err).Warn("RAG LLM call failed")
		return ragResponse{}, fuego.HTTPError{
			Status: http.StatusInternalServerError,
			Detail: "LLM request failed",
		}
	}

	resp := ragResponse{Answer: answer, Sources: sources, Usage: usage}

	// Persist messages to conversation if a conversation ID is provided.
	if body.ConversationID != "" && h.convStore != nil && userEmail != "" {
		ctx := gc.Request.Context()

		// Persist user message
		h.convStore.AddMessage(ctx, ai_conversation.Message{
			ConversationID: body.ConversationID,
			Role:           "user",
			Content:        body.Query,
		})

		// Persist assistant response with sources
		convSources := make([]ai_conversation.Source, len(sources))
		for i, s := range sources {
			convSources[i] = ai_conversation.Source{
				Repo: s.RepoSlug, FilePath: s.FilePath, Chunk: s.Chunk, Score: s.Score,
			}
		}
		assistantMsg, _ := h.convStore.AddMessage(ctx, ai_conversation.Message{
			ConversationID: body.ConversationID,
			Role:           "assistant",
			Content:        answer,
			Sources:        convSources,
			TokenUsage:     &ai_conversation.TokenUsage{InputTokens: usage.InputTokens, OutputTokens: usage.OutputTokens},
		})
		if assistantMsg != nil {
			resp.MessageID = assistantMsg.ID
		}
		resp.ConversationID = body.ConversationID

		// Auto-title from first message
		h.convStore.TouchConversation(ctx, body.ConversationID)
		conv, _ := h.convStore.GetConversation(ctx, body.ConversationID, userEmail)
		if conv != nil && conv.Title == "" {
			title := body.Query
			if len(title) > 100 {
				title = title[:100] + "…"
			}
			h.convStore.UpdateTitle(ctx, body.ConversationID, userEmail, title)
		}

		// Async summarization
		if h.summarizer != nil {
			go h.summarizer.MaybeSummarize(ctx, body.ConversationID, userEmail, body.Model)
		}
	}

	return resp, nil
}

// resolveRepoSlug accepts either a slug or a display name and returns the slug.
func (h *RAGRequesthandler) resolveRepoSlug(nameOrSlug string) string {
	if _, ok := h.backends[nameOrSlug]; ok {
		return nameOrSlug
	}
	for slug := range h.backends {
		s := h.loadRepoSettings(slug)
		if s != nil && s.Name == nameOrSlug {
			return slug
		}
	}
	return ""
}

func (h *RAGRequesthandler) canAccessRepo(repoSlug, userEmail string) bool {
	settings := h.loadRepoSettings(repoSlug)
	if settings == nil {
		return false
	}
	if settings.PublicAccess {
		return true
	}
	if userEmail == "" {
		return false
	}
	resolver := h.permResolvers[repoSlug]
	if resolver == nil {
		return true
	}
	return resolver.Check(userEmail, "/", permissions.Viewer)
}

func (h *RAGRequesthandler) loadRepoSettings(slug string) *searchRepoSettings {
	rc, ok := h.repoConfigs[slug]
	if !ok {
		return nil
	}
	name := rc.Name
	if name == "" {
		name = slug
	}
	return &searchRepoSettings{
		Name:         name,
		PublicAccess: rc.PublicAccess,
	}
}
