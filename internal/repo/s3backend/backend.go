package s3backend

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"mime"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/christianfischer/md-wiki-server/internal/repo"
	"github.com/pmezard/go-difflib/difflib"
	log "github.com/sirupsen/logrus"
)

// Backend implements repo.RepoBackend for S3-stored files.
type Backend struct {
	client   *s3.Client
	bucket   string
	prefix   string // S3 key prefix (e.g. "docs/")
	cacheDir string // local directory for RepoPath() compatibility

	// Versioning
	versioningEnabled bool

	// Commit metadata for the current batch of writes.
	commitMu    sync.Mutex
	commitMsg   string
	commitName  string
	commitEmail string
}

// New creates an S3 backend.
func New(bucket, prefix, region, accessKey, secretKey, endpoint, cacheDir string) (*Backend, error) {
	var opts []func(*awsconfig.LoadOptions) error

	if region != "" {
		opts = append(opts, awsconfig.WithRegion(region))
	}

	// Use explicit credentials if provided, otherwise default chain (IAM, env, shared config)
	if accessKey != "" && secretKey != "" {
		opts = append(opts, awsconfig.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(accessKey, secretKey, ""),
		))
	}

	cfg, err := awsconfig.LoadDefaultConfig(context.Background(), opts...)
	if err != nil {
		return nil, fmt.Errorf("s3: loading AWS config: %w", err)
	}

	var clientOpts []func(*s3.Options)
	if endpoint != "" {
		clientOpts = append(clientOpts, func(o *s3.Options) {
			o.BaseEndpoint = aws.String(endpoint)
			o.UsePathStyle = true // Required for MinIO and most S3-compatible services
		})
	}

	client := s3.NewFromConfig(cfg, clientOpts...)

	// Normalize prefix
	if prefix != "" && !strings.HasSuffix(prefix, "/") {
		prefix += "/"
	}

	return &Backend{
		client:   client,
		bucket:   bucket,
		prefix:   prefix,
		cacheDir: cacheDir,
	}, nil
}

// S3Client returns the underlying S3 client for use by other components (e.g. draft storage).
func (b *Backend) S3Client() *s3.Client { return b.client }

// Bucket returns the S3 bucket name.
func (b *Backend) Bucket() string { return b.bucket }

// Prefix returns the S3 key prefix.
func (b *Backend) Prefix() string { return b.prefix }

func (b *Backend) Type() string          { return "s3_store" }
func (b *Backend) RepoPath() string      { return b.cacheDir }
func (b *Backend) SupportsHistory() bool { return b.versioningEnabled }

func (b *Backend) Stat(path string) (repo.FileInfo, error) {
	ctx := context.Background()
	path = strings.TrimSuffix(path, "/")

	// Try as a file first (HeadObject).
	key := b.s3Key("", path)
	head, err := b.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(b.bucket),
		Key:    aws.String(key),
	})
	if err == nil {
		name := path
		if idx := strings.LastIndex(path, "/"); idx >= 0 {
			name = path[idx+1:]
		}
		var size int64
		if head.ContentLength != nil {
			size = *head.ContentLength
		}
		return repo.FileInfo{Name: name, IsDir: false, Size: size}, nil
	}

	// Check as a directory (prefix with at least one child).
	prefix := b.s3Key("", path) + "/"
	out, err := b.client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
		Bucket:  aws.String(b.bucket),
		Prefix:  aws.String(prefix),
		MaxKeys: aws.Int32(1),
	})
	if err != nil {
		return repo.FileInfo{}, fmt.Errorf("s3: stat %q: %w", path, err)
	}
	if out.KeyCount != nil && *out.KeyCount > 0 {
		name := path
		if idx := strings.LastIndex(path, "/"); idx >= 0 {
			name = path[idx+1:]
		}
		return repo.FileInfo{Name: name, IsDir: true}, nil
	}

	return repo.FileInfo{}, fmt.Errorf("s3: stat %q: not found", path)
}

