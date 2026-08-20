package mcp

import (
	"context"
	"sync"
	"testing"

	"github.com/christianfischer/md-wiki-server/internal/draft"
	"github.com/christianfischer/md-wiki-server/internal/permissions"
	"github.com/christianfischer/md-wiki-server/internal/repo"
	"github.com/christianfischer/md-wiki-server/internal/repo/repotest"
	"github.com/christianfischer/md-wiki-server/internal/store/git_repo"
	mcplib "github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testRepo = "wiki"

// testServerOpts lets individual tests tweak the default fixture.
type testServerOpts struct {
	readOnly     bool
	publicAccess bool
	resolver     *permissions.Resolver
	isAdmin      bool
}

func newTestServer(t *testing.T, backend *repotest.FakeBackend, opts testServerOpts) *Server {
	t.Helper()
	repoReady := &sync.Map{}
	repoReady.Store(testRepo, true)

	permResolvers := map[string]*permissions.Resolver{}
	if opts.resolver != nil {
		permResolvers[testRepo] = opts.resolver
	}

	server := &Server{}
	server.backends = map[string]repo.RepoBackend{testRepo: backend}
	server.repoConfigs = map[string]*git_repo.GitRepo{
		testRepo: {Name: "Wiki", ReadOnly: opts.readOnly, PublicAccess: opts.publicAccess},
	}
	server.repoReady = repoReady
	server.permResolvers = permResolvers
	server.draftManagers = map[string]*draft.Manager{testRepo: draft.NewManager(t.TempDir())}
	server.docPaths = map[string]string{}
	server.isSystemAdmin = func(identity string) bool { return opts.isAdmin }
	return server
}

func ctxWithIdentity(identity, email, name string) context.Context {
	ctx := context.Background()
	ctx = context.WithValue(ctx, userIdentityKey, identity)
	ctx = context.WithValue(ctx, userEmailKey, email)
	ctx = context.WithValue(ctx, userNameKey, name)
	return ctx
}

func callReq(args map[string]any) mcplib.CallToolRequest {
	return mcplib.CallToolRequest{Params: mcplib.CallToolParams{Arguments: args}}
}

func resultText(t *testing.T, res *mcplib.CallToolResult) string {
	t.Helper()
	require.NotNil(t, res)
	require.NotEmpty(t, res.Content)
	tc, ok := res.Content[0].(mcplib.TextContent)
	require.True(t, ok, "expected text content, got %T", res.Content[0])
	return tc.Text
}

func assertToolError(t *testing.T, res *mcplib.CallToolResult, wantMsg string) {
	t.Helper()
	require.NotNil(t, res)
	assert.True(t, res.IsError, "expected an error result")
	assert.Equal(t, wantMsg, resultText(t, res))
}

// --- Write tools: commit-message and response-shape regression checks ---
// These lock in that the Stage C repoactions refactor preserved MCP's
// external behavior exactly.

func TestCreateFile_CommitMessageAndResponse(t *testing.T) {
	backend := repotest.NewFakeBackend()
	s := newTestServer(t, backend, testServerOpts{publicAccess: true, resolver: resolverWithRoot(permissions.ContentManager)})
	ctx := ctxWithIdentity("alice@example.com", "alice@example.com", "Alice")

	res, err := s.createFile(ctx, callReq(map[string]any{
		"repo": testRepo, "path": "docs/page.md", "content": "hello",
	}))
	require.NoError(t, err)
	assert.False(t, res.IsError)
	assert.JSONEq(t, `{"name":"page.md","path":"docs/page.md"}`, resultText(t, res))
	require.Len(t, backend.SaveCalls, 1)
	assert.Equal(t, "create docs/page.md", backend.SaveCalls[0].Message)
	assert.Equal(t, "Alice", backend.SaveCalls[0].AuthorName)

	content, err := backend.GetFile("docs", "page.md")
	require.NoError(t, err)
	assert.Equal(t, "hello", string(content))
}

func TestCreateFile_Unauthenticated(t *testing.T) {
	backend := repotest.NewFakeBackend()
	s := newTestServer(t, backend, testServerOpts{publicAccess: true})
	res, err := s.createFile(context.Background(), callReq(map[string]any{
		"repo": testRepo, "path": "docs/page.md", "content": "hello",
	}))
	require.NoError(t, err)
	assertToolError(t, res, "authentication required")
}

func TestUpdateFile_CommitMessage(t *testing.T) {
	backend := repotest.NewFakeBackend().Seed("docs/page.md", []byte("old"))
	s := newTestServer(t, backend, testServerOpts{publicAccess: true, resolver: resolverWithRoot(permissions.ContentManager)})
	ctx := ctxWithIdentity("alice@example.com", "alice@example.com", "Alice")

	res, err := s.updateFile(ctx, callReq(map[string]any{
		"repo": testRepo, "path": "docs/page.md", "content": "new",
	}))
	require.NoError(t, err)
	assert.False(t, res.IsError)
	assert.Equal(t, "update docs/page.md", backend.SaveCalls[0].Message)
	content, _ := backend.GetFile("docs", "page.md")
	assert.Equal(t, "new", string(content))
}

func TestDeleteFile_CommitMessageAndResponse(t *testing.T) {
	backend := repotest.NewFakeBackend().Seed("docs/page.md", []byte("x"))
	s := newTestServer(t, backend, testServerOpts{publicAccess: true, resolver: resolverWithRoot(permissions.ContentManager)})
	ctx := ctxWithIdentity("alice@example.com", "alice@example.com", "Alice")

	res, err := s.deleteFile(ctx, callReq(map[string]any{"repo": testRepo, "path": "docs/page.md"}))
	require.NoError(t, err)
	assert.Equal(t, "deleted", resultText(t, res))
	assert.Equal(t, "delete docs/page.md", backend.SaveCalls[0].Message)
	_, err = backend.GetFile("docs", "page.md")
	assert.Error(t, err)
}

func TestMoveFile_CommitMessageAndResponse(t *testing.T) {
	backend := repotest.NewFakeBackend().Seed("docs/page.md", []byte("x"))
	s := newTestServer(t, backend, testServerOpts{publicAccess: true, resolver: resolverWithRoot(permissions.ContentManager)})
	ctx := ctxWithIdentity("alice@example.com", "alice@example.com", "Alice")

	res, err := s.moveFile(ctx, callReq(map[string]any{
		"repo": testRepo, "path": "docs/page.md", "destination": "archive",
	}))
	require.NoError(t, err)
	assert.JSONEq(t, `{"name":"page.md","path":"archive/page.md"}`, resultText(t, res))
	assert.Equal(t, "move docs/page.md to archive/page.md", backend.SaveCalls[0].Message)
	_, err = backend.GetFile("archive", "page.md")
	assert.NoError(t, err)
}

func TestRenameFile_CommitMessageAndResponse(t *testing.T) {
	backend := repotest.NewFakeBackend().Seed("docs/old.md", []byte("x"))
	s := newTestServer(t, backend, testServerOpts{publicAccess: true, resolver: resolverWithRoot(permissions.ContentManager)})
	ctx := ctxWithIdentity("alice@example.com", "alice@example.com", "Alice")

	res, err := s.renameFile(ctx, callReq(map[string]any{
		"repo": testRepo, "path": "docs/old.md", "name": "new.md",
	}))
	require.NoError(t, err)
	assert.JSONEq(t, `{"name":"new.md","path":"docs/new.md"}`, resultText(t, res))
	assert.Equal(t, "rename docs/old.md to docs/new.md", backend.SaveCalls[0].Message)
}

func TestCreateFolder_CommitMessageAndResponse(t *testing.T) {
	backend := repotest.NewFakeBackend()
	s := newTestServer(t, backend, testServerOpts{publicAccess: true, resolver: resolverWithRoot(permissions.ContentManager)})
	ctx := ctxWithIdentity("alice@example.com", "alice@example.com", "Alice")

	res, err := s.createFolder(ctx, callReq(map[string]any{
		"repo": testRepo, "path": "docs", "name": "new-folder",
	}))
	require.NoError(t, err)
	assert.JSONEq(t, `{"name":"new-folder","path":"docs/new-folder"}`, resultText(t, res))
	assert.Equal(t, "create folder docs/new-folder", backend.SaveCalls[0].Message)
}

func TestRenameFolder_CommitMessageAndResponse(t *testing.T) {
	backend := repotest.NewFakeBackend().Seed("docs/a.md", []byte("x"))
	s := newTestServer(t, backend, testServerOpts{publicAccess: true, resolver: resolverWithRoot(permissions.ContentManager)})
	ctx := ctxWithIdentity("alice@example.com", "alice@example.com", "Alice")

	res, err := s.renameFolder(ctx, callReq(map[string]any{
		"repo": testRepo, "path": "docs", "name": "documents",
	}))
	require.NoError(t, err)
	assert.JSONEq(t, `{"name":"documents","path":"documents"}`, resultText(t, res))
	assert.Equal(t, "rename folder docs to documents", backend.SaveCalls[0].Message)
}

func TestDeleteFolder_CommitMessageAndResponse(t *testing.T) {
	backend := repotest.NewFakeBackend().Seed("docs/a.md", []byte("x"))
	s := newTestServer(t, backend, testServerOpts{publicAccess: true, resolver: resolverWithRoot(permissions.ContentManager)})
	ctx := ctxWithIdentity("alice@example.com", "alice@example.com", "Alice")

	res, err := s.deleteFolder(ctx, callReq(map[string]any{"repo": testRepo, "path": "docs"}))
	require.NoError(t, err)
	assert.Equal(t, "deleted", resultText(t, res))
	assert.Equal(t, "delete folder docs", backend.SaveCalls[0].Message)
}

func TestMoveFolder_CommitMessageAndResponse(t *testing.T) {
	backend := repotest.NewFakeBackend().Seed("docs/a.md", []byte("x"))
	s := newTestServer(t, backend, testServerOpts{publicAccess: true, resolver: resolverWithRoot(permissions.ContentManager)})
	ctx := ctxWithIdentity("alice@example.com", "alice@example.com", "Alice")

	res, err := s.moveFolder(ctx, callReq(map[string]any{
		"repo": testRepo, "path": "docs", "destination": "archive",
	}))
	require.NoError(t, err)
	assert.JSONEq(t, `{"name":"docs","path":"archive/docs"}`, resultText(t, res))
	assert.Equal(t, "move folder docs to archive/docs", backend.SaveCalls[0].Message)
}

// --- Permission-check scenarios (via checkPermission, exercised through read_file) ---

func TestReadFile_ViewerAllowedContributorDenied(t *testing.T) {
	backend := repotest.NewFakeBackend().Seed("docs/page.md", []byte("hi"))
	resolver := resolverWithRoot(permissions.Viewer)
	s := newTestServer(t, backend, testServerOpts{publicAccess: true, resolver: resolver})
	ctx := ctxWithIdentity("alice@example.com", "alice@example.com", "Alice")

	res, err := s.readFile(ctx, callReq(map[string]any{"repo": testRepo, "path": "docs/page.md"}))
	require.NoError(t, err)
	assert.Equal(t, "hi", resultText(t, res))

	// Same identity, only Viewer — create requires Contributor.
	res, err = s.createFile(ctx, callReq(map[string]any{"repo": testRepo, "path": "docs/new.md", "content": "x"}))
	require.NoError(t, err)
	assertToolError(t, res, "not found")
}

func TestReadOnlyRepo_BlocksWritesEvenForAdmin(t *testing.T) {
	backend := repotest.NewFakeBackend()
	s := newTestServer(t, backend, testServerOpts{publicAccess: true, readOnly: true, isAdmin: true})
	ctx := ctxWithIdentity("admin@example.com", "admin@example.com", "Admin")

	res, err := s.createFile(ctx, callReq(map[string]any{"repo": testRepo, "path": "docs/page.md", "content": "x"}))
	require.NoError(t, err)
	assertToolError(t, res, "not found")
}

func TestAdminBypassesDotPathOnRead(t *testing.T) {
	backend := repotest.NewFakeBackend().Seed(".git/config", []byte("secret"))
	s := newTestServer(t, backend, testServerOpts{publicAccess: true, isAdmin: true})
	ctx := ctxWithIdentity("admin@example.com", "admin@example.com", "Admin")

	res, err := s.readFile(ctx, callReq(map[string]any{"repo": testRepo, "path": ".git/config"}))
	require.NoError(t, err)
	assert.Equal(t, "secret", resultText(t, res))
}

func TestDotPathDeniedForNonAdmin(t *testing.T) {
	backend := repotest.NewFakeBackend().Seed(".git/config", []byte("secret"))
	s := newTestServer(t, backend, testServerOpts{publicAccess: true})
	ctx := ctxWithIdentity("alice@example.com", "alice@example.com", "Alice")

	res, err := s.readFile(ctx, callReq(map[string]any{"repo": testRepo, "path": ".git/config"}))
	require.NoError(t, err)
	assertToolError(t, res, "not found")
}

func resolverWithRoot(level permissions.Level) *permissions.Resolver {
	pf := permissions.PermissionsFile{
		Root:  &permissions.PathEntry{Default: level},
		Paths: map[string]permissions.PathEntry{},
	}
	return permissions.NewResolver(pf)
}
