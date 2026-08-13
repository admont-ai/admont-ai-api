package llm

import (
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrock"
	bedrocktypes "github.com/aws/aws-sdk-go-v2/service/bedrock/types"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	runtimetypes "github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type mockBedrockRuntime struct {
	input  *bedrockruntime.ConverseInput
	output *bedrockruntime.ConverseOutput
	err    error
}

func (m *mockBedrockRuntime) Converse(_ context.Context, input *bedrockruntime.ConverseInput, _ ...func(*bedrockruntime.Options)) (*bedrockruntime.ConverseOutput, error) {
	m.input = input
	return m.output, m.err
}

func TestBedrockProviderDoChat(t *testing.T) {
	runtime := &mockBedrockRuntime{output: &bedrockruntime.ConverseOutput{
		Output: &runtimetypes.ConverseOutputMemberMessage{Value: runtimetypes.Message{
			Role: runtimetypes.ConversationRoleAssistant,
			Content: []runtimetypes.ContentBlock{
				&runtimetypes.ContentBlockMemberText{Value: "Hello "},
				&runtimetypes.ContentBlockMemberText{Value: "world"},
			},
		}},
		Usage: &runtimetypes.TokenUsage{InputTokens: aws.Int32(12), OutputTokens: aws.Int32(7)},
	}}
	provider := &BedrockProvider{
		runtime:      runtime,
		defaultModel: Model{ID: "default-model"},
		maxTokens:    100,
	}

	text, usage, err := provider.DoChat(context.Background(), "", "Be helpful", []ChatMessage{
		{Role: "user", Content: "Hi"},
		{Role: "assistant", Content: "Hello"},
	}, 30)
	require.NoError(t, err)
	assert.Equal(t, "Hello world", text)
	assert.Equal(t, TokenUsage{InputTokens: 12, OutputTokens: 7}, usage)
	require.NotNil(t, runtime.input)
	assert.Equal(t, "default-model", aws.ToString(runtime.input.ModelId))
	require.NotNil(t, runtime.input.InferenceConfig)
	assert.Equal(t, int32(30), aws.ToInt32(runtime.input.InferenceConfig.MaxTokens))
	require.Len(t, runtime.input.System, 1)
	assert.Equal(t, "Be helpful", runtime.input.System[0].(*runtimetypes.SystemContentBlockMemberText).Value)
	assert.Equal(t, runtimetypes.ConversationRoleUser, runtime.input.Messages[0].Role)
	assert.Equal(t, runtimetypes.ConversationRoleAssistant, runtime.input.Messages[1].Role)
}

func TestBedrockProviderRequiresModel(t *testing.T) {
	provider := &BedrockProvider{runtime: &mockBedrockRuntime{}}
	_, _, err := provider.Do(context.Background(), "", "", "hello", 0)
	require.ErrorContains(t, err, "default_model")
}

func TestBedrockCatalogFiltersAndIncludesProfiles(t *testing.T) {
	foundation := bedrockFoundationModels([]bedrocktypes.FoundationModelSummary{
		{ModelId: aws.String("active-text"), ModelName: aws.String("Text model"), ProviderName: aws.String("Amazon"), OutputModalities: []bedrocktypes.ModelModality{bedrocktypes.ModelModalityText}, ModelLifecycle: &bedrocktypes.FoundationModelLifecycle{Status: bedrocktypes.FoundationModelLifecycleStatusActive}},
		{ModelId: aws.String("legacy"), OutputModalities: []bedrocktypes.ModelModality{bedrocktypes.ModelModalityText}, ModelLifecycle: &bedrocktypes.FoundationModelLifecycle{Status: bedrocktypes.FoundationModelLifecycleStatusLegacy}},
		{ModelId: aws.String("embedding"), OutputModalities: []bedrocktypes.ModelModality{bedrocktypes.ModelModalityEmbedding}},
	})
	assert.Equal(t, []Model{{ID: "active-text", Name: "Amazon: Text model"}}, foundation)

	profiles := bedrockInferenceProfiles([]bedrocktypes.InferenceProfileSummary{
		{InferenceProfileArn: aws.String("arn:aws:bedrock:profile/active"), InferenceProfileName: aws.String("US Model"), Status: bedrocktypes.InferenceProfileStatusActive},
		{InferenceProfileArn: aws.String("arn:aws:bedrock:profile/inactive")},
	})
	assert.Equal(t, []Model{{ID: "arn:aws:bedrock:profile/active", Name: "Inference profile: US Model"}}, profiles)
}

var _ bedrockControlClient = (*bedrock.Client)(nil)
