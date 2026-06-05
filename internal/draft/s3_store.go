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

// EnsureGitignore is a no-op for S3 stores.
func (s *S3Store) EnsureGitignore() error { return nil }
