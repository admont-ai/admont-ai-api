// Package s3git implements a repo backend that maintains a local git repository
// with S3 as the file-sync remote. Files are read/written locally (git), and
// SaveChanges commits to git then uploads changed files to S3. Sync downloads
// changes from S3 into the local repo.
package s3git

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"mime"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/christianfischer/md-wiki-server/internal/repo"
	log "github.com/sirupsen/logrus"
)

// Backend implements repo.RepoBackend: local git repo + S3 sync.
type Backend struct {
	client   *s3.Client
	bucket   string
	prefix   string
	repoPath string
	branch   string
	mu       sync.Mutex
}

// New creates an s3_git backend.
func New(bucket, prefix, region, accessKey, secretKey, endpoint, repoPath, branch string) (*Backend, error) {
	var opts []func(*awsconfig.LoadOptions) error
	if region != "" {
		opts = append(opts, awsconfig.WithRegion(region))
	}
	if accessKey != "" && secretKey != "" {
		opts = append(opts, awsconfig.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(accessKey, secretKey, ""),
		))
	}

	cfg, err := awsconfig.LoadDefaultConfig(context.Background(), opts...)
	if err != nil {
		return nil, fmt.Errorf("s3git: loading AWS config: %w", err)
	}

	var clientOpts []func(*s3.Options)
	if endpoint != "" {
		clientOpts = append(clientOpts, func(o *s3.Options) {
			o.BaseEndpoint = aws.String(endpoint)
			o.UsePathStyle = true
		})
	}

	client := s3.NewFromConfig(cfg, clientOpts...)

	if prefix != "" && !strings.HasSuffix(prefix, "/") {
		prefix += "/"
	}
	if branch == "" {
		branch = "main"
	}

	return &Backend{
		client:   client,
		bucket:   bucket,
		prefix:   prefix,
		repoPath: repoPath,
		branch:   branch,
	}, nil
}

// S3Client returns the underlying S3 client.
func (b *Backend) S3Client() *s3.Client { return b.client }

// Bucket returns the S3 bucket name.
func (b *Backend) Bucket() string { return b.bucket }

// Prefix returns the S3 key prefix.
func (b *Backend) Prefix() string { return b.prefix }

func (b *Backend) Type() string          { return "s3_git" }
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

// Initialize creates a local git repo (if needed) and downloads files from S3.
func (b *Backend) Initialize(ctx context.Context) error {
	// Verify bucket access
	if _, err := b.client.HeadBucket(ctx, &s3.HeadBucketInput{
		Bucket: aws.String(b.bucket),
	}); err != nil {
		return fmt.Errorf("s3git: verifying bucket %q: %w", b.bucket, err)
	}

	// Create local git repo if it doesn't exist
	gitDir := filepath.Join(b.repoPath, ".git")
	if info, err := os.Stat(gitDir); err != nil || !info.IsDir() {
		if err := os.MkdirAll(b.repoPath, 0755); err != nil {
			return fmt.Errorf("s3git: creating directory: %w", err)
		}
		if out, err := exec.Command("git", "init", "-b", b.branch, b.repoPath).CombinedOutput(); err != nil {
			return fmt.Errorf("s3git: git init: %s: %w", strings.TrimSpace(string(out)), err)
		}
		// Exclude .DS_Store
		excludeDir := filepath.Join(b.repoPath, ".git", "info")
		if err := os.MkdirAll(excludeDir, 0755); err == nil {
			if f, err := os.OpenFile(filepath.Join(excludeDir, "exclude"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644); err == nil {
				f.WriteString(".DS_Store\n")
				f.Close()
			}
		}
	}

	// Download all files from S3 into the working tree
	if err := b.syncFromS3(ctx); err != nil {
		return fmt.Errorf("s3git: initial sync: %w", err)
	}

	// Commit the initial state
	b.gitAddAll()
	b.gitCommit("s3git: initial sync from S3", "s3git", "s3git@system")

	log.WithField("bucket", b.bucket).WithField("prefix", b.prefix).Info("s3git: backend initialized")
	return nil
}

// Sync downloads changes from S3 into the local repo and commits.
func (b *Backend) Sync() error {
	b.mu.Lock()
	defer b.mu.Unlock()

	if err := b.syncFromS3(context.Background()); err != nil {
		return err
	}
	b.gitAddAll()
	b.gitCommit("s3git: sync from S3", "s3git", "s3git@system")
	return nil
}

// --- File operations (local filesystem) ---

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

func (b *Backend) ListIndexableFiles(subfolder string) ([]string, error) {
	folderPath := filepath.Join(b.repoPath, subfolder)
	var files []string
	err := filepath.Walk(folderPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() && info.Name() == ".git" {
			return filepath.SkipDir
		}
		if !info.IsDir() && repo.IndexableFile(path) {
			relPath, _ := filepath.Rel(b.repoPath, path)
			files = append(files, relPath)
		}
		return nil
	})
	return files, err
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

// --- Persistence: git commit + S3 upload ---

func (b *Backend) SaveChanges(message, authorName, authorEmail string) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.gitAddAll()

	// Check for staged changes
	if err := exec.Command("git", "-C", b.repoPath, "diff", "--cached", "--quiet").Run(); err == nil {
		return nil // nothing to commit
	}

	if err := b.gitCommit(message, authorName, authorEmail); err != nil {
		return err
	}

	// Upload changed files to S3
	return b.uploadChangedToS3(message, authorName, authorEmail)
}

func (b *Backend) SaveChangesAsync(message, authorName, authorEmail string) {
	go func() {
		if err := b.SaveChanges(message, authorName, authorEmail); err != nil {
			log.WithError(err).Error("s3git: background save failed")
		}
	}()
}

