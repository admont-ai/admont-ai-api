package permissions

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseLevel(t *testing.T) {
	tests := []struct {
		input string
		want  Level
		err   bool
	}{
		{"none", None, false},
		{"", None, false},
		{"viewer", Viewer, false},
		{"contributor", Contributor, false},
		{"content_manager", ContentManager, false},
		{"manager", Manager, false},
		{"invalid", None, true},
	}
	for _, tt := range tests {
		got, err := ParseLevel(tt.input)
		if tt.err {
			assert.Error(t, err)
		} else {
			assert.NoError(t, err)
			assert.Equal(t, tt.want, got)
		}
	}
}

func TestLevelString(t *testing.T) {
	assert.Equal(t, "none", None.String())
	assert.Equal(t, "viewer", Viewer.String())
	assert.Equal(t, "contributor", Contributor.String())
	assert.Equal(t, "content_manager", ContentManager.String())
	assert.Equal(t, "manager", Manager.String())
}

func TestHierarchicalLevels(t *testing.T) {
	assert.True(t, Manager > ContentManager)
	assert.True(t, ContentManager > Contributor)
	assert.True(t, Contributor > Viewer)
	assert.True(t, Viewer > None)

	// Manager implies all lower levels
	assert.True(t, Manager >= ContentManager)
	assert.True(t, Manager >= Contributor)
	assert.True(t, Manager >= Viewer)

	// ContentManager implies contributor and viewer
	assert.True(t, ContentManager >= Contributor)
	assert.True(t, ContentManager >= Viewer)

	// Contributor implies viewer
	assert.True(t, Contributor >= Viewer)
}

func TestResolver_ExactFileMatch(t *testing.T) {
	pf := PermissionsFile{
		Version: 1,
		Root:    &PathEntry{Default: Viewer},
		Paths: map[string]PathEntry{
			"docs/secret.md": {
				Owner:   "alice@example.com",
				Default: None,
				Users: map[string]Level{
					"bob@example.com": Viewer,
				},
			},
		},
	}
	r := NewResolver(pf)

	// Owner gets full access (Manager)
	assert.True(t, r.Check("alice@example.com", "docs/secret.md", Manager))
	assert.Equal(t, Manager, r.EffectiveLevel("alice@example.com", "docs/secret.md"))

	// Bob has explicit viewer on file, but root also grants viewer — highest wins (viewer)
	assert.True(t, r.Check("bob@example.com", "docs/secret.md", Viewer))
	assert.False(t, r.Check("bob@example.com", "docs/secret.md", Contributor))

	// Unknown user: file default is none, but root default is viewer — escalation: viewer wins
	assert.True(t, r.Check("charlie@example.com", "docs/secret.md", Viewer))

	// Different file falls back to root
	assert.Equal(t, Viewer, r.EffectiveLevel("charlie@example.com", "other.md"))
}

func TestResolver_EscalationOnly(t *testing.T) {
	// Subfolder grants higher access than root — user gets the higher level
	pf := PermissionsFile{
		Version: 1,
		Root:    &PathEntry{Default: Viewer},
		Paths: map[string]PathEntry{
			"docs/": {
				Default: Contributor,
			},
		},
	}
	r := NewResolver(pf)

	// Files in docs/ get Contributor (escalated from root's Viewer)
	assert.Equal(t, Contributor, r.EffectiveLevel("user@example.com", "docs/file.md"))
	// Files outside docs/ get root's Viewer
	assert.Equal(t, Viewer, r.EffectiveLevel("user@example.com", "other.md"))
}

func TestResolver_CannotRestrictViaSubfolder(t *testing.T) {
	// Parent grants Contributor, subfolder tries to restrict to None — parent wins
	pf := PermissionsFile{
		Version: 1,
		Root:    &PathEntry{Default: Contributor},
		Paths: map[string]PathEntry{
			"restricted/": {
				Default: None,
			},
		},
	}
	r := NewResolver(pf)

	// restricted/ entry says None, but root says Contributor — highest wins
	assert.Equal(t, Contributor, r.EffectiveLevel("user@example.com", "restricted/file.md"))
	assert.True(t, r.Check("user@example.com", "restricted/file.md", Contributor))
}

