package git

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/christianfischer/md-wiki-server/internal/repo"
	log "github.com/sirupsen/logrus"
)

type Helper struct {
	repoPath   string
	branch     string
	lfsEnabled bool
	client     *Client
	mu         sync.Mutex
}

// RepoPath returns the local filesystem path of the repository.
func (h *Helper) RepoPath() string {
	return h.repoPath
}

// Stat returns metadata about a path relative to the repo root.
func (h *Helper) Stat(path string) (repo.FileInfo, error) {
	info, err := os.Stat(filepath.Join(h.repoPath, path))
	if err != nil {
		return repo.FileInfo{}, err
	}
	return repo.FileInfo{
		Name:  info.Name(),
		IsDir: info.IsDir(),
		Size:  info.Size(),
	}, nil
}

func NewHelper(repoPath string, repoUrl string, branch string, username string, authToken string, lfsEnabled bool) *Helper {
	if !filepath.IsAbs(repoPath) {
		log.Fatalf("git helper: repoPath must be absolute, got: %s", repoPath)
	}

	log.WithFields(log.Fields{"repoPath": repoPath, "repoUrl": repoUrl, "branch": branch, "username": username, "lfs": lfsEnabled}).Info("new Git helper")

	client := NewGitClient(repoPath, repoUrl, username, authToken, lfsEnabled)
	return &Helper{
		repoPath:   repoPath,
		branch:     branch,
		lfsEnabled: lfsEnabled,
		client:     client,
	}
}

// CommitAndPushSync stages, commits, and pushes synchronously.
// It serialises git operations per repository via a mutex.
func (h *Helper) CommitAndPushSync(commitMessage string, authorName string, authorEmail string) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	log.WithFields(log.Fields{"message": commitMessage, "author": authorName, "email": authorEmail}).Info("git commit and push started")

	log.Info("git staging all changes")
	if err := h.client.AddAll(); err != nil {
		return fmt.Errorf("git add all: %w", err)
	}

	if !h.client.HasStagedChanges() {
		log.Info("nothing to commit, skipping")
		return nil
	}

	log.WithField("message", commitMessage).Info("git committing")
	if err := h.client.Commit(commitMessage, authorName, authorEmail); err != nil {
		return fmt.Errorf("git commit: %w", err)
	}

	log.Info("git pushing")
	if err := h.client.Push(); err != nil {
		return fmt.Errorf("git push: %w", err)
	}

	log.WithField("message", commitMessage).Info("git commit and push completed")
	return nil
}

// CommitAndPush stages, commits, and pushes in the background.
// It serialises git operations per repository via a mutex.
func (h *Helper) CommitAndPush(commitMessage string, authorName string, authorEmail string) {
	go func() {
		if err := h.CommitAndPushSync(commitMessage, authorName, authorEmail); err != nil {
			log.WithError(err).Error("background commit and push failed")
		}
	}()
}

// CheckReadAccess verifies the remote is reachable with the configured credentials.
func (h *Helper) CheckReadAccess() error {
	return h.client.CheckReadAccess()
}

// CheckWriteAccess verifies push access to the remote (requires an existing clone).
func (h *Helper) CheckWriteAccess() error {
	return h.client.CheckWriteAccess()
}

