package requesthandler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path"
	"path/filepath"
	"strings"
	"sync"

	"github.com/christianfischer/md-wiki-server/internal/llm"
	"github.com/christianfischer/md-wiki-server/internal/permissions"
	"github.com/christianfischer/md-wiki-server/internal/pg_vector/backend"
	"github.com/christianfischer/md-wiki-server/internal/pg_vector/indexer"
	"github.com/christianfischer/md-wiki-server/internal/repo"
	"github.com/christianfischer/md-wiki-server/internal/store/ai_conversation"
	"github.com/christianfischer/md-wiki-server/internal/store/git_repo"
	"github.com/gin-gonic/gin"
	"github.com/go-fuego/fuego"
	log "github.com/sirupsen/logrus"
)

const (
	maxAgentIterations  = 12
	maxToolResultChars  = 8_000
	maxListEntries      = 500
	agentSearchTopK     = 8
)

type agentRequest struct {
	Query          string            `json:"query" validate:"required"`
	Model          string            `json:"model,omitempty"`
	History        []llm.ChatMessage `json:"history,omitempty"`
	ConversationID string            `json:"conversation_id,omitempty"`
}

type agentAction struct {
	Tool   string `json:"tool"`
	Path   string `json:"path,omitempty"`
	Status string `json:"status"` // "ok" or an error description
}

type agentResponse struct {
	Answer         string         `json:"answer"`
	Actions        []agentAction  `json:"actions"`
	Usage          llm.TokenUsage `json:"usage"`
	ConversationID string         `json:"conversation_id,omitempty"`
	MessageID      string         `json:"message_id,omitempty"`
}

// agentDirective is one turn of the JSON protocol: either a tool invocation
// or the final answer.
type agentDirective struct {
	Tool   string `json:"tool"`
	Args   struct {
		Path    string `json:"path"`
		Content string `json:"content"`
		Query   string `json:"query"`
	} `json:"args"`
	Answer string `json:"answer"`
}

// AgentRequesthandler runs the agentic AI assistant: an LLM loop with
// repo-scoped file tools (list/read/search/create/update).
type AgentRequesthandler struct {
	mu            sync.RWMutex
	llmClient     *llm.Client
	searchBackend *backend.Holder
	backends      map[string]repo.RepoBackend
	repoConfigs   map[string]*git_repo.GitRepo
	permResolvers map[string]*permissions.Resolver
	docPaths      map[string]string
	indexer       *indexer.Indexer
	convStore     *ai_conversation.Store
	summarizer    *llm.Summarizer
	isSystemAdmin func(string) bool
}

func NewAgentRequesthandler(
	llmClient *llm.Client,
	searchBackend *backend.Holder,
	backends map[string]repo.RepoBackend,
	repoConfigs map[string]*git_repo.GitRepo,
	permResolvers map[string]*permissions.Resolver,
	docPaths map[string]string,
	idx *indexer.Indexer,
) *AgentRequesthandler {
	return &AgentRequesthandler{
		llmClient:     llmClient,
		searchBackend: searchBackend,
		backends:      backends,
		repoConfigs:   repoConfigs,
		permResolvers: permResolvers,
		docPaths:      docPaths,
		indexer:       idx,
	}
}

// SetClient replaces the LLM client at runtime.
func (h *AgentRequesthandler) SetClient(client *llm.Client) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.llmClient = client
}

func (h *AgentRequesthandler) getClient() *llm.Client {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.llmClient
}

// SetSystemAdminCheck sets the function used to check if a user is a system admin.
func (h *AgentRequesthandler) SetSystemAdminCheck(fn func(string) bool) {
	h.isSystemAdmin = fn
}

func (h *AgentRequesthandler) SetConversationStore(store *ai_conversation.Store, summarizer *llm.Summarizer) {
	h.convStore = store
	h.summarizer = summarizer
}

