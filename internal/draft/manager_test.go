package draft

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDraftFilename(t *testing.T) {
	tests := []struct {
		name     string
		filename string
		email    string
		want     string
	}{
		{"basic", "readme.md", "alice@example.com", ".readme.md.draft.alice@example.com"},
		{"uppercase email", "doc.md", "Alice@Example.COM", ".doc.md.draft.alice@example.com"},
		{"nested filename", "deep-doc.md", "user@test.com", ".deep-doc.md.draft.user@test.com"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, draftFilename(tt.filename, tt.email))
		})
	}
}

func TestMetaFilename(t *testing.T) {
	result := metaFilename("readme.md", "alice@example.com")
	assert.Equal(t, ".readme.md.draft.alice@example.com.meta", result)
}

func TestFilesystemStore_SaveAndGetDraft(t *testing.T) {
	dir := t.TempDir()
	store := NewFilesystemStore(dir)

	content := []byte("# Draft Content\nHello world")
	err := store.SaveDraft("docs", "readme.md", "alice@example.com", "Alice", "abc123", content)
	require.NoError(t, err)

	got, meta, err := store.GetDraft("docs", "readme.md", "alice@example.com")
	require.NoError(t, err)
	assert.Equal(t, content, got)
	assert.NotNil(t, meta)
	assert.Equal(t, "alice@example.com", meta.UserEmail)
	assert.Equal(t, "Alice", meta.UserName)
	assert.Equal(t, "abc123", meta.BaseCommitHash)
	assert.Equal(t, filepath.Join("docs", "readme.md"), meta.OriginalFile)
}

func TestFilesystemStore_HasDraft(t *testing.T) {
	dir := t.TempDir()
	store := NewFilesystemStore(dir)

	assert.False(t, store.HasDraft("", "readme.md", "alice@example.com"))

	err := store.SaveDraft("", "readme.md", "alice@example.com", "Alice", "abc", []byte("content"))
	require.NoError(t, err)

	assert.True(t, store.HasDraft("", "readme.md", "alice@example.com"))
	assert.False(t, store.HasDraft("", "readme.md", "bob@example.com"))
}

func TestFilesystemStore_DeleteDraft(t *testing.T) {
	dir := t.TempDir()
	store := NewFilesystemStore(dir)

	err := store.SaveDraft("", "readme.md", "alice@example.com", "Alice", "abc", []byte("content"))
	require.NoError(t, err)
	assert.True(t, store.HasDraft("", "readme.md", "alice@example.com"))

	err = store.DeleteDraft("", "readme.md", "alice@example.com")
	require.NoError(t, err)
	assert.False(t, store.HasDraft("", "readme.md", "alice@example.com"))
}

func TestFilesystemStore_DeleteDraft_NonExistent(t *testing.T) {
	dir := t.TempDir()
	store := NewFilesystemStore(dir)

	err := store.DeleteDraft("", "nonexistent.md", "alice@example.com")
	assert.NoError(t, err)
}

func TestFilesystemStore_GetDraft_NonExistent(t *testing.T) {
	dir := t.TempDir()
	store := NewFilesystemStore(dir)

	_, _, err := store.GetDraft("", "nonexistent.md", "alice@example.com")
	assert.Error(t, err)
}

func TestFilesystemStore_EmailCaseInsensitive(t *testing.T) {
	dir := t.TempDir()
	store := NewFilesystemStore(dir)

	err := store.SaveDraft("", "readme.md", "Alice@Example.COM", "Alice", "abc", []byte("content"))
	require.NoError(t, err)

	assert.True(t, store.HasDraft("", "readme.md", "alice@example.com"))

	got, _, err := store.GetDraft("", "readme.md", "alice@example.com")
	require.NoError(t, err)
	assert.Equal(t, []byte("content"), got)
}

func TestFilesystemStore_UpdateDraft_PreservesCreatedAt(t *testing.T) {
	dir := t.TempDir()
	store := NewFilesystemStore(dir)

	err := store.SaveDraft("", "readme.md", "alice@example.com", "Alice", "abc", []byte("v1"))
	require.NoError(t, err)

	_, meta1, err := store.GetDraft("", "readme.md", "alice@example.com")
	require.NoError(t, err)
	createdAt := meta1.CreatedAt

	err = store.SaveDraft("", "readme.md", "alice@example.com", "Alice", "def", []byte("v2"))
	require.NoError(t, err)

	_, meta2, err := store.GetDraft("", "readme.md", "alice@example.com")
	require.NoError(t, err)
	assert.Equal(t, createdAt, meta2.CreatedAt)
	assert.True(t, meta2.UpdatedAt.After(meta1.UpdatedAt) || meta2.UpdatedAt.Equal(meta1.UpdatedAt))
}