// CloneRepo deletes the local repo if it exists and clones fresh
func (h *Helper) CloneRepo() error {
	log.WithField("branch", h.branch).Info("cloning repository")

	// Delete existing repo if it exists. Retry because an ongoing LFS
	// download may still be writing files.
	for attempt := 0; attempt < 3; attempt++ {
		if err := os.RemoveAll(h.repoPath); err != nil {
			if attempt < 2 {
				log.WithError(err).Warn("remove failed, retrying in 2s")
				time.Sleep(2 * time.Second)
				continue
			}
			return fmt.Errorf("remove %s: %w", h.repoPath, err)
		}
		break
	}

	var err error
	if h.branch != "" {
		err = h.client.CloneWithBranch(h.repoPath, h.branch)
	} else {
		err = h.client.Clone(h.repoPath)
	}
	if err != nil {
		return err
	}

	// Set up git exclude for .DS_Store so it never gets staged
	excludeDir := filepath.Join(h.repoPath, ".git", "info")
	if err := os.MkdirAll(excludeDir, 0755); err == nil {
		excludeFile := filepath.Join(excludeDir, "exclude")
		if data, err := os.ReadFile(excludeFile); err != nil || !strings.Contains(string(data), ".DS_Store") {
			f, err := os.OpenFile(excludeFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
			if err == nil {
				f.WriteString(".DS_Store\n")
				f.Close()
			}
		}
	}

	if h.lfsEnabled {
		if err := h.client.SetupLFS(); err != nil {
			log.WithError(err).Warn("failed to set up Git LFS, continuing")
		}
	}

	// Always pull LFS objects — the repo may use LFS upstream even if
	// lfsEnabled is false. lfsEnabled only controls tracking new files.
	if err := h.client.LFSPull(); err != nil {
		log.WithError(err).Warn("git lfs pull after clone failed, continuing")
	}

	return nil
}

// AddFile writes content to a file in a subfolder on disk.
func (h *Helper) AddFile(subfolder string, filename string, content []byte) error {
	log.WithFields(log.Fields{"subfolder": subfolder, "filename": filename}).Info("adding file")
	folderPath := filepath.Join(h.repoPath, subfolder)
	if err := os.MkdirAll(folderPath, 0755); err != nil {
		return err
	}

	filePath := filepath.Join(folderPath, filename)
	return os.WriteFile(filePath, content, 0644)
}

// DeleteFile deletes a file from a subfolder on disk.
func (h *Helper) DeleteFile(subfolder string, filename string) error {
	log.WithFields(log.Fields{"subfolder": subfolder, "filename": filename}).Info("deleting file")
	filePath := filepath.Join(h.repoPath, subfolder, filename)
	return os.Remove(filePath)
}

// DeleteFolder deletes a folder and all files within it on disk.
func (h *Helper) DeleteFolder(subfolder string) error {
	log.WithField("subfolder", subfolder).Info("deleting folder")
	folderPath := filepath.Join(h.repoPath, subfolder)
	return os.RemoveAll(folderPath)
}

// RenameFolder renames a folder on disk.
func (h *Helper) RenameFolder(oldSubfolder string, newSubfolder string) error {
	log.WithFields(log.Fields{"from": oldSubfolder, "to": newSubfolder}).Info("renaming folder")
	oldPath := filepath.Join(h.repoPath, oldSubfolder)
	newPath := filepath.Join(h.repoPath, newSubfolder)

	// Ensure parent of destination exists
	if err := os.MkdirAll(filepath.Dir(newPath), 0755); err != nil {
		return err
	}

	return os.Rename(oldPath, newPath)
}

// MoveFile moves a file from one subfolder to another on disk.
func (h *Helper) MoveFile(oldSubfolder string, oldFilename string, newSubfolder string, newFilename string) error {
	log.WithFields(log.Fields{"from": filepath.Join(oldSubfolder, oldFilename), "to": filepath.Join(newSubfolder, newFilename)}).Info("moving file")
	// Create destination folder if needed
	newFolderPath := filepath.Join(h.repoPath, newSubfolder)
	if err := os.MkdirAll(newFolderPath, 0755); err != nil {
		return err
	}

	oldPath := filepath.Join(h.repoPath, oldSubfolder, oldFilename)
	newPath := filepath.Join(h.repoPath, newSubfolder, newFilename)
	return os.Rename(oldPath, newPath)
}

// ListFolders returns all folder names within a subfolder.
func (h *Helper) ListFolders(subfolder string) ([]string, error) {
	log.WithField("subfolder", subfolder).Info("listing folders")
	folderPath := filepath.Join(h.repoPath, subfolder)

	entries, err := os.ReadDir(folderPath)
	if err != nil {
		return nil, err
	}

	var folders []string
	for _, entry := range entries {
		if entry.IsDir() {
			folders = append(folders, entry.Name())
		}
	}

	return folders, nil
}

// ListFiles returns all file names within a subfolder.
func (h *Helper) ListFiles(subfolder string) ([]string, error) {
	log.WithField("subfolder", subfolder).Info("listing files")
	folderPath := filepath.Join(h.repoPath, subfolder)

	entries, err := os.ReadDir(folderPath)
	if err != nil {
		return nil, err
	}

	var files []string
	for _, entry := range entries {
		if !entry.IsDir() {
			files = append(files, entry.Name())
		}
	}

	return files, nil
}

// GetFile returns the content of a file.
func (h *Helper) GetFile(subfolder string, filename string) ([]byte, error) {
	filePath := filepath.Join(h.repoPath, subfolder, filename)
	return os.ReadFile(filePath)
}

// listAllFiles returns all file paths (relative to repo root) recursively within a subfolder.
func (h *Helper) listAllFiles(subfolder string) ([]string, error) {
	folderPath := filepath.Join(h.repoPath, subfolder)
	var files []string
	err := filepath.Walk(folderPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			relPath, _ := filepath.Rel(h.repoPath, path)
			files = append(files, relPath)
		}
		return nil
	})
	return files, err
}

// GetFileHistory returns the change history for a file.
func (h *Helper) GetFileHistory(subfolder string, filename string) ([]FileChange, error) {
	log.WithFields(log.Fields{"subfolder": subfolder, "filename": filename}).Debug("getting file history")
	filePath := filepath.Join(subfolder, filename)
	return h.client.GetFileHistory(filePath)
}