// s3Key returns the full S3 key for a file.
func (b *Backend) s3Key(subfolder, filename string) string {
	parts := []string{}
	if b.prefix != "" {
		parts = append(parts, strings.TrimSuffix(b.prefix, "/"))
	}
	if subfolder != "" {
		parts = append(parts, subfolder)
	}
	if filename != "" {
		parts = append(parts, filename)
	}
	return strings.Join(parts, "/")
}

// s3Prefix returns the S3 prefix for listing within a subfolder.
func (b *Backend) s3Prefix(subfolder string) string {
	parts := []string{}
	if b.prefix != "" {
		parts = append(parts, strings.TrimSuffix(b.prefix, "/"))
	}
	if subfolder != "" {
		parts = append(parts, subfolder)
	}
	result := strings.Join(parts, "/")
	if result != "" {
		result += "/"
	}
	return result
}

// Initialize verifies bucket access, detects versioning, and creates the cache dir.
func (b *Backend) Initialize(ctx context.Context) error {
	if err := os.MkdirAll(b.cacheDir, 0755); err != nil {
		return fmt.Errorf("s3: creating cache dir: %w", err)
	}

	// Verify bucket access
	_, err := b.client.HeadBucket(ctx, &s3.HeadBucketInput{
		Bucket: aws.String(b.bucket),
	})
	if err != nil {
		return fmt.Errorf("s3: verifying bucket %q: %w", b.bucket, err)
	}

	// Detect bucket versioning
	verOut, err := b.client.GetBucketVersioning(ctx, &s3.GetBucketVersioningInput{
		Bucket: aws.String(b.bucket),
	})
	if err != nil {
		log.WithError(err).Warn("s3: failed to check bucket versioning, assuming disabled")
	} else {
		b.versioningEnabled = verOut.Status == s3types.BucketVersioningStatusEnabled
	}

	log.WithField("bucket", b.bucket).
		WithField("prefix", b.prefix).
		WithField("versioning", b.versioningEnabled).
		Info("s3: backend initialized")
	return nil
}

// Sync is a no-op for S3 (writes go directly).
func (b *Backend) Sync() error { return nil }

// --- File operations ---

func (b *Backend) GetFile(subfolder, filename string) ([]byte, error) {
	key := b.s3Key(subfolder, filename)
	return b.getObject(context.Background(), key)
}

func (b *Backend) AddFile(subfolder, filename string, content []byte) error {
	key := b.s3Key(subfolder, filename)
	input := &s3.PutObjectInput{
		Bucket: aws.String(b.bucket),
		Key:    aws.String(key),
		Body:   bytes.NewReader(content),
	}

	// Set content-type based on file extension
	if ct := mime.TypeByExtension(filepath.Ext(filename)); ct != "" {
		input.ContentType = aws.String(ct)
	}

	// Attach author metadata when versioning is enabled
	b.commitMu.Lock()
	if b.versioningEnabled && b.commitName != "" {
		input.Metadata = map[string]string{
			"author":       b.commitName,
			"author-email": b.commitEmail,
			"message":      b.commitMsg,
		}
	}
	b.commitMu.Unlock()

	_, err := b.client.PutObject(context.Background(), input)
	if err != nil {
		return fmt.Errorf("s3: put %q: %w", key, err)
	}
	return nil
}

func (b *Backend) DeleteFile(subfolder, filename string) error {
	key := b.s3Key(subfolder, filename)
	_, err := b.client.DeleteObject(context.Background(), &s3.DeleteObjectInput{
		Bucket: aws.String(b.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return fmt.Errorf("s3: delete %q: %w", key, err)
	}
	return nil
}

func (b *Backend) DeleteFolder(subfolder string) error {
	prefix := b.s3Prefix(subfolder)
	keys, err := b.listKeys(context.Background(), prefix, false)
	if err != nil {
		return err
	}
	return b.batchDelete(context.Background(), keys)
}

// batchDelete removes up to 1000 objects per API call using DeleteObjects.
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
			return fmt.Errorf("s3: batch delete: %w", err)
		}
	}
	return nil
}

