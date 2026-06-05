package permissions

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

const PermissionsFileName = ".file-permissions.yaml"

// Load reads and parses the .wiki-permissions.yaml file from the repo root.
// Returns nil, nil if the file doesn't exist.
func Load(repoPath string) (*Resolver, error) {
	filePath := filepath.Join(repoPath, PermissionsFileName)
	data, err := os.ReadFile(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading permissions file: %w", err)
	}

	return LoadFromData(data)
}

// LoadFromData parses a permissions YAML from raw bytes.
// Returns nil, nil if data is empty.
func LoadFromData(data []byte) (*Resolver, error) {
	if len(data) == 0 {
		return nil, nil
	}

	var pf PermissionsFile
	if err := yaml.Unmarshal(data, &pf); err != nil {
		return nil, fmt.Errorf("parsing permissions file: %w", err)
	}

	if pf.Version == 0 {
		pf.Version = 1
	}
	if pf.Paths == nil {
		pf.Paths = make(map[string]PathEntry)
	}

	// Migrate: move legacy "defaults" field into Root entry
	var legacy struct {
		Defaults Level `yaml:"defaults"`
	}
	_ = yaml.Unmarshal(data, &legacy)
	if legacy.Defaults > None && pf.Root == nil {
		pf.Root = &PathEntry{Default: legacy.Defaults}
	}

	// Migrate: move legacy root entry (empty key) to the Root field
	if entry, ok := pf.Paths[""]; ok {
		if pf.Root == nil {
			pf.Root = &entry
		} else {
			// Merge: legacy entry takes precedence for users/groups/owner
			if entry.Owner != "" {
				pf.Root.Owner = entry.Owner
			}
			if entry.Users != nil {
				pf.Root.Users = entry.Users
			}
			if entry.Groups != nil {
				pf.Root.Groups = entry.Groups
			}
			if entry.Default > pf.Root.Default {
				pf.Root.Default = entry.Default
			}
		}
		delete(pf.Paths, "")
	}

	return NewResolver(pf), nil
}

// Initialize creates a fresh Resolver with default settings, discarding any existing state.
// The caller is responsible for calling Save to persist it.
func Initialize(defaults Level) *Resolver {
	return NewResolver(PermissionsFile{
		Version: 1,
		Root:    &PathEntry{Default: defaults},
		Paths:   make(map[string]PathEntry),
	})
}

// Marshal serializes the resolver's permissions file to YAML bytes.
func Marshal(r *Resolver) ([]byte, error) {
	r.mu.RLock()
	pf := r.file
	r.mu.RUnlock()

	data, err := yaml.Marshal(&pf)
	if err != nil {
		return nil, fmt.Errorf("marshaling permissions file: %w", err)
	}
	return data, nil
}

// Save writes the current permissions state to .wiki-permissions.yaml in the repo.
func Save(repoPath string, r *Resolver) error {
	data, err := Marshal(r)
	if err != nil {
		return err
	}

	filePath := filepath.Join(repoPath, PermissionsFileName)
	tmp := filePath + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return fmt.Errorf("writing permissions file: %w", err)
	}
	if err := os.Rename(tmp, filePath); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("renaming permissions file: %w", err)
	}
	return nil
}
