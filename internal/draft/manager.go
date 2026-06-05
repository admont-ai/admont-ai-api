package draft

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	log "github.com/sirupsen/logrus"
)

// DraftMeta holds metadata about a draft.
type DraftMeta struct {
	OriginalFile   string    `json:"original_file"`
	UserEmail      string    `json:"user_email"`
	UserName       string    `json:"user_name"`
	BaseCommitHash string    `json:"base_commit_hash"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// Manager handles draft CRUD operations for a single repository.
// It delegates to an underlying Store implementation.
type Manager struct {
	store Store
}

// NewManager creates a new draft Manager backed by a filesystem store at repoPath.
func NewManager(repoPath string) *Manager {
	return &Manager{store: NewFilesystemStore(repoPath)}
}

// NewManagerWithStore creates a new draft Manager backed by the given store.
func NewManagerWithStore(store Store) *Manager {
	return &Manager{store: store}
}

// SaveDraft saves or updates a draft for a specific user.
func (m *Manager) SaveDraft(subfolder, filename, email, name, baseCommitHash string, content []byte) error {
	return m.store.SaveDraft(subfolder, filename, email, name, baseCommitHash, content)
}

// GetDraft returns the draft content and metadata for a specific user.
func (m *Manager) GetDraft(subfolder, filename, email string) ([]byte, *DraftMeta, error) {
	return m.store.GetDraft(subfolder, filename, email)
}

// GetDraftMeta returns the metadata for a draft.
func (m *Manager) GetDraftMeta(subfolder, filename, email string) (*DraftMeta, error) {
	return m.store.GetDraftMeta(subfolder, filename, email)
}

// HasDraft checks whether a draft exists for a given user and file.
func (m *Manager) HasDraft(subfolder, filename, email string) bool {
	return m.store.HasDraft(subfolder, filename, email)
}

// DeleteDraft removes a draft and its metadata sidecar.
func (m *Manager) DeleteDraft(subfolder, filename, email string) error {
	return m.store.DeleteDraft(subfolder, filename, email)
}

// EnsureGitignore delegates to the store.
func (m *Manager) EnsureGitignore() error {
	return m.store.EnsureGitignore()
}

// --- Filesystem Store ---

// FilesystemStore implements Store using the local filesystem.
type FilesystemStore struct {
	repoPath string
}

// NewFilesystemStore creates a filesystem-backed draft store.
func NewFilesystemStore(repoPath string) *FilesystemStore {
	return &FilesystemStore{repoPath: repoPath}
}

func (s *FilesystemStore) draftsDir(subfolder string) string {
	return filepath.Join(s.repoPath, subfolder, ".drafts")
}

func draftFilename(filename, email string) string {
	return fmt.Sprintf(".%s.draft.%s", filename, strings.ToLower(email))
}

func metaFilename(filename, email string) string {
	return draftFilename(filename, email) + ".meta"
}

func (s *FilesystemStore) draftPath(subfolder, filename, email string) string {
	return filepath.Join(s.draftsDir(subfolder), draftFilename(filename, email))
}

func (s *FilesystemStore) metaPath(subfolder, filename, email string) string {
	return filepath.Join(s.draftsDir(subfolder), metaFilename(filename, email))
}

func (s *FilesystemStore) SaveDraft(subfolder, filename, email, name, baseCommitHash string, content []byte) error {
	email = strings.ToLower(email)

	draftDir := s.draftsDir(subfolder)
	if err := os.MkdirAll(draftDir, 0755); err != nil {
		return fmt.Errorf("creating draft directory: %w", err)
	}

	dp := s.draftPath(subfolder, filename, email)
	if err := os.WriteFile(dp, content, 0644); err != nil {
		return fmt.Errorf("writing draft: %w", err)
	}

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
		return fmt.Errorf("marshaling draft meta: %w", err)
	}

	mp := s.metaPath(subfolder, filename, email)
	if err := os.WriteFile(mp, metaData, 0644); err != nil {
		return fmt.Errorf("writing draft meta: %w", err)
	}

	log.WithFields(log.Fields{"file": filename, "email": email, "subfolder": subfolder}).Info("draft saved")
	return nil
}

func (s *FilesystemStore) GetDraft(subfolder, filename, email string) ([]byte, *DraftMeta, error) {
	email = strings.ToLower(email)

	dp := s.draftPath(subfolder, filename, email)
	content, err := os.ReadFile(dp)
	if err != nil {
		return nil, nil, err
	}

	meta, err := s.GetDraftMeta(subfolder, filename, email)
	if err != nil {
		return content, nil, nil
	}

	return content, meta, nil
}

func (s *FilesystemStore) GetDraftMeta(subfolder, filename, email string) (*DraftMeta, error) {
	email = strings.ToLower(email)

	mp := s.metaPath(subfolder, filename, email)
	data, err := os.ReadFile(mp)
	if err != nil {
		return nil, err
	}

	var meta DraftMeta
	if err := json.Unmarshal(data, &meta); err != nil {
		return nil, fmt.Errorf("parsing draft meta: %w", err)
	}

	return &meta, nil
}

func (s *FilesystemStore) HasDraft(subfolder, filename, email string) bool {
	email = strings.ToLower(email)
	dp := s.draftPath(subfolder, filename, email)
	_, err := os.Stat(dp)
	return err == nil
}

func (s *FilesystemStore) DeleteDraft(subfolder, filename, email string) error {
	email = strings.ToLower(email)

	dp := s.draftPath(subfolder, filename, email)
	mp := s.metaPath(subfolder, filename, email)

	os.Remove(mp)
	if err := os.Remove(dp); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("deleting draft: %w", err)
	}

	s.cleanEmptyDraftsDir(subfolder)

	log.WithFields(log.Fields{"file": filename, "email": email, "subfolder": subfolder}).Info("draft deleted")
	return nil
}

func (s *FilesystemStore) cleanEmptyDraftsDir(subfolder string) {
	dir := s.draftsDir(subfolder)
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) > 0 {
		return
	}
	os.Remove(dir)
}

func (s *FilesystemStore) EnsureGitignore() error {
	gitignorePath := filepath.Join(s.repoPath, ".gitignore")

	content, err := os.ReadFile(gitignorePath)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("reading .gitignore: %w", err)
	}

	lines := strings.Split(string(content), "\n")
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == ".drafts" || trimmed == "**/.drafts" {
			return nil
		}
	}

	entry := "**/.drafts\n"
	if len(content) > 0 && !strings.HasSuffix(string(content), "\n") {
		entry = "\n" + entry
	}

	f, err := os.OpenFile(gitignorePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("opening .gitignore: %w", err)
	}
	defer f.Close()

	if _, err := f.WriteString(entry); err != nil {
		return fmt.Errorf("writing .gitignore: %w", err)
	}

	log.Info("**/.drafts added to .gitignore")
	return nil
}
