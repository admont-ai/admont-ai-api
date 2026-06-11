package requesthandler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"github.com/christianfischer/md-wiki-server/internal/llm"
	"github.com/gin-gonic/gin"
	"github.com/go-fuego/fuego"
	log "github.com/sirupsen/logrus"
)

type llmRequest struct {
	Action       string `json:"action" validate:"required"`
	Model        string `json:"model,omitempty"`
	Prompt       string `json:"prompt,omitempty"`
	Content      string `json:"content,omitempty"`
	Instructions string `json:"instructions,omitempty"`
	// FileType selects the output format for the "generate" action:
	// markdown (default), latex, mermaid, drawio, or text.
	FileType string `json:"file_type,omitempty"`
}

// generateSystemPrompts maps a file type to the system prompt used by the
// "generate" action. An unknown or empty file type falls back to markdown.
var generateSystemPrompts = map[string]string{
	"markdown": "You are a markdown content writer. Generate well-structured markdown content based on the user's request. " +
		"Output only the markdown content without wrapping it in a code block.",
	"latex": "You are a LaTeX document author. Generate a complete, compilable LaTeX document based on the user's request, " +
		"starting with \\documentclass and ending with \\end{document}. Use standard packages only where needed. " +
		"Output only the raw LaTeX source — no code fences, no explanations.",
	"mermaid": "You are a Mermaid diagram generator. Generate a single valid Mermaid diagram based on the user's request. " +
		"The output must start directly with the diagram type declaration (e.g. flowchart TD, sequenceDiagram, classDiagram, erDiagram, stateDiagram-v2, gantt). " +
		"Output only the raw Mermaid syntax — no code fences, no explanations, no markdown.",
	"drawio": "You are a draw.io (diagrams.net) diagram generator. Generate a complete, valid, uncompressed draw.io XML document based on the user's request. " +
		"The output must be a single <mxfile> element containing a <diagram> element with an <mxGraphModel> whose <root> starts with the two required cells " +
		"<mxCell id=\"0\"/> and <mxCell id=\"1\" parent=\"0\"/>, followed by the diagram's vertex and edge mxCell elements with sensible mxGeometry coordinates and sizes so the diagram is readable without overlaps. " +
		"Output only the raw XML — no XML declaration is required, no code fences, no explanations.",
	"text": "You are a plain-text writer. Generate well-structured plain text based on the user's request. " +
		"Do not use any markdown or other markup syntax. Output only the plain text — no code fences, no explanations.",
}

type llmResponse struct {
	Action   string         `json:"action"`
	Content  string         `json:"content"`
	Original string         `json:"original,omitempty"`
	Notes    string         `json:"notes,omitempty"`
	Usage    llm.TokenUsage `json:"usage"`
}

type LLMRequesthandler struct {
	mu     sync.RWMutex
	client *llm.Client
}

func NewLLMRequesthandler(client *llm.Client) *LLMRequesthandler {
	return &LLMRequesthandler{client: client}
}

// SetClient replaces the LLM client at runtime (e.g. after admin provider changes).
func (h *LLMRequesthandler) SetClient(client *llm.Client) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.client = client
}

func (h *LLMRequesthandler) getClient() *llm.Client {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.client
}

func (h *LLMRequesthandler) GetModels(c fuego.ContextNoBody) ([]llm.Model, error) {
	client := h.getClient()
	if client == nil || !client.HasProviders() {
		return []llm.Model{}, nil
	}
	return client.AllModels(), nil
}

