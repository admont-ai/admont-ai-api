package repoactions

import (
	"testing"

	"github.com/christianfischer/md-wiki-server/internal/permissions"
	"github.com/christianfischer/md-wiki-server/internal/repo/repotest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func resolverWithRoot(level permissions.Level) *permissions.Resolver {
	pf := permissions.PermissionsFile{
		Root:  &permissions.PathEntry{Default: level},
		Paths: map[string]permissions.PathEntry{},
	}
	return permissions.NewResolver(pf)
}

// --- CheckPermission (MCP semantics) ---

func TestCheckPermission_ReadOnlyBlocksEvenAdmin(t *testing.T) {
	d := Deps{ReadOnly: true, IsAdmin: true}
	err := CheckPermission(d, "alice", "docs/page.md", permissions.Contributor)
	require.Error(t, err)
	assert.Equal(t, "not found", err.Error())
}

func TestCheckPermission_ReadOnlyAllowsAnonymousViewer(t *testing.T) {
	// Read-only gate only blocks writes; with a nil resolver, anonymous
	// Viewer reads are still allowed through.
	d := Deps{ReadOnly: true}
	err := CheckPermission(d, "", "docs/page.md", permissions.Viewer)
	assert.NoError(t, err)
}

func TestCheckPermission_AdminBypassesDotPathCheck(t *testing.T) {
	d := Deps{IsAdmin: true}
	err := CheckPermission(d, "alice", ".git/config", permissions.ContentManager)
	assert.NoError(t, err, "admins bypass the dot-path check by design")
}

func TestCheckPermission_DotPathDeniedForNonAdmin(t *testing.T) {
	d := Deps{}
	err := CheckPermission(d, "alice", ".git/config", permissions.Viewer)
	require.Error(t, err)
	assert.Equal(t, "not found", err.Error())
}

func TestCheckPermission_NilResolverAnonymousViewerAllowed(t *testing.T) {
	d := Deps{Resolver: nil}
	assert.NoError(t, CheckPermission(d, "", "docs/page.md", permissions.Viewer))
}

func TestCheckPermission_NilResolverAnonymousWriteRequiresAuth(t *testing.T) {
	d := Deps{Resolver: nil}
	err := CheckPermission(d, "", "docs/page.md", permissions.Contributor)
	require.Error(t, err)
	assert.Equal(t, "authentication required", err.Error())
}

func TestCheckPermission_NilResolverAuthenticatedDenied(t *testing.T) {
	d := Deps{Resolver: nil}
	err := CheckPermission(d, "alice", "docs/page.md", permissions.Viewer)
	require.Error(t, err)
	assert.Equal(t, "not found", err.Error())
}

func TestCheckPermission_ResolverDenies(t *testing.T) {
	d := Deps{Resolver: resolverWithRoot(permissions.Viewer)}
	err := CheckPermission(d, "alice", "docs/page.md", permissions.Contributor)
	require.Error(t, err)
	assert.Equal(t, "not found", err.Error())

	err = CheckPermission(d, "", "docs/page.md", permissions.Contributor)
	require.Error(t, err)
	assert.Equal(t, "authentication required", err.Error())
}

func TestCheckPermission_ResolverAllows(t *testing.T) {
	d := Deps{Resolver: resolverWithRoot(permissions.ContentManager)}
	assert.NoError(t, CheckPermission(d, "alice", "docs/page.md", permissions.ContentManager))
}

// --- CheckPermSimple (agent semantics) ---

func TestCheckPermSimple_ReadOnlyMessageDiffersFromMCP(t *testing.T) {
	d := Deps{ReadOnly: true}
	err := CheckPermSimple(d, "alice", "docs/page.md", permissions.Contributor)
	require.Error(t, err)
	assert.Equal(t, "repository is read-only", err.Error())
}

func TestCheckPermSimple_NilResolverAlwaysDenied(t *testing.T) {
	// Unlike CheckPermission, there is no anonymous-viewer allowance here.
	d := Deps{Resolver: nil}
	err := CheckPermSimple(d, "", "docs/page.md", permissions.Viewer)
	require.Error(t, err)
	assert.Equal(t, "permission denied", err.Error())
}

func TestCheckPermSimple_ResolverDeniedMessage(t *testing.T) {
	d := Deps{Resolver: resolverWithRoot(permissions.Viewer)}
	err := CheckPermSimple(d, "alice", "docs/page.md", permissions.Contributor)
	require.Error(t, err)
	assert.Equal(t, "permission denied", err.Error())
}

func TestCheckPermSimple_AdminBypass(t *testing.T) {
	d := Deps{IsAdmin: true, Resolver: resolverWithRoot(permissions.Viewer)}
	assert.NoError(t, CheckPermSimple(d, "alice", "docs/page.md", permissions.ContentManager))
}