func TestFilesystemStore_SubfolderDrafts(t *testing.T) {
	dir := t.TempDir()
	store := NewFilesystemStore(dir)

	err := store.SaveDraft("docs/getting-started", "install.md", "alice@example.com", "Alice", "abc", []byte("install guide"))
	require.NoError(t, err)

	assert.True(t, store.HasDraft("docs/getting-started", "install.md", "alice@example.com"))
	assert.False(t, store.HasDraft("docs", "install.md", "alice@example.com"))
}

func TestFilesystemStore_EnsureGitignore_CreatesFile(t *testing.T) {
	dir := t.TempDir()
	store := NewFilesystemStore(dir)

	err := store.EnsureGitignore()
	require.NoError(t, err)

	content, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
	require.NoError(t, err)
	assert.Contains(t, string(content), "**/.drafts")
}

func TestFilesystemStore_EnsureGitignore_AlreadyPresent(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("node_modules\n**/.drafts\n"), 0644)

	store := NewFilesystemStore(dir)
	err := store.EnsureGitignore()
	require.NoError(t, err)

	content, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
	require.NoError(t, err)
	assert.Equal(t, 1, strings.Count(string(content), ".drafts"))
}

func TestFilesystemStore_EnsureGitignore_AppendsNewline(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("node_modules"), 0644)

	store := NewFilesystemStore(dir)
	err := store.EnsureGitignore()
	require.NoError(t, err)

	content, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
	require.NoError(t, err)
	assert.Contains(t, string(content), "node_modules\n**/.drafts")
}

func TestFilesystemStore_CleanEmptyDraftsDir(t *testing.T) {
	dir := t.TempDir()
	store := NewFilesystemStore(dir)

	err := store.SaveDraft("", "readme.md", "alice@example.com", "Alice", "abc", []byte("v1"))
	require.NoError(t, err)

	draftsDir := filepath.Join(dir, ".drafts")
	_, err = os.Stat(draftsDir)
	assert.NoError(t, err)

	err = store.DeleteDraft("", "readme.md", "alice@example.com")
	require.NoError(t, err)

	_, err = os.Stat(draftsDir)
	assert.True(t, os.IsNotExist(err), "empty .drafts dir should be cleaned up")
}

func TestFilesystemStore_ListDraftOwners_Empty(t *testing.T) {
	dir := t.TempDir()
	store := NewFilesystemStore(dir)

	owners, err := store.ListDraftOwners("", "readme.md")
	require.NoError(t, err)
	assert.Empty(t, owners)
}

func TestFilesystemStore_ListDraftOwners_SingleOwner(t *testing.T) {
	dir := t.TempDir()
	store := NewFilesystemStore(dir)
	require.NoError(t, store.SaveDraft("docs", "readme.md", "alice@example.com", "Alice", "abc", []byte("v1")))

	owners, err := store.ListDraftOwners("docs", "readme.md")
	require.NoError(t, err)
	require.Len(t, owners, 1)
	assert.Equal(t, "alice@example.com", owners[0].UserEmail)
}

func TestFilesystemStore_ListDraftOwners_MultipleOwners_ExcludesOtherFiles(t *testing.T) {
	dir := t.TempDir()
	store := NewFilesystemStore(dir)
	require.NoError(t, store.SaveDraft("docs", "readme.md", "alice@example.com", "Alice", "abc", []byte("v1")))
	require.NoError(t, store.SaveDraft("docs", "readme.md", "bob@example.com", "Bob", "abc", []byte("v2")))
	require.NoError(t, store.SaveDraft("docs", "other.md", "carol@example.com", "Carol", "abc", []byte("v3")))

	owners, err := store.ListDraftOwners("docs", "readme.md")
	require.NoError(t, err)
	require.Len(t, owners, 2)
	emails := []string{owners[0].UserEmail, owners[1].UserEmail}
	assert.ElementsMatch(t, []string{"alice@example.com", "bob@example.com"}, emails)
}

func TestFilesystemStore_ListDraftOwners_IgnoresMetaSidecars(t *testing.T) {
	dir := t.TempDir()
	store := NewFilesystemStore(dir)
	require.NoError(t, store.SaveDraft("", "readme.md", "alice@example.com", "Alice", "abc", []byte("v1")))

	owners, err := store.ListDraftOwners("", "readme.md")
	require.NoError(t, err)
	require.Len(t, owners, 1) // not 2 (draft + .meta counted separately)
}

func TestManager_OtherUsersDraft_NoDrafts(t *testing.T) {
	mgr := NewManager(t.TempDir())
	other, err := mgr.OtherUsersDraft("", "readme.md", "alice@example.com")
	require.NoError(t, err)
	assert.Nil(t, other)
}