func TestResolver_DeepNestingEscalation(t *testing.T) {
	pf := PermissionsFile{
		Version: 1,
		Root:    &PathEntry{Default: None},
		Paths: map[string]PathEntry{
			"a/": {
				Default: Viewer,
			},
			"a/b/": {
				Default: None, // tries to restrict — should not work
			},
			"a/b/c/": {
				Default: Contributor,
			},
		},
	}
	r := NewResolver(pf)

	// a/ grants Viewer
	assert.Equal(t, Viewer, r.EffectiveLevel("user@example.com", "a/file.md"))
	// a/b/ tries None, but a/ grants Viewer — Viewer wins
	assert.Equal(t, Viewer, r.EffectiveLevel("user@example.com", "a/b/file.md"))
	// a/b/c/ grants Contributor, which is higher than a/'s Viewer — Contributor wins
	assert.Equal(t, Contributor, r.EffectiveLevel("user@example.com", "a/b/c/file.md"))
}

func TestResolver_UserEscalationInSubfolder(t *testing.T) {
	pf := PermissionsFile{
		Version: 1,
		Root:    &PathEntry{Default: Viewer},
		Paths: map[string]PathEntry{
			"project/": {
				Default: Viewer,
				Users: map[string]Level{
					"bob@example.com": Contributor,
				},
			},
		},
	}
	r := NewResolver(pf)

	// Bob gets Contributor from project/ (higher than root's Viewer)
	assert.Equal(t, Contributor, r.EffectiveLevel("bob@example.com", "project/readme.md"))
	// Others get Viewer (same as root)
	assert.Equal(t, Viewer, r.EffectiveLevel("charlie@example.com", "project/readme.md"))
}

func TestResolver_FolderInheritance(t *testing.T) {
	pf := PermissionsFile{
		Version: 1,
		Root:    &PathEntry{Default: None},
		Paths: map[string]PathEntry{
			"private/": {
				Owner:   "alice@example.com",
				Default: None,
				Users: map[string]Level{
					"bob@example.com": Viewer,
				},
			},
		},
	}
	r := NewResolver(pf)

	// Charlie has None from root and None from private/ — no access
	assert.False(t, r.Check("charlie@example.com", "private/doc.md", Viewer))
	// Bob gets Viewer from private/
	assert.True(t, r.Check("bob@example.com", "private/doc.md", Viewer))
	// Alice is owner — Manager
	assert.True(t, r.Check("alice@example.com", "private/doc.md", Manager))

	// Nested file also inherits
	assert.False(t, r.Check("charlie@example.com", "private/sub/deep.md", Viewer))
}

func TestResolver_FileOverridesFolder(t *testing.T) {
	pf := PermissionsFile{
		Version: 1,
		Root:    &PathEntry{Default: None},
		Paths: map[string]PathEntry{
			"private/": {
				Owner:   "alice@example.com",
				Default: None,
			},
			"private/public.md": {
				Owner:   "alice@example.com",
				Default: Viewer,
			},
		},
	}
	r := NewResolver(pf)

	// Regular file in private/ — root=None, private/=None → no access
	assert.False(t, r.Check("bob@example.com", "private/other.md", Viewer))

	// public.md has Viewer entry which escalates above None
	assert.True(t, r.Check("bob@example.com", "private/public.md", Viewer))
}

func TestResolver_RootFallback(t *testing.T) {
	pf := PermissionsFile{
		Version: 1,
		Root:    &PathEntry{Default: Contributor},
	}
	r := NewResolver(pf)

	assert.Equal(t, Contributor, r.EffectiveLevel("anyone@example.com", "any/file.md"))
	assert.True(t, r.Check("anyone@example.com", "any/file.md", Contributor))
	assert.False(t, r.Check("anyone@example.com", "any/file.md", ContentManager))
}

