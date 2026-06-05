package git

import (
	"bufio"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"time"

	log "github.com/sirupsen/logrus"
)

// FileChange represents a single change to a file in the git history
type FileChange struct {
	CommitHash  string
	Author      string
	AuthorEmail string
	Date        time.Time
	Message     string
	Diff        string
}

func (f FileChange) String() string {
	return fmt.Sprintf("Commit: %s\nAuthor: %s <%s>\nDate: %s\nMessage: %s\n\n%s",
		f.CommitHash, f.Author, f.AuthorEmail, f.Date.Format(time.RFC3339), f.Message, f.Diff)
}

type Client struct {
	repoUrl    string
	repoPath   string
	branch     string
	username   string
	authToken  string
	lfsEnabled bool
}

func NewGitClient(repoPath string, repoUrl string, username string, authToken string, lfsEnabled bool) *Client {
	return &Client{
		repoPath:   repoPath,
		repoUrl:    repoUrl,
		username:   username,
		authToken:  authToken,
		lfsEnabled: lfsEnabled,
	}
}

// sanitizeGitOutput replaces embedded credentials in git error output with redacted versions.
func (c *Client) sanitizeGitOutput(output string) string {
	auth := c.authURL()
	if auth != c.repoUrl {
		return strings.ReplaceAll(output, auth, c.repoUrl)
	}
	return output
}

// authURL returns the repo URL with embedded credentials for CLI git.
func (c *Client) authURL() string {
	u, err := url.Parse(c.repoUrl)
	if err != nil {
		return c.repoUrl
	}
	if c.username != "" || c.authToken != "" {
		u.User = url.UserPassword(c.username, c.authToken)
	}
	return u.String()
}

// noCredentialEnv returns environment variables that prevent git from
// reading or writing the system credential store (e.g. macOS Keychain).
func noCredentialEnv() []string {
	return append(os.Environ(),
		"GIT_TERMINAL_PROMPT=0",
		"GIT_ASKPASS=",
	)
}

// CheckReadAccess verifies that the remote repository is reachable and readable
// using git ls-remote. Returns nil on success.
func (c *Client) CheckReadAccess() error {
	cmd := exec.Command("git", "-c", "credential.helper=", "ls-remote", "--exit-code", "--quiet", c.authURL())
	cmd.Env = noCredentialEnv()
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git ls-remote: %s: %w", c.sanitizeGitOutput(strings.TrimSpace(string(out))), err)
	}
	return nil
}

// CheckWriteAccess verifies push access to the remote by performing a dry-run push.
// This requires an existing local clone (repoPath must be a valid git repo).
func (c *Client) CheckWriteAccess() error {
	cmd := exec.Command("git", "-C", c.repoPath, "-c", "credential.helper=", "push", "--dry-run", c.authURL())
	cmd.Env = noCredentialEnv()
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git push --dry-run: %s: %w", c.sanitizeGitOutput(strings.TrimSpace(string(out))), err)
	}
	return nil
}

// Clone clones the repository to the specified path using the git CLI.
// LFS smudge is skipped during clone to avoid auth failures on public repos;
// LFS objects are fetched separately after clone.
func (c *Client) Clone(path string) error {
	cmd := exec.Command("git", "-c", "credential.helper=", "clone", "--progress", c.authURL(), path)
	cmd.Env = append(noCredentialEnv(), "GIT_LFS_SKIP_SMUDGE=1")
	if err := c.runClone(cmd, path); err != nil {
		return err
	}
	// Reset remote to plain URL so LFS pull doesn't use embedded credentials.
	exec.Command("git", "-C", path, "remote", "set-url", "origin", c.repoUrl).Run()
	return nil
}

// CloneWithBranch clones a specific branch to the specified path using the git CLI.
// LFS smudge is skipped during clone; LFS objects are fetched separately after clone.
func (c *Client) CloneWithBranch(path string, branch string) error {
	cmd := exec.Command("git", "-c", "credential.helper=", "clone", "--progress", "--branch", branch, "--single-branch", c.authURL(), path)
	cmd.Env = append(noCredentialEnv(), "GIT_LFS_SKIP_SMUDGE=1")
	if err := c.runClone(cmd, path); err != nil {
		return err
	}
	exec.Command("git", "-C", path, "remote", "set-url", "origin", c.repoUrl).Run()
	return nil
}

