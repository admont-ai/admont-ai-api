package permissions

import (
	"fmt"
	"strings"
)

// --- Group CRUD ---

// GetGroups returns a copy of all groups.
func (r *Resolver) GetGroups() map[string][]string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if r.file.Groups == nil {
		return nil
	}
	out := make(map[string][]string, len(r.file.Groups))
	for name, members := range r.file.Groups {
		cp := make([]string, len(members))
		copy(cp, members)
		out[name] = cp
	}
	return out
}

// GetGroup returns a copy of the members for a group, or nil if not found.
func (r *Resolver) GetGroup(name string) ([]string, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	members, ok := r.file.Groups[name]
	if !ok {
		return nil, false
	}
	cp := make([]string, len(members))
	copy(cp, members)
	return cp, true
}

// AddGroup creates a new group with the given members. Returns an error if it already exists.
func (r *Resolver) AddGroup(name string, members []string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.file.Groups == nil {
		r.file.Groups = make(map[string][]string)
	}
	if _, exists := r.file.Groups[name]; exists {
		return fmt.Errorf("group %q already exists", name)
	}
	r.file.Groups[name] = members
	return nil
}

// UpdateGroup replaces the members of an existing group. Returns an error if it doesn't exist.
func (r *Resolver) UpdateGroup(name string, members []string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.file.Groups == nil || r.file.Groups[name] == nil {
		return fmt.Errorf("group %q not found", name)
	}
	r.file.Groups[name] = members
	return nil
}

// RemoveGroup deletes a group and removes it from all path entries. Returns an error if it doesn't exist.
func (r *Resolver) RemoveGroup(name string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.file.Groups == nil {
		return fmt.Errorf("group %q not found", name)
	}
	if _, exists := r.file.Groups[name]; !exists {
		return fmt.Errorf("group %q not found", name)
	}
	delete(r.file.Groups, name)

	// Clean up references in root entry
	if r.file.Root != nil {
		delete(r.file.Root.Groups, name)
	}

	// Clean up references in path entries
	for pathKey, entry := range r.file.Paths {
		if _, ok := entry.Groups[name]; ok {
			delete(entry.Groups, name)
			r.file.Paths[pathKey] = entry
		}
	}
	return nil
}

// --- Path entry writers ---

// SetOwner sets the owner on a path entry, creating it if needed.
func (r *Resolver) SetOwner(pathKey, email string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if pathKey == "" {
		if r.file.Root == nil {
			r.file.Root = &PathEntry{Default: r.rootDefault()}
		}
		r.file.Root.Owner = email
		return
	}

	entry, ok := r.file.Paths[pathKey]
	if !ok {
		entry = PathEntry{Default: r.rootDefault()}
	}
	entry.Owner = email
	r.file.Paths[pathKey] = entry
}

// ReplaceUsers replaces all user permissions on a path entry.
func (r *Resolver) ReplaceUsers(pathKey string, users map[string]Level) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if pathKey == "" {
		if r.file.Root == nil {
			r.file.Root = &PathEntry{Default: r.rootDefault()}
		}
		r.file.Root.Users = users
		return
	}

	entry, ok := r.file.Paths[pathKey]
	if !ok {
		entry = PathEntry{Default: r.rootDefault()}
	}
	entry.Users = users
	r.file.Paths[pathKey] = entry
}

// ReplaceGroups replaces all group permissions on a path entry.
func (r *Resolver) ReplaceGroups(pathKey string, groups map[string]Level) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if pathKey == "" {
		if r.file.Root == nil {
			r.file.Root = &PathEntry{Default: r.rootDefault()}
		}
		r.file.Root.Groups = groups
		return
	}

	entry, ok := r.file.Paths[pathKey]
	if !ok {
		entry = PathEntry{Default: r.rootDefault()}
	}
	entry.Groups = groups
	r.file.Paths[pathKey] = entry
}