func TestResolver_NoRoot(t *testing.T) {
	pf := PermissionsFile{
		Version: 1,
	}
	r := NewResolver(pf)

	// No root entry means no access
	assert.Equal(t, None, r.EffectiveLevel("anyone@example.com", "any/file.md"))
	assert.False(t, r.Check("anyone@example.com", "any/file.md", Viewer))
}

func TestResolver_FolderWithoutTrailingSlash(t *testing.T) {
	pf := PermissionsFile{
		Version: 1,
		Root:    &PathEntry{Default: None},
		Paths: map[string]PathEntry{
			"Folder 1": {
				Default: None,
			},
		},
	}
	r := NewResolver(pf)

	// Root=None, Folder 1=None → no access
	assert.False(t, r.Check("user@example.com", "Folder 1/file.md", Viewer))
	assert.False(t, r.Check("user@example.com", "Folder 1/sub/deep.md", Viewer))

	assert.Equal(t, None, r.EffectiveLevel("user@example.com", "Folder 1"))
	assert.Equal(t, None, r.EffectiveLevel("user@example.com", "Folder 1/"))
}

func TestResolver_NearestAncestor(t *testing.T) {
	pf := PermissionsFile{
		Version: 1,
		Root:    &PathEntry{Default: None},
		Paths: map[string]PathEntry{
			"a/": {
				Default: None,
			},
			"a/b/": {
				Default: Contributor,
			},
		},
	}
	r := NewResolver(pf)

	// a/b/ grants Contributor — highest across root(None), a/(None), a/b/(Contributor)
	assert.Equal(t, Contributor, r.EffectiveLevel("user@example.com", "a/b/file.md"))

	// a/ has None, root has None — no access
	assert.Equal(t, None, r.EffectiveLevel("user@example.com", "a/file.md"))
}

func TestResolver_IsOwner(t *testing.T) {
	pf := PermissionsFile{
		Version: 1,
		Root:    &PathEntry{Default: Viewer},
		Paths: map[string]PathEntry{
			"docs/": {
				Owner: "alice@example.com",
			},
		},
	}
	r := NewResolver(pf)

	assert.True(t, r.IsOwner("alice@example.com", "docs/file.md"))
	assert.False(t, r.IsOwner("bob@example.com", "docs/file.md"))
	assert.False(t, r.IsOwner("alice@example.com", "other/file.md"))
}

func TestResolver_EffectiveSource(t *testing.T) {
	pf := PermissionsFile{
		Version: 1,
		Root:    &PathEntry{Default: Viewer},
		Paths: map[string]PathEntry{
			"docs/file.md": {Default: Contributor},
			"private/":     {Default: None},
		},
	}
	r := NewResolver(pf)

	level, source := r.EffectiveSource("user@example.com", "docs/file.md")
	assert.Equal(t, Contributor, level)
	assert.Equal(t, "path:docs/file.md", source)

	// private/ has None but root has Viewer — root wins
	level, source = r.EffectiveSource("user@example.com", "private/sub/file.md")
	assert.Equal(t, Viewer, level)
	assert.Equal(t, "root", source)

	level, source = r.EffectiveSource("user@example.com", "other.md")
	assert.Equal(t, Viewer, level)
	assert.Equal(t, "root", source)
}

func TestResolver_EmptyEmail(t *testing.T) {
	pf := PermissionsFile{
		Version: 1,
		Root:    &PathEntry{Default: None},
		Paths: map[string]PathEntry{
			"secret.md": {
				Owner:   "alice@example.com",
				Default: None,
			},
		},
	}
	r := NewResolver(pf)

	// Unauthenticated user (empty email) gets entry default, not owner access
	assert.Equal(t, None, r.EffectiveLevel("", "secret.md"))
}

