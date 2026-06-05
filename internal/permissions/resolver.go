package permissions

import (
	"path/filepath"
	"strings"
	"sync"
)

// Resolver checks file/folder permissions against a parsed PermissionsFile.
type Resolver struct {
	mu   sync.RWMutex
	file PermissionsFile
}

// NewResolver creates a Resolver from a parsed PermissionsFile.
func NewResolver(pf PermissionsFile) *Resolver {
	return &Resolver{file: pf}
}

// File returns a copy of the underlying PermissionsFile.
func (r *Resolver) File() PermissionsFile {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.file
}

// Check returns true if the user has at least the required permission level on the given path.
func (r *Resolver) Check(userEmail, filePath string, required Level) bool {
	return r.EffectiveLevel(userEmail, filePath) >= required
}

// EffectiveLevel returns the user's resolved permission level for the given path.
// Permissions only escalate: the highest level from root, any ancestor folder,
// and the exact path entry is returned. A subfolder cannot restrict access
// granted by a parent.
func (r *Resolver) EffectiveLevel(userEmail, filePath string) Level {
	r.mu.RLock()
	defer r.mu.RUnlock()
	level, _ := r.effectiveLevelLocked(userEmail, filePath)
	return level
}

// effectiveLevelLocked computes the highest permission from root through all ancestors to the exact path.
// Returns both the level and the source description. Caller must hold at least r.mu.RLock.
func (r *Resolver) effectiveLevelLocked(userEmail, filePath string) (Level, string) {
	filePath = normalizePath(filePath)

	best := None
	source := "none"

	// 1. Start with root
	if r.file.Root != nil {
		lvl := r.resolveEntry(*r.file.Root, userEmail)
		if lvl > best {
			best = lvl
			source = "root"
		}
	}

	// 2. Walk from root toward the target path, checking each ancestor folder
	parts := strings.Split(filePath, "/")
	for i := 0; i < len(parts)-1; i++ {
		dir := strings.Join(parts[:i+1], "/")
		if entry, ok := r.lookupFolder(dir); ok {
			lvl := r.resolveEntry(entry, userEmail)
			if lvl > best {
				best = lvl
				source = "folder:" + dir
			}
		}
	}

	// 3. Exact path match
	if entry, ok := r.file.Paths[filePath]; ok {
		lvl := r.resolveEntry(entry, userEmail)
		if lvl > best {
			best = lvl
			source = "path:" + filePath
		}
	}
	if entry, ok := r.file.Paths[filePath+"/"]; ok {
		lvl := r.resolveEntry(entry, userEmail)
		if lvl > best {
			best = lvl
			source = "path:" + filePath + "/"
		}
	}

	return best, source
}

// IsOwner returns true if the user is the owner of the path or its nearest ancestor entry.
func (r *Resolver) IsOwner(userEmail, filePath string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()

	filePath = normalizePath(filePath)

	if entry, ok := r.file.Paths[filePath]; ok {
		return entry.Owner == userEmail
	}
	if entry, ok := r.file.Paths[filePath+"/"]; ok {
		return entry.Owner == userEmail
	}

	dir := filePath
	for {
		dir = parentDir(dir)
		if dir == "" {
			break
		}
		if entry, ok := r.lookupFolder(dir); ok {
			return entry.Owner == userEmail
		}
	}

	if r.file.Root != nil {
		return r.file.Root.Owner == userEmail
	}

	return false
}

// EffectiveSource returns both the level and the source description for diagnostic/API purposes.
// Source is one of: "path:<path>", "folder:<path>", "root", "none".
func (r *Resolver) EffectiveSource(userEmail, filePath string) (Level, string) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.effectiveLevelLocked(userEmail, filePath)
}

// HasAccessibleDescendant returns true if any path entry under the given folder
// grants the user at least the required level. This is used for listing: folders
// that the user cannot directly access are still shown if they contain accessible content.
func (r *Resolver) HasAccessibleDescendant(userEmail, folderPath string, required Level) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()

	folderPath = normalizePath(folderPath)
	prefix := folderPath + "/"

	for pathKey := range r.file.Paths {
		if strings.HasPrefix(pathKey, prefix) {
			lvl, _ := r.effectiveLevelLocked(userEmail, pathKey)
			if lvl >= required {
				return true
			}
		}
	}
	return false
}

// HasAnyAccess returns true if the user has at least the required level on the root
// or on any path entry in the permissions file.
func (r *Resolver) HasAnyAccess(userEmail string, required Level) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if r.file.Root != nil {
		if r.resolveEntry(*r.file.Root, userEmail) >= required {
			return true
		}
	}
	for pathKey := range r.file.Paths {
		lvl, _ := r.effectiveLevelLocked(userEmail, pathKey)
		if lvl >= required {
			return true
		}
	}
	return false
}

// GetEntry returns the PathEntry for a specific path key, if it exists.
func (r *Resolver) GetEntry(pathKey string) (PathEntry, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if pathKey == "" && r.file.Root != nil {
		return *r.file.Root, true
	}
	if entry, ok := r.file.Paths[pathKey]; ok {
		return entry, true
	}
	// Try with/without trailing slash
	clean := strings.TrimSuffix(pathKey, "/")
	if entry, ok := r.file.Paths[clean]; ok {
		return entry, true
	}
	if entry, ok := r.file.Paths[clean+"/"]; ok {
		return entry, true
	}
	return PathEntry{}, false
}

func (r *Resolver) resolveEntry(entry PathEntry, userEmail string) Level {
	if userEmail != "" && entry.Owner == userEmail {
		return Manager // owner gets full access
	}
	if userEmail != "" {
		if level, ok := entry.Users[userEmail]; ok {
			return level
		}
		// Check group permissions — highest matching group level wins
		if len(entry.Groups) > 0 && len(r.file.Groups) > 0 {
			bestLevel := None
			matched := false
			for groupName, members := range r.file.Groups {
				level, hasPermission := entry.Groups[groupName]
				if !hasPermission {
					continue
				}
				for _, member := range members {
					if member == userEmail {
						matched = true
						if level > bestLevel {
							bestLevel = level
						}
						break
					}
				}
			}
			if matched {
				return bestLevel
			}
		}
	}
	return entry.Default
}

// rootDefault returns the root entry's default level, or None if no root entry exists.
func (r *Resolver) rootDefault() Level {
	if r.file.Root != nil {
		return r.file.Root.Default
	}
	return None
}

// lookupFolder checks for a folder entry with or without trailing slash.
func (r *Resolver) lookupFolder(dir string) (PathEntry, bool) {
	if entry, ok := r.file.Paths[dir+"/"]; ok {
		return entry, true
	}
	if entry, ok := r.file.Paths[dir]; ok {
		return entry, true
	}
	return PathEntry{}, false
}

// normalizePath cleans up a file path for consistent lookup.
func normalizePath(p string) string {
	p = filepath.Clean(p)
	p = strings.TrimPrefix(p, "/")
	if p == "." {
		return ""
	}
	return p
}

// parentDir returns the parent directory of a path, or "" if at root.
func parentDir(p string) string {
	p = strings.TrimSuffix(p, "/")
	dir := filepath.Dir(p)
	if dir == "." || dir == "/" || dir == p {
		return ""
	}
	return dir
}
