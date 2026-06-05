package draft

// Store is the storage interface for draft operations.
type Store interface {
	SaveDraft(subfolder, filename, email, name, baseCommitHash string, content []byte) error
	GetDraft(subfolder, filename, email string) ([]byte, *DraftMeta, error)
	GetDraftMeta(subfolder, filename, email string) (*DraftMeta, error)
	HasDraft(subfolder, filename, email string) bool
	DeleteDraft(subfolder, filename, email string) error
	// EnsureGitignore is a no-op for non-filesystem stores.
	EnsureGitignore() error
}