func TestResolver_HasAccessibleDescendant(t *testing.T) {
	pf := PermissionsFile{
		Version: 1,
		Root:    &PathEntry{Default: None},
		Paths: map[string]PathEntry{
			"projects/": {
				Default: None,
			},
			"projects/public/": {
				Default: Viewer,
			},
			"secret/": {
				Default: None,
			},
		},
	}
	r := NewResolver(pf)

	// projects/ has None but projects/public/ grants Viewer
	assert.True(t, r.HasAccessibleDescendant("user@example.com", "projects", Viewer))
	// secret/ has no accessible descendants
	assert.False(t, r.HasAccessibleDescendant("user@example.com", "secret", Viewer))
}

// --- Writer tests ---

func TestWriter_SetOwner(t *testing.T) {
	r := NewResolver(PermissionsFile{Version: 1, Root: &PathEntry{Default: Viewer}, Paths: map[string]PathEntry{}})
	r.SetOwner("new-file.md", "alice@example.com")

	entry, ok := r.GetEntry("new-file.md")
	assert.True(t, ok)
	assert.Equal(t, "alice@example.com", entry.Owner)
}

func TestWriter_SetUserPermission(t *testing.T) {
	r := NewResolver(PermissionsFile{Version: 1, Root: &PathEntry{Default: Viewer}, Paths: map[string]PathEntry{}})
	r.SetUserPermission("file.md", "bob@example.com", Contributor)

	entry, ok := r.GetEntry("file.md")
	assert.True(t, ok)
	assert.Equal(t, Contributor, entry.Users["bob@example.com"])
}

func TestWriter_RemoveEntry(t *testing.T) {
	r := NewResolver(PermissionsFile{
		Version: 1,
		Root:    &PathEntry{Default: Viewer},
		Paths: map[string]PathEntry{
			"file.md": {Owner: "alice@example.com"},
		},
	})
	r.RemoveEntry("file.md")

	_, ok := r.GetEntry("file.md")
	assert.False(t, ok)
}

func TestWriter_RemoveEntriesUnder(t *testing.T) {
	r := NewResolver(PermissionsFile{
		Version: 1,
		Root:    &PathEntry{Default: Viewer},
		Paths: map[string]PathEntry{
			"docs/":         {Owner: "alice@example.com"},
			"docs/a.md":     {Owner: "alice@example.com"},
			"docs/sub/":     {Owner: "alice@example.com"},
			"docs/sub/b.md": {Owner: "alice@example.com"},
			"other/":        {Owner: "bob@example.com"},
		},
	})
	r.RemoveEntriesUnder("docs")

	_, ok := r.GetEntry("docs/")
	assert.False(t, ok)
	_, ok = r.GetEntry("docs/a.md")
	assert.False(t, ok)
	_, ok = r.GetEntry("docs/sub/")
	assert.False(t, ok)

	// other/ should remain
	_, ok = r.GetEntry("other/")
	assert.True(t, ok)
}

func TestWriter_RenamePath_File(t *testing.T) {
	r := NewResolver(PermissionsFile{
		Version: 1,
		Root:    &PathEntry{Default: Viewer},
		Paths: map[string]PathEntry{
			"old.md": {Owner: "alice@example.com", Default: Contributor},
		},
	})
	r.RenamePath("old.md", "new.md")

	_, ok := r.GetEntry("old.md")
	assert.False(t, ok)

	entry, ok := r.GetEntry("new.md")
	assert.True(t, ok)
	assert.Equal(t, "alice@example.com", entry.Owner)
	assert.Equal(t, Contributor, entry.Default)
}

func TestWriter_RenamePath_Folder(t *testing.T) {
	r := NewResolver(PermissionsFile{
		Version: 1,
		Root:    &PathEntry{Default: Viewer},
		Paths: map[string]PathEntry{
			"old/":        {Owner: "alice@example.com"},
			"old/file.md": {Owner: "alice@example.com"},
			"old/sub/":    {Owner: "alice@example.com"},
		},
	})
	r.RenamePath("old/", "new/")

	_, ok := r.GetEntry("old/")
	assert.False(t, ok)
	_, ok = r.GetEntry("old/file.md")
	assert.False(t, ok)

	entry, ok := r.GetEntry("new/")
	assert.True(t, ok)
	assert.Equal(t, "alice@example.com", entry.Owner)

	_, ok = r.GetEntry("new/file.md")
	assert.True(t, ok)

	_, ok = r.GetEntry("new/sub/")
	assert.True(t, ok)
}

