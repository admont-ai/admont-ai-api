package requesthandler

import (
	"testing"

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
