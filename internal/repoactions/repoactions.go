// Package repoactions holds the repo/file-op logic shared by the MCP tool
// handlers (internal/mcp) and the chat-agent tool loop
// (internal/request_handler), so the two independent callers don't drift.
//
// Response shaping (MCP's structured JSON vs. the agent's plain strings) and
// commit-message conventions stay caller-side — every function here takes
// the finished commit message as a parameter rather than composing one.
package repoactions

import (
	"errors"
	"path/filepath"
	"strings"

	"github.com/christianfischer/md-wiki-server/internal/permissions"
	"github.com/christianfischer/md-wiki-server/internal/pg_vector/indexer"
	"github.com/christianfischer/md-wiki-server/internal/repo"
)

// Deps bundles the per-repo collaborators a repoactions call needs. Build one
// per call (or reuse across calls for the same repo/identity).
type Deps struct {
	Backend  repo.RepoBackend
	RepoSlug string

	Resolver *permissions.Resolver // nil = no permissions file for this repo
	ReadOnly bool
	IsAdmin  bool // caller pre-resolves isSystemAdmin(identity)

	Indexer     *indexer.Indexer // nil = indexing disabled
	ShouldIndex bool

	// SavePermissions persists the resolver's state back to the backend
	// (marshal + write the permissions file). Only invoked when Resolver is
	// non-nil and a write op modifies it. May be left nil for callers (e.g.
	// the agent) whose operations never touch permission bookkeeping.
	SavePermissions func()
}

// CheckPermission reproduces the MCP tool handlers' permission semantics
// (internal/mcp/server.go's original checkPermission): the read-only gate
// runs first (so even admins are blocked on a read-only repo), then the
// admin bypass (which therefore CAN act on dot-paths), then the dot-path
// denial, then resolver-based checks with an anonymous-viewer allowance when
// no resolver exists and a distinct "authentication required" vs "not
// found" error depending on whether identity is empty.
func CheckPermission(d Deps, identity, path string, required permissions.Level) error {
	if d.ReadOnly && required > permissions.Viewer {
		return errors.New("not found")
	}
	if identity != "" && d.IsAdmin {
		return nil
	}
	if IsDotPath(path) {
		return errors.New("not found")
	}
	if d.Resolver == nil {
		if identity == "" && required <= permissions.Viewer {
			return nil
		}
		if identity == "" {
			return errors.New("authentication required")
		}
		return errors.New("not found")
	}
	if !d.Resolver.Check(identity, path, required) {
		if identity == "" {
			return errors.New("authentication required")
		}
		return errors.New("not found")
	}
	return nil
}

// CheckPermSimple reproduces the chat-agent tool loop's permission semantics
// (internal/request_handler/agent_requesthandler.go's original checkPerm):
// no anonymous-identity allowance, no dot-path check (the agent checks that
// separately, in validatePath, before calling this), and a single
// "permission denied" error rather than MCP's authentication/not-found
// distinction. Deliberately NOT unified with CheckPermission — unifying
// would silently change one caller's observable error behavior.
func CheckPermSimple(d Deps, identity, path string, required permissions.Level) error {
	if d.ReadOnly && required > permissions.Viewer {
		return errors.New("repository is read-only")
	}
	if identity != "" && d.IsAdmin {
		return nil
	}
	if d.Resolver == nil {
		return errors.New("permission denied")
	}
	if !d.Resolver.Check(identity, path, required) {
		return errors.New("permission denied")
	}
	return nil
}

// CreateFile writes a new file, persists the change, and indexes it if
// enabled. fullPath is the path as passed to the caller's request (used for
// indexing) — kept as its own parameter rather than rejoining
// subfolder+filename, since filepath.Join can normalize a path differently
// than the caller's original string.
func CreateFile(d Deps, subfolder, filename, fullPath string, content []byte, commitMessage, authorName, authorEmail string) error {
	if err := d.Backend.AddFile(subfolder, filename, content); err != nil {
		return err
	}
	d.Backend.SaveChangesAsync(commitMessage, authorName, authorEmail)
	if d.ShouldIndex && d.Indexer != nil {
		d.Indexer.IndexFile(d.RepoSlug, fullPath)
	}
	return nil
}

// UpdateFile overwrites an existing file's content, persists the change, and
// re-indexes it if enabled.
func UpdateFile(d Deps, subfolder, filename, fullPath string, content []byte, commitMessage, authorName, authorEmail string) error {
	if err := d.Backend.AddFile(subfolder, filename, content); err != nil {
		return err
	}
	d.Backend.SaveChangesAsync(commitMessage, authorName, authorEmail)
	if d.ShouldIndex && d.Indexer != nil {
		d.Indexer.IndexFile(d.RepoSlug, fullPath)
	}
	return nil
}

