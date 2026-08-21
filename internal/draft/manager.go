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

// OtherUsersDraft returns metadata for another user's pending draft on
// (subfolder, filename), or nil if none exists. callerEmail's own draft (if
// any) is always excluded — a user is never locked out of their own draft.
// If more than one other user has a draft (a legacy state from before
// cross-user draft locking existed, since drafts were previously per-user
// with no cross-user awareness), the most recently updated one is returned.
func (m *Manager) OtherUsersDraft(subfolder, filename, callerEmail string) (*DraftMeta, error) {
	owners, err := m.store.ListDraftOwners(subfolder, filename)
	if err != nil {
		return nil, err
	}
	return pickOtherUsersDraft(owners, callerEmail)
}

// OtherUsersDraftUnderFolder is the recursive form of OtherUsersDraft, used
// for folder-level operations (delete_folder, move_folder) that would
// orphan or invalidate any pending draft underneath the folder. Returns the
// blocking draft's metadata and the repo-relative path of the file it
// belongs to, or (nil, "", nil) if nothing under the folder blocks the
// caller.
func (m *Manager) OtherUsersDraftUnderFolder(folderPath, callerEmail string) (*DraftMeta, string, error) {
	return m.store.OtherUsersDraftUnderFolder(folderPath, callerEmail)
}

// pickOtherUsersDraft excludes callerEmail (case-insensitive) from owners
// and returns the most-recently-updated remaining entry, logging a warning
// if more than one remains.
func pickOtherUsersDraft(owners []*DraftMeta, callerEmail string) (*DraftMeta, error) {
	var others []*DraftMeta
	for _, o := range owners {
		if !strings.EqualFold(o.UserEmail, callerEmail) {
			others = append(others, o)
		}
	}
	if len(others) == 0 {
		return nil, nil
	}
	if len(others) > 1 {
		log.WithField("file", others[0].OriginalFile).Warn("multiple other users have pending drafts on this file")
	}
	best := others[0]
	for _, o := range others[1:] {
		if o.UpdatedAt.After(best.UpdatedAt) {
			best = o
		}
	}
	return best, nil
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

// ListDraftOwners returns metadata for every user with a pending draft on
// (subfolder, filename), by matching draft filenames against the known
// filename's prefix — unambiguous since filename is known here (unlike the
// folder-recursive walk below, where filenames aren't known in advance).
func (s *FilesystemStore) ListDraftOwners(subfolder, filename string) ([]*DraftMeta, error) {
	entries, err := os.ReadDir(s.draftsDir(subfolder))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("listing drafts directory: %w", err)
	}

	prefix := "." + filename + ".draft."
	var owners []*DraftMeta
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasPrefix(name, prefix) || strings.HasSuffix(name, ".meta") {
			continue
		}
		email := strings.TrimPrefix(name, prefix)
		meta, err := s.GetDraftMeta(subfolder, filename, email)
		if err != nil {
			log.WithError(err).WithField("file", name).Warn("failed to read draft meta while listing owners")
			continue
		}
		owners = append(owners, meta)
	}
	return owners, nil
}

// OtherUsersDraftUnderFolder walks the subtree under folderPath looking for
// any ".drafts" directory, reading each draft's ".meta" sidecar directly
// (it already carries OriginalFile/UserEmail/UpdatedAt, sidestepping the
// ambiguity of parsing an arbitrary draft filename back into
// filename+email when both may themselves contain dots).
func (s *FilesystemStore) OtherUsersDraftUnderFolder(folderPath, callerEmail string) (*DraftMeta, string, error) {
	root := filepath.Join(s.repoPath, folderPath)
	var found []*DraftMeta

	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if !d.IsDir() || d.Name() != ".drafts" {
			return nil
		}
		entries, err := os.ReadDir(path)
		if err != nil {
			log.WithError(err).WithField("dir", path).Warn("failed to read drafts directory during folder scan")
			return nil
		}
		for _, e := range entries {
			name := e.Name()
			if e.IsDir() || !strings.HasSuffix(name, ".meta") {
				continue
			}
			data, err := os.ReadFile(filepath.Join(path, name))
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
		return nil
	})
	if err != nil {
		return nil, "", fmt.Errorf("scanning folder for pending drafts: %w", err)
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