// runClone executes a git clone command, streaming its output line by line to the logger.
// Git writes progress to stderr, so we capture both stdout and stderr.
func (c *Client) runClone(cmd *exec.Cmd, path string) error {
	logger := log.WithField("repo", c.repoUrl)

	// Git writes progress and errors to stderr
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("git clone: creating stderr pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("git clone: creating stdout pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("git clone: starting command: %w", err)
	}

	// Stream both pipes, collecting lines for error reporting
	var hasErrors bool
	var checkoutFailed bool
	scanAndLog := func(r io.Reader, source string) {
		scanner := bufio.NewScanner(r)
		scanner.Split(scanCRLF)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" {
				continue
			}
			lower := strings.ToLower(line)
			if strings.Contains(lower, "clone succeeded, but checkout failed") {
				checkoutFailed = true
			}
			if strings.HasPrefix(lower, "error") || strings.HasPrefix(lower, "fatal") || strings.HasPrefix(lower, "warning") {
				hasErrors = true
				logger.WithField("source", source).Warn(line)
			} else {
				logger.WithField("source", source).Info(line)
			}
		}
	}

	go scanAndLog(stdout, "stdout")
	scanAndLog(stderr, "stderr")

	if err := cmd.Wait(); err != nil {
		if checkoutFailed {
			// Clone succeeded but checkout failed (e.g. LFS errors) — attempt recovery
			logger.Warn("clone succeeded but checkout failed, attempting git restore")
			restore := exec.Command("git", "-C", path, "restore", "--source=HEAD", ":/")
			if out, restoreErr := restore.CombinedOutput(); restoreErr != nil {
				logger.WithField("output", strings.TrimSpace(string(out))).Warn("git restore failed, continuing anyway")
			} else {
				logger.Info("git restore completed successfully")
			}
		} else {
			return fmt.Errorf("git clone failed for %s: %w", path, err)
		}
	}

	if hasErrors {
		logger.Warn("git clone completed with warnings/errors (see above)")
	}

	return nil
}

// scanCRLF is a bufio.SplitFunc that splits on \n, \r\n, or \r (bare carriage return).
// This handles git's progress output which uses \r for in-place updates.
func scanCRLF(data []byte, atEOF bool) (advance int, token []byte, err error) {
	if atEOF && len(data) == 0 {
		return 0, nil, nil
	}
	for i := 0; i < len(data); i++ {
		if data[i] == '\n' {
			return i + 1, data[:i], nil
		}
		if data[i] == '\r' {
			if i+1 < len(data) && data[i+1] == '\n' {
				return i + 2, data[:i], nil
			}
			return i + 1, data[:i], nil
		}
	}
	if atEOF {
		return len(data), data, nil
	}
	return 0, nil, nil
}

// Pull fetches and merges changes from the remote repository using the
// git CLI so that new Git LFS objects are fetched automatically via smudge filters.
func (c *Client) Pull() error {
	// Fetch using auth URL directly — avoids embedding credentials in the remote
	// which would break LFS pulls on repos where the PAT lacks LFS access.
	fetchArgs := []string{"-C", c.repoPath, "-c", "credential.helper=", "fetch", c.authURL()}
	if c.branch != "" {
		fetchArgs = append(fetchArgs, c.branch)
	}
	cmd := exec.Command("git", fetchArgs...)
	cmd.Env = noCredentialEnv()
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git fetch: %s: %w", c.sanitizeGitOutput(strings.TrimSpace(string(out))), err)
	}

	// Force-reset to remote branch, discarding local changes
	resetRef := "FETCH_HEAD"
	if c.branch != "" {
		resetRef = "origin/" + c.branch
	}
	resetCmd := exec.Command("git", "-C", c.repoPath, "reset", "--hard", resetRef)
	resetCmd.Env = append(os.Environ(), "GIT_LFS_SKIP_SMUDGE=1")
	if out, err := resetCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git reset: %s: %w", strings.TrimSpace(string(out)), err)
	}

	// Always pull LFS objects — the repo may use LFS even if lfsEnabled is false.
	// lfsEnabled only controls whether we set up LFS tracking for new files.
	if err := c.LFSPull(); err != nil {
		log.WithError(err).Warn("git lfs pull failed, continuing")
	}

	return nil
}