// DeleteFile removes a file, drops any permissions-file entry for it,
// persists the change, and removes it from the search index if enabled.
func DeleteFile(d Deps, subfolder, filename, fullPath, commitMessage, authorName, authorEmail string) error {
	if err := d.Backend.DeleteFile(subfolder, filename); err != nil {
		return err
	}
	if d.Resolver != nil {
		d.Resolver.RemoveEntry(fullPath)
		if d.SavePermissions != nil {
			d.SavePermissions()
		}
	}
	d.Backend.SaveChangesAsync(commitMessage, authorName, authorEmail)
	if d.ShouldIndex && d.Indexer != nil {
		d.Indexer.DeleteFileIndex(d.RepoSlug, fullPath)
	}
	return nil
}

// MoveFile relocates a file (covers both a cross-folder move and a same-folder
// rename — both are a single backend.MoveFile call with different arguments),
// updates any permissions-file entry for it, persists the change, and
// re-indexes it under the new path if enabled.
func MoveFile(d Deps, oldSubfolder, oldFilename, newSubfolder, newFilename, oldPath, newPath, commitMessage, authorName, authorEmail string) error {
	if err := d.Backend.MoveFile(oldSubfolder, oldFilename, newSubfolder, newFilename); err != nil {
		return err
	}
	if d.Resolver != nil {
		d.Resolver.RenamePath(oldPath, newPath)
		if d.SavePermissions != nil {
			d.SavePermissions()
		}
	}
	d.Backend.SaveChangesAsync(commitMessage, authorName, authorEmail)
	if d.ShouldIndex && d.Indexer != nil {
		d.Indexer.DeleteFileIndex(d.RepoSlug, oldPath)
		d.Indexer.IndexFile(d.RepoSlug, newPath)
	}
	return nil
}

// CreateFolder creates a folder (backed by a placeholder .gitkeep file, since
// the backends have no native empty-directory concept) and persists the change.
func CreateFolder(d Deps, folderPath, commitMessage, authorName, authorEmail string) error {
	if err := d.Backend.AddFile(folderPath, ".gitkeep", []byte{}); err != nil {
		return err
	}
	d.Backend.SaveChangesAsync(commitMessage, authorName, authorEmail)
	return nil
}

// RenameFolder relocates a folder (covers both an in-place rename and a move
// to a different parent — both are a single backend.RenameFolder call),
// updates permissions-file entries under it (trailing slash, matching the
// original MCP behavior for folder paths), persists the change, and
// re-indexes its contents under the new path if enabled.
func RenameFolder(d Deps, oldPath, newPath, commitMessage, authorName, authorEmail string) error {
	if err := d.Backend.RenameFolder(oldPath, newPath); err != nil {
		return err
	}
	if d.Resolver != nil {
		d.Resolver.RenamePath(oldPath+"/", newPath+"/")
		if d.SavePermissions != nil {
			d.SavePermissions()
		}
	}
	d.Backend.SaveChangesAsync(commitMessage, authorName, authorEmail)
	if d.ShouldIndex && d.Indexer != nil {
		d.Indexer.DeleteFolderIndex(d.RepoSlug, oldPath)
		d.Indexer.ReindexFolder(d.RepoSlug, newPath)
	}
	return nil
}

// DeleteFolder removes a folder and everything under it, drops any
// permissions-file entries under it, persists the change, and removes its
// contents from the search index if enabled.
func DeleteFolder(d Deps, folderPath, commitMessage, authorName, authorEmail string) error {
	if err := d.Backend.DeleteFolder(folderPath); err != nil {
		return err
	}
	if d.Resolver != nil {
		d.Resolver.RemoveEntriesUnder(folderPath)
		if d.SavePermissions != nil {
			d.SavePermissions()
		}
	}
	d.Backend.SaveChangesAsync(commitMessage, authorName, authorEmail)
	if d.ShouldIndex && d.Indexer != nil {
		d.Indexer.DeleteFolderIndex(d.RepoSlug, folderPath)
	}
	return nil
}

// SplitPath splits a repo-relative path into (subfolder, filename), matching
// filepath.Dir/Base semantics with "" (not ".") for a root-level file.
func SplitPath(filePath string) (subfolder, filename string) {
	subfolder = filepath.Dir(filePath)
	if subfolder == "." {
		subfolder = ""
	}
	filename = filepath.Base(filePath)
	return
}

// IsDotPath reports whether any path segment starts with "." (hidden files,
// .git, .drafts, etc.), which are always denied to non-admin callers.
func IsDotPath(filePath string) bool {
	for _, part := range strings.Split(filePath, "/") {
		if strings.HasPrefix(part, ".") {
			return true
		}
	}
	return false
}