// Agent handles POST /repos/:repo/agent — the agentic edit mode.
func (h *AgentRequesthandler) Agent(c fuego.ContextWithBody[agentRequest]) (agentResponse, error) {
	client := h.getClient()
	if client == nil || !client.HasProviders() {
		return agentResponse{}, fuego.HTTPError{Status: http.StatusNotImplemented, Detail: "LLM not configured"}
	}

	gc := c.Context().(*gin.Context)
	repoSlug := c.PathParam("repo")
	userName, identity := getIdentity(gc)
	if identity == "" {
		return agentResponse{}, fuego.UnauthorizedError{Detail: "authentication required"}
	}
	if _, ok := h.backends[repoSlug]; !ok {
		return agentResponse{}, fuego.NotFoundError{Detail: "repository not found"}
	}

	body, err := c.Body()
	if err != nil {
		return agentResponse{}, fuego.BadRequestError{Detail: "invalid request body"}
	}
	if strings.TrimSpace(body.Query) == "" {
		return agentResponse{}, fuego.BadRequestError{Detail: "query is required"}
	}
	if body.Model != "" && !client.ValidModel(body.Model) {
		return agentResponse{}, fuego.BadRequestError{Detail: "invalid model"}
	}

	log.WithFields(log.Fields{"user": identity, "repo": repoSlug, "model": body.Model, "history": len(body.History)}).
		Info("agent request")

	ctx := gc.Request.Context()
	system := h.agentSystemPrompt(repoSlug)

	messages := make([]llm.ChatMessage, 0, len(body.History)+1+2*maxAgentIterations)
	messages = append(messages, body.History...)
	messages = append(messages, llm.ChatMessage{Role: "user", Content: body.Query})

	var actions []agentAction
	var totalUsage llm.TokenUsage
	answer := ""

	for i := 0; i < maxAgentIterations; i++ {
		reply, usage, err := client.DoChat(ctx, body.Model, system, messages, client.LimitFor("agent"))
		totalUsage.InputTokens += usage.InputTokens
		totalUsage.OutputTokens += usage.OutputTokens
		if err != nil {
			log.WithError(err).Warn("agent LLM call failed")
			return agentResponse{}, fuego.HTTPError{Detail: "LLM request failed: " + providerErrorMessage(err)}
		}

		directive, ok := parseAgentReply(reply)
		if !ok {
			// A malformed tool attempt gets a protocol correction instead of
			// leaking raw JSON to the user as the final answer.
			if strings.Contains(reply, `"tool"`) {
				messages = append(messages,
					llm.ChatMessage{Role: "assistant", Content: reply},
					llm.ChatMessage{Role: "user", Content: `PROTOCOL_ERROR: your reply was not exactly one valid JSON object. Reply with a single {"tool": ..., "args": {...}} or {"answer": "..."} object and nothing else.`},
				)
				continue
			}
			// The model answered in plain text — treat it as the final answer.
			answer = strings.TrimSpace(stripCodeFence(reply))
			break
		}
		if directive.Answer != "" {
			answer = directive.Answer
			break
		}

		result, action := h.execTool(ctx, repoSlug, identity, userName, directive)
		if action != nil {
			actions = append(actions, *action)
		}
		log.WithFields(log.Fields{"repo": repoSlug, "tool": directive.Tool, "path": directive.Args.Path}).
			Info("agent tool call")

		messages = append(messages,
			llm.ChatMessage{Role: "assistant", Content: reply},
			llm.ChatMessage{Role: "user", Content: "TOOL_RESULT " + directive.Tool + ":\n" + truncateResult(result)},
		)
	}

	if answer == "" {
		answer = "I reached the maximum number of steps before finishing. The changes performed so far are listed below."
	}
	if actions == nil {
		actions = []agentAction{}
	}

	resp := agentResponse{Answer: answer, Actions: actions, Usage: totalUsage}

	// Persist the exchange to the conversation (mirrors the RAG handler).
	if body.ConversationID != "" && h.convStore != nil {
		persisted := answer
		if changes := formatChanges(actions); changes != "" {
			persisted += "\n\n" + changes
		}

		h.convStore.AddMessage(ctx, ai_conversation.Message{
			ConversationID: body.ConversationID,
			Role:           "user",
			Content:        body.Query,
		})
		assistantMsg, _ := h.convStore.AddMessage(ctx, ai_conversation.Message{
			ConversationID: body.ConversationID,
			Role:           "assistant",
			Content:        persisted,
			TokenUsage:     &ai_conversation.TokenUsage{InputTokens: totalUsage.InputTokens, OutputTokens: totalUsage.OutputTokens},
		})
		if assistantMsg != nil {
			resp.MessageID = assistantMsg.ID
		}
		resp.ConversationID = body.ConversationID

		h.convStore.TouchConversation(ctx, body.ConversationID)
		conv, _ := h.convStore.GetConversation(ctx, body.ConversationID, identity)
		if conv != nil && conv.Title == "" {
			title := body.Query
			if len(title) > 100 {
				title = title[:100] + "…"
			}
			h.convStore.UpdateTitle(ctx, body.ConversationID, identity, title)
		}
		if h.summarizer != nil {
			go h.summarizer.MaybeSummarize(ctx, body.ConversationID, identity, body.Model)
		}
	}

	return resp, nil
}

