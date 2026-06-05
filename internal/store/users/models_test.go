package users

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestUserEntry_Identity(t *testing.T) {
	tests := []struct {
		name string
		user UserEntry
		want string
	}{
		{
			"internal user",
			UserEntry{Internal: true, Email: "admin@example.com"},
			"internal:admin@example.com",
		},
		{
			"external user with provider",
			UserEntry{Internal: false, Provider: "google", Email: "alice@example.com"},
			"google:alice@example.com",
		},
		{
			"external user with github",
			UserEntry{Internal: false, Provider: "github", Email: "dev@example.com"},
			"github:dev@example.com",
		},
		{
			"external user without provider (legacy)",
			UserEntry{Internal: false, Provider: "", Email: "legacy@example.com"},
			"legacy@example.com",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.user.Identity())
		})
	}
}

func TestHasRole(t *testing.T) {
	tests := []struct {
		name string
		user UserEntry
		role Role
		want bool
	}{
		{
			"super admin has any role",
			UserEntry{SuperAdmin: true, Roles: nil},
			RoleSystemAdmin,
			true,
		},
		{
			"super admin has user_admin",
			UserEntry{SuperAdmin: true},
			RoleUserAdmin,
			true,
		},
		{
			"super admin has repo_admin",
			UserEntry{SuperAdmin: true},
			RoleRepoAdmin,
			true,
		},
		{
			"regular user with matching role",
			UserEntry{Roles: []string{"system_admin"}},
			RoleSystemAdmin,
			true,
		},
		{
			"regular user without matching role",
			UserEntry{Roles: []string{"user_admin"}},
			RoleSystemAdmin,
			false,
		},
		{
			"regular user with no roles",
			UserEntry{Roles: nil},
			RoleRepoAdmin,
			false,
		},
		{
			"regular user with empty roles slice",
			UserEntry{Roles: []string{}},
			RoleRepoAdmin,
			false,
		},
		{
			"user with multiple roles",
			UserEntry{Roles: []string{"user_admin", "repo_admin"}},
			RoleRepoAdmin,
			true,
		},
		{
			"user with multiple roles missing requested",
			UserEntry{Roles: []string{"user_admin", "repo_admin"}},
			RoleSystemAdmin,
			false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, HasRole(tt.user, tt.role))
		})
	}
}

func TestAllRoles_Complete(t *testing.T) {
	assert.Equal(t, 3, len(AllRoles))
	assert.Contains(t, AllRoles, RoleSystemAdmin)
	assert.Contains(t, AllRoles, RoleUserAdmin)
	assert.Contains(t, AllRoles, RoleRepoAdmin)
}
