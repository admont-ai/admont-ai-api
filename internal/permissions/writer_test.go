package permissions

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolver_GroupCRUD(t *testing.T) {
	r := Initialize(Contributor)

	err := r.AddGroup("editors", []string{"alice@example.com", "bob@example.com"})
	require.NoError(t, err)

	groups := r.GetGroups()
	assert.Contains(t, groups, "editors")
	assert.Equal(t, []string{"alice@example.com", "bob@example.com"}, groups["editors"])

	members, ok := r.GetGroup("editors")
	assert.True(t, ok)
	assert.Equal(t, 2, len(members))

	err = r.UpdateGroup("editors", []string{"charlie@example.com"})
	require.NoError(t, err)
	members, _ = r.GetGroup("editors")
	assert.Equal(t, []string{"charlie@example.com"}, members)

	err = r.RemoveGroup("editors")
	require.NoError(t, err)
	_, ok = r.GetGroup("editors")
	assert.False(t, ok)
}

func TestResolver_AddGroup_Duplicate(t *testing.T) {
	r := Initialize(Contributor)

	err := r.AddGroup("team", []string{"alice@example.com"})
	require.NoError(t, err)

	err = r.AddGroup("team", []string{"bob@example.com"})
	assert.Error(t, err)
}

func TestResolver_UpdateGroup_NonExistent(t *testing.T) {
	r := Initialize(Contributor)

	err := r.UpdateGroup("nonexistent", []string{"alice@example.com"})
	assert.Error(t, err)
}

func TestResolver_RemoveGroup_NonExistent(t *testing.T) {
	r := Initialize(Contributor)

	err := r.RemoveGroup("nonexistent")
	assert.Error(t, err)
}

func TestResolver_GetGroup_NonExistent(t *testing.T) {
	r := Initialize(Contributor)

	_, ok := r.GetGroup("missing")
	assert.False(t, ok)
}

func TestResolver_SetOwner(t *testing.T) {
	r := Initialize(Viewer)

	r.SetOwner("docs/readme.md", "google:alice@example.com")
	assert.True(t, r.IsOwner("google:alice@example.com", "docs/readme.md"))
}

func TestResolver_SetUserPermission(t *testing.T) {
	r := Initialize(Viewer)

	r.SetOwner("docs/", "google:admin@example.com")
	r.SetUserPermission("docs/", "google:bob@example.com", ContentManager)

	level := r.EffectiveLevel("google:bob@example.com", "docs/")
	assert.Equal(t, ContentManager, level)
}

func TestResolver_SetDefault(t *testing.T) {
	r := Initialize(None)

	r.SetOwner("secret/", "admin@example.com")
	r.SetDefault("secret/", Contributor)

	level := r.EffectiveLevel("random@example.com", "secret/file.md")
	assert.Equal(t, Contributor, level)

	level = r.EffectiveLevel("random@example.com", "other/file.md")
	assert.Equal(t, None, level)
}

func TestResolver_RemoveEntry(t *testing.T) {
	r := Initialize(Viewer)

	r.SetOwner("docs/readme.md", "alice@example.com")
	r.RemoveEntry("docs/readme.md")

	assert.False(t, r.IsOwner("alice@example.com", "docs/readme.md"))
}

func TestResolver_RemoveEntriesUnder(t *testing.T) {
	r := Initialize(Viewer)

	r.SetOwner("docs/", "alice@example.com")
	r.SetOwner("docs/readme.md", "alice@example.com")
	r.SetOwner("docs/sub/", "alice@example.com")
	r.SetOwner("other/file.md", "bob@example.com")

	r.RemoveEntriesUnder("docs/")

	assert.False(t, r.IsOwner("alice@example.com", "docs/"))
	assert.False(t, r.IsOwner("alice@example.com", "docs/readme.md"))
	assert.False(t, r.IsOwner("alice@example.com", "docs/sub/"))
	assert.True(t, r.IsOwner("bob@example.com", "other/file.md"))
}