func (h *AgentRequesthandler) agentSystemPrompt(repoSlug string) string {
	scope := ""
	if dp := h.docPaths[repoSlug]; dp != "" {
		scope = fmt.Sprintf(" All documents live under the folder %q — every path must start with that prefix.", dp)
	}
	return fmt.Sprintf(`You are an AI agent working on the document repository %q of a markdown wiki.%s
You fulfill the user's request by reading and writing documents with tools.

Available tools:
- {"tool": "list_files", "args": {}} — list all files in the repository
- {"tool": "read_file", "args": {"path": "docs/page.md"}} — read a file
- {"tool": "search", "args": {"query": "..."}} — semantic search across the repository's documents
- {"tool": "create_file", "args": {"path": "docs/new-page.md", "content": "..."}} — create a new file (fails if it exists)
- {"tool": "update_file", "args": {"path": "docs/page.md", "content": "..."}} — replace the full content of an existing file

Protocol — every reply must be EXACTLY ONE JSON object, nothing else:
- To use a tool: {"tool": "<name>", "args": {...}}
- To finish: {"answer": "<final answer to the user in markdown, summarizing what you did>"}
Never send more than one JSON object per reply — one tool call at a time, then wait for its result.
After each tool call you receive a message starting with TOOL_RESULT containing the result or error.

Rules:
- Only work inside this repository; paths are relative (no leading slash, no "..").
- Read a file before updating it, and preserve content unrelated to the request.
- File conventions: .md = markdown documents, .mmd = Mermaid diagram source, .drawio = uncompressed draw.io XML, .csv = CSV.
- Prefer few, purposeful tool calls. When the request is unclear, finish with an answer asking for clarification instead of guessing.`, repoSlug, scope)
}

// parseAgentReply extracts the FIRST JSON directive from a model reply —
// models sometimes emit several tool calls in one turn despite instructions;
// the rest is ignored and the model re-issues remaining calls after seeing
// the tool result. Returns ok=false when the reply contains no usable
// directive (plain-text answer).
func parseAgentReply(reply string) (*agentDirective, bool) {
	s := strings.TrimSpace(stripCodeFence(reply))
	start := strings.Index(s, "{")
	if start == -1 {
		return nil, false
	}
	dec := json.NewDecoder(strings.NewReader(s[start:]))
	var d agentDirective
	if err := dec.Decode(&d); err != nil {
		return nil, false
	}
	if d.Tool == "" && d.Answer == "" {
		return nil, false
	}
	return &d, true
}