func (b *Backend) MoveFile(oldSubfolder, oldFilename, newSubfolder, newFilename string) error {
	oldKey := b.s3Key(oldSubfolder, oldFilename)
	newKey := b.s3Key(newSubfolder, newFilename)

	_, err := b.client.CopyObject(context.Background(), &s3.CopyObjectInput{
		Bucket:     aws.String(b.bucket),
		Key:        aws.String(newKey),
		CopySource: aws.String(b.bucket + "/" + oldKey),
	})
	if err != nil {
		return fmt.Errorf("s3: copy %q -> %q: %w", oldKey, newKey, err)
	}

	_, err = b.client.DeleteObject(context.Background(), &s3.DeleteObjectInput{
		Bucket: aws.String(b.bucket),
		Key:    aws.String(oldKey),
	})
	if err != nil {
		return fmt.Errorf("s3: delete original %q after copy: %w", oldKey, err)
	}
	return nil
}

func (b *Backend) RenameFolder(oldSubfolder, newSubfolder string) error {
	oldPrefix := b.s3Prefix(oldSubfolder)
	keys, err := b.listKeys(context.Background(), oldPrefix, false)
	if err != nil {
		return err
	}
	if len(keys) == 0 {
		return nil
	}

	ctx := context.Background()
	const concurrency = 10
	sem := make(chan struct{}, concurrency)
	errCh := make(chan error, len(keys))

	// Copy all keys concurrently
	for _, key := range keys {
		sem <- struct{}{}
		go func(key string) {
			defer func() { <-sem }()
			relPath := strings.TrimPrefix(key, oldPrefix)
			newKey := b.s3Prefix(newSubfolder) + relPath
			_, err := b.client.CopyObject(ctx, &s3.CopyObjectInput{
				Bucket:     aws.String(b.bucket),
				Key:        aws.String(newKey),
				CopySource: aws.String(b.bucket + "/" + key),
			})
			if err != nil {
				errCh <- fmt.Errorf("s3: copy %q -> %q: %w", key, newKey, err)
			}
		}(key)
	}
	// Wait for all copies
	for range concurrency {
		sem <- struct{}{}
	}
	close(errCh)
	for err := range errCh {
		return err
	}

	// Batch delete originals
	return b.batchDelete(ctx, keys)
}

func (b *Backend) ListEntries(subfolder string) ([]repo.FileEntry, error) {
	prefix := b.s3Prefix(subfolder)
	ctx := context.Background()

	var entries []repo.FileEntry
	paginator := s3.NewListObjectsV2Paginator(b.client, &s3.ListObjectsV2Input{
		Bucket:    aws.String(b.bucket),
		Prefix:    aws.String(prefix),
		Delimiter: aws.String("/"),
	})

	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("s3: list %q: %w", prefix, err)
		}

		for _, cp := range page.CommonPrefixes {
			name := strings.TrimSuffix(strings.TrimPrefix(*cp.Prefix, prefix), "/")
			if name == "" || name == ".git" {
				continue
			}
			entries = append(entries, repo.FileEntry{
				Name:  name,
				Path:  subfolder,
				IsDir: true,
			})
		}

		for _, obj := range page.Contents {
			name := strings.TrimPrefix(*obj.Key, prefix)
			if name == "" || strings.Contains(name, "/") {
				continue
			}
			if name == ".gitkeep" || name == ".DS_Store" {
				continue
			}
			entries = append(entries, repo.FileEntry{
				Name: name,
				Path: subfolder,
				Size: *obj.Size,
			})
		}
	}

	return entries, nil
}

