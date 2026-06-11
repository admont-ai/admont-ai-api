package requesthandler

import (
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

func TestGenerateSystemPrompts(t *testing.T) {
	// Every file type the UI offers must have a prompt, and the fallback must exist.
	for _, ft := range []string{"markdown", "latex", "mermaid", "drawio", "text"} {
		assert.NotEmpty(t, generateSystemPrompts[ft], "missing system prompt for %s", ft)
	}
}