// execTool validates and executes one tool call. It returns the textual tool
// result for the conversation and, for write attempts, an action record.
func (h *AgentRequesthandler) execTool(ctx context.Context, repoSlug, identity, userName string, d *agentDirective) (string, *agentAction) {
	switch d.Tool {
	case "list_files":
		return h.toolListFiles(repoSlug, identity), nil

	case "read_file":
		return h.toolReadFile(repoSlug, identity, d.Args.Path), nil

	case "search":
		return h.toolSearch(ctx, repoSlug, identity, d.Args.Query), nil

	case "create_file":
		result := h.toolWriteFile(repoSlug, identity, userName, d.Args.Path, d.Args.Content, true)
		return result, &agentAction{Tool: "create_file", Path: d.Args.Path, Status: actionStatus(result)}

	case "update_file":
		result := h.toolWriteFile(repoSlug, identity, userName, d.Args.Path, d.Args.Content, false)
		return result, &agentAction{Tool: "update_file", Path: d.Args.Path, Status: actionStatus(result)}

	default:
		return fmt.Sprintf("error: unknown tool %q", d.Tool), nil
	}
}

func actionStatus(result string) string {
	if strings.HasPrefix(result, "error:") {
		return result
	}
	return "ok"
}

// validatePath normalizes a tool path and rejects traversal, absolute paths,
// dot-paths, and paths outside the repo's configured doc path.
func (h *AgentRequesthandler) validatePath(repoSlug, p string) (string, error) {
	if strings.TrimSpace(p) == "" {
		return "", errors.New("path is required")
	}
	clean := path.Clean(strings.ReplaceAll(p, "\\", "/"))
	if strings.HasPrefix(clean, "/") || clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", errors.New("path must be relative and inside the repository")
	}
	if isDotPath(clean) {
		return "", errors.New("dot-paths are not allowed")
	}
	if dp := h.docPaths[repoSlug]; dp != "" && !strings.HasPrefix(clean, dp+"/") {
		return "", fmt.Errorf("path must be under %q", dp+"/")
	}
	return clean, nil
}

// checkPerm enforces repo read-only state and per-path permission levels.
func (h *AgentRequesthandler) checkPerm(repoSlug, identity, p string, required permissions.Level) error {
	if rc, ok := h.repoConfigs[repoSlug]; ok && rc.ReadOnly && required > permissions.Viewer {
		return errors.New("repository is read-only")
	}
	if identity != "" && h.isSystemAdmin != nil && h.isSystemAdmin(identity) {
		return nil
	}
	resolver := h.permResolvers[repoSlug]
	if resolver == nil {
		return errors.New("permission denied")
	}
	if !resolver.Check(identity, p, required) {
		return errors.New("permission denied")
	}
	return nil
}

func (h *AgentRequesthandler) shouldIndex(repoSlug string) bool {
	if h.indexer == nil {
		return false
	}
	rc, ok := h.repoConfigs[repoSlug]
	return ok && rc.SearchProviderID != nil
}

func (h *AgentRequesthandler) toolListFiles(repoSlug, identity string) string {
	backend := h.backends[repoSlug]
	entries, err := backend.ListEntries(h.docPaths[repoSlug])
	if err != nil {
		return "error: failed to list files: " + err.Error()
	}
	resolver := h.permResolvers[repoSlug]
	isAdmin := h.isSystemAdmin != nil && h.isSystemAdmin(identity)

	var sb strings.Builder
	count := 0
	for _, e := range entries {
		if e.IsDir {
			continue
		}
		entryPath := filepath.Join(e.Path, e.Name)
		if !isAdmin && isDotPath(entryPath) {
			continue
		}
		if !isAdmin && resolver != nil && !resolver.Check(identity, entryPath, permissions.Viewer) {
			continue
		}
		sb.WriteString(entryPath)
		sb.WriteString("\n")
		count++
		if count >= maxListEntries {
			sb.WriteString(fmt.Sprintf("… (truncated at %d files)\n", maxListEntries))
			break
		}
	}
	if count == 0 {
		return "(no files)"
	}
	return sb.String()
}