func TestManager_OtherUsersDraft_OnlyCallersOwnDraft(t *testing.T) {
	mgr := NewManager(t.TempDir())
	require.NoError(t, mgr.SaveDraft("", "readme.md", "alice@example.com", "Alice", "abc", []byte("v1")))

	// The critical regression: a user must never be locked out by their own draft.
	other, err := mgr.OtherUsersDraft("", "readme.md", "alice@example.com")
	require.NoError(t, err)
	assert.Nil(t, other)
}

func TestManager_OtherUsersDraft_AnotherUsersDraft(t *testing.T) {
	mgr := NewManager(t.TempDir())
	require.NoError(t, mgr.SaveDraft("", "readme.md", "bob@example.com", "Bob", "abc", []byte("v1")))

	other, err := mgr.OtherUsersDraft("", "readme.md", "alice@example.com")
	require.NoError(t, err)
	require.NotNil(t, other)
	assert.Equal(t, "bob@example.com", other.UserEmail)
}

func TestManager_OtherUsersDraft_MultipleOthers_ReturnsMostRecentlyUpdated(t *testing.T) {
	mgr := NewManager(t.TempDir())
	require.NoError(t, mgr.SaveDraft("", "readme.md", "bob@example.com", "Bob", "abc", []byte("v1")))
	time.Sleep(10 * time.Millisecond)
	require.NoError(t, mgr.SaveDraft("", "readme.md", "carol@example.com", "Carol", "abc", []byte("v2")))

	other, err := mgr.OtherUsersDraft("", "readme.md", "alice@example.com")
	require.NoError(t, err)
	require.NotNil(t, other)
	assert.Equal(t, "carol@example.com", other.UserEmail)
}

func TestManager_OtherUsersDraft_CaseInsensitiveEmailMatch(t *testing.T) {
	mgr := NewManager(t.TempDir())
	require.NoError(t, mgr.SaveDraft("", "readme.md", "Alice@Example.com", "Alice", "abc", []byte("v1")))

	other, err := mgr.OtherUsersDraft("", "readme.md", "alice@example.com")
	require.NoError(t, err)
	assert.Nil(t, other)
}

func TestManager_OtherUsersDraftUnderFolder_Empty(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "docs"), 0755))
	mgr := NewManager(dir)

	other, path, err := mgr.OtherUsersDraftUnderFolder("docs", "alice@example.com")
	require.NoError(t, err)
	assert.Nil(t, other)
	assert.Empty(t, path)
}

func TestManager_OtherUsersDraftUnderFolder_NestedFile(t *testing.T) {
	dir := t.TempDir()
	mgr := NewManager(dir)
	require.NoError(t, mgr.SaveDraft("docs/guides", "install.md", "bob@example.com", "Bob", "abc", []byte("v1")))

	other, path, err := mgr.OtherUsersDraftUnderFolder("docs", "alice@example.com")
	require.NoError(t, err)
	require.NotNil(t, other)
	assert.Equal(t, "bob@example.com", other.UserEmail)
	assert.Equal(t, filepath.Join("docs/guides", "install.md"), path)
}

func TestManager_OtherUsersDraftUnderFolder_ExcludesCallersOwnDraft(t *testing.T) {
	dir := t.TempDir()
	mgr := NewManager(dir)
	require.NoError(t, mgr.SaveDraft("docs", "install.md", "alice@example.com", "Alice", "abc", []byte("v1")))

	other, _, err := mgr.OtherUsersDraftUnderFolder("docs", "alice@example.com")
	require.NoError(t, err)
	assert.Nil(t, other)
}

func TestManager_OtherUsersDraftUnderFolder_OutsideFolderNotMatched(t *testing.T) {
	dir := t.TempDir()
	mgr := NewManager(dir)
	require.NoError(t, mgr.SaveDraft("other", "install.md", "bob@example.com", "Bob", "abc", []byte("v1")))

	other, _, err := mgr.OtherUsersDraftUnderFolder("docs", "alice@example.com")
	require.NoError(t, err)
	assert.Nil(t, other)
}

func TestManager_DelegatesToStore(t *testing.T) {
	dir := t.TempDir()
	mgr := NewManager(dir)

	err := mgr.SaveDraft("", "test.md", "user@example.com", "User", "sha1", []byte("hello"))
	require.NoError(t, err)

	assert.True(t, mgr.HasDraft("", "test.md", "user@example.com"))

	content, meta, err := mgr.GetDraft("", "test.md", "user@example.com")
	require.NoError(t, err)
	assert.Equal(t, []byte("hello"), content)
	assert.Equal(t, "user@example.com", meta.UserEmail)

	err = mgr.DeleteDraft("", "test.md", "user@example.com")
	require.NoError(t, err)
	assert.False(t, mgr.HasDraft("", "test.md", "user@example.com"))
}
