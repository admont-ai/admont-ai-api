package draft

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	log "github.com/sirupsen/logrus"
)

// S3Store implements Store using S3 for draft storage.
// Drafts are stored under {prefix}.drafts/{email_hash}/{subfolder}/{filename}
// with sibling .meta.json objects.
type S3Store struct {
	client *s3.Client
	bucket string
	prefix string // e.g. "docs/" — the repo's S3 key prefix
}

// NewS3Store creates an S3-backed draft store.
func NewS3Store(client *s3.Client, bucket, prefix string) *S3Store {
	if prefix != "" && !strings.HasSuffix(prefix, "/") {
		prefix += "/"
	}
	return &S3Store{client: client, bucket: bucket, prefix: prefix}
}

func (s *S3Store) draftKey(subfolder, filename, email string) string {
	hash := emailHash(email)
	parts := []string{strings.TrimSuffix(s.prefix, "/"), ".drafts", hash}
	if subfolder != "" {
		parts = append(parts, subfolder)
	}
	parts = append(parts, draftFilename(filename, email))
	return strings.Join(parts, "/")
}

func (s *S3Store) metaKey(subfolder, filename, email string) string {
	return s.draftKey(subfolder, filename, email) + ".meta.json"
}

func emailHash(email string) string {
	h := sha256.Sum256([]byte(strings.ToLower(email)))
	return hex.EncodeToString(h[:8])
}

func (s *S3Store) SaveDraft(subfolder, filename, email, name, baseCommitHash string, content []byte) error {
	email = strings.ToLower(email)
	ctx := context.Background()

	// Write draft content
	key := s.draftKey(subfolder, filename, email)
	if _, err := s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
		Body:   bytes.NewReader(content),
	}); err != nil {
		return fmt.Errorf("s3 draft: put %q: %w", key, err)
	}

	// Load or create meta
	now := time.Now().UTC()
	meta, _ := s.GetDraftMeta(subfolder, filename, email)
	if meta == nil {
		meta = &DraftMeta{
			OriginalFile:   filepath.Join(subfolder, filename),
			UserEmail:      email,
			UserName:       name,
			BaseCommitHash: baseCommitHash,
			CreatedAt:      now,
		}
	}
	meta.UpdatedAt = now
	if meta.BaseCommitHash == "" {
		meta.BaseCommitHash = baseCommitHash
	}

	metaData, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return fmt.Errorf("s3 draft: marshal meta: %w", err)
	}

	metaKey := s.metaKey(subfolder, filename, email)
	if _, err := s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(metaKey),
		Body:   bytes.NewReader(metaData),
	}); err != nil {
		return fmt.Errorf("s3 draft: put meta %q: %w", metaKey, err)
	}

	log.WithFields(log.Fields{"file": filename, "email": email, "subfolder": subfolder}).Info("s3 draft saved")
	return nil
}

func (s *S3Store) GetDraft(subfolder, filename, email string) ([]byte, *DraftMeta, error) {
	email = strings.ToLower(email)
	ctx := context.Background()

	key := s.draftKey(subfolder, filename, email)
	out, err := s.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return nil, nil, err
	}
	defer out.Body.Close()
	content, err := io.ReadAll(out.Body)
	if err != nil {
		return nil, nil, err
	}

	meta, _ := s.GetDraftMeta(subfolder, filename, email)
	return content, meta, nil
}

func (s *S3Store) GetDraftMeta(subfolder, filename, email string) (*DraftMeta, error) {
	email = strings.ToLower(email)
	ctx := context.Background()

	metaKey := s.metaKey(subfolder, filename, email)
	out, err := s.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(metaKey),
	})
	if err != nil {
		return nil, err
	}
	defer out.Body.Close()
	data, err := io.ReadAll(out.Body)
	if err != nil {
		return nil, err
	}

	var meta DraftMeta
	if err := json.Unmarshal(data, &meta); err != nil {
		return nil, fmt.Errorf("s3 draft: parse meta: %w", err)
	}
	return &meta, nil
}

func (s *S3Store) HasDraft(subfolder, filename, email string) bool {
	email = strings.ToLower(email)
	ctx := context.Background()

	key := s.draftKey(subfolder, filename, email)
	_, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	return err == nil
}

func (s *S3Store) DeleteDraft(subfolder, filename, email string) error {
	email = strings.ToLower(email)
	ctx := context.Background()

	key := s.draftKey(subfolder, filename, email)
	metaKey := s.metaKey(subfolder, filename, email)

	s.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(metaKey),
	})
	if _, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	}); err != nil {
		return fmt.Errorf("s3 draft: delete %q: %w", key, err)
	}

	log.WithFields(log.Fields{"file": filename, "email": email, "subfolder": subfolder}).Info("s3 draft deleted")
	return nil
}

// draftsPrefix returns the key prefix under which every user's drafts for
// this repo live (across all files) — {prefix}.drafts/.
func (s *S3Store) draftsPrefix() string {
	return strings.TrimSuffix(s.prefix, "/") + "/.drafts/"
}