func (h *AgentRequesthandler) toolReadFile(repoSlug, identity, p string) string {
	clean, err := h.validatePath(repoSlug, p)
	if err != nil {
		return "error: " + err.Error()
	}
	if err := h.checkPerm(repoSlug, identity, clean, permissions.Viewer); err != nil {
		return "error: " + err.Error()
	}
	content, err := h.backends[repoSlug].GetFile(splitPath(clean))
	if err != nil {
		return "error: file not found"
	}
	return string(content)
}

func (h *AgentRequesthandler) toolSearch(ctx context.Context, repoSlug, identity, query string) string {
	if strings.TrimSpace(query) == "" {
		return "error: query is required"
	}
	if h.searchBackend == nil {
		return "error: search is not available"
	}
	b := h.searchBackend.Get()
	if b == nil {
		return "error: search is not available"
	}
	// Over-fetch, then enforce per-document access so the assistant never sees
	// chunks from files the user cannot read.
	results, err := b.HybridSearch(ctx, []string{repoSlug}, query, "", overfetchK(agentSearchTopK), 0)
	if err != nil {
		return "error: search failed: " + err.Error()
	}
	results = filterAccessibleResults(results, h.permResolvers, h.isSystemAdmin, identity, agentSearchTopK)
	if len(results) == 0 {
		return "(no results)"
	}
	var sb strings.Builder
	for i, r := range results {
		fmt.Fprintf(&sb, "[%d] %s\n%s\n\n", i+1, r.FilePath, r.Chunk)
	}
	return sb.String()
}

func (h *AgentRequesthandler) toolWriteFile(repoSlug, identity, userName, p, content string, create bool) string {
	clean, err := h.validatePath(repoSlug, p)
	if err != nil {
		return "error: " + err.Error()
	}
	if strings.TrimSpace(content) == "" {
		return "error: content is required"
	}
	if err := h.checkPerm(repoSlug, identity, clean, permissions.Contributor); err != nil {
		return "error: " + err.Error()
	}

	backend := h.backends[repoSlug]
	subfolder, filename := splitPath(clean)

	_, statErr := backend.GetFile(subfolder, filename)
	if create && statErr == nil {
		return "error: file already exists — use update_file"
	}
	if !create && statErr != nil {
		return "error: file does not exist — use create_file"
	}

	if err := backend.AddFile(subfolder, filename, []byte(content)); err != nil {
		return "error: writing file: " + err.Error()
	}

	verb := "update"
	if create {
		verb = "create"
	}
	backend.SaveChangesAsync(fmt.Sprintf("AI: %s %s", verb, clean), userName, identity)
	if h.shouldIndex(repoSlug) {
		h.indexer.IndexFile(repoSlug, clean)
	}
	return fmt.Sprintf("%sd %s (%d bytes)", verb, clean, len(content))
}

// splitPath splits a repo-relative path into (subfolder, filename).
func splitPath(p string) (string, string) {
	subfolder := filepath.Dir(p)
	if subfolder == "." {
		subfolder = ""
	}
	return subfolder, filepath.Base(p)
}

func truncateResult(s string) string {
	if len(s) <= maxToolResultChars {
		return s
	}
	return s[:maxToolResultChars] + "\n… (truncated)"
}

// formatChanges renders the performed write actions as a markdown list.
func formatChanges(actions []agentAction) string {
	var lines []string
	for _, a := range actions {
		if a.Status != "ok" {
			continue
		}
		verb := "Updated"
		if a.Tool == "create_file" {
			verb = "Created"
		}
		lines = append(lines, fmt.Sprintf("- %s `%s`", verb, a.Path))
	}
	if len(lines) == 0 {
		return ""
	}
	return "**Changes:**\n" + strings.Join(lines, "\n")
}
