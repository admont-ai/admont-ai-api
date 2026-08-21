package requesthandler

import (
	"testing"

	"github.com/christianfischer/md-wiki-server/internal/draft"
	"github.com/christianfischer/md-wiki-server/internal/permissions"
	"github.com/christianfischer/md-wiki-server/internal/repo"
	"github.com/christianfischer/md-wiki-server/internal/repo/repotest"
	"github.com/christianfischer/md-wiki-server/internal/store/git_repo"
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

// --- toolWriteFile / checkPerm regression checks (repoactions migration) ---
// These lock in that the agent's commit-message convention ("AI: create/update
// <path>") and its own response strings survived the repoactions refactor.

func agentHandlerWithBackend(backend *repotest.FakeBackend, resolver *permissions.Resolver, readOnly bool) *AgentRequesthandler {
	permResolvers := map[string]*permissions.Resolver{}
	if resolver != nil {
		permResolvers["wiki"] = resolver
	}
	return &AgentRequesthandler{
		backends:      map[string]repo.RepoBackend{"wiki": backend},
		repoConfigs:   map[string]*git_repo.GitRepo{"wiki": {Name: "Wiki", ReadOnly: readOnly}},
		permResolvers: permResolvers,
		docPaths:      map[string]string{},
	}
}

func TestToolWriteFile_CreateUsesAIPrefixedCommitMessage(t *testing.T) {
	backend := repotest.NewFakeBackend()
	resolver := permissions.NewResolver(permissions.PermissionsFile{
		Root:  &permissions.PathEntry{Default: permissions.ContentManager},
		Paths: map[string]permissions.PathEntry{},
	})
	h := agentHandlerWithBackend(backend, resolver, false)

	result := h.toolWriteFile("wiki", "alice@example.com", "Alice", "docs/page.md", "hello", true)
	assert.Equal(t, "created docs/page.md (5 bytes)", result)
	require.Len(t, backend.SaveCalls, 1)
	assert.Equal(t, "AI: create docs/page.md", backend.SaveCalls[0].Message)
	// authorName/authorEmail args are (userName, identity) — matches the
	// original inline call exactly, even though "identity" here is an email.
	assert.Equal(t, "Alice", backend.SaveCalls[0].AuthorName)
	assert.Equal(t, "alice@example.com", backend.SaveCalls[0].AuthorEmail)

	content, err := backend.GetFile("docs", "page.md")
	require.NoError(t, err)
	assert.Equal(t, "hello", string(content))
}

func TestToolWriteFile_UpdateUsesAIPrefixedCommitMessage(t *testing.T) {
	backend := repotest.NewFakeBackend().Seed("docs/page.md", []byte("old"))
	resolver := permissions.NewResolver(permissions.PermissionsFile{
		Root:  &permissions.PathEntry{Default: permissions.ContentManager},
		Paths: map[string]permissions.PathEntry{},
	})
	h := agentHandlerWithBackend(backend, resolver, false)

	result := h.toolWriteFile("wiki", "alice@example.com", "Alice", "docs/page.md", "new content", false)
	assert.Equal(t, "updated docs/page.md (11 bytes)", result)
	assert.Equal(t, "AI: update docs/page.md", backend.SaveCalls[0].Message)
}

func TestCheckPerm_ReadOnlyMessage(t *testing.T) {
	h := agentHandlerWithBackend(repotest.NewFakeBackend(), nil, true)
	err := h.checkPerm("wiki", "alice@example.com", "docs/page.md", permissions.Contributor)
	require.Error(t, err)
	assert.Equal(t, "repository is read-only", err.Error())
}

func TestCheckPerm_NilResolverDenied(t *testing.T) {
	h := agentHandlerWithBackend(repotest.NewFakeBackend(), nil, false)
	err := h.checkPerm("wiki", "alice@example.com", "docs/page.md", permissions.Viewer)
	require.Error(t, err)
	assert.Equal(t, "permission denied", err.Error())
}

// --- toolWriteFile draft lock ---

func contentManagerResolver() *permissions.Resolver {
	return permissions.NewResolver(permissions.PermissionsFile{
		Root:  &permissions.PathEntry{Default: permissions.ContentManager},
		Paths: map[string]permissions.PathEntry{},
	})
}

func TestToolWriteFile_Update_BlockedByOtherUsersDraft(t *testing.T) {
	backend := repotest.NewFakeBackend().Seed("docs/page.md", []byte("old"))
	h := agentHandlerWithBackend(backend, contentManagerResolver(), false)
	dm := draft.NewManager(t.TempDir())
	require.NoError(t, dm.SaveDraft("docs", "page.md", "bob@example.com", "Bob", "abc", []byte("bob's draft")))
	h.draftManagers = map[string]*draft.Manager{"wiki": dm}

	result := h.toolWriteFile("wiki", "alice@example.com", "Alice", "docs/page.md", "alice's edit", false)
	assert.Contains(t, result, "error:")
	assert.Contains(t, result, "bob@example.com")
}

func TestToolWriteFile_Create_NotBlockedByDrafts(t *testing.T) {
	backend := repotest.NewFakeBackend()
	h := agentHandlerWithBackend(backend, contentManagerResolver(), false)
	dm := draft.NewManager(t.TempDir())
	// A pending draft on a DIFFERENT (not-yet-created) file must not block
	// creating this one — but even so, create only ever checks statErr, not
	// drafts, since a brand-new file can't have a pre-existing draft.
	h.draftManagers = map[string]*draft.Manager{"wiki": dm}

	result := h.toolWriteFile("wiki", "alice@example.com", "Alice", "docs/new.md", "hello", true)
	assert.Equal(t, "created docs/new.md (5 bytes)", result)
}

func TestToolWriteFile_Update_AdminBypassesLock(t *testing.T) {
	backend := repotest.NewFakeBackend().Seed("docs/page.md", []byte("old"))
	h := agentHandlerWithBackend(backend, contentManagerResolver(), false)
	dm := draft.NewManager(t.TempDir())
	require.NoError(t, dm.SaveDraft("docs", "page.md", "bob@example.com", "Bob", "abc", []byte("bob's draft")))
	h.draftManagers = map[string]*draft.Manager{"wiki": dm}
	h.isSystemAdmin = func(string) bool { return true }

	result := h.toolWriteFile("wiki", "admin@example.com", "Admin", "docs/page.md", "admin edit", false)
	assert.Equal(t, "updated docs/page.md (10 bytes)", result)
}
