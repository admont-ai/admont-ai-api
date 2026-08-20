// Package repotest provides an in-memory fake of repo.RepoBackend for tests
// that exercise repoactions, MCP tool handlers, or the chat-agent tool loop
// without a real git/S3 backend.
package repotest

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/christianfischer/md-wiki-server/internal/repo"
)

// SaveCall records one SaveChanges(Async) invocation for assertions.
type SaveCall struct {
	Message     string
	AuthorName  string
	AuthorEmail string
}

// FakeBackend is a map-backed, in-memory repo.RepoBackend. SaveChangesAsync
// is synchronous here (it just records the call) so tests don't need to poll.
type FakeBackend struct {
	mu    sync.Mutex
	files map[string][]byte // full relative path ("dir/file.md" or "file.md") -> content

	supportsHistory bool
	history         []repo.FileChange // synthetic history returned for any file, newest first
	commitContent   map[string]string // "commitHash|path" -> content, set via SeedCommitContent

	SaveCalls []SaveCall
}

// NewFakeBackend returns an empty FakeBackend with history support enabled
// and one synthetic history entry per file (so history-dependent tools have
// something to assert on).
func NewFakeBackend() *FakeBackend {
	return &FakeBackend{
		files:           make(map[string][]byte),
		supportsHistory: true,
		history: []repo.FileChange{
			{
				CommitHash:  "abc123",
				Author:      "Test Author",
				AuthorEmail: "author@example.com",
				Date:        time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC),
				Message:     "initial commit",
			},
		},
	}
}

// SetSupportsHistory toggles SupportsHistory() for backends that simulate a
// non-git store (e.g. S3), which returns repo.ErrNotSupported from the
// history-related methods.
func (f *FakeBackend) SetSupportsHistory(v bool) *FakeBackend {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.supportsHistory = v
	return f
}

// Seed pre-populates a file, bypassing AddFile's bookkeeping — for test setup.
func (f *FakeBackend) Seed(fullPath string, content []byte) *FakeBackend {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.files[fullPath] = content
	return f
}

func joinPath(subfolder, filename string) string {
	if subfolder == "" || subfolder == "." {
		return filename
	}
	return filepath.Join(subfolder, filename)
}

func (f *FakeBackend) Type() string          { return "fake" }
func (f *FakeBackend) RepoPath() string      { return "/fake" }
func (f *FakeBackend) SupportsHistory() bool { return f.supportsHistory }

func (f *FakeBackend) Stat(path string) (repo.FileInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	path = strings.TrimSuffix(path, "/")
	if content, ok := f.files[path]; ok {
		return repo.FileInfo{Name: filepath.Base(path), IsDir: false, Size: int64(len(content))}, nil
	}
	prefix := path + "/"
	for p := range f.files {
		if strings.HasPrefix(p, prefix) || path == "" {
			return repo.FileInfo{Name: filepath.Base(path), IsDir: true}, nil
		}
	}
	return repo.FileInfo{}, repo.ErrNotSupported
}

func (f *FakeBackend) Initialize(ctx context.Context) error { return nil }
func (f *FakeBackend) Sync() error                          { return nil }

func (f *FakeBackend) GetFile(subfolder, filename string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	content, ok := f.files[joinPath(subfolder, filename)]
	if !ok {
		return nil, repo.ErrNotSupported
	}
	return content, nil
}

func (f *FakeBackend) AddFile(subfolder, filename string, content []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.files[joinPath(subfolder, filename)] = content
	return nil
}

func (f *FakeBackend) DeleteFile(subfolder, filename string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	p := joinPath(subfolder, filename)
	if _, ok := f.files[p]; !ok {
		return repo.ErrNotSupported
	}
	delete(f.files, p)
	return nil
}

func (f *FakeBackend) DeleteFolder(subfolder string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	prefix := subfolder + "/"
	for p := range f.files {
		if strings.HasPrefix(p, prefix) {
			delete(f.files, p)
		}
	}
	return nil
}

func (f *FakeBackend) MoveFile(oldSubfolder, oldFilename, newSubfolder, newFilename string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	oldPath := joinPath(oldSubfolder, oldFilename)
	content, ok := f.files[oldPath]
	if !ok {
		return repo.ErrNotSupported
	}
	delete(f.files, oldPath)
	f.files[joinPath(newSubfolder, newFilename)] = content
	return nil
}

func (f *FakeBackend) RenameFolder(oldSubfolder, newSubfolder string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	prefix := oldSubfolder + "/"
	moved := make(map[string][]byte)
	for p, content := range f.files {
		if strings.HasPrefix(p, prefix) {
			moved[newSubfolder+"/"+strings.TrimPrefix(p, prefix)] = content
			delete(f.files, p)
		} else if p == oldSubfolder {
			moved[newSubfolder] = content
			delete(f.files, p)
		}
	}
	for p, content := range moved {
		f.files[p] = content
	}
	return nil
}