func (h *LLMRequesthandler) HandleLLM(c fuego.ContextWithBody[llmRequest]) (llmResponse, error) {
	client := h.getClient()
	if client == nil || !client.HasProviders() {
		return llmResponse{}, fuego.HTTPError{Status: http.StatusNotImplemented, Detail: "LLM not configured"}
	}

	gc := c.Context().(*gin.Context)
	identity, _ := gc.Get("user_identity")
	userEmail, _ := identity.(string)

	body, err := c.Body()
	if err != nil {
		return llmResponse{}, err
	}

	if body.Model != "" && !client.ValidModel(body.Model) {
		return llmResponse{}, fuego.BadRequestError{Detail: "invalid model"}
	}

	log.WithFields(log.Fields{"user": userEmail, "action": body.Action, "model": body.Model}).Info("LLM request")

	ctx := c.Context()

	switch body.Action {
	// --- Free-form actions (prompt → response) ---

	case "ask":
		if body.Prompt == "" {
			return llmResponse{}, fuego.BadRequestError{Detail: "prompt is required for ask"}
		}
		system := "You are a helpful assistant for a markdown wiki. Answer the user's question clearly and concisely. Use markdown formatting."
		result, usage, err := client.Do(ctx, body.Model, system, body.Prompt)
		if err != nil {
			return llmResponse{}, llmError("ask", err)
		}
		return llmResponse{Action: "ask", Content: result, Usage: usage}, nil

	case "generate":
		if body.Prompt == "" {
			return llmResponse{}, fuego.BadRequestError{Detail: "prompt is required for generate"}
		}
		system, ok := generateSystemPrompts[body.FileType]
		if !ok {
			system = generateSystemPrompts["markdown"]
		}
		result, usage, err := client.Do(ctx, body.Model, system, body.Prompt)
		if err != nil {
			return llmResponse{}, llmError("generate", err)
		}
		return llmResponse{Action: "generate", Content: stripCodeFence(result), Usage: usage}, nil

	case "summarize":
		if body.Content == "" {
			return llmResponse{}, fuego.BadRequestError{Detail: "content is required for summarize"}
		}
		system := "Summarize the following text. Be concise and capture the key points. Use markdown formatting with bullet points where appropriate."
		result, usage, err := client.Do(ctx, body.Model, system, body.Content)
		if err != nil {
			return llmResponse{}, llmError("summarize", err)
		}
		return llmResponse{Action: "summarize", Content: result, Usage: usage}, nil

	// --- Content-editing actions (content → replaced content + notes) ---

	case "improve":
		if body.Content == "" {
			return llmResponse{}, fuego.BadRequestError{Detail: "content is required for improve"}
		}
		return h.doEdit(ctx, body.Model, body.Content,
			"Improve the writing quality, clarity, and flow of the following markdown text. "+
				"Preserve the original meaning and markdown formatting.",
			body.Instructions)

	case "fix_spelling":
		if body.Content == "" {
			return llmResponse{}, fuego.BadRequestError{Detail: "content is required for fix_spelling"}
		}
		return h.doEdit(ctx, body.Model, body.Content,
			"Fix all spelling and grammar errors in the following markdown text. "+
				"Do not change the meaning, tone, or formatting. Only correct errors.",
			"")

	case "shorten":
		if body.Content == "" {
			return llmResponse{}, fuego.BadRequestError{Detail: "content is required for shorten"}
		}
		return h.doEdit(ctx, body.Model, body.Content,
			"Make the following markdown text more concise while preserving the meaning and key information. "+
				"Remove redundancy and tighten the language. Keep the markdown formatting.",
			body.Instructions)

	case "polish":
		// Generic edit with custom instructions (backward compatible)
		if body.Content == "" {
			return llmResponse{}, fuego.BadRequestError{Detail: "content is required for polish"}
		}
		return h.doEdit(ctx, body.Model, body.Content,
			"Improve the following markdown content. Fix grammar, improve clarity, and enhance formatting while preserving the original meaning.",
			body.Instructions)

	default:
		return llmResponse{}, fuego.BadRequestError{Detail: fmt.Sprintf("unknown action: %q", body.Action)}
	}
}

// doEdit handles all content-editing actions using a structured JSON response format.
func (h *LLMRequesthandler) doEdit(ctx context.Context, model, content, task, instructions string) (llmResponse, error) {
	system := task + "\n\n" +
		"Respond with a JSON object containing exactly two fields:\n" +
		"- \"content\": the edited markdown text\n" +
		"- \"notes\": a brief description of what was changed\n\n" +
		"Output the raw JSON only. Do not wrap it in markdown code fences."

	userMessage := content
	if instructions != "" {
		userMessage += "\n\n---\nAdditional instructions: " + instructions
	}

	c := h.getClient()
	if c == nil || !c.HasProviders() {
		return llmResponse{}, fuego.HTTPError{Status: http.StatusNotImplemented, Detail: "LLM not configured"}
	}
	result, usage, err := c.Do(ctx, model, system, userMessage)
	if err != nil {
		return llmResponse{}, llmError("edit", err)
	}

	edited, notes := parseEditResponse(result)
	return llmResponse{Action: "edit", Content: edited, Original: content, Notes: notes, Usage: usage}, nil
}

// stripCodeFence unwraps a response that consists of a single markdown code
// fence (with optional language tag), which models often emit for XML or
// Mermaid output despite instructions. Content with internal fences (e.g.
// generated markdown containing code examples) is returned unchanged.
func stripCodeFence(s string) string {
	trimmed := strings.TrimSpace(s)
	if !strings.HasPrefix(trimmed, "```") || !strings.HasSuffix(trimmed, "```") {
		return s
	}
	firstNL := strings.Index(trimmed, "\n")
	if firstNL == -1 {
		return s
	}
	inner := trimmed[firstNL+1 : len(trimmed)-3]
	// If the inner content contains another fence, the outer markers are
	// probably not a simple wrapper — leave the response untouched.
	if strings.Contains(inner, "```") {
		return s
	}
	return strings.TrimSpace(inner)
}

func llmError(action string, err error) fuego.HTTPError {
	log.WithError(err).WithField("action", action).Warn("LLM request failed")
	return fuego.HTTPError{Detail: "LLM request failed"}
}

// parseEditResponse extracts the content and notes from a JSON response.
func parseEditResponse(raw string) (content, notes string) {
	trimmed := strings.TrimSpace(raw)
	// Strip markdown code fences if present
	if strings.HasPrefix(trimmed, "```") {
		if i := strings.Index(trimmed, "\n"); i != -1 {
			trimmed = trimmed[i+1:]
		}
		if j := strings.LastIndex(trimmed, "```"); j != -1 {
			trimmed = trimmed[:j]
		}
		trimmed = strings.TrimSpace(trimmed)
	}

	// Try new format: {"content": ..., "notes": ...}
	var result struct {
		Content string `json:"content"`
		Notes   string `json:"notes"`
	}
	if err := json.Unmarshal([]byte(trimmed), &result); err == nil && result.Content != "" {
		return result.Content, result.Notes
	}

	// Backward compat: {"polished": ..., "notes": ...}
	var legacy struct {
		Polished string `json:"polished"`
		Notes    string `json:"notes"`
	}
	if err := json.Unmarshal([]byte(trimmed), &legacy); err == nil && legacy.Polished != "" {
		return legacy.Polished, legacy.Notes
	}

	// Fallback: return raw text
	return strings.TrimSpace(raw), ""
}
