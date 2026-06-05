package llm

import (
	"context"
	"fmt"

	"github.com/openai/openai-go/v3"
)

// doOpenAICompatChat is a shared helper for all OpenAI-compatible providers.
func doOpenAICompatChat(ctx context.Context, client openai.Client, model string, maxTokens int64, systemPrompt string, messages []ChatMessage, providerName string) (string, TokenUsage, error) {
	msgs := []openai.ChatCompletionMessageParamUnion{
		openai.SystemMessage(systemPrompt),
	}
	for _, m := range messages {
		switch m.Role {
		case "assistant":
			msgs = append(msgs, openai.AssistantMessage(m.Content))
		default:
			msgs = append(msgs, openai.UserMessage(m.Content))
		}
	}
	resp, err := client.Chat.Completions.New(ctx, openai.ChatCompletionNewParams{
		Model:               openai.ChatModel(model),
		MaxCompletionTokens: openai.Int(maxTokens),
		Messages:            msgs,
	})
	if err != nil {
		return "", TokenUsage{}, fmt.Errorf("%s API call: %w", providerName, err)
	}
	usage := TokenUsage{InputTokens: resp.Usage.PromptTokens, OutputTokens: resp.Usage.CompletionTokens}
	if len(resp.Choices) == 0 {
		return "", usage, fmt.Errorf("no choices in %s response", providerName)
	}
	return resp.Choices[0].Message.Content, usage, nil
}
