package requesthandler

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestStripCodeFence(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "plain content unchanged",
			in:   "<mxfile><diagram/></mxfile>",
			want: "<mxfile><diagram/></mxfile>",
		},
		{
			name: "unwraps fence with language tag",
			in:   "```xml\n<mxfile><diagram/></mxfile>\n```",
			want: "<mxfile><diagram/></mxfile>",
		},
		{
			name: "unwraps bare fence",
			in:   "```\nflowchart TD\n  A --> B\n```",
			want: "flowchart TD\n  A --> B",
		},
		{
			name: "unwraps with surrounding whitespace",
			in:   "\n```mermaid\nsequenceDiagram\n```\n",
			want: "sequenceDiagram",
		},
		{
			name: "markdown with internal fences untouched",
			in:   "```md\n# Title\n\n```go\nfunc main() {}\n```\n```",
			want: "```md\n# Title\n\n```go\nfunc main() {}\n```\n```",
		},
		{
			name: "content not starting with fence untouched",
			in:   "Here you go:\n```xml\n<mxfile/>\n```",
			want: "Here you go:\n```xml\n<mxfile/>\n```",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, stripCodeFence(tt.in))
		})
	}
}

func TestProviderErrorMessage(t *testing.T) {
	anthropicErr := `anthropic API call: POST "https://api.anthropic.com/v1/messages": 400 Bad Request (Request-ID: req_011) {"type":"error","error":{"type":"invalid_request_error","message":"Your credit balance is too low to access the Anthropic API. Please go to Plans & Billing to upgrade or purchase credits."},"request_id":"req_011"}`
	assert.Equal(t,
		"Your credit balance is too low to access the Anthropic API. Please go to Plans & Billing to upgrade or purchase credits.",
		providerErrorMessage(errFromString(anthropicErr)))

	assert.Equal(t, "connection refused", providerErrorMessage(errFromString("connection refused")))

	long := strings.Repeat("x", 400)
	assert.Equal(t, long[:300]+"…", providerErrorMessage(errFromString(long)))
}

type stringError string

func (e stringError) Error() string { return string(e) }

func errFromString(s string) error { return stringError(s) }

func TestGenerateSystemPrompts(t *testing.T) {
	// Every file type the UI offers must have a prompt, and the fallback must exist.
	for _, ft := range []string{"markdown", "latex", "mermaid", "drawio", "text"} {
		assert.NotEmpty(t, generateSystemPrompts[ft], "missing system prompt for %s", ft)
	}
}
