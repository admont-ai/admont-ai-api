package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// perplexityAgentURL is Perplexity's Agent API endpoint. It replaces the
// retired Sonar Chat Completions API (/chat/completions, removed
// 2026-09-27) and has a different request/response shape: model selection
// happens via "preset" instead of a model ID, the prompt is "input" instead
// of a "messages" array, the system prompt is a top-level "instructions"
// field, and the response is a typed "output" array rather than "choices".
const perplexityAgentURL = "https://api.perplexity.ai/v1/agent"

// perplexityModels are Perplexity's Agent API research-depth presets. The
// Agent API has no model-listing endpoint and no per-model IDs to fetch —
// "preset" is the entire selection axis — so this list is maintained by
// hand.
var perplexityModels = []Model{
	{ID: "fast", Name: "Perplexity Fast"},
	{ID: "low", Name: "Perplexity Low"},
	{ID: "medium", Name: "Perplexity Medium"},
	{ID: "high", Name: "Perplexity High"},
	{ID: "xhigh", Name: "Perplexity Extra High"},
}

var PerplexityDefaultModel = Model{ID: "low", Name: "Perplexity Low"}

// PerplexityFetcher returns Perplexity's hardcoded preset list. See
// perplexityModels for why this can't be fetched dynamically.
type PerplexityFetcher struct{}

func NewPerplexityFetcher() *PerplexityFetcher { return &PerplexityFetcher{} }

func (f *PerplexityFetcher) FetchModels(ctx context.Context) ([]Model, error) {
	return perplexityModels, nil
}

type PerplexityProvider struct {
	apiKey     string
	maxTokens  int64
	httpClient *http.Client
}

func NewPerplexityProvider(apiKey string, maxTokens int64) *PerplexityProvider {
	return &PerplexityProvider{
		apiKey:     apiKey,
		maxTokens:  maxTokens,
		httpClient: &http.Client{},
	}
}

func (p *PerplexityProvider) Name() string        { return "perplexity" }
func (p *PerplexityProvider) DefaultModel() Model { return PerplexityDefaultModel }

type perplexityAgentRequest struct {
	Preset          string `json:"preset"`
	Input           any    `json:"input"`
	Instructions    string `json:"instructions,omitempty"`
	MaxOutputTokens int64  `json:"max_output_tokens,omitempty"`
}

type perplexityInputMessage struct {
	Type    string `json:"type"`
	Role    string `json:"role"`
	Content string `json:"content"`
}

type perplexityAgentResponse struct {
	Status string                 `json:"status"`
	Output []perplexityOutputItem `json:"output"`
	Usage  perplexityUsage        `json:"usage"`
	Error  *perplexityAgentError  `json:"error"`
}

type perplexityOutputItem struct {
	Type    string                    `json:"type"`
	Content []perplexityOutputContent `json:"content"`
}

type perplexityOutputContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type perplexityUsage struct {
	InputTokens  int64 `json:"input_tokens"`
	OutputTokens int64 `json:"output_tokens"`
}

type perplexityAgentError struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Code    string `json:"code"`
}

// call sends a request to the Agent API and extracts the assistant's reply
// text out of the (possibly multi-item, e.g. search_results + message)
// output array.
func (p *PerplexityProvider) call(ctx context.Context, model, systemPrompt string, input any, maxTokens int64) (string, TokenUsage, error) {
	if model == "" {
		model = PerplexityDefaultModel.ID
	}
	reqBody := perplexityAgentRequest{
		Preset:          model,
		Input:           input,
		Instructions:    systemPrompt,
		MaxOutputTokens: effectiveTokens(maxTokens, p.maxTokens),
	}
	buf, err := json.Marshal(reqBody)
	if err != nil {
		return "", TokenUsage{}, fmt.Errorf("encoding perplexity request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, perplexityAgentURL, bytes.NewReader(buf))
	if err != nil {
		return "", TokenUsage{}, fmt.Errorf("creating perplexity request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+p.apiKey)

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return "", TokenUsage{}, fmt.Errorf("calling perplexity: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", TokenUsage{}, fmt.Errorf("reading perplexity response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		var errResp struct {
			Error *perplexityAgentError `json:"error"`
		}
		if json.Unmarshal(body, &errResp) == nil && errResp.Error != nil {
			return "", TokenUsage{}, fmt.Errorf("perplexity API error (%d): %s", resp.StatusCode, errResp.Error.Message)
		}
		return "", TokenUsage{}, fmt.Errorf("perplexity returned status %d: %s", resp.StatusCode, string(body))
	}

	var agentResp perplexityAgentResponse
	if err := json.Unmarshal(body, &agentResp); err != nil {
		return "", TokenUsage{}, fmt.Errorf("decoding perplexity response: %w", err)
	}

	usage := TokenUsage{InputTokens: agentResp.Usage.InputTokens, OutputTokens: agentResp.Usage.OutputTokens}

	var text string
	for _, item := range agentResp.Output {
		if item.Type != "message" {
			continue
		}
		for _, c := range item.Content {
			if c.Type == "output_text" {
				text += c.Text
			}
		}
	}
	if text == "" {
		if agentResp.Error != nil {
			return "", usage, fmt.Errorf("perplexity API error: %s", agentResp.Error.Message)
		}
		return "", usage, fmt.Errorf("no text content in perplexity response")
	}

	return text, usage, nil
}

func (p *PerplexityProvider) DoChat(ctx context.Context, model, systemPrompt string, messages []ChatMessage, maxTokens int64) (string, TokenUsage, error) {
	input := make([]perplexityInputMessage, 0, len(messages))
	for _, m := range messages {
		input = append(input, perplexityInputMessage{Type: "message", Role: m.Role, Content: m.Content})
	}
	return p.call(ctx, model, systemPrompt, input, maxTokens)
}

func (p *PerplexityProvider) Do(ctx context.Context, model, systemPrompt, userPrompt string, maxTokens int64) (string, TokenUsage, error) {
	return p.call(ctx, model, systemPrompt, userPrompt, maxTokens)
}
