package requesthandler

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseAgentReply_ToolCall(t *testing.T) {
	d, ok := parseAgentReply(`{"tool": "read_file", "args": {"path": "docs/page.md"}}`)
	require.True(t, ok)
	assert.Equal(t, "read_file", d.Tool)
	assert.Equal(t, "docs/page.md", d.Args.Path)
}

func TestParseAgentReply_Answer(t *testing.T) {
	d, ok := parseAgentReply(`{"answer": "All done. I created the page."}`)
	require.True(t, ok)
	assert.Empty(t, d.Tool)
	assert.Equal(t, "All done. I created the page.", d.Answer)
}

func TestParseAgentReply_FencedJSON(t *testing.T) {
	d, ok := parseAgentReply("```json\n{\"tool\": \"list_files\", \"args\": {}}\n```")
	require.True(t, ok)
	assert.Equal(t, "list_files", d.Tool)
}

func TestParseAgentReply_JSONWithSurroundingProse(t *testing.T) {
	d, ok := parseAgentReply("I will now read the file.\n{\"tool\": \"read_file\", \"args\": {\"path\": \"a.md\"}}")
	require.True(t, ok)
	assert.Equal(t, "read_file", d.Tool)
	assert.Equal(t, "a.md", d.Args.Path)
}

func TestParseAgentReply_MultipleObjectsTakesFirst(t *testing.T) {
	d, ok := parseAgentReply(`{"tool": "list_files", "args": {}} {"tool": "create_file", "args": {"path": "kubernetes.md", "content": "# K8s"}}`)
	require.True(t, ok)
	assert.Equal(t, "list_files", d.Tool)

	// Trailing prose after the object is ignored too.
	d, ok = parseAgentReply(`{"tool": "read_file", "args": {"path": "a.md"}} Now I will read the file.`)
	require.True(t, ok)
	assert.Equal(t, "read_file", d.Tool)
}

func TestParseAgentReply_PlainText(t *testing.T) {
	_, ok := parseAgentReply("Here is a summary of the document without any JSON.")
	assert.False(t, ok)

	// JSON without tool or answer keys is not a directive either.
	_, ok = parseAgentReply(`{"something": "else"}`)
	assert.False(t, ok)
}

func TestAgentValidatePath(t *testing.T) {
	h := &AgentRequesthandler{docPaths: map[string]string{"scoped": "docs"}}

	// Valid relative paths.
	p, err := h.validatePath("wiki", "folder/page.md")
	require.NoError(t, err)
	assert.Equal(t, "folder/page.md", p)

	// Path cleaning.
	p, err = h.validatePath("wiki", "folder//sub/../page.md")
	require.NoError(t, err)
	assert.Equal(t, "folder/page.md", p)

	// Traversal and absolute paths rejected.
	for _, bad := range []string{"../outside.md", "..", "/etc/passwd", "a/../../b.md", ""} {
		_, err := h.validatePath("wiki", bad)
		assert.Error(t, err, "path %q must be rejected", bad)
	}

	// Dot-paths rejected (permissions file, drafts, git internals).
	for _, bad := range []string{".wiki-permissions.yaml", "docs/.drafts/x.md", ".git/config"} {
		_, err := h.validatePath("wiki", bad)
		assert.Error(t, err, "dot path %q must be rejected", bad)
	}

	// docPath scoping.
	_, err = h.validatePath("scoped", "other/page.md")
	assert.Error(t, err)
	p, err = h.validatePath("scoped", "docs/page.md")
	require.NoError(t, err)
	assert.Equal(t, "docs/page.md", p)
}

func TestFormatChanges(t *testing.T) {
	assert.Empty(t, formatChanges(nil))
	assert.Empty(t, formatChanges([]agentAction{{Tool: "create_file", Path: "a.md", Status: "error: denied"}}))

	out := formatChanges([]agentAction{
		{Tool: "create_file", Path: "docs/a.md", Status: "ok"},
		{Tool: "update_file", Path: "docs/b.md", Status: "ok"},
	})
	assert.Contains(t, out, "Created `docs/a.md`")
	assert.Contains(t, out, "Updated `docs/b.md`")
}