// AddAll stages all changes for commit.
func (c *Client) AddAll() error {
	if out, err := exec.Command("git", "-C", c.repoPath, "add", "-A").CombinedOutput(); err != nil {
		return fmt.Errorf("git add: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}

// HasStagedChanges returns true if there are staged changes ready to commit.
func (c *Client) HasStagedChanges() bool {
	err := exec.Command("git", "-C", c.repoPath, "diff", "--cached", "--quiet").Run()
	return err != nil // exit code 1 means there are differences
}

// Commit creates a commit with the staged changes.
func (c *Client) Commit(message string, authorName string, authorEmail string) error {
	cmd := exec.Command("git", "-C", c.repoPath, "commit", "-m", message,
		"--author", fmt.Sprintf("%s <%s>", authorName, authorEmail))
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git commit: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}

// Push pushes commits to the remote repository.
func (c *Client) Push() error {
	cmd := exec.Command("git", "-C", c.repoPath, "-c", "credential.helper=", "push", c.authURL())
	cmd.Env = noCredentialEnv()
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git push: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}

// lfsTrackPatterns are common binary file extensions to track via Git LFS.
var lfsTrackPatterns = []string{
	"*.png", "*.jpg", "*.jpeg", "*.gif", "*.bmp", "*.ico", "*.svg", "*.webp", "*.avif",
	"*.mp4", "*.mov", "*.avi", "*.mkv", "*.webm",
	"*.mp3", "*.wav", "*.ogg", "*.flac",
	"*.pdf", "*.zip", "*.tar.gz", "*.gz", "*.7z", "*.rar",
	"*.ttf", "*.otf", "*.woff", "*.woff2",
	"*.psd", "*.ai", "*.sketch",
}

// SetupLFS initialises Git LFS in the repo and tracks common binary patterns.
func (c *Client) SetupLFS() error {
	if out, err := exec.Command("git", "-C", c.repoPath, "lfs", "install", "--local").CombinedOutput(); err != nil {
		return fmt.Errorf("git lfs install: %s: %w", strings.TrimSpace(string(out)), err)
	}

	args := append([]string{"-C", c.repoPath, "lfs", "track"}, lfsTrackPatterns...)
	if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
		return fmt.Errorf("git lfs track: %s: %w", strings.TrimSpace(string(out)), err)
	}

	log.WithField("repo", c.repoPath).Info("Git LFS configured")
	return nil
}

// LFSPull installs LFS hooks locally (if needed) and fetches LFS objects
// without credentials so that public repos are accessed anonymously.
func (c *Client) LFSPull() error {
	// Ensure LFS is installed in this repo (needed after clone with GIT_LFS_SKIP_SMUDGE=1)
	exec.Command("git", "-C", c.repoPath, "lfs", "install", "--local").Run()

	noAuthEnv := append(os.Environ(),
		"GIT_TERMINAL_PROMPT=0",
		"GIT_CONFIG_NOSYSTEM=1", // skip system credential helpers (e.g. osxkeychain)
		"GIT_ASKPASS=/bin/echo", // return empty on any credential prompt
		"HOME="+os.TempDir(),    // skip user-level ~/.gitconfig
	)

	cmd := exec.Command("git", "-C", c.repoPath, "lfs", "pull")
	cmd.Env = noAuthEnv
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git lfs pull: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}

// GetFileHistory returns the change history for a file using git log --follow.
func (c *Client) GetFileHistory(filePath string) ([]FileChange, error) {
	cmd := exec.Command("git", "-C", c.repoPath, "log",
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
	var changes []FileChange

	for _, entry := range entries {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}

		var fc FileChange
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

func isValidCommitRef(ref string) bool {
	if len(ref) < 1 || len(ref) > 40 {
		return false
	}
	for _, c := range ref {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}

// GetFileAtCommit returns the full content of a file at a specific commit.
func (c *Client) GetFileAtCommit(commitHash string, filePath string) (string, error) {
	if !isValidCommitRef(commitHash) {
		return "", fmt.Errorf("invalid commit hash: %q", commitHash)
	}
	cmd := exec.Command("git", "-C", c.repoPath, "show", commitHash+":"+filePath)
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git show %s:%s: %w", commitHash, filePath, err)
	}
	return string(out), nil
}

// GetFileDiffWithCommit returns a unified diff of a file between a given commit and HEAD.
func (c *Client) GetFileDiffWithCommit(commitHash string, filePath string) (string, error) {
	if !isValidCommitRef(commitHash) {
		return "", fmt.Errorf("invalid commit hash: %q", commitHash)
	}
	cmd := exec.Command("git", "-C", c.repoPath, "diff", commitHash+"..HEAD", "--", filePath)
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git diff %s..HEAD -- %s: %w", commitHash, filePath, err)
	}
	return string(out), nil
}

// StatusEntry represents the working-tree status of a single file.
type StatusEntry struct {
	Path     string `json:"path"`
	Staging  string `json:"staging"`
	Worktree string `json:"worktree"`
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

// Status returns the worktree status of the repository.
func (c *Client) Status() ([]StatusEntry, error) {
	out, err := exec.Command("git", "-C", c.repoPath, "status", "--porcelain").Output()
	if err != nil {
		return nil, fmt.Errorf("git status: %w", err)
	}

	var entries []StatusEntry
	for _, line := range strings.Split(string(out), "\n") {
		if len(line) < 4 {
			continue
		}
		staging := line[0]
		worktree := line[1]
		path := line[3:]
		entries = append(entries, StatusEntry{
			Path:     path,
			Staging:  porcelainCodeString(staging),
			Worktree: porcelainCodeString(worktree),
		})
	}
	return entries, nil
}

// HeadHash returns the SHA of the current HEAD commit.
func (c *Client) HeadHash() (string, error) {
	out, err := exec.Command("git", "-C", c.repoPath, "rev-parse", "HEAD").Output()
	if err != nil {
		return "", fmt.Errorf("git rev-parse HEAD: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// DiffChangedFiles returns lists of added/modified and deleted file paths between two commits.
// If oldHash is empty, it walks the full tree at newHash (first-run scenario).
func (c *Client) DiffChangedFiles(oldHash, newHash string) (changed []string, deleted []string, err error) {
	if oldHash == "" {
		// First run: list all files at newHash
		out, err := exec.Command("git", "-C", c.repoPath, "ls-tree", "-r", "--name-only", newHash).Output()
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

	out, err := exec.Command("git", "-C", c.repoPath, "diff", "--name-status", oldHash+".."+newHash).Output()
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
			// Rename: old path is deleted, new path is changed
			if len(parts) >= 3 {
				deleted = append(deleted, parts[1])
				changed = append(changed, parts[2])
			}
		default:
			// A, M, C, T, etc.
			changed = append(changed, parts[1])
		}
	}

	return changed, deleted, nil
}

// GetFileCommitHash returns the most recent commit hash that modified the specified file.
func (c *Client) GetFileCommitHash(filePath string) (string, error) {
	out, err := exec.Command("git", "-C", c.repoPath, "log", "-1", "--format=%H", "--", filePath).Output()
	if err != nil {
		return "", fmt.Errorf("git log for file %s: %w", filePath, err)
	}
	hash := strings.TrimSpace(string(out))
	if hash == "" {
		return "", fmt.Errorf("no commits found for file: %s", filePath)
	}
	return hash, nil
}
