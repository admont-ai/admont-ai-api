package requesthandler

import (
	"testing"

	"github.com/christianfischer/md-wiki-server/internal/draft"
	"github.com/christianfischer/md-wiki-server/internal/permissions"
	"github.com/christianfischer/md-wiki-server/internal/store/git_repo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func resolverWithRoot(level permissions.Level) *permissions.Resolver {
	return permissions.NewResolver(permissions.PermissionsFile{
		Root:  &permissions.PathEntry{Default: level},
		Paths: map[string]permissions.PathEntry{},
	})
}

// TestCheckPermission_RootFolder_ContributorNotAdmin is the regression test
// for a real bug reported against POST /repos/:repo/folder/ (creating a
// folder at repo root): the handler checks permission on "./" (the "no
// parent folder" sentinel plus the trailing slash checkPermission appends
// for directory paths), which used to be flagged as a hidden dot-path,
// denying every non-admin Contributor regardless of their actual grant.
func TestCheckPermission_RootFolder_ContributorNotAdmin(t *testing.T) {
	h := &RepoRequesthandler{
		repoConfigs:   map[string]*git_repo.GitRepo{"wiki": {Name: "Wiki"}},
		permResolvers: map[string]*permissions.Resolver{"wiki": resolverWithRoot(permissions.Contributor)},
	}

	err := h.checkPermission("wiki", "alice@example.com", "./", permissions.Contributor)
	require.NoError(t, err)
}

func TestCheckPermission_DotPathStillDeniedForNonAdmin(t *testing.T) {
	h := &RepoRequesthandler{
		repoConfigs:   map[string]*git_repo.GitRepo{"wiki": {Name: "Wiki"}},
		permResolvers: map[string]*permissions.Resolver{"wiki": resolverWithRoot(permissions.ContentManager)},
	}

	err := h.checkPermission("wiki", "alice@example.com", ".git/config", permissions.Contributor)
	assert.Error(t, err)
}

// --- draftLockError / draftLockErrorForFolder ---

func handlerWithDraftManager(t *testing.T, isAdmin func(string) bool) (*RepoRequesthandler, *draft.Manager) {
	t.Helper()
	dm := draft.NewManager(t.TempDir())
	h := &RepoRequesthandler{
		draftManagers: map[string]*draft.Manager{"wiki": dm},
		isSystemAdmin: isAdmin,
	}
	return h, dm
}

func TestDraftLockError_NoDraft(t *testing.T) {
	h, _ := handlerWithDraftManager(t, nil)
	err := h.draftLockError("wiki", "docs", "page.md", "alice@example.com")
	assert.NoError(t, err)
}

func TestDraftLockError_OwnDraftNotBlocked(t *testing.T) {
	h, dm := handlerWithDraftManager(t, nil)
	require.NoError(t, dm.SaveDraft("docs", "page.md", "alice@example.com", "Alice", "abc", []byte("v1")))

	err := h.draftLockError("wiki", "docs", "page.md", "alice@example.com")
	assert.NoError(t, err)
}

func TestDraftLockError_OtherUsersDraftBlocks(t *testing.T) {
	h, dm := handlerWithDraftManager(t, nil)
	require.NoError(t, dm.SaveDraft("docs", "page.md", "bob@example.com", "Bob", "abc", []byte("v1")))

	err := h.draftLockError("wiki", "docs", "page.md", "alice@example.com")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "bob@example.com")
}

func TestDraftLockError_AdminBypasses(t *testing.T) {
	h, dm := handlerWithDraftManager(t, func(string) bool { return true })
	require.NoError(t, dm.SaveDraft("docs", "page.md", "bob@example.com", "Bob", "abc", []byte("v1")))

	err := h.draftLockError("wiki", "docs", "page.md", "alice@example.com")
	assert.NoError(t, err)
}

func TestDraftLockErrorForFolder_OtherUsersDraftBlocks(t *testing.T) {
	h, dm := handlerWithDraftManager(t, nil)
	require.NoError(t, dm.SaveDraft("docs/guides", "install.md", "bob@example.com", "Bob", "abc", []byte("v1")))

	err := h.draftLockErrorForFolder("wiki", "docs", "alice@example.com")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "bob@example.com")
}

func TestDraftLockErrorForFolder_AdminBypasses(t *testing.T) {
	h, dm := handlerWithDraftManager(t, func(string) bool { return true })
	require.NoError(t, dm.SaveDraft("docs/guides", "install.md", "bob@example.com", "Bob", "abc", []byte("v1")))

	err := h.draftLockErrorForFolder("wiki", "docs", "alice@example.com")
	assert.NoError(t, err)
}