// --- File operations ---

func TestCreateFile(t *testing.T) {
	backend := repotest.NewFakeBackend()
	d := Deps{Backend: backend, RepoSlug: "wiki"}

	err := CreateFile(d, "docs", "page.md", "docs/page.md", []byte("hello"), "create docs/page.md", "Alice", "alice@example.com")
	require.NoError(t, err)

	content, err := backend.GetFile("docs", "page.md")
	require.NoError(t, err)
	assert.Equal(t, "hello", string(content))
	require.Len(t, backend.SaveCalls, 1)
	assert.Equal(t, "create docs/page.md", backend.SaveCalls[0].Message)
	assert.Equal(t, "Alice", backend.SaveCalls[0].AuthorName)
}

func TestUpdateFile(t *testing.T) {
	backend := repotest.NewFakeBackend().Seed("docs/page.md", []byte("old"))
	d := Deps{Backend: backend, RepoSlug: "wiki"}

	err := UpdateFile(d, "docs", "page.md", "docs/page.md", []byte("new"), "update docs/page.md", "Alice", "alice@example.com")
	require.NoError(t, err)

	content, _ := backend.GetFile("docs", "page.md")
	assert.Equal(t, "new", string(content))
	assert.Equal(t, "update docs/page.md", backend.SaveCalls[0].Message)
}

func TestDeleteFile_RemovesPermissionsEntryAndSaves(t *testing.T) {
	backend := repotest.NewFakeBackend().Seed("docs/page.md", []byte("x"))
	resolver := resolverWithRoot(permissions.Viewer)
	resolver.SetUserPermission("docs/page.md", "alice@example.com", permissions.ContentManager)
	saved := false
	d := Deps{
		Backend:         backend,
		RepoSlug:        "wiki",
		Resolver:        resolver,
		SavePermissions: func() { saved = true },
	}

	err := DeleteFile(d, "docs", "page.md", "docs/page.md", "delete docs/page.md", "Alice", "alice@example.com")
	require.NoError(t, err)

	_, err = backend.GetFile("docs", "page.md")
	assert.Error(t, err, "file should be gone")
	assert.True(t, saved, "SavePermissions should be invoked when the resolver changed")
	assert.Equal(t, "delete docs/page.md", backend.SaveCalls[0].Message)
	_, ok := resolver.GetEntry("docs/page.md")
	assert.False(t, ok, "permissions entry should be removed")
}

func TestMoveFile_UpdatesResolverAndIndexPaths(t *testing.T) {
	backend := repotest.NewFakeBackend().Seed("docs/page.md", []byte("x"))
	resolver := resolverWithRoot(permissions.Viewer)
	resolver.SetUserPermission("docs/page.md", "alice@example.com", permissions.ContentManager)
	d := Deps{Backend: backend, RepoSlug: "wiki", Resolver: resolver, SavePermissions: func() {}}

	err := MoveFile(d, "docs", "page.md", "archive", "page.md", "docs/page.md", "archive/page.md", "move docs/page.md to archive/page.md", "Alice", "alice@example.com")
	require.NoError(t, err)

	_, err = backend.GetFile("docs", "page.md")
	assert.Error(t, err)
	content, err := backend.GetFile("archive", "page.md")
	require.NoError(t, err)
	assert.Equal(t, "x", string(content))
	assert.Equal(t, "move docs/page.md to archive/page.md", backend.SaveCalls[0].Message)

	_, oldOk := resolver.GetEntry("docs/page.md")
	assert.False(t, oldOk)
	_, newOk := resolver.GetEntry("archive/page.md")
	assert.True(t, newOk, "permissions entry should follow the rename")
}

// renameFile (same-folder) uses the exact same MoveFile primitive as a
// cross-folder move — confirm that path works too.
func TestMoveFile_SameFolderRename(t *testing.T) {
	backend := repotest.NewFakeBackend().Seed("docs/old.md", []byte("x"))
	d := Deps{Backend: backend, RepoSlug: "wiki"}

	err := MoveFile(d, "docs", "old.md", "docs", "new.md", "docs/old.md", "docs/new.md", "rename docs/old.md to docs/new.md", "Alice", "alice@example.com")
	require.NoError(t, err)

	_, err = backend.GetFile("docs", "old.md")
	assert.Error(t, err)
	content, err := backend.GetFile("docs", "new.md")
	require.NoError(t, err)
	assert.Equal(t, "x", string(content))
}

// --- Folder operations ---

func TestCreateFolder(t *testing.T) {
	backend := repotest.NewFakeBackend()
	d := Deps{Backend: backend, RepoSlug: "wiki"}

	err := CreateFolder(d, "docs/new-folder", "create folder docs/new-folder", "Alice", "alice@example.com")
	require.NoError(t, err)

	_, err = backend.GetFile("docs/new-folder", ".gitkeep")
	require.NoError(t, err)
	assert.Equal(t, "create folder docs/new-folder", backend.SaveCalls[0].Message)
}