// GetFileAtCommit returns the content of a file at a specific commit.
func (h *Helper) GetFileAtCommit(commitHash string, subfolder string, filename string) (string, error) {
	log.WithFields(log.Fields{"commit": commitHash, "subfolder": subfolder, "filename": filename}).Debug("getting file at commit")
	filePath := filepath.Join(subfolder, filename)
	return h.client.GetFileAtCommit(commitHash, filePath)
}

// GetFileDiffWithCommit returns a diff between a specific commit and HEAD for a file.
func (h *Helper) GetFileDiffWithCommit(commitHash string, subfolder string, filename string) (string, error) {
	log.WithFields(log.Fields{"commit": commitHash, "subfolder": subfolder, "filename": filename}).Debug("getting file diff with commit")
	filePath := filepath.Join(subfolder, filename)
	return h.client.GetFileDiffWithCommit(commitHash, filePath)
}

// GetFileCommitHash returns the most recent commit hash for a file.
func (h *Helper) GetFileCommitHash(subfolder string, filename string) (string, error) {
	log.WithFields(log.Fields{"subfolder": subfolder, "filename": filename}).Debug("getting file commit hash")
	filePath := filepath.Join(subfolder, filename)
	return h.client.GetFileCommitHash(filePath)
}

type FileEntry struct {
	Name  string
	Path  string
	IsDir bool
	Size  int64
}

func (h *Helper) ListEntries(subfolder string) ([]FileEntry, error) {
	log.WithField("subfolder", subfolder).Info("listing entries")
	folderPath := filepath.Join(h.repoPath, subfolder)

	var entries []FileEntry
	emptyDirs := map[string]bool{}

	err := filepath.Walk(folderPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() && info.Name() == ".git" {
			return filepath.SkipDir
		}
		if info.IsDir() && path != folderPath {
			emptyDirs[path] = true
			return nil
		}
		if !info.IsDir() && info.Name() != ".gitkeep" && info.Name() != ".DS_Store" {
			// Mark parent as non-empty
			delete(emptyDirs, filepath.Dir(path))
			relPath, _ := filepath.Rel(folderPath, path)
			dir := filepath.Dir(relPath)
			if dir == "." {
				dir = ""
			}
			entries = append(entries, FileEntry{
				Name: info.Name(),
				Path: dir,
				Size: info.Size(),
			})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	for dir := range emptyDirs {
		relPath, _ := filepath.Rel(folderPath, dir)
		parent := filepath.Dir(relPath)
		if parent == "." {
			parent = ""
		}
		entries = append(entries, FileEntry{
			Name:  filepath.Base(relPath),
			Path:  parent,
			IsDir: true,
		})
	}

	return entries, nil
}

// ReadOrder reads the .order file in a directory and returns the list of entry names.
// Returns nil, nil if no .order file exists.
func (h *Helper) ReadOrder(dirPath string) ([]string, error) {
	orderFile := filepath.Join(h.repoPath, dirPath, ".order")
	data, err := os.ReadFile(orderFile)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var order []string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			order = append(order, line)
		}
	}
	return order, nil
}

// WriteOrder writes a .order file in a directory with one entry name per line.
func (h *Helper) WriteOrder(dirPath string, order []string) error {
	content := strings.Join(order, "\n") + "\n"
	return h.AddFile(dirPath, ".order", []byte(content))
}

// HeadHash returns the SHA of the current HEAD commit.
func (h *Helper) HeadHash() (string, error) {
	return h.client.HeadHash()
}

// DiffChangedFiles returns lists of changed and deleted file paths between two commits.
func (h *Helper) DiffChangedFiles(oldHash, newHash string) (changed []string, deleted []string, err error) {
	return h.client.DiffChangedFiles(oldHash, newHash)
}

// ListFilesWithExtensions returns all file paths (relative to repo root)
// under the given subfolder whose extension matches one of exts.
// Pass subfolder "" to search the entire repository.
func (h *Helper) ListFilesWithExtensions(subfolder string, exts []string) ([]string, error) {
	allFiles, err := h.listAllFiles(subfolder)
	if err != nil {
		return nil, err
	}
	var matched []string
	for _, f := range allFiles {
		ext := strings.ToLower(filepath.Ext(f))
		for _, e := range exts {
			if ext == e {
				matched = append(matched, f)
				break
			}
		}
	}
	return matched, nil
}

// Pull fetches and merges changes from the remote repository.
func (h *Helper) Pull() error {
	log.Info("pulling from remote")
	return h.client.Pull()
}

// Status returns the worktree status of the repository.
func (h *Helper) Status() ([]StatusEntry, error) {
	return h.client.Status()
}