// listDraftObjectKeys lists every object key under draftsPrefix(). The
// email-hash segment comes before subfolder/filename in the key layout (see
// draftKey), so there's no way to construct a narrower prefix for "drafts on
// one file" or "drafts under one folder" — every call here scans the
// repo's whole .drafts/ tree.
func (s *S3Store) listDraftObjectKeys(ctx context.Context) ([]string, error) {
	prefix := s.draftsPrefix()
	var keys []string
	paginator := s3.NewListObjectsV2Paginator(s.client, &s3.ListObjectsV2Input{
		Bucket: aws.String(s.bucket),
		Prefix: aws.String(prefix),
	})
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("s3 draft: list %q: %w", prefix, err)
		}
		for _, obj := range page.Contents {
			keys = append(keys, strings.TrimPrefix(*obj.Key, prefix))
		}
	}
	return keys, nil
}

// splitDraftKey parses a key already trimmed of draftsPrefix() — of the
// form "{hash}/{subfolder...}/{draftFilename}" (or ".meta.json"-suffixed for
// the sidecar) — into (subfolder, lastSegment, isMeta). The hash segment is
// discarded; it only exists to shard by user and carries no information not
// already in the draft filename / meta content.
func splitDraftKey(trimmedKey string) (subfolder, lastSegment string, isMeta bool) {
	parts := strings.Split(trimmedKey, "/")
	if len(parts) < 2 {
		return "", "", false
	}
	lastSegment = parts[len(parts)-1]
	if strings.HasSuffix(lastSegment, ".meta.json") {
		isMeta = true
		lastSegment = strings.TrimSuffix(lastSegment, ".meta.json")
	}
	subfolder = strings.Join(parts[1:len(parts)-1], "/")
	return subfolder, lastSegment, isMeta
}

// ListDraftOwners returns metadata for every user with a pending draft on
// (subfolder, filename). filename is known here, so matching the draft
// filename's prefix against it is unambiguous (unlike the folder-recursive
// walk below, where filenames aren't known in advance).
func (s *S3Store) ListDraftOwners(subfolder, filename string) ([]*DraftMeta, error) {
	ctx := context.Background()
	keys, err := s.listDraftObjectKeys(ctx)
	if err != nil {
		return nil, err
	}

	prefix := "." + filename + ".draft."
	var owners []*DraftMeta
	for _, k := range keys {
		sf, last, isMeta := splitDraftKey(k)
		if isMeta || sf != subfolder || !strings.HasPrefix(last, prefix) {
			continue
		}
		email := strings.TrimPrefix(last, prefix)
		meta, err := s.GetDraftMeta(subfolder, filename, email)
		if err != nil {
			log.WithError(err).WithField("key", k).Warn("failed to read draft meta while listing owners")
			continue
		}
		owners = append(owners, meta)
	}
	return owners, nil
}

// OtherUsersDraftUnderFolder scans every draft under the repo (see
// listDraftObjectKeys) and keeps the ".meta.json" sidecars whose
// reconstructed subfolder falls under folderPath, reading each directly
// (it already carries OriginalFile/UserEmail/UpdatedAt, sidestepping the
// ambiguity of parsing an arbitrary draft filename back into
// filename+email when both may themselves contain dots).
func (s *S3Store) OtherUsersDraftUnderFolder(folderPath, callerEmail string) (*DraftMeta, string, error) {
	ctx := context.Background()
	keys, err := s.listDraftObjectKeys(ctx)
	if err != nil {
		return nil, "", err
	}

	var found []*DraftMeta
	for _, k := range keys {
		sf, _, isMeta := splitDraftKey(k)
		if !isMeta || !underFolder(sf, folderPath) {
			continue
		}
		out, err := s.client.GetObject(ctx, &s3.GetObjectInput{
			Bucket: aws.String(s.bucket),
			Key:    aws.String(s.draftsPrefix() + k),
		})
		if err != nil {
			continue
		}
		data, err := io.ReadAll(out.Body)
		out.Body.Close()
		if err != nil {
			continue
		}
		var meta DraftMeta
		if err := json.Unmarshal(data, &meta); err != nil {
			continue
		}
		if !strings.EqualFold(meta.UserEmail, callerEmail) {
			found = append(found, &meta)
		}
	}

	if len(found) == 0 {
		return nil, "", nil
	}
	best := found[0]
	for _, m := range found[1:] {
		if m.UpdatedAt.After(best.UpdatedAt) {
			best = m
		}
	}
	return best, best.OriginalFile, nil
}

// underFolder reports whether subfolder is folderPath itself or nested
// under it. folderPath == "" matches every subfolder (repo root).
func underFolder(subfolder, folderPath string) bool {
	if folderPath == "" {
		return true
	}
	return subfolder == folderPath || strings.HasPrefix(subfolder, folderPath+"/")
}

// EnsureGitignore is a no-op for S3 stores.
func (s *S3Store) EnsureGitignore() error { return nil }