func TestRenameFolder_UsesTrailingSlashForResolverBookkeeping(t *testing.T) {
	backend := repotest.NewFakeBackend().Seed("docs/a.md", []byte("x"))
	resolver := resolverWithRoot(permissions.Viewer)
	resolver.SetUserPermission("docs/", "alice@example.com", permissions.ContentManager)
	saved := false
	d := Deps{Backend: backend, RepoSlug: "wiki", Resolver: resolver, SavePermissions: func() { saved = true }}

	err := RenameFolder(d, "docs", "documents", "rename folder docs to documents", "Alice", "alice@example.com")
	require.NoError(t, err)

	_, oldOk := resolver.GetEntry("docs/")
	assert.False(t, oldOk)
	_, newOk := resolver.GetEntry("documents/")
	assert.True(t, newOk)
	assert.True(t, saved)
	assert.Equal(t, "rename folder docs to documents", backend.SaveCalls[0].Message)
}

// moveFolder (different parent) uses the exact same RenameFolder primitive
// as an in-place rename — confirm that path works too.
func TestRenameFolder_Move(t *testing.T) {
	backend := repotest.NewFakeBackend().Seed("docs/a.md", []byte("x"))
	d := Deps{Backend: backend, RepoSlug: "wiki"}

	err := RenameFolder(d, "docs", "archive/docs", "move folder docs to archive/docs", "Alice", "alice@example.com")
	require.NoError(t, err)

	_, err = backend.GetFile("archive/docs", "a.md")
	require.NoError(t, err)
}

func TestDeleteFolder_RemovesEntriesUnder(t *testing.T) {
	backend := repotest.NewFakeBackend().Seed("docs/a.md", []byte("x"))
	resolver := resolverWithRoot(permissions.Viewer)
	resolver.SetUserPermission("docs/a.md", "alice@example.com", permissions.ContentManager)
	saved := false
	d := Deps{Backend: backend, RepoSlug: "wiki", Resolver: resolver, SavePermissions: func() { saved = true }}

	err := DeleteFolder(d, "docs", "delete folder docs", "Alice", "alice@example.com")
	require.NoError(t, err)

	_, err = backend.GetFile("docs", "a.md")
	assert.Error(t, err)
	assert.True(t, saved)
	_, ok := resolver.GetEntry("docs/a.md")
	assert.False(t, ok)
}

// --- Path helpers ---

func TestSplitPath(t *testing.T) {
	tests := []struct {
		path       string
		wantFolder string
		wantFile   string
	}{
		{"readme.md", "", "readme.md"},
		{"docs/readme.md", "docs", "readme.md"},
		{"a/b/c/file.md", "a/b/c", "file.md"},
	}
	for _, tt := range tests {
		folder, file := SplitPath(tt.path)
		assert.Equal(t, tt.wantFolder, folder, tt.path)
		assert.Equal(t, tt.wantFile, file, tt.path)
	}
}

func TestIsDotPath(t *testing.T) {
	tests := []struct {
		path string
		want bool
	}{
		{"docs/readme.md", false},
		{".git/config", true},
		{"docs/.hidden/file.md", true},
		{".gitignore", true},
		{"my.folder/readme.md", false},
		{".", false},  // "repo root" sentinel used by callers, not a hidden name
		{"./", false}, // same sentinel with the trailing slash checkPermission callers append
		{"..", true},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, IsDotPath(tt.path), tt.path)
	}
}

// TestCheckPermission_RootFolderNotTreatedAsDotPath is the regression test
// for a real bug: creating a folder/file at the repo root builds a
// permission-check path of "./" (the "no parent folder" sentinel plus the
// trailing slash checkPermission callers append for directory paths), which
// IsDotPath used to flag as a hidden path — denying every non-admin
// Contributor from creating anything at repo root regardless of their
// actual permission level.
func TestCheckPermission_RootFolderNotTreatedAsDotPath(t *testing.T) {
	d := Deps{Resolver: resolverWithRoot(permissions.Contributor)}
	assert.NoError(t, CheckPermission(d, "alice", "./", permissions.Contributor))
}

func TestCheckPermSimple_RootFolderNotTreatedAsDotPath(t *testing.T) {
	// CheckPermSimple doesn't call IsDotPath at all (see its doc comment),
	// but assert the same root-path scenario works here too since it shares
	// the same "./" sentinel construction at call sites.
	d := Deps{Resolver: resolverWithRoot(permissions.Contributor)}
	assert.NoError(t, CheckPermSimple(d, "alice", "./", permissions.Contributor))
}
