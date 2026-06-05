package permissions

import "fmt"

// Level represents a hierarchical permission role.
// Higher levels imply all lower levels: Manager > ContentManager > Contributor > Viewer > None.
type Level int

const (
	None           Level = 0
	Viewer         Level = 1
	Contributor    Level = 2
	ContentManager Level = 3
	Manager        Level = 4
)

func (l Level) String() string {
	switch l {
	case None:
		return "none"
	case Viewer:
		return "viewer"
	case Contributor:
		return "contributor"
	case ContentManager:
		return "content_manager"
	case Manager:
		return "manager"
	default:
		return "none"
	}
}

// ParseLevel converts a string to a Level.
func ParseLevel(s string) (Level, error) {
	switch s {
	case "none", "":
		return None, nil
	case "viewer":
		return Viewer, nil
	case "contributor":
		return Contributor, nil
	case "content_manager":
		return ContentManager, nil
	case "manager":
		return Manager, nil
	default:
		return None, fmt.Errorf("unknown permission level: %q", s)
	}
}

// MarshalYAML implements yaml.Marshaler.
func (l Level) MarshalYAML() (interface{}, error) {
	return l.String(), nil
}

// UnmarshalYAML implements yaml.Unmarshaler.
func (l *Level) UnmarshalYAML(unmarshal func(interface{}) error) error {
	var s string
	if err := unmarshal(&s); err != nil {
		return err
	}
	parsed, err := ParseLevel(s)
	if err != nil {
		return err
	}
	*l = parsed
	return nil
}

// PathEntry defines permissions for a specific path.
type PathEntry struct {
	Owner   string           `yaml:"owner,omitempty"`
	Default Level            `yaml:"default"`
	Users   map[string]Level `yaml:"users,omitempty"`
	Groups  map[string]Level `yaml:"groups,omitempty"`
}

// PermissionsFile is the top-level structure of .wiki-permissions.yaml.
type PermissionsFile struct {
	Version int                  `yaml:"version"`
	Root    *PathEntry           `yaml:"root,omitempty"`
	Groups  map[string][]string  `yaml:"groups,omitempty"`
	Paths   map[string]PathEntry `yaml:"paths,omitempty"`
}
