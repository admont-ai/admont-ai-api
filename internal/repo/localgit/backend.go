package localgit

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/christianfischer/md-wiki-server/internal/repo"
	log "github.com/sirupsen/logrus"
)

// Backend implements repo.RepoBackend for local git repositories (no remote).
type Backend struct {
	repoPath string
	branch   string
	mu       sync.Mutex
}

// New creates a local_git backend at the given path.
func New(repoPath, branch string) *Backend {
	if branch == "" {
		branch = "main"
	}
	return &Backend{
		repoPath: repoPath,
		branch:   branch,
	}
}

func (b *Backend) Type() string          { return "local_git" }
func (b *Backend) RepoPath() string      { return b.repoPath }
func (b *Backend) SupportsHistory() bool { return true }

func (b *Backend) Stat(path string) (repo.FileInfo, error) {
	info, err := os.Stat(filepath.Join(b.repoPath, path))
	if err != nil {
		return repo.FileInfo{}, err
	}
	return repo.FileInfo{
		Name:  info.Name(),
		IsDir: info.IsDir(),
		Size:  info.Size(),
	}, nil
}

// Initialize creates the git repo if it doesn't already exist.
func (b *Backend) Initialize(_ context.Context) error {
	gitDir := filepath.Join(b.repoPath, ".git")
	if info, err := os.Stat(gitDir); err == nil && info.IsDir() {
		log.WithField("path", b.repoPath).Info("local_git: repo already initialized")
		return nil
	}

	if err := os.MkdirAll(b.repoPath, 0755); err != nil {
		return fmt.Errorf("local_git: creating directory: %w", err)
	}

	// git init
	cmd := exec.Command("git", "init", "-b", b.branch, b.repoPath)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("local_git: git init: %s: %w", strings.TrimSpace(string(out)), err)
	}

	// Set up .git/info/exclude for .DS_Store
	excludeDir := filepath.Join(b.repoPath, ".git", "info")
	if err := os.MkdirAll(excludeDir, 0755); err == nil {
		excludeFile := filepath.Join(excludeDir, "exclude")
		if f, err := os.OpenFile(excludeFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644); err == nil {
			f.WriteString(".DS_Store\n")
			f.Close()
		}
	}

	// Create initial commit so HEAD exists
	cmd = exec.Command("git", "-C", b.repoPath, "commit", "--allow-empty", "-m", "initial commit")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("local_git: initial commit: %s: %w", strings.TrimSpace(string(out)), err)
	}

	log.WithField("path", b.repoPath).Info("local_git: repository initialized")
	return nil
}

// Sync is a no-op for local_git (no remote to pull from).
func (b *Backend) Sync() error { return nil }

// --- File operations (direct filesystem, same as git.Helper) ---

func (b *Backend) GetFile(subfolder, filename string) ([]byte, error) {
	return os.ReadFile(filepath.Join(b.repoPath, subfolder, filename))
}

func (b *Backend) AddFile(subfolder, filename string, content []byte) error {
	folderPath := filepath.Join(b.repoPath, subfolder)
	if err := os.MkdirAll(folderPath, 0755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(folderPath, filename), content, 0644)
}

func (b *Backend) DeleteFile(subfolder, filename string) error {
	return os.Remove(filepath.Join(b.repoPath, subfolder, filename))
}

func (b *Backend) DeleteFolder(subfolder string) error {
	return os.RemoveAll(filepath.Join(b.repoPath, subfolder))
}

func (b *Backend) MoveFile(oldSubfolder, oldFilename, newSubfolder, newFilename string) error {
	newFolderPath := filepath.Join(b.repoPath, newSubfolder)
	if err := os.MkdirAll(newFolderPath, 0755); err != nil {
		return err
	}
	return os.Rename(
		filepath.Join(b.repoPath, oldSubfolder, oldFilename),
		filepath.Join(b.repoPath, newSubfolder, newFilename),
	)
}

func (b *Backend) RenameFolder(oldSubfolder, newSubfolder string) error {
	newPath := filepath.Join(b.repoPath, newSubfolder)
	if err := os.MkdirAll(filepath.Dir(newPath), 0755); err != nil {
		return err
	}
	return os.Rename(filepath.Join(b.repoPath, oldSubfolder), newPath)
}

