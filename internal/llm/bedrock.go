package llm

import (
	"context"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/bedrock"
	bedrocktypes "github.com/aws/aws-sdk-go-v2/service/bedrock/types"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	runtimetypes "github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
)

// bedrockRuntimeClient is the subset of the runtime client used by this provider.
type bedrockRuntimeClient interface {
	Converse(context.Context, *bedrockruntime.ConverseInput, ...func(*bedrockruntime.Options)) (*bedrockruntime.ConverseOutput, error)
}

// bedrockControlClient is the subset of the control-plane client used to list models.
type bedrockControlClient interface {
	ListInferenceProfiles(context.Context, *bedrock.ListInferenceProfilesInput, ...func(*bedrock.Options)) (*bedrock.ListInferenceProfilesOutput, error)
}

// BedrockProvider invokes Bedrock text/chat models through its model-agnostic Converse API.
type BedrockProvider struct {
	runtime      bedrockRuntimeClient
	control      bedrockControlClient
	defaultModel Model
	maxTokens    int64
}

// NewBedrockProvider creates a provider using AWS's standard credential chain.
func NewBedrockProvider(ctx context.Context, region, defaultModel string, maxTokens int64) (*BedrockProvider, error) {
	if strings.TrimSpace(region) == "" {
		return nil, fmt.Errorf("bedrock region is required")
	}
	cfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(region))
	if err != nil {
		return nil, fmt.Errorf("loading AWS config for Bedrock: %w", err)
	}
	return &BedrockProvider{
		runtime:      bedrockruntime.NewFromConfig(cfg),
		control:      bedrock.NewFromConfig(cfg),
		defaultModel: Model{ID: defaultModel, Name: defaultModel},
		maxTokens:    maxTokens,
	}, nil
}

func (p *BedrockProvider) Name() string        { return "bedrock" }
func (p *BedrockProvider) DefaultModel() Model { return p.defaultModel }

func (p *BedrockProvider) Do(ctx context.Context, model, systemPrompt, userPrompt string, maxTokens int64) (string, TokenUsage, error) {
	return p.converse(ctx, model, systemPrompt, []ChatMessage{{Role: "user", Content: userPrompt}}, maxTokens)
}

func (p *BedrockProvider) DoChat(ctx context.Context, model, systemPrompt string, messages []ChatMessage, maxTokens int64) (string, TokenUsage, error) {
	return p.converse(ctx, model, systemPrompt, messages, maxTokens)
}

func (p *BedrockProvider) converse(ctx context.Context, model, systemPrompt string, messages []ChatMessage, requestedTokens int64) (string, TokenUsage, error) {
	if model == "" {
		model = p.defaultModel.ID
	}
	if model == "" {
		return "", TokenUsage{}, fmt.Errorf("bedrock model is required: configure default_model or provide model")
	}

	input := &bedrockruntime.ConverseInput{
		ModelId:  aws.String(model),
		Messages: bedrockMessages(messages),
	}
	if systemPrompt != "" {
		input.System = []runtimetypes.SystemContentBlock{&runtimetypes.SystemContentBlockMemberText{Value: systemPrompt}}
	}
	if max := effectiveTokens(requestedTokens, p.maxTokens); max > 0 {
		if max > int64(^uint32(0)>>1) {
			return "", TokenUsage{}, fmt.Errorf("bedrock max tokens exceeds supported range")
		}
		input.InferenceConfig = &runtimetypes.InferenceConfiguration{MaxTokens: aws.Int32(int32(max))}
	}

	resp, err := p.runtime.Converse(ctx, input)
	if err != nil {
		return "", TokenUsage{}, fmt.Errorf("bedrock Converse model %q: %w", model, err)
	}
	usage := TokenUsage{}
	if resp.Usage != nil {
		usage.InputTokens = int64(aws.ToInt32(resp.Usage.InputTokens))
		usage.OutputTokens = int64(aws.ToInt32(resp.Usage.OutputTokens))
	}
	output, ok := resp.Output.(*runtimetypes.ConverseOutputMemberMessage)
	if !ok {
		return "", usage, fmt.Errorf("bedrock Converse model %q returned no message", model)
	}
	text := bedrockText(output.Value.Content)
	if text == "" {
		return "", usage, fmt.Errorf("bedrock Converse model %q returned no text content", model)
	}
	return text, usage, nil
}

func bedrockMessages(messages []ChatMessage) []runtimetypes.Message {
	out := make([]runtimetypes.Message, 0, len(messages))
	for _, message := range messages {
		role := runtimetypes.ConversationRoleUser
		if message.Role == "assistant" {
			role = runtimetypes.ConversationRoleAssistant
		}
		out = append(out, runtimetypes.Message{
			Role:    role,
			Content: []runtimetypes.ContentBlock{&runtimetypes.ContentBlockMemberText{Value: message.Content}},
		})
	}
	return out
}

func bedrockText(blocks []runtimetypes.ContentBlock) string {
	var text strings.Builder
	for _, block := range blocks {
		if textBlock, ok := block.(*runtimetypes.ContentBlockMemberText); ok {
			text.WriteString(textBlock.Value)
		}
	}
	return text.String()
}

// FetchModels lists active inference profiles that can be passed directly to
// Converse as a model ARN.
func (p *BedrockProvider) FetchModels(ctx context.Context) ([]Model, error) {
	var models []Model
	var nextToken *string
	for {
		profiles, err := p.control.ListInferenceProfiles(ctx, &bedrock.ListInferenceProfilesInput{NextToken: nextToken})
		if err != nil {
			return nil, fmt.Errorf("listing Bedrock inference profiles: %w", err)
		}
		models = append(models, bedrockInferenceProfiles(profiles.InferenceProfileSummaries)...)
		if profiles.NextToken == nil || *profiles.NextToken == "" {
			break
		}
		nextToken = profiles.NextToken
	}
	return models, nil
}

func bedrockInferenceProfiles(summaries []bedrocktypes.InferenceProfileSummary) []Model {
	models := make([]Model, 0, len(summaries))
	for _, summary := range summaries {
		if summary.Status != bedrocktypes.InferenceProfileStatusActive || aws.ToString(summary.InferenceProfileArn) == "" {
			continue
		}
		name := aws.ToString(summary.InferenceProfileName)
		if name == "" {
			name = aws.ToString(summary.InferenceProfileId)
		}
		models = append(models, Model{ID: aws.ToString(summary.InferenceProfileArn), Name: name})
	}
	return models
}
