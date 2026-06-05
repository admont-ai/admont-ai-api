package draft

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

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