func (f *FakeBackend) ListEntries(subfolder string) ([]repo.FileEntry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	prefix := ""
	if subfolder != "" && subfolder != "." {
		prefix = subfolder + "/"
	}
	seenDirs := map[string]bool{}
	var entries []repo.FileEntry
	for p, content := range f.files {
		if !strings.HasPrefix(p, prefix) {
			continue
		}
		rest := strings.TrimPrefix(p, prefix)
		if rest == "" {
			continue
		}
		if idx := strings.Index(rest, "/"); idx >= 0 {
			dir := rest[:idx]
			if !seenDirs[dir] {
				seenDirs[dir] = true
				entries = append(entries, repo.FileEntry{Name: dir, Path: subfolder, IsDir: true})
			}
			continue
		}
		entries = append(entries, repo.FileEntry{Name: rest, Path: subfolder, IsDir: false, Size: int64(len(content))})
	}
	return entries, nil
}

func (f *FakeBackend) ListFiles(subfolder string) ([]string, error) {
	entries, _ := f.ListEntries(subfolder)
	var out []string
	for _, e := range entries {
		if !e.IsDir {
			out = append(out, e.Name)
		}
	}
	return out, nil
}

func (f *FakeBackend) ListFolders(subfolder string) ([]string, error) {
	entries, _ := f.ListEntries(subfolder)
	var out []string
	for _, e := range entries {
		if e.IsDir {
			out = append(out, e.Name)
		}
	}
	return out, nil
}

func (f *FakeBackend) ListIndexableFiles(subfolder string) ([]string, error) {
	return f.ListFiles(subfolder)
}

func (f *FakeBackend) ReadOrder(dirPath string) ([]string, error)      { return nil, nil }
func (f *FakeBackend) WriteOrder(dirPath string, order []string) error { return nil }

func (f *FakeBackend) SaveChanges(message, authorName, authorEmail string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.SaveCalls = append(f.SaveCalls, SaveCall{Message: message, AuthorName: authorName, AuthorEmail: authorEmail})
	return nil
}

// SaveChangesAsync is synchronous in the fake: it records the call
// immediately instead of spawning a goroutine, so tests don't need to poll.
func (f *FakeBackend) SaveChangesAsync(message, authorName, authorEmail string) {
	_ = f.SaveChanges(message, authorName, authorEmail)
}

func (f *FakeBackend) GetFileHistory(subfolder, filename string) ([]repo.FileChange, error) {
	if !f.supportsHistory {
		return nil, repo.ErrNotSupported
	}
	if _, err := f.GetFile(subfolder, filename); err != nil {
		return nil, err
	}
	return f.history, nil
}

func (f *FakeBackend) GetFileAtCommit(commitHash, subfolder, filename string) (string, error) {
	if !f.supportsHistory {
		return "", repo.ErrNotSupported
	}
	f.mu.Lock()
	if content, ok := f.commitContent[commitHash+"|"+joinPath(subfolder, filename)]; ok {
		f.mu.Unlock()
		return content, nil
	}
	f.mu.Unlock()
	content, err := f.GetFile(subfolder, filename)
	if err != nil {
		return "", err
	}
	return string(content), nil
}

// SeedCommitContent registers the content GetFileAtCommit returns for a
// specific (commitHash, path) pair, overriding the default fallback (which
// just returns the file's current content) — for tests that need a file's
// content to have genuinely diverged since a given commit.
func (f *FakeBackend) SeedCommitContent(commitHash, fullPath, content string) *FakeBackend {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.commitContent == nil {
		f.commitContent = make(map[string]string)
	}
	f.commitContent[commitHash+"|"+fullPath] = content
	return f
}

func (f *FakeBackend) GetFileDiffWithCommit(commitHash, subfolder, filename string) (string, error) {
	if !f.supportsHistory {
		return "", repo.ErrNotSupported
	}
	return "fake diff for " + joinPath(subfolder, filename) + " vs " + commitHash, nil
}

func (f *FakeBackend) GetFileCommitHash(subfolder, filename string) (string, error) {
	if !f.supportsHistory {
		return "", repo.ErrNotSupported
	}
	if _, err := f.GetFile(subfolder, filename); err != nil {
		return "", err
	}
	return f.history[0].CommitHash, nil
}

func (f *FakeBackend) HeadHash() (string, error) {
	if !f.supportsHistory {
		return "", repo.ErrNotSupported
	}
	return "head-hash", nil
}

func (f *FakeBackend) DiffChangedFiles(oldHash, newHash string) (changed []string, deleted []string, err error) {
	if !f.supportsHistory {
		return nil, nil, repo.ErrNotSupported
	}
	return nil, nil, nil
}

func (f *FakeBackend) Status() ([]repo.StatusEntry, error) {
	return nil, nil
}
