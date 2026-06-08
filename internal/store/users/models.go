package users

// Role represents a user role in the system.
type Role string

const (
	RoleSystemAdmin Role = "system_admin"
	RoleUserAdmin   Role = "user_admin"
	RoleRepoAdmin   Role = "repo_admin"
)

// AllRoles contains all defined roles in the system.
var AllRoles = []Role{RoleSystemAdmin, RoleUserAdmin, RoleRepoAdmin}

type UserEntry struct {
	ID int `yaml:"-" json:"id"`
	// Internal is derived from Provider (== "internal") and kept for API
	// compatibility; it is populated when scanning rows, not stored directly.
	Internal          bool     `yaml:"-" json:"internal"`
	Provider          string   `yaml:"provider" json:"provider"`
	Email             string   `yaml:"email" json:"email"`
	FirstName         string   `yaml:"first_name" json:"first_name"`
	LastName          string   `yaml:"last_name" json:"last_name"`
	SuperAdmin        bool     `yaml:"super_admin" json:"super_admin"`
	Roles             []string `yaml:"roles" json:"roles"`
	TOTPEnabled       bool     `yaml:"-" json:"totp_enabled,omitempty"`
	PasswordExpired   bool     `yaml:"-" json:"password_expired,omitempty"`
	Suspended         bool     `yaml:"-" json:"suspended,omitempty"`
	PasswordChangedAt string   `yaml:"-" json:"password_changed_at,omitempty"`
	CreatedAt         string   `yaml:"-" json:"created_at,omitempty"`
	UpdatedAt         string   `yaml:"-" json:"updated_at,omitempty"`
}

// Identity returns the canonical "provider:email" identity string.
func (u UserEntry) Identity() string {
	if u.Internal {
		return "internal:" + u.Email
	}
	if u.Provider == "" {
		return u.Email
	}
	return u.Provider + ":" + u.Email
}

type UserGroup struct {
	Name        string      `yaml:"name" json:"name"`
	Description string      `yaml:"description" json:"description"`
	Members     []UserEntry `yaml:"members" json:"members"`
	Roles       []string    `yaml:"roles" json:"roles"`
}

// GroupMemberRef identifies a user for group membership operations.
type GroupMemberRef struct {
	UserID int
}

// HasRole checks if a user entry has the given role. Super admins have all roles.
func HasRole(entry UserEntry, role Role) bool {
	if entry.SuperAdmin {
		return true
	}
	for _, r := range entry.Roles {
		if r == string(role) {
			return true
		}
	}
	return false
}