func (b *Backend) ListEntries(subfolder string) ([]repo.FileEntry, error) {
	folderPath := filepath.Join(b.repoPath, subfolder)

	var entries []repo.FileEntry
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
			delete(emptyDirs, filepath.Dir(path))
			relPath, _ := filepath.Rel(folderPath, path)
			dir := filepath.Dir(relPath)
			if dir == "." {
				dir = ""
			}
			entries = append(entries, repo.FileEntry{
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
		entries = append(entries, repo.FileEntry{
			Name:  filepath.Base(relPath),
			Path:  parent,
			IsDir: true,
		})
	}

	return entries, nil
}

func (b *Backend) ListFiles(subfolder string) ([]string, error) {
	folderPath := filepath.Join(b.repoPath, subfolder)
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

func (b *Backend) ListFolders(subfolder string) ([]string, error) {
	folderPath := filepath.Join(b.repoPath, subfolder)
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

func (b *Backend) ListAllMdFiles(subfolder string) ([]string, error) {
	folderPath := filepath.Join(b.repoPath, subfolder)
	var mdFiles []string
	err := filepath.Walk(folderPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() && info.Name() == ".git" {
			return filepath.SkipDir
		}
		if !info.IsDir() && filepath.Ext(path) == ".md" {
			relPath, _ := filepath.Rel(b.repoPath, path)
			mdFiles = append(mdFiles, relPath)
		}
		return nil
	})
	return mdFiles, err
}

func (b *Backend) ReadOrder(dirPath string) ([]string, error) {
	orderFile := filepath.Join(b.repoPath, dirPath, ".order")
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

func (b *Backend) WriteOrder(dirPath string, order []string) error {
	content := strings.Join(order, "\n") + "\n"
	return b.AddFile(dirPath, ".order", []byte(content))
}

// --- Persistence (local commit, no push) ---

func (b *Backend) SaveChanges(message, authorName, authorEmail string) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	// Stage all changes
	if out, err := exec.Command("git", "-C", b.repoPath, "add", "-A").CombinedOutput(); err != nil {
		return fmt.Errorf("local_git: git add: %s: %w", strings.TrimSpace(string(out)), err)
	}

	// Check for staged changes
	if err := exec.Command("git", "-C", b.repoPath, "diff", "--cached", "--quiet").Run(); err == nil {
		// No staged changes
		return nil
	}

	// Commit
	cmd := exec.Command("git", "-C", b.repoPath, "commit", "-m", message,
		"--author", fmt.Sprintf("%s <%s>", authorName, authorEmail))
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("local_git: git commit: %s: %w", strings.TrimSpace(string(out)), err)
	}

	return nil
}

func (b *Backend) SaveChangesAsync(message, authorName, authorEmail string) {
	go func() {
		if err := b.SaveChanges(message, authorName, authorEmail); err != nil {
			log.WithError(err).Error("local_git: background commit failed")
		}
	}()
}

// ConnectRemote adds a remote and pushes existing history (for promotion to remote_git).
func (b *Backend) ConnectRemote(url, branch string) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	if branch == "" {
		branch = b.branch
	}

	// Add remote
	cmd := exec.Command("git", "-C", b.repoPath, "remote", "add", "origin", url)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("local_git: git remote add: %s: %w", strings.TrimSpace(string(out)), err)
	}

	// Push
	cmd = exec.Command("git", "-C", b.repoPath, "push", "-u", "origin", branch)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("local_git: git push: %s: %w", strings.TrimSpace(string(out)), err)
	}

	return nil
}

// --- History (full git history support) ---

func (b *Backend) GetFileHistory(subfolder, filename string) ([]repo.FileChange, error) {
	filePath := filepath.Join(subfolder, filename)
	cmd := exec.Command("git", "-C", b.repoPath, "log",
		"--format=commit:%H%nauthor:%an%nemail:%ae%ndate:%aI%nsubject:%s%n---",
		"--", filePath)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git log: %w", err)
	}

	output := strings.TrimSpace(string(out))
	if output == "" {
		return nil, nil
	}

	entries := strings.Split(output, "---")
	var changes []repo.FileChange

	for _, entry := range entries {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}

		var fc repo.FileChange
		for _, line := range strings.Split(entry, "\n") {
			switch {
			case strings.HasPrefix(line, "commit:"):
				fc.CommitHash = strings.TrimPrefix(line, "commit:")
			case strings.HasPrefix(line, "author:"):
				fc.Author = strings.TrimPrefix(line, "author:")
			case strings.HasPrefix(line, "email:"):
				fc.AuthorEmail = strings.TrimPrefix(line, "email:")
			case strings.HasPrefix(line, "date:"):
				if t, err := time.Parse(time.RFC3339, strings.TrimPrefix(line, "date:")); err == nil {
					fc.Date = t
				}
			case strings.HasPrefix(line, "subject:"):
				fc.Message = strings.TrimPrefix(line, "subject:")
			}
		}

		if fc.CommitHash != "" {
			fc.Diff = fmt.Sprintf("--- a/%s\n+++ b/%s\n", filePath, filePath)
			changes = append(changes, fc)
		}
	}

	return changes, nil
}

