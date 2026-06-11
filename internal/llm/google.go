package llm

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"google.golang.org/genai"
)

// GoogleFetcher fetches models dynamically from the Google GenAI API.
type GoogleFetcher struct {
	apiKey string
}

// NewGoogleFetcher creates a fetcher for the given API key.
func NewGoogleFetcher(apiKey string) *GoogleFetcher {
	return &GoogleFetcher{apiKey: apiKey}
}

func (f *GoogleFetcher) FetchModels(ctx context.Context) ([]Model, error) {
	client, err := genai.NewClient(ctx, &genai.ClientConfig{
		APIKey:  f.apiKey,
		Backend: genai.BackendGeminiAPI,
	})
	if err != nil {
		return nil, fmt.Errorf("google genai client: %w", err)
	}

	var models []Model
	for m, err := range client.Models.All(ctx) {
		if err != nil {
			return nil, fmt.Errorf("google models list: %w", err)
		}
		name := m.Name
		if !strings.HasPrefix(name, "models/gemini") {
			continue
		}
		if len(m.SupportedActions) > 0 && !slices.Contains(m.SupportedActions, "generateContent") {
			continue
		}
		lower := strings.ToLower(name)
		if strings.Contains(lower, "embedding") || strings.Contains(lower, "aqa") {
			continue
		}
		id := strings.TrimPrefix(name, "models/")
		displayName := m.DisplayName
		if displayName == "" {
			displayName = id
		}
		models = append(models, Model{ID: id, Name: displayName})
	}
	return FilterModels("google", models), nil
}

var GoogleDefaultModel = Model{ID: "gemini-2.5-flash", Name: "Gemini 2.5 Flash"}

type GoogleProvider struct {
	client    *genai.Client
	maxTokens int64
}

func NewGoogleProvider(apiKey string, maxTokens int64) *GoogleProvider {
	client, err := genai.NewClient(context.Background(), &genai.ClientConfig{
		APIKey:  apiKey,
		Backend: genai.BackendGeminiAPI,
	})
	if err != nil {
		panic(fmt.Sprintf("google genai client: %v", err))
	}
	return &GoogleProvider{
		client:    client,
		maxTokens: maxTokens,
	}
}

func (p *GoogleProvider) Name() string        { return "google" }
func (p *GoogleProvider) DefaultModel() Model { return GoogleDefaultModel }

func (p *GoogleProvider) DoChat(ctx context.Context, model, systemPrompt string, messages []ChatMessage, reqMaxTokens int64) (string, TokenUsage, error) {
	if model == "" {
		model = GoogleDefaultModel.ID
	}
	maxTokens := int32(effectiveTokens(reqMaxTokens, p.maxTokens))
	contents := make([]*genai.Content, 0, len(messages))
	for _, m := range messages {
		role := "user"
		if m.Role == "assistant" {
			role = "model"
		}
		contents = append(contents, &genai.Content{
			Role:  role,
			Parts: []*genai.Part{genai.NewPartFromText(m.Content)},
		})
	}
	resp, err := p.client.Models.GenerateContent(ctx, model, contents, &genai.GenerateContentConfig{
		SystemInstruction: genai.NewContentFromText(systemPrompt, genai.RoleUser),
		MaxOutputTokens:   maxTokens,
	})
	if err != nil {
		return "", TokenUsage{}, fmt.Errorf("google API call: %w", err)
	}
	usage := TokenUsage{}
	if resp.UsageMetadata != nil {
		usage.InputTokens = int64(resp.UsageMetadata.PromptTokenCount)
		usage.OutputTokens = int64(resp.UsageMetadata.CandidatesTokenCount)
	}
	if resp.Text() == "" {
		return "", usage, fmt.Errorf("no text content in google response")
	}
	return resp.Text(), usage, nil
}

func (p *GoogleProvider) Do(ctx context.Context, model, systemPrompt, userPrompt string, reqMaxTokens int64) (string, TokenUsage, error) {
	if model == "" {
		model = GoogleDefaultModel.ID
	}
	maxTokens := int32(effectiveTokens(reqMaxTokens, p.maxTokens))
	resp, err := p.client.Models.GenerateContent(ctx, model, genai.Text(userPrompt), &genai.GenerateContentConfig{
		SystemInstruction: genai.NewContentFromText(systemPrompt, genai.RoleUser),
		MaxOutputTokens:   maxTokens,
	})
	if err != nil {
		return "", TokenUsage{}, fmt.Errorf("google API call: %w", err)
	}

	usage := TokenUsage{}
	if resp.UsageMetadata != nil {
		usage.InputTokens = int64(resp.UsageMetadata.PromptTokenCount)
		usage.OutputTokens = int64(resp.UsageMetadata.CandidatesTokenCount)
	}

	if resp.Text() == "" {
		return "", usage, fmt.Errorf("no text content in google response")
	}

	return resp.Text(), usage, nil
}