// --- YAML round-trip test ---

func TestYAMLRoundTrip(t *testing.T) {
	dir := t.TempDir()

	pf := PermissionsFile{
		Version: 1,
		Root:    &PathEntry{Default: Viewer},
		Paths: map[string]PathEntry{
			"docs/": {
				Owner:   "alice@example.com",
				Default: Contributor,
				Users: map[string]Level{
					"bob@example.com": Manager,
				},
			},
			"private/": {
				Owner:   "alice@example.com",
				Default: None,
			},
		},
	}
	r := NewResolver(pf)

	err := Save(dir, r)
	require.NoError(t, err)

	// Verify file exists
	_, err = os.Stat(filepath.Join(dir, PermissionsFileName))
	require.NoError(t, err)

	// Load it back
	r2, err := Load(dir)
	require.NoError(t, err)
	require.NotNil(t, r2)

	pf2 := r2.File()
	assert.Equal(t, 1, pf2.Version)
	assert.NotNil(t, pf2.Root)
	assert.Equal(t, Viewer, pf2.Root.Default)
	assert.Len(t, pf2.Paths, 2)

	docsEntry := pf2.Paths["docs/"]
	assert.Equal(t, "alice@example.com", docsEntry.Owner)
	assert.Equal(t, Contributor, docsEntry.Default)
	assert.Equal(t, Manager, docsEntry.Users["bob@example.com"])
}

func TestLoad_NoFile(t *testing.T) {
	dir := t.TempDir()
	r, err := Load(dir)
	assert.NoError(t, err)
	assert.Nil(t, r)
}

func TestInitialize(t *testing.T) {
	r := Initialize(Contributor)
	pf := r.File()

	assert.Equal(t, 1, pf.Version)
	assert.NotNil(t, pf.Root)
	assert.Equal(t, Contributor, pf.Root.Default)
	assert.Empty(t, pf.Paths)

	// All users get Contributor by default
	assert.Equal(t, Contributor, r.EffectiveLevel("anyone@example.com", "any/path.md"))
	assert.True(t, r.Check("anyone@example.com", "any/path.md", Viewer))
	assert.True(t, r.Check("anyone@example.com", "any/path.md", Contributor))
	assert.False(t, r.Check("anyone@example.com", "any/path.md", ContentManager))
}

func TestInitialize_SaveAndLoad(t *testing.T) {
	dir := t.TempDir()

	r := Initialize(Viewer)
	r.SetOwner("docs/", "alice@example.com")

	err := Save(dir, r)
	require.NoError(t, err)

	// Re-initialize should reset everything
	r2 := Initialize(Manager)
	err = Save(dir, r2)
	require.NoError(t, err)

	loaded, err := Load(dir)
	require.NoError(t, err)
	require.NotNil(t, loaded)

	pf := loaded.File()
	assert.NotNil(t, pf.Root)
	assert.Equal(t, Manager, pf.Root.Default)
	assert.Empty(t, pf.Paths) // previous entries wiped
}

func TestLoad_MigrateLegacyDefaults(t *testing.T) {
	dir := t.TempDir()

	// Write a legacy file with "defaults" field
	legacy := []byte("version: 1\ndefaults: contributor\npaths:\n  docs/:\n    default: viewer\n")
	err := os.WriteFile(filepath.Join(dir, PermissionsFileName), legacy, 0644)
	require.NoError(t, err)

	r, err := Load(dir)
	require.NoError(t, err)
	require.NotNil(t, r)

	// Legacy defaults should be migrated to root
	pf := r.File()
	assert.NotNil(t, pf.Root)
	assert.Equal(t, Contributor, pf.Root.Default)

	// Path entries preserved
	assert.Equal(t, Viewer, pf.Paths["docs/"].Default)
}