func (b *Backend) ListFiles(subfolder string) ([]string, error) {
	prefix := b.s3Prefix(subfolder)
	ctx := context.Background()

	var files []string
	paginator := s3.NewListObjectsV2Paginator(b.client, &s3.ListObjectsV2Input{
		Bucket:    aws.String(b.bucket),
		Prefix:    aws.String(prefix),
		Delimiter: aws.String("/"),
	})

	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("s3: list files %q: %w", prefix, err)
		}
		for _, obj := range page.Contents {
			name := strings.TrimPrefix(*obj.Key, prefix)
			if name != "" && !strings.Contains(name, "/") {
				files = append(files, name)
			}
		}
	}

	return files, nil
}

func (b *Backend) ListFolders(subfolder string) ([]string, error) {
	prefix := b.s3Prefix(subfolder)
	ctx := context.Background()

	var folders []string
	paginator := s3.NewListObjectsV2Paginator(b.client, &s3.ListObjectsV2Input{
		Bucket:    aws.String(b.bucket),
		Prefix:    aws.String(prefix),
		Delimiter: aws.String("/"),
	})

	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("s3: list folders %q: %w", prefix, err)
		}
		for _, cp := range page.CommonPrefixes {
			name := strings.TrimSuffix(strings.TrimPrefix(*cp.Prefix, prefix), "/")
			if name != "" {
				folders = append(folders, name)
			}
		}
	}

	return folders, nil
}

func (b *Backend) ListIndexableFiles(subfolder string) ([]string, error) {
	prefix := b.s3Prefix(subfolder)
	ctx := context.Background()

	var files []string
	paginator := s3.NewListObjectsV2Paginator(b.client, &s3.ListObjectsV2Input{
		Bucket: aws.String(b.bucket),
		Prefix: aws.String(prefix),
	})

	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("s3: list indexable files %q: %w", prefix, err)
		}
		for _, obj := range page.Contents {
			key := *obj.Key
			if repo.IndexableFile(key) {
				relPath := strings.TrimPrefix(key, b.prefix)
				files = append(files, relPath)
			}
		}
	}

	return files, nil
}