// SetUserPermission sets a specific user's permission on a path entry.
func (r *Resolver) SetUserPermission(pathKey, email string, level Level) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if pathKey == "" {
		if r.file.Root == nil {
			r.file.Root = &PathEntry{Default: r.rootDefault()}
		}
		if r.file.Root.Users == nil {
			r.file.Root.Users = make(map[string]Level)
		}
		r.file.Root.Users[email] = level
		return
	}

	entry, ok := r.file.Paths[pathKey]
	if !ok {
		entry = PathEntry{Default: r.rootDefault()}
	}
	if entry.Users == nil {
		entry.Users = make(map[string]Level)
	}
	entry.Users[email] = level
	r.file.Paths[pathKey] = entry
}

// SetGroupPermission sets a specific group's permission on a path entry.
func (r *Resolver) SetGroupPermission(pathKey, groupName string, level Level) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if pathKey == "" {
		if r.file.Root == nil {
			r.file.Root = &PathEntry{Default: r.rootDefault()}
		}
		if r.file.Root.Groups == nil {
			r.file.Root.Groups = make(map[string]Level)
		}
		r.file.Root.Groups[groupName] = level
		return
	}

	entry, ok := r.file.Paths[pathKey]
	if !ok {
		entry = PathEntry{Default: r.rootDefault()}
	}
	if entry.Groups == nil {
		entry.Groups = make(map[string]Level)
	}
	entry.Groups[groupName] = level
	r.file.Paths[pathKey] = entry
}

// RemoveGroupPermission removes a specific group's permission from a path entry.
func (r *Resolver) RemoveGroupPermission(pathKey, groupName string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	entry, ok := r.file.Paths[pathKey]
	if !ok {
		return
	}
	delete(entry.Groups, groupName)
	r.file.Paths[pathKey] = entry
}

// RemoveUserPermission removes a specific user's permission from a path entry.
func (r *Resolver) RemoveUserPermission(pathKey, email string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	entry, ok := r.file.Paths[pathKey]
	if !ok {
		return
	}
	delete(entry.Users, email)
	r.file.Paths[pathKey] = entry
}

// SetDefault sets the default permission level for a path entry.
func (r *Resolver) SetDefault(pathKey string, level Level) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if pathKey == "" {
		if r.file.Root == nil {
			r.file.Root = &PathEntry{}
		}
		r.file.Root.Default = level
		return
	}

	entry, ok := r.file.Paths[pathKey]
	if !ok {
		entry = PathEntry{}
	}
	entry.Default = level
	r.file.Paths[pathKey] = entry
}

// RemoveEntry removes a path entry entirely.
func (r *Resolver) RemoveEntry(pathKey string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if pathKey == "" {
		r.file.Root = nil
		return
	}

	delete(r.file.Paths, pathKey)
}

// RemoveEntriesUnder removes a path entry and all child entries (for folder deletion).
func (r *Resolver) RemoveEntriesUnder(folderPath string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	prefix := folderPath
	if !strings.HasSuffix(prefix, "/") {
		prefix += "/"
	}

	delete(r.file.Paths, prefix)
	for key := range r.file.Paths {
		if strings.HasPrefix(key, prefix) {
			delete(r.file.Paths, key)
		}
	}
}

// RenamePath updates permission entries when a file or folder is moved/renamed.
// For a file: oldPath and newPath are file paths (no trailing slash).
// For a folder: oldPath and newPath should include trailing slash.
func (r *Resolver) RenamePath(oldPath, newPath string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Direct entry rename
	if entry, ok := r.file.Paths[oldPath]; ok {
		delete(r.file.Paths, oldPath)
		r.file.Paths[newPath] = entry
	}

	// For folder renames, also update all children
	oldPrefix := oldPath
	if !strings.HasSuffix(oldPrefix, "/") {
		return // file path, no children to update
	}

	newPrefix := newPath
	if !strings.HasSuffix(newPrefix, "/") {
		newPrefix += "/"
	}

	for key, entry := range r.file.Paths {
		if strings.HasPrefix(key, oldPrefix) {
			suffix := strings.TrimPrefix(key, oldPrefix)
			newKey := newPrefix + suffix
			delete(r.file.Paths, key)
			r.file.Paths[newKey] = entry
		}
	}
}