func (b *Backend) GetFileAtCommit(commitHash, subfolder, filename string) (string, error) {
	filePath := filepath.Join(subfolder, filename)
	out, err := exec.Command("git", "-C", b.repoPath, "show", commitHash+":"+filePath).Output()
	if err != nil {
		return "", fmt.Errorf("git show %s:%s: %w", commitHash, filePath, err)
	}
	return string(out), nil
}

func (b *Backend) GetFileDiffWithCommit(commitHash, subfolder, filename string) (string, error) {
	filePath := filepath.Join(subfolder, filename)
	out, err := exec.Command("git", "-C", b.repoPath, "diff", commitHash+"..HEAD", "--", filePath).Output()
	if err != nil {
		return "", fmt.Errorf("git diff %s..HEAD -- %s: %w", commitHash, filePath, err)
	}
	return string(out), nil
}

func (b *Backend) GetFileCommitHash(subfolder, filename string) (string, error) {
	filePath := filepath.Join(subfolder, filename)
	out, err := exec.Command("git", "-C", b.repoPath, "log", "-1", "--format=%H", "--", filePath).Output()
	if err != nil {
		return "", fmt.Errorf("git log for file %s: %w", filePath, err)
	}
	hash := strings.TrimSpace(string(out))
	if hash == "" {
		return "", fmt.Errorf("no commits found for file: %s", filePath)
	}
	return hash, nil
}

func (b *Backend) HeadHash() (string, error) {
	out, err := exec.Command("git", "-C", b.repoPath, "rev-parse", "HEAD").Output()
	if err != nil {
		return "", fmt.Errorf("git rev-parse HEAD: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

func (b *Backend) DiffChangedFiles(oldHash, newHash string) (changed []string, deleted []string, err error) {
	if oldHash == "" {
		out, err := exec.Command("git", "-C", b.repoPath, "ls-tree", "-r", "--name-only", newHash).Output()
		if err != nil {
			return nil, nil, fmt.Errorf("git ls-tree: %w", err)
		}
		for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			if line != "" {
				changed = append(changed, line)
			}
		}
		return changed, nil, nil
	}

	out, err := exec.Command("git", "-C", b.repoPath, "diff", "--name-status", oldHash+".."+newHash).Output()
	if err != nil {
		return nil, nil, fmt.Errorf("git diff --name-status: %w", err)
	}

	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 3)
		if len(parts) < 2 {
			continue
		}
		status := parts[0]
		switch {
		case status == "D":
			deleted = append(deleted, parts[1])
		case strings.HasPrefix(status, "R"):
			if len(parts) >= 3 {
				deleted = append(deleted, parts[1])
				changed = append(changed, parts[2])
			}
		default:
			changed = append(changed, parts[1])
		}
	}

	return changed, deleted, nil
}

func (b *Backend) Status() ([]repo.StatusEntry, error) {
	out, err := exec.Command("git", "-C", b.repoPath, "status", "--porcelain").Output()
	if err != nil {
		return nil, fmt.Errorf("git status: %w", err)
	}

	var entries []repo.StatusEntry
	for _, line := range strings.Split(string(out), "\n") {
		if len(line) < 4 {
			continue
		}
		staging := line[0]
		worktree := line[1]
		path := line[3:]
		entries = append(entries, repo.StatusEntry{
			Path:     path,
			Staging:  porcelainCodeString(staging),
			Worktree: porcelainCodeString(worktree),
		})
	}
	return entries, nil
}

func porcelainCodeString(code byte) string {
	switch code {
	case 'M':
		return "modified"
	case 'A':
		return "added"
	case 'D':
		return "deleted"
	case 'R':
		return "renamed"
	case 'C':
		return "copied"
	case 'U':
		return "unmerged"
	case '?':
		return "untracked"
	case ' ':
		return ""
	default:
		return string(code)
	}
}