func TestResolver_RenamePath_File(t *testing.T) {
	r := Initialize(Viewer)

	r.SetOwner("docs/old-name.md", "alice@example.com")
	r.SetUserPermission("docs/old-name.md", "bob@example.com", ContentManager)

	r.RenamePath("docs/old-name.md", "docs/new-name.md")

	assert.True(t, r.IsOwner("alice@example.com", "docs/new-name.md"))
	assert.False(t, r.IsOwner("alice@example.com", "docs/old-name.md"))

	level := r.EffectiveLevel("bob@example.com", "docs/new-name.md")
	assert.Equal(t, ContentManager, level)
}

func TestResolver_RenamePath_Folder(t *testing.T) {
	r := Initialize(Viewer)

	r.SetOwner("docs/", "alice@example.com")
	r.SetOwner("docs/readme.md", "alice@example.com")
	r.SetOwner("docs/sub/", "alice@example.com")

	r.RenamePath("docs/", "documentation/")

	assert.True(t, r.IsOwner("alice@example.com", "documentation/"))
	assert.True(t, r.IsOwner("alice@example.com", "documentation/readme.md"))
	assert.True(t, r.IsOwner("alice@example.com", "documentation/sub/"))

	assert.False(t, r.IsOwner("alice@example.com", "docs/"))
	assert.False(t, r.IsOwner("alice@example.com", "docs/readme.md"))
}

func TestResolver_ReplaceUsers(t *testing.T) {
	r := Initialize(Viewer)

	r.SetOwner("docs/", "admin@example.com")
	r.SetUserPermission("docs/", "alice@example.com", ContentManager)
	r.SetUserPermission("docs/", "bob@example.com", Manager)

	r.ReplaceUsers("docs/", map[string]Level{
		"charlie@example.com": Contributor,
	})

	level := r.EffectiveLevel("charlie@example.com", "docs/")
	assert.Equal(t, Contributor, level)

	level = r.EffectiveLevel("alice@example.com", "docs/")
	assert.NotEqual(t, ContentManager, level)
}

func TestResolver_SetGroupPermission(t *testing.T) {
	r := Initialize(Viewer)

	err := r.AddGroup("editors", []string{"alice@example.com"})
	require.NoError(t, err)
	r.SetOwner("docs/", "admin@example.com")
	r.SetGroupPermission("docs/", "editors", ContentManager)

	level := r.EffectiveLevel("alice@example.com", "docs/")
	assert.Equal(t, ContentManager, level)
}

func TestResolver_RemoveUserPermission(t *testing.T) {
	r := Initialize(Viewer)

	r.SetOwner("docs/", "admin@example.com")
	r.SetUserPermission("docs/", "alice@example.com", Manager)

	r.RemoveUserPermission("docs/", "alice@example.com")

	level := r.EffectiveLevel("alice@example.com", "docs/")
	assert.NotEqual(t, Manager, level)
}

func TestResolver_RemoveGroupPermission(t *testing.T) {
	r := Initialize(Viewer)

	err := r.AddGroup("team", []string{"alice@example.com"})
	require.NoError(t, err)
	r.SetOwner("docs/", "admin@example.com")
	r.SetGroupPermission("docs/", "team", Manager)
	r.RemoveGroupPermission("docs/", "team")

	level := r.EffectiveLevel("alice@example.com", "docs/")
	assert.NotEqual(t, Manager, level)
}

func TestResolver_MarshalRoundTrip(t *testing.T) {
	r := Initialize(Contributor)

	err := r.AddGroup("editors", []string{"alice@example.com"})
	require.NoError(t, err)
	r.SetOwner("docs/", "admin@example.com")
	r.SetUserPermission("docs/", "bob@example.com", Manager)
	r.SetDefault("docs/", Viewer)

	data, err := Marshal(r)
	require.NoError(t, err)

	r2, err := LoadFromData(data)
	require.NoError(t, err)

	assert.True(t, r2.IsOwner("admin@example.com", "docs/"))
	assert.Equal(t, Manager, r2.EffectiveLevel("bob@example.com", "docs/"))
	groups := r2.GetGroups()
	assert.Contains(t, groups, "editors")
}
