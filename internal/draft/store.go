package draft

// Store is the storage interface for draft operations.
type Store interface {
	SaveDraft(subfolder, filename, email, name, baseCommitHash string, content []byte) error
	GetDraft(subfolder, filename, email string) ([]byte, *DraftMeta, error)
	GetDraftMeta(subfolder, filename, email string) (*DraftMeta, error)
	HasDraft(subfolder, filename, email string) bool
	DeleteDraft(subfolder, filename, email string) error
	// ListDraftOwners returns metadata for every user with a pending draft
	// on (subfolder, filename). Returns a nil/empty slice, not an error,
	// when none exist.
	ListDraftOwners(subfolder, filename string) ([]*DraftMeta, error)
	// OtherUsersDraftUnderFolder returns metadata for another user's pending
	// draft on any file under folderPath (recursive), excluding
	// callerEmail's own drafts, plus the repo-relative path of the file it
	// belongs to. Returns (nil, "", nil) when nothing blocks the caller.
	OtherUsersDraftUnderFolder(folderPath, callerEmail string) (*DraftMeta, string, error)
	// EnsureGitignore is a no-op for non-filesystem stores.
	EnsureGitignore() error
}