// --- History (full git support) ---

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
		entries = append(entries, repo.StatusEntry{
			Path:     line[3:],
			Staging:  porcelainCodeString(line[0]),
			Worktree: porcelainCodeString(line[1]),
		})
	}
	return entries, nil
}

// --- Internal helpers ---

func (b *Backend) s3Key(relPath string) string {
	if b.prefix != "" {
		return strings.TrimSuffix(b.prefix, "/") + "/" + relPath
	}
	return relPath
}

// syncFromS3 downloads all S3 objects under the prefix into the working tree.
func (b *Backend) syncFromS3(ctx context.Context) error {
	input := &s3.ListObjectsV2Input{
		Bucket: aws.String(b.bucket),
		Prefix: aws.String(b.prefix),
	}

	paginator := s3.NewListObjectsV2Paginator(b.client, input)
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return fmt.Errorf("s3git: list objects: %w", err)
		}
		for _, obj := range page.Contents {
			key := *obj.Key
			relPath := strings.TrimPrefix(key, b.prefix)
			if relPath == "" || strings.HasSuffix(relPath, "/") {
				continue
			}

			// Download
			out, err := b.client.GetObject(ctx, &s3.GetObjectInput{
				Bucket: aws.String(b.bucket),
				Key:    aws.String(key),
			})
			if err != nil {
				log.WithError(err).WithField("key", key).Warn("s3git: failed to download object")
				continue
			}
			data, err := io.ReadAll(out.Body)
			out.Body.Close()
			if err != nil {
				continue
			}

			// Write to local filesystem
			localPath := filepath.Join(b.repoPath, relPath)
			if err := os.MkdirAll(filepath.Dir(localPath), 0755); err != nil {
				continue
			}
			os.WriteFile(localPath, data, 0644)
		}
	}
	return nil
}

// uploadChangedToS3 uploads all tracked files that changed in the last commit.
func (b *Backend) uploadChangedToS3(message, authorName, authorEmail string) error {
	ctx := context.Background()

	// Get changed files from the last commit
	out, err := exec.Command("git", "-C", b.repoPath, "diff", "--name-status", "HEAD~1..HEAD").Output()
	if err != nil {
		// First commit or error — upload all tracked files
		return b.uploadAllToS3(ctx)
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
		filePath := parts[1]

		switch {
		case status == "D":
			// Delete from S3
			key := b.s3Key(filePath)
			b.client.DeleteObject(ctx, &s3.DeleteObjectInput{
				Bucket: aws.String(b.bucket),
				Key:    aws.String(key),
			})
		case strings.HasPrefix(status, "R") && len(parts) >= 3:
			// Rename: delete old, upload new
			oldKey := b.s3Key(parts[1])
			b.client.DeleteObject(ctx, &s3.DeleteObjectInput{
				Bucket: aws.String(b.bucket),
				Key:    aws.String(oldKey),
			})
			b.uploadFileToS3(ctx, parts[2])
		default:
			// Add or modify
			b.uploadFileToS3(ctx, filePath)
		}
	}
	return nil
}

func (b *Backend) uploadAllToS3(ctx context.Context) error {
	return filepath.Walk(b.repoPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() && info.Name() == ".git" {
			return filepath.SkipDir
		}
		if info.IsDir() {
			return nil
		}
		relPath, _ := filepath.Rel(b.repoPath, path)
		return b.uploadFileToS3(ctx, relPath)
	})
}

func (b *Backend) uploadFileToS3(ctx context.Context, relPath string) error {
	localPath := filepath.Join(b.repoPath, relPath)
	data, err := os.ReadFile(localPath)
	if err != nil {
		return err
	}

	key := b.s3Key(relPath)
	input := &s3.PutObjectInput{
		Bucket: aws.String(b.bucket),
		Key:    aws.String(key),
		Body:   bytes.NewReader(data),
	}
	if ct := mime.TypeByExtension(filepath.Ext(relPath)); ct != "" {
		input.ContentType = aws.String(ct)
	}

	_, err = b.client.PutObject(ctx, input)
	if err != nil {
		return fmt.Errorf("s3git: upload %q: %w", key, err)
	}
	return nil
}

func (b *Backend) gitAddAll() {
	exec.Command("git", "-C", b.repoPath, "add", "-A").Run()
}

func (b *Backend) gitCommit(message, authorName, authorEmail string) error {
	// Check for staged changes first
	if err := exec.Command("git", "-C", b.repoPath, "diff", "--cached", "--quiet").Run(); err == nil {
		return nil // nothing staged
	}

	cmd := exec.Command("git", "-C", b.repoPath, "commit", "-m", message,
		"--author", fmt.Sprintf("%s <%s>", authorName, authorEmail))
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("s3git: git commit: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}

// batchDelete removes objects using the batch API.
func (b *Backend) batchDelete(ctx context.Context, keys []string) error {
	const batchSize = 1000
	for i := 0; i < len(keys); i += batchSize {
		end := i + batchSize
		if end > len(keys) {
			end = len(keys)
		}
		objects := make([]s3types.ObjectIdentifier, end-i)
		for j, key := range keys[i:end] {
			objects[j] = s3types.ObjectIdentifier{Key: aws.String(key)}
		}
		_, err := b.client.DeleteObjects(ctx, &s3.DeleteObjectsInput{
			Bucket: aws.String(b.bucket),
			Delete: &s3types.Delete{Objects: objects, Quiet: aws.Bool(true)},
		})
		if err != nil {
			return fmt.Errorf("s3git: batch delete: %w", err)
		}
	}
	return nil
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
