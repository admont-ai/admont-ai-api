package repo

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"time"
)

// ErrNotSupported is returned by backends that do not support an operation (e.g. history on S3).
var ErrNotSupported = errors.New("operation not supported by this backend")

// IndexableExtensions are the file types included in the search index.
var IndexableExtensions = []string{".md", ".xlsx", ".csv"}

// IndexableFile reports whether a file is included in the search index.
func IndexableFile(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	for _, e := range IndexableExtensions {
		if ext == e {
			return true
		}
	}
	return false
}

// RepoBackend is the interface implemented by all repository backends.
type RepoBackend interface {
	Type() string          // "remote_git", "local_git", "s3_git", "s3_store"
	RepoPath() string      // local filesystem path (clone dir, local dir, or cache dir)
	SupportsHistory() bool // false for S3

	// Stat returns metadata about a path (file or directory). The path is
	// relative to the repo root, e.g. "docs/page.md" or "docs".
	Stat(path string) (FileInfo, error)

	// Lifecycle
	Initialize(ctx context.Context) error // clone/init/setup
	Sync() error                          // pull (remote_git), no-op (others)

	// File operations
	GetFile(subfolder, filename string) ([]byte, error)
	AddFile(subfolder, filename string, content []byte) error
	DeleteFile(subfolder, filename string) error
	DeleteFolder(subfolder string) error
	MoveFile(oldSubfolder, oldFilename, newSubfolder, newFilename string) error
	RenameFolder(oldSubfolder, newSubfolder string) error
	ListEntries(subfolder string) ([]FileEntry, error)
	ListFiles(subfolder string) ([]string, error)
	ListFolders(subfolder string) ([]string, error)
	ListIndexableFiles(subfolder string) ([]string, error)
	ReadOrder(dirPath string) ([]string, error)
	WriteOrder(dirPath string, order []string) error

	// Persistence
	SaveChanges(message, authorName, authorEmail string) error
	SaveChangesAsync(message, authorName, authorEmail string)

	// History (returns ErrNotSupported for backends without git)
	GetFileHistory(subfolder, filename string) ([]FileChange, error)
	GetFileAtCommit(commitHash, subfolder, filename string) (string, error)
	GetFileDiffWithCommit(commitHash, subfolder, filename string) (string, error)
	GetFileCommitHash(subfolder, filename string) (string, error)
	HeadHash() (string, error)
	DiffChangedFiles(oldHash, newHash string) (changed []string, deleted []string, err error)
	Status() ([]StatusEntry, error)
}

// FileInfo holds basic metadata about a file or directory within a backend.
type FileInfo struct {
	Name  string
	IsDir bool
	Size  int64
}

// FileEntry represents a single file or directory entry.
type FileEntry struct {
	Name  string
	Path  string
	IsDir bool
	Size  int64
}

// FileChange represents a single change to a file in the git history.
type FileChange struct {
	CommitHash  string
	Author      string
	AuthorEmail string
	Date        time.Time
	Message     string
	Diff        string
}

// StatusEntry represents the working-tree status of a single file.
type StatusEntry struct {
	Path     string `json:"path"`
	Staging  string `json:"staging"`
	Worktree string `json:"worktree"`
}
