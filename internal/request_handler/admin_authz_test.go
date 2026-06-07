package requesthandler

import (
	"testing"

	"github.com/christianfischer/md-wiki-server/internal/store/users"
)

func newAdminHandlerWithUsers(us []users.UserEntry) *AdminRequesthandler {
	return &AdminRequesthandler{users: us}
}

func TestIsSuperAdmin(t *testing.T) {
	h := newAdminHandlerWithUsers([]users.UserEntry{
		{Internal: true, Email: "boss@x.com", SuperAdmin: true},
		{Internal: true, Email: "useradmin@x.com", Roles: []string{"user_admin"}},
		{Internal: false, Provider: "google", Email: "ext@x.com", SuperAdmin: true},
	})

	if !h.IsSuperAdmin("internal:boss@x.com") {
		t.Error("expected boss to be super admin")
	}
	if h.IsSuperAdmin("internal:useradmin@x.com") {
		t.Error("user_admin must NOT be a super admin")
	}
	if !h.IsSuperAdmin("google:ext@x.com") {
		t.Error("expected external super admin to be recognized")
	}
	if h.IsSuperAdmin("internal:nobody@x.com") {
		t.Error("unknown identity must not be super admin")
	}
	if h.IsSuperAdmin("") {
		t.Error("empty identity must not be super admin")
	}
}

func TestCountSuperAdmins(t *testing.T) {
	h := newAdminHandlerWithUsers([]users.UserEntry{
		{Internal: true, Email: "a@x.com", SuperAdmin: true},
		{Internal: true, Email: "b@x.com", SuperAdmin: true},
		{Internal: true, Email: "c@x.com"},
	})
	if got := h.countSuperAdmins(); got != 2 {
		t.Errorf("countSuperAdmins() = %d, want 2", got)
	}

	none := newAdminHandlerWithUsers([]users.UserEntry{{Internal: true, Email: "c@x.com"}})
	if got := none.countSuperAdmins(); got != 0 {
		t.Errorf("countSuperAdmins() = %d, want 0", got)
	}
}