func (b *Backend) ReadOrder(dirPath string) ([]string, error) {
	data, err := b.GetFile(dirPath, ".order")
	if err != nil {
		return nil, nil
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

// --- Persistence ---
// For S3, writes are immediate. SaveChanges stores commit metadata so subsequent
// AddFile calls can attach it to S3 object metadata (for version history).

func (b *Backend) SaveChanges(message, authorName, authorEmail string) error {
	b.commitMu.Lock()
	defer b.commitMu.Unlock()
	b.commitMsg = message
	b.commitName = authorName
	b.commitEmail = authorEmail
	return nil
}

func (b *Backend) SaveChangesAsync(message, authorName, authorEmail string) {
	b.SaveChanges(message, authorName, authorEmail)
}

// --- History ---

func (b *Backend) GetFileHistory(subfolder, filename string) ([]repo.FileChange, error) {
	if !b.versioningEnabled {
		return nil, repo.ErrNotSupported
	}
	key := b.s3Key(subfolder, filename)
	ctx := context.Background()

	out, err := b.client.ListObjectVersions(ctx, &s3.ListObjectVersionsInput{
		Bucket: aws.String(b.bucket),
		Prefix: aws.String(key),
	})
	if err != nil {
		return nil, fmt.Errorf("s3: list versions %q: %w", key, err)
	}

	var changes []repo.FileChange
	// Limit metadata fetches for performance
	const maxMetadataFetch = 20
	for i, v := range out.Versions {
		if *v.Key != key {
			continue // skip keys that just share the prefix
		}
		fc := repo.FileChange{
			CommitHash: aws.ToString(v.VersionId),
			Date:       aws.ToTime(v.LastModified),
		}

		// Fetch metadata for the first N versions
		if i < maxMetadataFetch && v.VersionId != nil {
			head, err := b.client.HeadObject(ctx, &s3.HeadObjectInput{
				Bucket:    aws.String(b.bucket),
				Key:       aws.String(key),
				VersionId: v.VersionId,
			})
			if err == nil && head.Metadata != nil {
				fc.Author = head.Metadata["author"]
				fc.AuthorEmail = head.Metadata["author-email"]
				fc.Message = head.Metadata["message"]
			}
		}

		changes = append(changes, fc)
	}

	return changes, nil
}

func (b *Backend) GetFileAtCommit(commitHash, subfolder, filename string) (string, error) {
	if !b.versioningEnabled {
		return "", repo.ErrNotSupported
	}
	key := b.s3Key(subfolder, filename)
	out, err := b.client.GetObject(context.Background(), &s3.GetObjectInput{
		Bucket:    aws.String(b.bucket),
		Key:       aws.String(key),
		VersionId: aws.String(commitHash),
	})
	if err != nil {
		return "", fmt.Errorf("s3: get version %q@%s: %w", key, commitHash, err)
	}
	defer out.Body.Close()
	data, err := io.ReadAll(out.Body)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func (b *Backend) GetFileDiffWithCommit(commitHash, subfolder, filename string) (string, error) {
	if !b.versioningEnabled {
		return "", repo.ErrNotSupported
	}

	// Get the old version
	oldContent, err := b.GetFileAtCommit(commitHash, subfolder, filename)
	if err != nil {
		return "", fmt.Errorf("getting old version: %w", err)
	}

	// Get current version
	currentData, err := b.GetFile(subfolder, filename)
	if err != nil {
		return "", fmt.Errorf("getting current version: %w", err)
	}

	filePath := subfolder
	if filePath != "" {
		filePath += "/"
	}
	filePath += filename

	diff := difflib.UnifiedDiff{
		A:        difflib.SplitLines(oldContent),
		B:        difflib.SplitLines(string(currentData)),
		FromFile: "a/" + filePath,
		ToFile:   "b/" + filePath,
		Context:  3,
	}
	text, err := difflib.GetUnifiedDiffString(diff)
	if err != nil {
		return "", fmt.Errorf("computing diff: %w", err)
	}
	return text, nil
}

func (b *Backend) GetFileCommitHash(subfolder, filename string) (string, error) {
	if !b.versioningEnabled {
		return "", repo.ErrNotSupported
	}
	key := b.s3Key(subfolder, filename)
	head, err := b.client.HeadObject(context.Background(), &s3.HeadObjectInput{
		Bucket: aws.String(b.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return "", fmt.Errorf("s3: head %q: %w", key, err)
	}
	if head.VersionId == nil {
		return "", repo.ErrNotSupported
	}
	return *head.VersionId, nil
}

// HeadHash is not meaningful for S3 (no global commit).
func (b *Backend) HeadHash() (string, error) {
	return "", repo.ErrNotSupported
}

// DiffChangedFiles is not supported for S3.
func (b *Backend) DiffChangedFiles(oldHash, newHash string) (changed []string, deleted []string, err error) {
	return nil, nil, repo.ErrNotSupported
}

// Status returns nil for S3 (no staging area).
func (b *Backend) Status() ([]repo.StatusEntry, error) {
	return nil, nil
}

// --- Internal helpers ---

func (b *Backend) getObject(ctx context.Context, key string) ([]byte, error) {
	out, err := b.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(b.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return nil, fmt.Errorf("s3: get %q: %w", key, err)
	}
	defer out.Body.Close()
	return io.ReadAll(out.Body)
}

func (b *Backend) listKeys(ctx context.Context, prefix string, delimited bool) ([]string, error) {
	input := &s3.ListObjectsV2Input{
		Bucket: aws.String(b.bucket),
		Prefix: aws.String(prefix),
	}
	if delimited {
		input.Delimiter = aws.String("/")
	}

	var keys []string
	paginator := s3.NewListObjectsV2Paginator(b.client, input)
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("s3: list %q: %w", prefix, err)
		}
		for _, obj := range page.Contents {
			keys = append(keys, *obj.Key)
		}
	}
	return keys, nil
}
