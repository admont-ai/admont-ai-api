package remotegit

import (
	"context"

	"github.com/christianfischer/md-wiki-server/internal/git"
	"github.com/christianfischer/md-wiki-server/internal/repo"
)

// Backend wraps an existing git.Helper to implement the RepoBackend interface.
type Backend struct {
	helper *git.Helper
}

// New creates a remote_git backend wrapping a git.Helper.
func New(helper *git.Helper) *Backend {
	return &Backend{helper: helper}
}

func (b *Backend) Type() string          { return "remote_git" }
func (b *Backend) RepoPath() string      { return b.helper.RepoPath() }
func (b *Backend) SupportsHistory() bool { return true }

func (b *Backend) Stat(path string) (repo.FileInfo, error) {
	return b.helper.Stat(path)
}

func (b *Backend) Initialize(_ context.Context) error {
	return b.helper.CloneRepo()
}

func (b *Backend) Sync() error {
	return b.helper.Pull()
}

// --- File operations ---

func (b *Backend) GetFile(subfolder, filename string) ([]byte, error) {
	return b.helper.GetFile(subfolder, filename)
}

func (b *Backend) AddFile(subfolder, filename string, content []byte) error {
	return b.helper.AddFile(subfolder, filename, content)
}

func (b *Backend) DeleteFile(subfolder, filename string) error {
	return b.helper.DeleteFile(subfolder, filename)
}

func (b *Backend) DeleteFolder(subfolder string) error {
	return b.helper.DeleteFolder(subfolder)
}

func (b *Backend) MoveFile(oldSubfolder, oldFilename, newSubfolder, newFilename string) error {
	return b.helper.MoveFile(oldSubfolder, oldFilename, newSubfolder, newFilename)
}

func (b *Backend) RenameFolder(oldSubfolder, newSubfolder string) error {
	return b.helper.RenameFolder(oldSubfolder, newSubfolder)
}

func (b *Backend) ListEntries(subfolder string) ([]repo.FileEntry, error) {
	entries, err := b.helper.ListEntries(subfolder)
	if err != nil {
		return nil, err
	}
	result := make([]repo.FileEntry, len(entries))
	for i, e := range entries {
		result[i] = repo.FileEntry{
			Name:  e.Name,
			Path:  e.Path,
			IsDir: e.IsDir,
			Size:  e.Size,
		}
	}
	return result, nil
}

func (b *Backend) ListFiles(subfolder string) ([]string, error) {
	return b.helper.ListFiles(subfolder)
}

func (b *Backend) ListFolders(subfolder string) ([]string, error) {
	return b.helper.ListFolders(subfolder)
}

func (b *Backend) ListIndexableFiles(subfolder string) ([]string, error) {
	return b.helper.ListFilesWithExtensions(subfolder, repo.IndexableExtensions)
}

func (b *Backend) ReadOrder(dirPath string) ([]string, error) {
	return b.helper.ReadOrder(dirPath)
}

func (b *Backend) WriteOrder(dirPath string, order []string) error {
	return b.helper.WriteOrder(dirPath, order)
}

// --- Persistence ---

func (b *Backend) SaveChanges(message, authorName, authorEmail string) error {
	return b.helper.CommitAndPushSync(message, authorName, authorEmail)
}

func (b *Backend) SaveChangesAsync(message, authorName, authorEmail string) {
	b.helper.CommitAndPush(message, authorName, authorEmail)
}

// --- History ---

func (b *Backend) GetFileHistory(subfolder, filename string) ([]repo.FileChange, error) {
	changes, err := b.helper.GetFileHistory(subfolder, filename)
	if err != nil {
		return nil, err
	}
	result := make([]repo.FileChange, len(changes))
	for i, c := range changes {
		result[i] = repo.FileChange{
			CommitHash:  c.CommitHash,
			Author:      c.Author,
			AuthorEmail: c.AuthorEmail,
			Date:        c.Date,
			Message:     c.Message,
			Diff:        c.Diff,
		}
	}
	return result, nil
}

func (b *Backend) GetFileAtCommit(commitHash, subfolder, filename string) (string, error) {
	return b.helper.GetFileAtCommit(commitHash, subfolder, filename)
}

func (b *Backend) GetFileDiffWithCommit(commitHash, subfolder, filename string) (string, error) {
	return b.helper.GetFileDiffWithCommit(commitHash, subfolder, filename)
}

func (b *Backend) GetFileCommitHash(subfolder, filename string) (string, error) {
	return b.helper.GetFileCommitHash(subfolder, filename)
}

func (b *Backend) HeadHash() (string, error) {
	return b.helper.HeadHash()
}

func (b *Backend) DiffChangedFiles(oldHash, newHash string) (changed []string, deleted []string, err error) {
	return b.helper.DiffChangedFiles(oldHash, newHash)
}

func (b *Backend) Status() ([]repo.StatusEntry, error) {
	entries, err := b.helper.Status()
	if err != nil {
		return nil, err
	}
	result := make([]repo.StatusEntry, len(entries))
	for i, e := range entries {
		result[i] = repo.StatusEntry{
			Path:     e.Path,
			Staging:  e.Staging,
			Worktree: e.Worktree,
		}
	}
	return result, nil
}
