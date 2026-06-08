package requesthandler

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/christianfischer/md-wiki-server/internal/auth"
	"github.com/christianfischer/md-wiki-server/internal/draft"
	gitpkg "github.com/christianfischer/md-wiki-server/internal/git"
	"github.com/christianfischer/md-wiki-server/internal/llm"
	"github.com/christianfischer/md-wiki-server/internal/pg_vector/backend"
	"github.com/christianfischer/md-wiki-server/internal/pg_vector/indexer"
	"github.com/christianfischer/md-wiki-server/internal/repo"
	"github.com/christianfischer/md-wiki-server/internal/repo/localgit"
	"github.com/christianfischer/md-wiki-server/internal/repo/repofactory"
	"github.com/christianfischer/md-wiki-server/internal/repo/s3backend"
	"github.com/christianfischer/md-wiki-server/internal/store"
	storeauth "github.com/christianfischer/md-wiki-server/internal/store/auth_provider"
	"github.com/christianfischer/md-wiki-server/internal/store/git_repo"
	storellm "github.com/christianfischer/md-wiki-server/internal/store/llm_provider"
	storesearch "github.com/christianfischer/md-wiki-server/internal/store/search_provider"
	"github.com/christianfischer/md-wiki-server/internal/store/users"
	"github.com/go-fuego/fuego"
	log "github.com/sirupsen/logrus"
	"golang.org/x/crypto/bcrypt"
)

// SearchBackendFactory creates a SearchBackend from a provider type and config.
// The caller is responsible for closing the returned backend.
type SearchBackendFactory func(providerType string, providerConfig map[string]string) (backend.SearchBackend, error)

type AdminRequesthandler struct {
	store                *store.Store
	backends             map[string]repo.RepoBackend
	draftManagers        map[string]*draft.Manager
	docPaths             map[string]string
	repoConfigs          map[string]*git_repo.GitRepo
	indexer              *indexer.Indexer
	backendHolder        *backend.Holder
	repoState            backend.RepoStateStore
	searchBackendFactory SearchBackendFactory
	users                []users.UserEntry
	groups               []users.UserGroup
	localRepoPath        string
	localGitPath         string
	repoReady            *sync.Map
	authProviders        []storeauth.AuthProvider
	defaultBaseURL       string
	httpRegistry         *auth.Registry
	mcpRegistry          *auth.Registry
	modelRegistry        *llm.ModelRegistry
	llmRebuild           func()
	invalidateSessions   func(identity string)
	mu                   sync.RWMutex
}

// SetSessionInvalidator sets the callback used to revoke a user's existing
// tokens after an admin changes their password.
func (h *AdminRequesthandler) SetSessionInvalidator(fn func(identity string)) {
	h.invalidateSessions = fn
}

// SetIndexer sets the optional search indexer for triggering index updates on repo add/remove.
func (h *AdminRequesthandler) SetIndexer(idx *indexer.Indexer) {
	h.indexer = idx
}

// SetSearchBackend sets the search backend holder and repo state store for admin operations.
func (h *AdminRequesthandler) SetSearchBackend(bh *backend.Holder, rs backend.RepoStateStore) {
	h.backendHolder = bh
	h.repoState = rs
}

// SetSearchBackendFactory sets the factory for lazily creating search backends at runtime.
func (h *AdminRequesthandler) SetSearchBackendFactory(f SearchBackendFactory) {
	h.searchBackendFactory = f
}

// SetLLMRebuild sets the callback invoked after LLM provider changes to rebuild the client.
func (h *AdminRequesthandler) SetLLMRebuild(fn func()) {
	h.llmRebuild = fn
}

func NewAdminRequesthandler(
	store *store.Store,
	backends map[string]repo.RepoBackend,
	draftManagers map[string]*draft.Manager,
	docPaths map[string]string,
	users []users.UserEntry,
	groups []users.UserGroup,
	localRepoPath string,
	localGitPath string,
	repoConfigs map[string]*git_repo.GitRepo,
	repoReady *sync.Map,
	authProviders []storeauth.AuthProvider,
	defaultBaseURL string,
	httpRegistry *auth.Registry,
	mcpRegistry *auth.Registry,
	modelRegistry *llm.ModelRegistry,
) *AdminRequesthandler {
	return &AdminRequesthandler{
		store:          store,
		backends:       backends,
		draftManagers:  draftManagers,
		docPaths:       docPaths,
		repoConfigs:    repoConfigs,
		users:          users,
		groups:         groups,
		localRepoPath:  localRepoPath,
		localGitPath:   localGitPath,
		repoReady:      repoReady,
		authProviders:  authProviders,
		defaultBaseURL: defaultBaseURL,
		httpRegistry:   httpRegistry,
		mcpRegistry:    mcpRegistry,
		modelRegistry:  modelRegistry,
	}
}

// IsSystemAdmin checks if the identity has the "system_admin" role.
func (h *AdminRequesthandler) IsSystemAdmin(identity string) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.hasRole(identity, users.RoleSystemAdmin)
}

// IsUserAdmin checks if the identity has the "user_admin" role.
func (h *AdminRequesthandler) IsUserAdmin(identity string) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.hasRole(identity, users.RoleUserAdmin)
}

// CanManageRepos checks if the identity has the "repo_admin" role.
func (h *AdminRequesthandler) CanManageRepos(identity string) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.hasRole(identity, users.RoleRepoAdmin)
}

// IsSuperAdmin reports whether the identity belongs to a super admin.
func (h *AdminRequesthandler) IsSuperAdmin(identity string) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for _, u := range h.users {
		if auth.MatchIdentity(u.Identity(), identity) {
			return u.SuperAdmin
		}
	}
	return false
}

// countSuperAdmins returns the number of super-admin users. Caller must hold mu.
func (h *AdminRequesthandler) countSuperAdmins() int {
	n := 0
	for _, u := range h.users {
		if u.SuperAdmin {
			n++
		}
	}
	return n
}

// hasRole checks if a user with the given identity has the specified role. Caller must hold at least mu.RLock.
// Falls back to the database if the user is not found in the in-memory cache.
func (h *AdminRequesthandler) hasRole(identity string, role users.Role) bool {
	for _, u := range h.users {
		if auth.MatchIdentity(u.Identity(), identity) {
			if users.HasRole(u, role) {
				return true
			}
			return false
		}
	}
	for _, g := range h.groups {
		for _, m := range g.Members {
			if auth.MatchIdentity(m.Identity(), identity) {
				for _, r := range g.Roles {
					if r == string(role) {
						return true
					}
				}
				return false
			}
		}
	}

	// User not in memory cache — check DB and refresh cache if found.
	dbUsers, err := h.store.Users.ListAllUsers(context.Background())
	if err != nil {
		return false
	}
	h.users = dbUsers
	for _, u := range h.users {
		if auth.MatchIdentity(u.Identity(), identity) {
			return users.HasRole(u, role)
		}
	}
	return false
}

// GetUserRoles returns the effective roles for the given identity, combining user and group roles.
// Super admins get all defined roles.
func (h *AdminRequesthandler) GetUserRoles(identity string) []string {
	h.mu.RLock()
	defer h.mu.RUnlock()

	// Find user in cache (or refresh from DB).
	var user *users.UserEntry
	for i := range h.users {
		if auth.MatchIdentity(h.users[i].Identity(), identity) {
			user = &h.users[i]
			break
		}
	}
	if user == nil {
		dbUsers, err := h.store.Users.ListAllUsers(context.Background())
		if err != nil {
			return nil
		}
		h.users = dbUsers
		for i := range h.users {
			if auth.MatchIdentity(h.users[i].Identity(), identity) {
				user = &h.users[i]
				break
			}
		}
	}
	if user == nil {
		return nil
	}

	if user.SuperAdmin {
		roles := make([]string, len(users.AllRoles))
		for i, r := range users.AllRoles {
			roles[i] = string(r)
		}
		return roles
	}

	// Collect roles from user + groups (deduplicated).
	seen := make(map[string]bool)
	for _, r := range user.Roles {
		seen[r] = true
	}
	for _, g := range h.groups {
		for _, m := range g.Members {
			if auth.MatchIdentity(m.Identity(), identity) {
				for _, r := range g.Roles {
					seen[r] = true
				}
				break
			}
		}
	}

	roles := make([]string, 0, len(seen))
	for r := range seen {
		roles = append(roles, r)
	}
	return roles
}

type repoInfoResponse struct {
	Slug              string `json:"slug"`
	RepoUrl           string `json:"repo_url"`
	Branch            string `json:"branch"`
	Authenticated     bool   `json:"authenticated"`
	LFSEnabled        bool   `json:"lfs_enabled"`
	SearchProvider    string `json:"search_provider"`
	SearchIndexStatus string `json:"search_index_status"`
	DocPath           string `json:"doc_path,omitempty"`
	Name              string `json:"name,omitempty"`
	Username          string `json:"username,omitempty"`
	AuthToken         string `json:"auth_token,omitempty"`
	PublicAccess      bool   `json:"public_access"`
	ReadOnly          bool   `json:"read_only"`
	BackendType       string `json:"backend_type"`
	S3Bucket          string `json:"s3_bucket,omitempty"`
	S3Prefix          string `json:"s3_prefix,omitempty"`
	S3Region          string `json:"s3_region,omitempty"`
	S3Endpoint        string `json:"s3_endpoint,omitempty"`
	S3HasCredentials  bool   `json:"s3_has_credentials,omitempty"`
}

func (h *AdminRequesthandler) searchIndexStatus(rc *git_repo.GitRepo, slug string) string {
	if rc == nil || rc.SearchProviderID == nil || h.indexer == nil {
		return "disabled"
	}
	return h.indexer.Status(slug)
}

// searchProviderName resolves a search provider ID to its name, or "" if nil/not found.
func (h *AdminRequesthandler) searchProviderName(id *int) string {
	if id == nil {
		return ""
	}
	name, err := h.store.Search.GetSearchProviderNameByID(context.Background(), *id)
	if err != nil {
		return ""
	}
	return name
}

// resolveSearchProviderID resolves a search provider name to its ID, or nil if empty.
func (h *AdminRequesthandler) resolveSearchProviderID(name string) (*int, error) {
	if name == "" {
		return nil, nil
	}
	id, err := h.store.Search.GetSearchProviderID(context.Background(), name)
	if err != nil {
		return nil, err
	}
	return &id, nil
}

func obfuscateToken(token string) string {
	if len(token) <= 4 {
		return strings.Repeat("*", len(token))
	}
	return strings.Repeat("*", len(token)-4) + token[len(token)-4:]
}

type addRepoRequest struct {
	RepoUrl        string `json:"repo_url"`
	Branch         string `json:"branch"`
	Authenticated  bool   `json:"authenticated"`
	LFSEnabled     bool   `json:"lfs_enabled"`
	SearchProvider string `json:"search_provider"`
	Username       string `json:"username"`
	AuthToken      string `json:"auth_token"`
	DocPath        string `json:"doc_path"`
	Name           string `json:"name"`
	PublicAccess   bool   `json:"public_access"`
	ReadOnly       bool   `json:"read_only"`

	// Backend fields
	BackendType string `json:"backend_type"` // "remote_git" (default), "local_git", "s3_git", "s3_store"
	S3Bucket    string `json:"s3_bucket"`
	S3Prefix    string `json:"s3_prefix"`
	S3Region    string `json:"s3_region"`
	S3AccessKey string `json:"s3_access_key"`
	S3SecretKey string `json:"s3_secret_key"`
	S3Endpoint  string `json:"s3_endpoint"`
	Slug        string `json:"slug"` // explicit slug for non-remote-git types
}

type addRepoResponse struct {
	Slug              string `json:"slug"`
	RepoUrl           string `json:"repo_url"`
	Branch            string `json:"branch"`
	Authenticated     bool   `json:"authenticated"`
	LFSEnabled        bool   `json:"lfs_enabled"`
	SearchProvider    string `json:"search_provider"`
	SearchIndexStatus string `json:"search_index_status"`
	DocPath           string `json:"doc_path,omitempty"`
	Name              string `json:"name,omitempty"`
	PublicAccess      bool   `json:"public_access"`
	ReadOnly          bool   `json:"read_only"`
	BackendType       string `json:"backend_type"`
	S3Bucket          string `json:"s3_bucket,omitempty"`
	S3Prefix          string `json:"s3_prefix,omitempty"`
	S3Region          string `json:"s3_region,omitempty"`
	S3Endpoint        string `json:"s3_endpoint,omitempty"`
}

type messageResponse struct {
	Message string `json:"message"`
}

// --- User CRUD ---

// persistUser saves a user to the appropriate table.
func (h *AdminRequesthandler) persistUser(ctx context.Context, u users.UserEntry) error {
	if u.Internal {
		return h.store.Users.UpsertInternalUser(ctx, u)
	}
	return h.store.Users.UpsertExternalUser(ctx, u)
}

// GetAllUsers returns all internal and external users.
func (h *AdminRequesthandler) GetAllUsers(c fuego.ContextNoBody) ([]users.UserEntry, error) {
	h.mu.RLock()
	defer h.mu.RUnlock()

	out := make([]users.UserEntry, len(h.users))
	copy(out, h.users)
	return out, nil
}

// --- Internal user CRUD ---

type addInternalUserRequest struct {
	Email           string   `json:"email" validate:"required"`
	FirstName       string   `json:"first_name"`
	LastName        string   `json:"last_name"`
	Password        string   `json:"password" validate:"required"`
	SuperAdmin      bool     `json:"super_admin"`
	Roles           []string `json:"roles"`
	PasswordExpired bool     `json:"password_expired"`
	Suspended       bool     `json:"suspended"`
}

type updateInternalUserRequest struct {
	FirstName       *string  `json:"first_name"`
	LastName        *string  `json:"last_name"`
	Password        *string  `json:"password,omitempty"`
	SuperAdmin      *bool    `json:"super_admin"`
	Roles           []string `json:"roles"`
	PasswordExpired *bool    `json:"password_expired"`
	Suspended       *bool    `json:"suspended"`
}

// GetInternalUsers returns all internal users.
func (h *AdminRequesthandler) GetInternalUsers(c fuego.ContextNoBody) ([]users.UserEntry, error) {
	h.mu.RLock()
	defer h.mu.RUnlock()

	var out []users.UserEntry
	for _, u := range h.users {
		if u.Internal {
			out = append(out, u)
		}
	}
	return out, nil
}

// AddInternalUser creates a new internal user with a password.
func (h *AdminRequesthandler) AddInternalUser(c fuego.ContextWithBody[addInternalUserRequest]) (users.UserEntry, error) {
	body, err := c.Body()
	if err != nil {
		return users.UserEntry{}, fuego.BadRequestError{Detail: "invalid request body"}
	}
	if body.Email == "" {
		return users.UserEntry{}, fuego.BadRequestError{Detail: "email is required"}
	}
	if body.Password == "" {
		return users.UserEntry{}, fuego.BadRequestError{Detail: "password is required"}
	}

	// Only super admins may create users with super-admin status or roles.
	if body.SuperAdmin || len(body.Roles) > 0 {
		callerID, _ := ginCtxBody(c).Get("user_identity")
		cid, _ := callerID.(string)
		if !h.IsSuperAdmin(cid) {
			return users.UserEntry{}, fuego.ForbiddenError{Detail: "only a super admin can assign roles or super-admin status"}
		}
	}

	ctx := context.Background()
	roles := body.Roles
	if roles == nil {
		roles = []string{}
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	for _, u := range h.users {
		if u.Internal && u.Email == body.Email {
			return users.UserEntry{}, fuego.ConflictError{Detail: fmt.Sprintf("internal user %q already exists", body.Email)}
		}
	}

	entry := users.UserEntry{
		Internal:        true,
		Email:           body.Email,
		FirstName:       body.FirstName,
		LastName:        body.LastName,
		SuperAdmin:      body.SuperAdmin,
		Roles:           roles,
		PasswordExpired: body.PasswordExpired,
		Suspended:       body.Suspended,
	}

	if err := h.store.Users.UpsertInternalUser(ctx, entry); err != nil {
		return users.UserEntry{}, fuego.InternalServerError{Detail: fmt.Sprintf("saving user: %v", err)}
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(body.Password), bcrypt.DefaultCost)
	if err != nil {
		return users.UserEntry{}, fuego.InternalServerError{Detail: "failed to hash password"}
	}
	if err := h.store.Users.SetPasswordHash(ctx, body.Email, string(hash)); err != nil {
		return users.UserEntry{}, fuego.InternalServerError{Detail: fmt.Sprintf("saving password: %v", err)}
	}

	h.users = append(h.users, entry)
	log.WithFields(log.Fields{"email": body.Email}).Info("internal user added via admin API")
	return entry, nil
}

// UpdateInternalUser updates an internal user.
func (h *AdminRequesthandler) UpdateInternalUser(c fuego.ContextWithBody[updateInternalUserRequest]) (users.UserEntry, error) {
	email := ginCtxBody(c).Param("email")
	if email == "" {
		return users.UserEntry{}, fuego.BadRequestError{Detail: "email is required"}
	}

	body, err := c.Body()
	if err != nil {
		return users.UserEntry{}, fuego.BadRequestError{Detail: "invalid request body"}
	}

	// Only super admins may grant/revoke super-admin status or modify roles.
	if body.SuperAdmin != nil || body.Roles != nil {
		callerID, _ := ginCtxBody(c).Get("user_identity")
		cid, _ := callerID.(string)
		if !h.IsSuperAdmin(cid) {
			return users.UserEntry{}, fuego.ForbiddenError{Detail: "only a super admin can modify roles or super-admin status"}
		}
	}

	ctx := context.Background()

	h.mu.Lock()
	defer h.mu.Unlock()

	for i := range h.users {
		if h.users[i].Internal && h.users[i].Email == email {
			// Prevent removing the last super admin via demotion.
			if h.users[i].SuperAdmin && body.SuperAdmin != nil && !*body.SuperAdmin && h.countSuperAdmins() <= 1 {
				return users.UserEntry{}, fuego.BadRequestError{Detail: "cannot demote the last super admin"}
			}
			if body.FirstName != nil {
				h.users[i].FirstName = *body.FirstName
			}
			if body.LastName != nil {
				h.users[i].LastName = *body.LastName
			}
			if body.SuperAdmin != nil {
				h.users[i].SuperAdmin = *body.SuperAdmin
			}
			if body.Roles != nil {
				h.users[i].Roles = body.Roles
			}
			if body.PasswordExpired != nil {
				h.users[i].PasswordExpired = *body.PasswordExpired
			}
			if body.Suspended != nil {
				h.users[i].Suspended = *body.Suspended
			}

			if err := h.store.Users.UpsertInternalUser(ctx, h.users[i]); err != nil {
				return users.UserEntry{}, fuego.InternalServerError{Detail: fmt.Sprintf("saving user: %v", err)}
			}

			if body.Password != nil && *body.Password != "" {
				hash, err := bcrypt.GenerateFromPassword([]byte(*body.Password), bcrypt.DefaultCost)
				if err != nil {
					return users.UserEntry{}, fuego.InternalServerError{Detail: "failed to hash password"}
				}
				if err := h.store.Users.SetPasswordHash(ctx, email, string(hash)); err != nil {
					return users.UserEntry{}, fuego.InternalServerError{Detail: fmt.Sprintf("saving password: %v", err)}
				}
				// Revoke the user's existing tokens after an admin password reset.
				if h.invalidateSessions != nil {
					h.invalidateSessions(h.users[i].Identity())
				}
			}

			log.WithFields(log.Fields{"email": email}).Info("internal user updated via admin API")
			return h.users[i], nil
		}
	}

	return users.UserEntry{}, fuego.NotFoundError{Detail: fmt.Sprintf("internal user %q not found", email)}
}

// DeleteInternalUser removes an internal user.
func (h *AdminRequesthandler) DeleteInternalUser(c fuego.ContextNoBody) (messageResponse, error) {
	email := ginCtx(c).Param("email")
	if email == "" {
		return messageResponse{}, fuego.BadRequestError{Detail: "email is required"}
	}

	ctx := context.Background()

	h.mu.Lock()
	defer h.mu.Unlock()

	for i := range h.users {
		if h.users[i].Internal && h.users[i].Email == email {
			if h.users[i].SuperAdmin && h.countSuperAdmins() <= 1 {
				return messageResponse{}, fuego.BadRequestError{Detail: "cannot delete the last super admin"}
			}
			if err := h.store.Users.DeleteInternalUser(ctx, email); err != nil {
				return messageResponse{}, fuego.InternalServerError{Detail: fmt.Sprintf("deleting user: %v", err)}
			}
			h.users = append(h.users[:i], h.users[i+1:]...)
			log.WithFields(log.Fields{"email": email}).Info("internal user removed via admin API")
			return messageResponse{Message: fmt.Sprintf("internal user %q removed", email)}, nil
		}
	}

	return messageResponse{}, fuego.NotFoundError{Detail: fmt.Sprintf("internal user %q not found", email)}
}

// ResetInternalUserTOTP disables TOTP for an internal user (admin action).
func (h *AdminRequesthandler) ResetInternalUserTOTP(c fuego.ContextNoBody) (messageResponse, error) {
	email := ginCtx(c).Param("email")
	if email == "" {
		return messageResponse{}, fuego.BadRequestError{Detail: "email is required"}
	}

	ctx := context.Background()

	user, err := h.store.Users.GetInternalUser(ctx, email)
	if err != nil {
		return messageResponse{}, fuego.InternalServerError{Detail: fmt.Sprintf("looking up user: %v", err)}
	}
	if user == nil {
		return messageResponse{}, fuego.NotFoundError{Detail: fmt.Sprintf("internal user %q not found", email)}
	}

	if err := h.store.Users.DisableTOTP(ctx, email); err != nil {
		return messageResponse{}, fuego.InternalServerError{Detail: fmt.Sprintf("disabling TOTP: %v", err)}
	}

	log.WithFields(log.Fields{"email": email}).Info("TOTP reset for internal user via admin API")
	return messageResponse{Message: fmt.Sprintf("TOTP disabled for %q", email)}, nil
}

// --- External user CRUD ---

type addExternalUserRequest struct {
	Provider   string   `json:"provider" validate:"required"`
	Email      string   `json:"email" validate:"required"`
	FirstName  string   `json:"first_name"`
	LastName   string   `json:"last_name"`
	SuperAdmin bool     `json:"super_admin"`
	Roles      []string `json:"roles"`
}

type updateExternalUserRequest struct {
	FirstName  *string  `json:"first_name"`
	LastName   *string  `json:"last_name"`
	SuperAdmin *bool    `json:"super_admin"`
	Roles      []string `json:"roles"`
}

// GetExternalUsers returns all external users.
func (h *AdminRequesthandler) GetExternalUsers(c fuego.ContextNoBody) ([]users.UserEntry, error) {
	h.mu.RLock()
	defer h.mu.RUnlock()

	var out []users.UserEntry
	for _, u := range h.users {
		if !u.Internal {
			out = append(out, u)
		}
	}
	return out, nil
}

// AddExternalUser creates a new external user.
func (h *AdminRequesthandler) AddExternalUser(c fuego.ContextWithBody[addExternalUserRequest]) (users.UserEntry, error) {
	body, err := c.Body()
	if err != nil {
		return users.UserEntry{}, fuego.BadRequestError{Detail: "invalid request body"}
	}
	if body.Provider == "" {
		return users.UserEntry{}, fuego.BadRequestError{Detail: "provider is required"}
	}
	if body.Email == "" {
		return users.UserEntry{}, fuego.BadRequestError{Detail: "email is required"}
	}

	// Only super admins may create users with super-admin status or roles.
	if body.SuperAdmin || len(body.Roles) > 0 {
		callerID, _ := ginCtxBody(c).Get("user_identity")
		cid, _ := callerID.(string)
		if !h.IsSuperAdmin(cid) {
			return users.UserEntry{}, fuego.ForbiddenError{Detail: "only a super admin can assign roles or super-admin status"}
		}
	}

	ctx := context.Background()
	// Validate the provider exists (by name) before creating the user.
	if _, err := h.store.Auth.GetAuthProviderID(ctx, body.Provider); err != nil {
		return users.UserEntry{}, fuego.BadRequestError{Detail: fmt.Sprintf("unknown provider %q", body.Provider)}
	}

	roles := body.Roles
	if roles == nil {
		roles = []string{}
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	for _, u := range h.users {
		if !u.Internal && u.Provider == body.Provider && u.Email == body.Email {
			return users.UserEntry{}, fuego.ConflictError{Detail: fmt.Sprintf("external user (%s:%s) already exists", body.Provider, body.Email)}
		}
	}

	entry := users.UserEntry{
		Provider:   body.Provider,
		Email:      body.Email,
		FirstName:  body.FirstName,
		LastName:   body.LastName,
		SuperAdmin: body.SuperAdmin,
		Roles:      roles,
	}

	if err := h.store.Users.UpsertExternalUser(ctx, entry); err != nil {
		return users.UserEntry{}, fuego.InternalServerError{Detail: fmt.Sprintf("saving user: %v", err)}
	}

	h.users = append(h.users, entry)
	log.WithFields(log.Fields{"provider": body.Provider, "email": body.Email}).Info("external user added via admin API")
	return entry, nil
}

// UpdateExternalUser updates an external user.
func (h *AdminRequesthandler) UpdateExternalUser(c fuego.ContextWithBody[updateExternalUserRequest]) (users.UserEntry, error) {
	gc := ginCtxBody(c)
	providerName := gc.Param("provider")
	email := gc.Param("email")
	if providerName == "" || email == "" {
		return users.UserEntry{}, fuego.BadRequestError{Detail: "provider and email are required"}
	}

	body, err := c.Body()
	if err != nil {
		return users.UserEntry{}, fuego.BadRequestError{Detail: "invalid request body"}
	}

	// Only super admins may grant/revoke super-admin status or modify roles.
	if body.SuperAdmin != nil || body.Roles != nil {
		callerID, _ := gc.Get("user_identity")
		cid, _ := callerID.(string)
		if !h.IsSuperAdmin(cid) {
			return users.UserEntry{}, fuego.ForbiddenError{Detail: "only a super admin can modify roles or super-admin status"}
		}
	}

	ctx := context.Background()

	h.mu.Lock()
	defer h.mu.Unlock()

	for i := range h.users {
		if !h.users[i].Internal && h.users[i].Provider == providerName && h.users[i].Email == email {
			if h.users[i].SuperAdmin && body.SuperAdmin != nil && !*body.SuperAdmin && h.countSuperAdmins() <= 1 {
				return users.UserEntry{}, fuego.BadRequestError{Detail: "cannot demote the last super admin"}
			}
			if body.FirstName != nil {
				h.users[i].FirstName = *body.FirstName
			}
			if body.LastName != nil {
				h.users[i].LastName = *body.LastName
			}
			if body.SuperAdmin != nil {
				h.users[i].SuperAdmin = *body.SuperAdmin
			}
			if body.Roles != nil {
				h.users[i].Roles = body.Roles
			}

			if err := h.store.Users.UpsertExternalUser(ctx, h.users[i]); err != nil {
				return users.UserEntry{}, fuego.InternalServerError{Detail: fmt.Sprintf("saving user: %v", err)}
			}

			log.WithFields(log.Fields{"provider": providerName, "email": email}).Info("external user updated via admin API")
			return h.users[i], nil
		}
	}

	return users.UserEntry{}, fuego.NotFoundError{Detail: fmt.Sprintf("external user (%s:%s) not found", providerName, email)}
}

// DeleteExternalUser removes an external user.
func (h *AdminRequesthandler) DeleteExternalUser(c fuego.ContextNoBody) (messageResponse, error) {
	gc := ginCtx(c)
	providerName := gc.Param("provider")
	email := gc.Param("email")
	if providerName == "" || email == "" {
		return messageResponse{}, fuego.BadRequestError{Detail: "provider and email are required"}
	}

	ctx := context.Background()

	h.mu.Lock()
	defer h.mu.Unlock()

	for i := range h.users {
		if !h.users[i].Internal && h.users[i].Provider == providerName && h.users[i].Email == email {
			if err := h.store.Users.DeleteExternalUser(ctx, providerName, email); err != nil {
				return messageResponse{}, fuego.InternalServerError{Detail: fmt.Sprintf("deleting user: %v", err)}
			}
			h.users = append(h.users[:i], h.users[i+1:]...)
			log.WithFields(log.Fields{"provider": providerName, "email": email}).Info("external user removed via admin API")
			return messageResponse{Message: fmt.Sprintf("external user (%s:%s) removed", providerName, email)}, nil
		}
	}

	return messageResponse{}, fuego.NotFoundError{Detail: fmt.Sprintf("external user (%s:%s) not found", providerName, email)}
}

// --- Group CRUD ---

type memberRef struct {
	Provider string `json:"provider" validate:"required"`
	Email    string `json:"email" validate:"required"`
}

type addGroupRequest struct {
	Name        string      `json:"name" validate:"required"`
	Description string      `json:"description"`
	Members     []memberRef `json:"members"`
	Roles       []string    `json:"roles"`
}

type updateGroupRequest struct {
	Description string      `json:"description"`
	Members     []memberRef `json:"members"`
	Roles       []string    `json:"roles"`
}

// GetGroups returns all user groups.
func (h *AdminRequesthandler) GetGroups(c fuego.ContextNoBody) ([]users.UserGroup, error) {
	h.mu.RLock()
	defer h.mu.RUnlock()

	out := make([]users.UserGroup, len(h.groups))
	copy(out, h.groups)
	return out, nil
}

// AddGroup adds a new user group and persists the change.
func (h *AdminRequesthandler) AddGroup(c fuego.ContextWithBody[addGroupRequest]) (users.UserGroup, error) {
	body, err := c.Body()
	if err != nil {
		return users.UserGroup{}, fuego.BadRequestError{Detail: "invalid request body"}
	}
	if body.Name == "" {
		return users.UserGroup{}, fuego.BadRequestError{Detail: "name is required"}
	}

	// Only super admins may create groups that confer roles.
	if len(body.Roles) > 0 {
		callerID, _ := ginCtxBody(c).Get("user_identity")
		cid, _ := callerID.(string)
		if !h.IsSuperAdmin(cid) {
			return users.UserGroup{}, fuego.ForbiddenError{Detail: "only a super admin can assign roles to a group"}
		}
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	for _, g := range h.groups {
		if g.Name == body.Name {
			return users.UserGroup{}, fuego.ConflictError{Detail: fmt.Sprintf("group %q already exists", body.Name)}
		}
	}

	group := users.UserGroup{
		Name:        body.Name,
		Description: body.Description,
		Roles:       body.Roles,
	}

	ctx := context.Background()
	if err := h.store.Users.UpsertGroup(ctx, group); err != nil {
		return users.UserGroup{}, fuego.InternalServerError{Detail: fmt.Sprintf("saving group: %v", err)}
	}

	if len(body.Members) > 0 {
		memberRefs, err := h.resolveMembers(ctx, body.Members)
		if err != nil {
			return users.UserGroup{}, fuego.BadRequestError{Detail: err.Error()}
		}
		if err := h.store.Users.SetGroupMembers(ctx, body.Name, memberRefs); err != nil {
			return users.UserGroup{}, fuego.InternalServerError{Detail: fmt.Sprintf("setting group members: %v", err)}
		}
	}

	// Re-read from DB to get full member info
	saved, err := h.store.Users.GetGroup(ctx, body.Name)
	if err != nil || saved == nil {
		return users.UserGroup{}, fuego.InternalServerError{Detail: "failed to reload group"}
	}

	h.groups = append(h.groups, *saved)
	log.WithField("name", body.Name).Info("group added via admin API")
	return *saved, nil
}

// UpdateGroup updates a group's description, roles, and members.
func (h *AdminRequesthandler) UpdateGroup(c fuego.ContextWithBody[updateGroupRequest]) (users.UserGroup, error) {
	name := ginCtxBody(c).Param("name")
	if name == "" {
		return users.UserGroup{}, fuego.BadRequestError{Detail: "name is required"}
	}

	body, err := c.Body()
	if err != nil {
		return users.UserGroup{}, fuego.BadRequestError{Detail: "invalid request body"}
	}

	// Only super admins may change the roles a group confers.
	if len(body.Roles) > 0 {
		callerID, _ := ginCtxBody(c).Get("user_identity")
		cid, _ := callerID.(string)
		if !h.IsSuperAdmin(cid) {
			return users.UserGroup{}, fuego.ForbiddenError{Detail: "only a super admin can assign roles to a group"}
		}
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	idx := -1
	for i := range h.groups {
		if h.groups[i].Name == name {
			idx = i
			break
		}
	}
	if idx < 0 {
		return users.UserGroup{}, fuego.NotFoundError{Detail: fmt.Sprintf("group %q not found", name)}
	}

	ctx := context.Background()

	h.groups[idx].Description = body.Description
	h.groups[idx].Roles = body.Roles
	if err := h.store.Users.UpsertGroup(ctx, h.groups[idx]); err != nil {
		return users.UserGroup{}, fuego.InternalServerError{Detail: fmt.Sprintf("saving group: %v", err)}
	}

	memberRefs, err := h.resolveMembers(ctx, body.Members)
	if err != nil {
		return users.UserGroup{}, fuego.BadRequestError{Detail: err.Error()}
	}
	if err := h.store.Users.SetGroupMembers(ctx, name, memberRefs); err != nil {
		return users.UserGroup{}, fuego.InternalServerError{Detail: fmt.Sprintf("setting group members: %v", err)}
	}

	// Re-read from DB to get full member info
	saved, err := h.store.Users.GetGroup(ctx, name)
	if err != nil || saved == nil {
		return users.UserGroup{}, fuego.InternalServerError{Detail: "failed to reload group"}
	}

	h.groups[idx] = *saved
	log.WithField("name", name).Info("group updated via admin API")
	return *saved, nil
}

// RemoveGroup removes a user group and persists the change.
func (h *AdminRequesthandler) RemoveGroup(c fuego.ContextNoBody) (messageResponse, error) {
	name := ginCtx(c).Param("name")
	if name == "" {
		return messageResponse{}, fuego.BadRequestError{Detail: "name is required"}
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	for i := range h.groups {
		if h.groups[i].Name == name {
			if err := h.store.Users.DeleteGroup(context.Background(), name); err != nil {
				return messageResponse{}, fuego.InternalServerError{Detail: fmt.Sprintf("deleting group: %v", err)}
			}
			h.groups = append(h.groups[:i], h.groups[i+1:]...)
			log.WithField("name", name).Info("group removed via admin API")
			return messageResponse{Message: fmt.Sprintf("group %q removed", name)}, nil
		}
	}

	return messageResponse{}, fuego.NotFoundError{Detail: fmt.Sprintf("group %q not found", name)}
}

// resolveMembers resolves member references to group member refs, verifying each user exists.
func (h *AdminRequesthandler) resolveMembers(ctx context.Context, members []memberRef) ([]users.GroupMemberRef, error) {
	refs := make([]users.GroupMemberRef, 0, len(members))
	for _, m := range members {
		if m.Provider == "" || m.Email == "" {
			return nil, fmt.Errorf("member requires both provider and email")
		}
		id, err := h.store.Users.GetUserID(ctx, m.Provider, m.Email)
		if err != nil {
			return nil, fmt.Errorf("user %s:%s not found", m.Provider, m.Email)
		}
		refs = append(refs, users.GroupMemberRef{UserID: id})
	}
	return refs, nil
}

// --- Repo Management ---

// GetRepos returns all repo configs with obfuscated auth tokens.
func (h *AdminRequesthandler) GetRepos(c fuego.ContextNoBody) ([]repoInfoResponse, error) {
	h.mu.RLock()
	defer h.mu.RUnlock()

	repos := make([]repoInfoResponse, 0, len(h.repoConfigs))
	for slug, rc := range h.repoConfigs {
		info := h.repoInfoFor(slug, rc)
		info.Username = rc.Username
		info.AuthToken = obfuscateToken(rc.AuthToken)
		repos = append(repos, info)
	}

	return repos, nil
}

// AddRepo adds a new repo to the runtime config, clones it, and registers it.
func (h *AdminRequesthandler) AddRepo(c fuego.ContextWithBody[addRepoRequest]) (addRepoResponse, error) {
	body, err := c.Body()
	if err != nil {
		return addRepoResponse{}, fuego.BadRequestError{Detail: "invalid request body"}
	}

	log.WithField("body", body).Info("add repo request")

	backendType := body.BackendType
	if backendType == "" {
		backendType = "remote_git"
	}

	// Validate per backend type
	switch backendType {
	case "remote_git":
		if body.RepoUrl == "" {
			return addRepoResponse{}, fuego.BadRequestError{Detail: "repo_url is required for remote_git backend"}
		}
		if body.Authenticated && (body.Username == "" || body.AuthToken == "") {
			return addRepoResponse{}, fuego.BadRequestError{Detail: "username and auth_token are required when authenticated is true"}
		}
		// Verify read access to the remote
		username := body.Username
		authToken := body.AuthToken
		if !body.Authenticated {
			username = ""
			authToken = ""
		}
		gitClient := gitpkg.NewGitClient("", body.RepoUrl, username, authToken, false)
		if err := gitClient.CheckReadAccess(); err != nil {
			return addRepoResponse{}, fuego.BadRequestError{Detail: fmt.Sprintf("cannot access repository: %v", err)}
		}
	case "local_git":
		// local_git needs either a local_path or will use the default clone path
	case "s3_git":
		if body.S3Bucket == "" {
			return addRepoResponse{}, fuego.BadRequestError{Detail: "s3_bucket is required for s3_git backend"}
		}
	case "s3_store":
		if body.S3Bucket == "" {
			return addRepoResponse{}, fuego.BadRequestError{Detail: "s3_bucket is required for s3_store backend"}
		}
	default:
		return addRepoResponse{}, fuego.BadRequestError{Detail: fmt.Sprintf("unknown backend_type: %q", backendType)}
	}

	searchProviderID, err := h.resolveSearchProviderID(body.SearchProvider)
	if err != nil {
		return addRepoResponse{}, fuego.BadRequestError{Detail: fmt.Sprintf("invalid search_provider: %v", err)}
	}

	newRepo := git_repo.GitRepo{
		RepoUrl:          body.RepoUrl,
		Branch:           body.Branch,
		Authenticated:    body.Authenticated,
		LFSEnabled:       body.LFSEnabled,
		SearchProviderID: searchProviderID,
		DocPath:          body.DocPath,
		Name:             body.Name,
		BackendType:      backendType,
		S3Bucket:         body.S3Bucket,
		S3Prefix:         body.S3Prefix,
		S3Region:         body.S3Region,
		S3AccessKey:      body.S3AccessKey,
		S3SecretKey:      body.S3SecretKey,
		S3Endpoint:       body.S3Endpoint,
	}
	if body.Authenticated {
		newRepo.Username = body.Username
		newRepo.AuthToken = body.AuthToken
	}
	newRepo.PublicAccess = body.PublicAccess
	newRepo.ReadOnly = body.ReadOnly
	slug := newRepo.Slug()
	if body.Slug != "" {
		slug = body.Slug
	}
	if slug == "" {
		return addRepoResponse{}, fuego.BadRequestError{Detail: "could not derive slug — provide an explicit slug"}
	}

	// Check if repo already exists
	h.mu.RLock()
	_, exists := h.backends[slug]
	h.mu.RUnlock()
	if exists {
		return addRepoResponse{}, fuego.ConflictError{Detail: fmt.Sprintf("repo %q already exists", slug)}
	}

	if newRepo.Branch == "" {
		newRepo.Branch = "main"
	}

	// Persist to database
	if err := h.store.Repos.UpsertRepoWithSlug(context.Background(), slug, newRepo); err != nil {
		return addRepoResponse{}, fuego.InternalServerError{Detail: fmt.Sprintf("saving repo: %v", err)}
	}

	// Register in memory with ready=false, then clone in the background
	var repoPath string
	if backendType == "local_git" {
		repoPath = filepath.Join(h.localGitPath, slug)
	} else {
		repoPath = filepath.Join(h.localRepoPath, slug)
	}
	repoCopy := newRepo
	backend, err := repofactory.NewBackend(&repoCopy, repoPath)
	if err != nil {
		return addRepoResponse{}, fuego.InternalServerError{Detail: fmt.Sprintf("creating backend: %v", err)}
	}

	var dm *draft.Manager
	if s3b, ok := backend.(*s3backend.Backend); ok {
		dm = draft.NewManagerWithStore(draft.NewS3Store(s3b.S3Client(), s3b.Bucket(), s3b.Prefix()))
	} else {
		dm = draft.NewManager(backend.RepoPath())
	}

	h.mu.Lock()
	h.backends[slug] = backend
	h.draftManagers[slug] = dm
	if body.DocPath != "" {
		h.docPaths[slug] = body.DocPath
	}
	h.repoConfigs[slug] = &repoCopy
	h.mu.Unlock()

	h.repoReady.Store(slug, false)

	go func() {
		if err := backend.Initialize(context.Background()); err != nil {
			log.WithError(err).WithField("repo", slug).Error("background clone failed")
			return
		}
		if backendType == "remote_git" || backendType == "local_git" || backendType == "s3_git" {
			if err := dm.EnsureGitignore(); err != nil {
				log.WithError(err).WithField("repo", slug).Warn("failed to ensure .drafts in .gitignore")
			}
		}

		// Verify write access after clone if the repo is writable
		if backendType == "remote_git" && !newRepo.ReadOnly {
			writeClient := gitpkg.NewGitClient(backend.RepoPath(), newRepo.RepoUrl, newRepo.Username, newRepo.AuthToken, false)
			if err := writeClient.CheckWriteAccess(); err != nil {
				log.WithError(err).WithField("repo", slug).Warn("repository is not writable — consider marking it read-only")
			}
		}

		h.repoReady.Store(slug, true)
		log.WithField("slug", slug).Info("repo cloned successfully (background)")

		if h.indexer != nil && newRepo.SearchProviderID != nil {
			h.indexer.FullReindex(slug)
		}
	}()

	resp := addRepoResponse{
		Slug:              slug,
		RepoUrl:           newRepo.RepoUrl,
		Branch:            newRepo.Branch,
		Authenticated:     newRepo.Authenticated,
		LFSEnabled:        newRepo.LFSEnabled,
		SearchProvider:    body.SearchProvider,
		SearchIndexStatus: h.searchIndexStatus(&newRepo, slug),
		DocPath:           body.DocPath,
		Name:              newRepo.Name,
		PublicAccess:      newRepo.PublicAccess,
		ReadOnly:          newRepo.ReadOnly,
		BackendType:       backendType,
	}
	if backendType == "s3_git" || backendType == "s3_store" {
		resp.S3Bucket = newRepo.S3Bucket
		resp.S3Prefix = newRepo.S3Prefix
		resp.S3Region = newRepo.S3Region
		resp.S3Endpoint = newRepo.S3Endpoint
	}
	return resp, nil
}

// RemoveRepo removes a repo from the runtime config, deletes the local clone, and unregisters it.
func (h *AdminRequesthandler) RemoveRepo(c fuego.ContextNoBody) (messageResponse, error) {
	slug := ginCtx(c).Param("slug")
	if slug == "" {
		return messageResponse{}, fuego.BadRequestError{Detail: "slug is required"}
	}

	h.mu.Lock()
	backend, exists := h.backends[slug]
	if !exists {
		h.mu.Unlock()
		return messageResponse{}, fuego.NotFoundError{Detail: fmt.Sprintf("repo %q not found", slug)}
	}

	repoPath := backend.RepoPath()
	repoConfig := h.repoConfigs[slug]
	searchIndexEnabled := repoConfig != nil && repoConfig.SearchProviderID != nil
	delete(h.backends, slug)
	delete(h.draftManagers, slug)
	delete(h.docPaths, slug)
	delete(h.repoConfigs, slug)
	h.mu.Unlock()

	// Delete the local clone
	if err := os.RemoveAll(repoPath); err != nil {
		log.WithError(err).WithField("repo", slug).Warn("failed to delete repo clone")
	}

	// Remove from database
	if err := h.store.Repos.DeleteRepo(context.Background(), slug); err != nil {
		return messageResponse{}, fuego.InternalServerError{Detail: fmt.Sprintf("deleting repo: %v", err)}
	}

	if h.indexer != nil && searchIndexEnabled {
		h.indexer.DeleteRepoIndex(slug)
	}

	log.WithField("slug", slug).Info("repo removed via admin API")
	return messageResponse{Message: fmt.Sprintf("repo %q removed", slug)}, nil
}

// RecloneRepo re-clones a repository in the background.
func (h *AdminRequesthandler) RecloneRepo(c fuego.ContextNoBody) (messageResponse, error) {
	slug := ginCtx(c).Param("slug")
	if slug == "" {
		return messageResponse{}, fuego.BadRequestError{Detail: "slug is required"}
	}

	h.mu.RLock()
	backend, ok := h.backends[slug]
	rc := h.repoConfigs[slug]
	h.mu.RUnlock()
	if !ok {
		return messageResponse{}, fuego.NotFoundError{Detail: fmt.Sprintf("repo %q not found", slug)}
	}

	h.repoReady.Store(slug, false)

	go func() {
		if err := backend.Initialize(context.Background()); err != nil {
			h.repoReady.Store(slug, true)
			log.WithError(err).WithField("repo", slug).Error("background reclone failed")
			return
		}
		h.repoReady.Store(slug, true)
		log.WithField("slug", slug).Info("repo recloned successfully (background)")

		if h.indexer != nil && rc != nil && rc.SearchProviderID != nil {
			h.indexer.FullReindex(slug)
		}
	}()

	return messageResponse{Message: fmt.Sprintf("reclone started for repo %q", slug)}, nil
}

// ReindexRepo triggers a full search index rebuild for a repo.
func (h *AdminRequesthandler) ReindexRepo(c fuego.ContextNoBody) (messageResponse, error) {
	slug := ginCtx(c).Param("slug")
	if slug == "" {
		return messageResponse{}, fuego.BadRequestError{Detail: "slug is required"}
	}

	if h.backendHolder == nil || h.backendHolder.Get() == nil {
		return messageResponse{}, fuego.BadRequestError{Detail: "search is not enabled — no search backend is active"}
	}

	h.mu.RLock()
	rc, ok := h.repoConfigs[slug]
	h.mu.RUnlock()
	if !ok {
		return messageResponse{}, fuego.NotFoundError{Detail: fmt.Sprintf("repo %q not found", slug)}
	}
	if rc.SearchProviderID == nil {
		return messageResponse{}, fuego.BadRequestError{Detail: fmt.Sprintf("search_provider is not set for repo %q", slug)}
	}

	h.indexer.FullReindex(slug)
	log.WithField("slug", slug).Info("search reindex triggered via admin API")
	return messageResponse{Message: fmt.Sprintf("search reindex started for repo %q", slug)}, nil
}

// --- Repo Update ---

type updateRepoSettingsRequest struct {
	RepoUrl        string  `json:"repo_url"`
	Branch         string  `json:"branch"`
	Authenticated  *bool   `json:"authenticated"`
	LFSEnabled     *bool   `json:"lfs_enabled"`
	SearchProvider *string `json:"search_provider"`
	Username       string  `json:"username"`
	AuthToken      string  `json:"auth_token"`
	DocPath        string  `json:"doc_path"`
	Name           string  `json:"name"`
	PublicAccess   *bool   `json:"public_access"`
	ReadOnly       *bool   `json:"read_only"`

	// S3 backend fields (only applied when backend_type is "s3_git" or "s3_store")
	S3Bucket    *string `json:"s3_bucket"`
	S3Prefix    *string `json:"s3_prefix"`
	S3Region    *string `json:"s3_region"`
	S3AccessKey *string `json:"s3_access_key"`
	S3SecretKey *string `json:"s3_secret_key"`
	S3Endpoint  *string `json:"s3_endpoint"`
}

// UpdateRepoSettings updates general settings for an existing repo (everything except permissions).
func (h *AdminRequesthandler) UpdateRepoSettings(c fuego.ContextWithBody[updateRepoSettingsRequest]) (repoInfoResponse, error) {
	slug := ginCtxBody(c).Param("slug")
	if slug == "" {
		return repoInfoResponse{}, fuego.BadRequestError{Detail: "slug is required"}
	}

	body, err := c.Body()
	if err != nil {
		return repoInfoResponse{}, fuego.BadRequestError{Detail: "invalid request body"}
	}

	log.WithField("slug", slug).WithField("body", body).Info("update repo settings request")

	// Resolve search provider name to ID before taking the lock
	var newSearchProviderID *int
	var searchProviderResolved bool
	if body.SearchProvider != nil {
		searchProviderResolved = true
		resolved, err := h.resolveSearchProviderID(*body.SearchProvider)
		if err != nil {
			return repoInfoResponse{}, fuego.BadRequestError{Detail: fmt.Sprintf("invalid search_provider: %v", err)}
		}
		newSearchProviderID = resolved
	}

	h.mu.Lock()
	rc, ok := h.repoConfigs[slug]
	if !ok {
		h.mu.Unlock()
		return repoInfoResponse{}, fuego.NotFoundError{Detail: fmt.Sprintf("repo %q not found", slug)}
	}

	oldSearchProviderID := rc.SearchProviderID

	rc.RepoUrl = body.RepoUrl
	rc.Branch = body.Branch
	rc.DocPath = body.DocPath
	rc.Name = body.Name
	if body.Authenticated != nil {
		rc.Authenticated = *body.Authenticated
	}
	if body.LFSEnabled != nil {
		rc.LFSEnabled = *body.LFSEnabled
	}
	if searchProviderResolved {
		rc.SearchProviderID = newSearchProviderID
	}
	if rc.Authenticated {
		if body.Username != "" {
			rc.Username = body.Username
		}
		if body.AuthToken != "" {
			rc.AuthToken = body.AuthToken
		}
		if rc.Username == "" || rc.AuthToken == "" {
			h.mu.Unlock()
			return repoInfoResponse{}, fuego.BadRequestError{Detail: "username and auth_token are required when authenticated is true"}
		}
	} else {
		rc.Username = ""
		rc.AuthToken = ""
	}
	if body.PublicAccess != nil {
		rc.PublicAccess = *body.PublicAccess
	}
	if body.ReadOnly != nil {
		rc.ReadOnly = *body.ReadOnly
	}

	// Apply S3 field updates
	s3Changed := false
	if rc.BackendType == "s3_git" || rc.BackendType == "s3_store" {
		if body.S3Bucket != nil {
			if *body.S3Bucket == "" {
				h.mu.Unlock()
				return repoInfoResponse{}, fuego.BadRequestError{Detail: "s3_bucket cannot be empty"}
			}
			if *body.S3Bucket != rc.S3Bucket {
				rc.S3Bucket = *body.S3Bucket
				s3Changed = true
			}
		}
		if body.S3Prefix != nil && *body.S3Prefix != rc.S3Prefix {
			rc.S3Prefix = *body.S3Prefix
			s3Changed = true
		}
		if body.S3Region != nil && *body.S3Region != rc.S3Region {
			rc.S3Region = *body.S3Region
			s3Changed = true
		}
		if body.S3AccessKey != nil {
			rc.S3AccessKey = *body.S3AccessKey
			s3Changed = true
		}
		if body.S3SecretKey != nil {
			rc.S3SecretKey = *body.S3SecretKey
			s3Changed = true
		}
		if body.S3Endpoint != nil && *body.S3Endpoint != rc.S3Endpoint {
			rc.S3Endpoint = *body.S3Endpoint
			s3Changed = true
		}
	}

	// Detect local_git → remote_git promotion
	promoteToRemote := rc.BackendType == "local_git" && body.RepoUrl != "" && rc.RepoUrl != ""

	// Verify access when credentials or URL changed for remote_git repos
	if rc.BackendType == "remote_git" && rc.RepoUrl != "" {
		needsCheck := body.RepoUrl != "" || body.Username != "" || body.AuthToken != "" || body.Authenticated != nil
		if needsCheck {
			username := rc.Username
			authToken := rc.AuthToken
			if !rc.Authenticated {
				username = ""
				authToken = ""
			}
			h.mu.Unlock()

			checkClient := gitpkg.NewGitClient("", rc.RepoUrl, username, authToken, false)
			if err := checkClient.CheckReadAccess(); err != nil {
				return repoInfoResponse{}, fuego.BadRequestError{Detail: fmt.Sprintf("cannot access repository with updated settings: %v", err)}
			}

			// Write access check: if not read-only and repo is already cloned, verify push
			if !rc.ReadOnly {
				h.mu.RLock()
				backend, hasBackend := h.backends[slug]
				h.mu.RUnlock()
				if hasBackend {
					writeClient := gitpkg.NewGitClient(backend.RepoPath(), rc.RepoUrl, username, authToken, false)
					if err := writeClient.CheckWriteAccess(); err != nil {
						return repoInfoResponse{}, fuego.BadRequestError{Detail: fmt.Sprintf("repository is not writable with updated settings: %v", err)}
					}
				}
			}

			h.mu.Lock()
		}
	}
	h.mu.Unlock()

	if err := h.store.Repos.UpsertRepoWithSlug(context.Background(), slug, *rc); err != nil {
		return repoInfoResponse{}, fuego.InternalServerError{Detail: fmt.Sprintf("saving repo: %v", err)}
	}

	// Handle local_git → remote_git promotion
	if promoteToRemote {
		h.mu.RLock()
		backend := h.backends[slug]
		h.mu.RUnlock()

		if lgBackend, ok := backend.(*localgit.Backend); ok {
			branch := rc.Branch
			if branch == "" {
				branch = "main"
			}
			if err := lgBackend.ConnectRemote(rc.RepoUrl, branch); err != nil {
				log.WithError(err).WithField("slug", slug).Error("failed to promote local_git to remote_git")
				return repoInfoResponse{}, fuego.InternalServerError{Detail: fmt.Sprintf("promoting to remote_git: %v", err)}
			}

			// Update backend type in config and DB
			h.mu.Lock()
			rc.BackendType = "remote_git"
			h.mu.Unlock()

			if err := h.store.Repos.UpsertRepoWithSlug(context.Background(), slug, *rc); err != nil {
				return repoInfoResponse{}, fuego.InternalServerError{Detail: fmt.Sprintf("saving promoted repo: %v", err)}
			}

			// Swap backend instance
			repoPath := backend.RepoPath()
			newBackend, err := repofactory.NewBackend(rc, repoPath)
			if err != nil {
				return repoInfoResponse{}, fuego.InternalServerError{Detail: fmt.Sprintf("creating remote_git backend: %v", err)}
			}

			h.mu.Lock()
			h.backends[slug] = newBackend
			h.mu.Unlock()

			log.WithField("slug", slug).Info("local_git repo promoted to remote_git")
		}
	}

	// Recreate S3 backend when connection settings changed
	if s3Changed {
		var repoPath string
		if rc.BackendType == "local_git" {
			repoPath = filepath.Join(h.localGitPath, slug)
		} else {
			repoPath = filepath.Join(h.localRepoPath, slug)
		}
		repoCopy := *rc
		newBackend, err := repofactory.NewBackend(&repoCopy, repoPath)
		if err != nil {
			return repoInfoResponse{}, fuego.InternalServerError{Detail: fmt.Sprintf("creating s3 backend: %v", err)}
		}

		var dm *draft.Manager
		if s3b, ok := newBackend.(*s3backend.Backend); ok {
			dm = draft.NewManagerWithStore(draft.NewS3Store(s3b.S3Client(), s3b.Bucket(), s3b.Prefix()))
		} else {
			dm = draft.NewManager(newBackend.RepoPath())
		}

		h.mu.Lock()
		h.backends[slug] = newBackend
		h.draftManagers[slug] = dm
		h.mu.Unlock()

		// Re-initialize in background
		h.repoReady.Store(slug, false)
		go func() {
			if err := newBackend.Initialize(context.Background()); err != nil {
				log.WithError(err).WithField("repo", slug).Error("s3 backend re-initialization failed")
			}
			h.repoReady.Store(slug, true)
			log.WithField("slug", slug).Info("s3 backend re-initialized after settings update")
		}()
	}

	if h.indexer != nil {
		oldSet := oldSearchProviderID != nil
		newSet := rc.SearchProviderID != nil
		changed := oldSet != newSet || (oldSet && newSet && *oldSearchProviderID != *rc.SearchProviderID)
		if changed {
			if newSet {
				h.indexer.FullReindex(slug)
			} else {
				h.indexer.DeleteRepoIndex(slug)
			}
		}
	}

	log.WithField("slug", slug).Info("repo settings updated via admin API")

	return h.repoInfoFor(slug, rc), nil
}

// repoInfoFor builds a repoInfoResponse from a repo config.
func (h *AdminRequesthandler) repoInfoFor(slug string, rc *git_repo.GitRepo) repoInfoResponse {
	bt := rc.BackendType
	if bt == "" {
		bt = "remote_git"
	}
	resp := repoInfoResponse{
		Slug:              slug,
		RepoUrl:           rc.RepoUrl,
		Branch:            rc.Branch,
		Authenticated:     rc.Authenticated,
		LFSEnabled:        rc.LFSEnabled,
		SearchProvider:    h.searchProviderName(rc.SearchProviderID),
		SearchIndexStatus: h.searchIndexStatus(rc, slug),
		DocPath:           rc.DocPath,
		Name:              rc.Name,
		PublicAccess:      rc.PublicAccess,
		ReadOnly:          rc.ReadOnly,
		BackendType:       bt,
	}
	if bt == "s3_git" || bt == "s3_store" {
		resp.S3Bucket = rc.S3Bucket
		resp.S3Prefix = rc.S3Prefix
		resp.S3Region = rc.S3Region
		resp.S3Endpoint = rc.S3Endpoint
		resp.S3HasCredentials = rc.S3AccessKey != ""
	}
	return resp
}

// GetSupportedAuthProviders returns the list of auth provider types that the server supports.
func (h *AdminRequesthandler) GetSupportedAuthProviders(c fuego.ContextNoBody) ([]string, error) {
	return auth.SupportedProviders(), nil
}

// --- Auth Provider CRUD ---

type authProviderResponse struct {
	Name         string   `json:"name"`
	ClientID     string   `json:"client_id"`
	ClientSecret string   `json:"client_secret"`
	TenantID     string   `json:"tenant_id,omitempty"`
	IssuerURL    string   `json:"issuer_url,omitempty"`
	Domain       string   `json:"domain,omitempty"`
	Scopes       []string `json:"scopes,omitempty"`
}

type addAuthProviderRequest struct {
	Name         string   `json:"name" validate:"required"`
	ClientID     string   `json:"client_id" validate:"required"`
	ClientSecret string   `json:"client_secret" validate:"required"`
	TenantID     string   `json:"tenant_id,omitempty"`
	IssuerURL    string   `json:"issuer_url,omitempty"`
	Domain       string   `json:"domain,omitempty"`
	Scopes       []string `json:"scopes,omitempty"`
}

type updateAuthProviderRequest struct {
	ClientID     string   `json:"client_id"`
	ClientSecret string   `json:"client_secret"`
	TenantID     string   `json:"tenant_id,omitempty"`
	IssuerURL    string   `json:"issuer_url,omitempty"`
	Domain       string   `json:"domain,omitempty"`
	Scopes       []string `json:"scopes,omitempty"`
}

func obfuscateSecret(s string) string {
	if len(s) <= 3 {
		return strings.Repeat("*", len(s))
	}
	return strings.Repeat("*", len(s)-3) + s[len(s)-3:]
}

func (h *AdminRequesthandler) authProviderToResponse(p storeauth.AuthProvider) authProviderResponse {
	return authProviderResponse{
		Name:         p.Name,
		ClientID:     p.ClientID,
		ClientSecret: obfuscateSecret(p.ClientSecret),
		TenantID:     p.TenantID,
		IssuerURL:    p.IssuerURL,
		Domain:       p.Domain,
		Scopes:       p.Scopes,
	}
}

// GetAuthProviders returns all auth providers.
func (h *AdminRequesthandler) GetAuthProviders(c fuego.ContextNoBody) ([]authProviderResponse, error) {
	h.mu.RLock()
	defer h.mu.RUnlock()

	var out []authProviderResponse
	for _, p := range h.authProviders {
		out = append(out, h.authProviderToResponse(p))
	}

	return out, nil
}

// AddAuthProvider adds a new auth provider.
func (h *AdminRequesthandler) AddAuthProvider(c fuego.ContextWithBody[addAuthProviderRequest]) (authProviderResponse, error) {
	body, err := c.Body()
	if err != nil {
		return authProviderResponse{}, fuego.BadRequestError{Detail: "invalid request body"}
	}
	if body.Name == "" || body.ClientID == "" || body.ClientSecret == "" {
		return authProviderResponse{}, fuego.BadRequestError{Detail: "name, client_id, and client_secret are required"}
	}
	if body.Name == "hydra" {
		return authProviderResponse{}, fuego.BadRequestError{Detail: "hydra is the internal auth provider and cannot be added as an external provider"}
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	// Check for duplicates across all providers
	for _, p := range h.authProviders {
		if p.Name == body.Name {
			return authProviderResponse{}, fuego.ConflictError{Detail: fmt.Sprintf("auth provider %q already exists", body.Name)}
		}
	}

	newProvider := storeauth.AuthProvider{
		Name:         body.Name,
		ClientID:     body.ClientID,
		ClientSecret: body.ClientSecret,
		TenantID:     body.TenantID,
		IssuerURL:    body.IssuerURL,
		Domain:       body.Domain,
		Scopes:       body.Scopes,
	}

	// Register in both registries
	if err := h.registerProvider(newProvider); err != nil {
		return authProviderResponse{}, fuego.BadRequestError{Detail: err.Error()}
	}

	if err := h.store.Auth.UpsertAuthProvider(context.Background(), newProvider); err != nil {
		h.httpRegistry.Unregister(newProvider.Name)
		h.mcpRegistry.Unregister(newProvider.Name)
		return authProviderResponse{}, fuego.InternalServerError{Detail: fmt.Sprintf("saving auth provider: %v", err)}
	}

	h.authProviders = append(h.authProviders, newProvider)

	log.WithField("name", body.Name).Info("auth provider added via admin API")
	return h.authProviderToResponse(newProvider), nil
}

// UpdateAuthProvider updates an existing auth provider.
func (h *AdminRequesthandler) UpdateAuthProvider(c fuego.ContextWithBody[updateAuthProviderRequest]) (authProviderResponse, error) {
	name := ginCtxBody(c).Param("name")
	if name == "" {
		return authProviderResponse{}, fuego.BadRequestError{Detail: "name is required"}
	}

	body, err := c.Body()
	if err != nil {
		return authProviderResponse{}, fuego.BadRequestError{Detail: "invalid request body"}
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	for i := range h.authProviders {
		if h.authProviders[i].Name == name {
			if body.ClientID != "" {
				h.authProviders[i].ClientID = body.ClientID
			}
			if body.ClientSecret != "" {
				h.authProviders[i].ClientSecret = body.ClientSecret
			}
			h.authProviders[i].TenantID = body.TenantID
			h.authProviders[i].IssuerURL = body.IssuerURL
			h.authProviders[i].Domain = body.Domain
			h.authProviders[i].Scopes = body.Scopes

			// Re-register in both registries
			h.httpRegistry.Unregister(name)
			h.mcpRegistry.Unregister(name)
			if err := h.registerProvider(h.authProviders[i]); err != nil {
				return authProviderResponse{}, fuego.InternalServerError{Detail: fmt.Sprintf("re-registering provider: %v", err)}
			}

			if err := h.store.Auth.UpsertAuthProvider(context.Background(), h.authProviders[i]); err != nil {
				return authProviderResponse{}, fuego.InternalServerError{Detail: fmt.Sprintf("saving auth provider: %v", err)}
			}

			log.WithField("name", name).Info("auth provider updated via admin API")
			return h.authProviderToResponse(h.authProviders[i]), nil
		}
	}

	return authProviderResponse{}, fuego.NotFoundError{Detail: fmt.Sprintf("auth provider %q not found", name)}
}

// RemoveAuthProvider removes an auth provider.
func (h *AdminRequesthandler) RemoveAuthProvider(c fuego.ContextNoBody) (messageResponse, error) {
	name := ginCtx(c).Param("name")
	if name == "" {
		return messageResponse{}, fuego.BadRequestError{Detail: "name is required"}
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	for i := range h.authProviders {
		if h.authProviders[i].Name == name {
			if err := h.store.Auth.DeleteAuthProvider(context.Background(), name); err != nil {
				return messageResponse{}, fuego.InternalServerError{Detail: fmt.Sprintf("deleting auth provider: %v", err)}
			}

			h.authProviders = append(h.authProviders[:i], h.authProviders[i+1:]...)
			h.httpRegistry.Unregister(name)
			h.mcpRegistry.Unregister(name)

			log.WithField("name", name).Info("auth provider removed via admin API")
			return messageResponse{Message: fmt.Sprintf("auth provider %q removed", name)}, nil
		}
	}

	return messageResponse{}, fuego.NotFoundError{Detail: fmt.Sprintf("auth provider %q not found", name)}
}

// registerProvider registers a provider in both HTTP and MCP registries.
func (h *AdminRequesthandler) registerProvider(p storeauth.AuthProvider) error {
	httpEntry, err := auth.NewProviderFromConfig(p, h.defaultBaseURL+"/auth/callback")
	if err != nil {
		return fmt.Errorf("creating HTTP provider %q: %w", p.Name, err)
	}
	h.httpRegistry.Register(httpEntry)

	mcpEntry, err := auth.NewProviderFromConfig(p, h.defaultBaseURL+"/mcp/callback")
	if err != nil {
		h.httpRegistry.Unregister(p.Name)
		return fmt.Errorf("creating MCP provider %q: %w", p.Name, err)
	}
	h.mcpRegistry.Register(mcpEntry)

	return nil
}

// --- LLM Provider CRUD ---

type llmProviderResponse struct {
	Name         string `json:"name"`
	ProviderType string `json:"provider_type"`
	APIKey       string `json:"api_key"`
	BaseURL      string `json:"base_url,omitempty"`
	MaxTokens    int64  `json:"max_tokens"`
	DefaultModel string `json:"default_model"`
}

type addLLMProviderRequest struct {
	Name         string `json:"name" validate:"required"`
	ProviderType string `json:"provider_type" validate:"required"`
	APIKey       string `json:"api_key"`
	BaseURL      string `json:"base_url"`
	MaxTokens    int64  `json:"max_tokens"`
	DefaultModel string `json:"default_model"`
}

type updateLLMProviderRequest struct {
	ProviderType string `json:"provider_type"`
	APIKey       string `json:"api_key"`
	BaseURL      string `json:"base_url"`
	MaxTokens    int64  `json:"max_tokens"`
	DefaultModel string `json:"default_model"`
}

func obfuscateAPIKey(key string) string {
	if len(key) <= 4 {
		return strings.Repeat("*", len(key))
	}
	return strings.Repeat("*", len(key)-4) + key[len(key)-4:]
}

type supportedLLMProviderResponse struct {
	Name          string             `json:"name"`
	Models        []llmModelResponse `json:"models"`
	DefaultModel  string             `json:"default_model"`
	RequiredField string             `json:"required_field"`
}

type llmModelResponse struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// GetSupportedLLMProviders returns the list of LLM provider types the system can integrate with.
func (h *AdminRequesthandler) GetSupportedLLMProviders(c fuego.ContextNoBody) ([]supportedLLMProviderResponse, error) {
	toModelResponses := func(models []llm.Model) []llmModelResponse {
		out := make([]llmModelResponse, len(models))
		for i, m := range models {
			out[i] = llmModelResponse{ID: m.ID, Name: m.Name}
		}
		return out
	}

	type providerDef struct {
		name          string
		requiredField string
	}
	providers := []providerDef{
		{"anthropic", "api_key"},
		{"deepseek", "api_key"},
		{"google", "api_key"},
		{"meta", "api_key"},
		{"mistral", "api_key"},
		{"ollama", "base_url"},
		{"openai", "api_key"},
		{"perplexity", "api_key"},
		{"xai", "api_key"},
	}

	var out []supportedLLMProviderResponse
	for _, p := range providers {
		models := h.modelRegistry.Models(p.name)
		defaultModel := h.modelRegistry.DefaultModel(p.name)
		out = append(out, supportedLLMProviderResponse{
			Name:          p.name,
			Models:        toModelResponses(models),
			DefaultModel:  defaultModel.ID,
			RequiredField: p.requiredField,
		})
	}
	return out, nil
}

// GetLLMProviders returns all LLM providers.
func (h *AdminRequesthandler) GetLLMProviders(c fuego.ContextNoBody) ([]llmProviderResponse, error) {
	providers, err := h.store.LLM.ListLLMProviders(context.Background())
	if err != nil {
		return nil, fuego.InternalServerError{Detail: fmt.Sprintf("listing LLM providers: %v", err)}
	}

	var out []llmProviderResponse
	for _, p := range providers {
		out = append(out, llmProviderResponse{
			Name:         p.Name,
			ProviderType: p.ProviderType,
			APIKey:       obfuscateAPIKey(p.APIKey),
			BaseURL:      p.BaseURL,
			MaxTokens:    p.MaxTokens,
			DefaultModel: p.DefaultModel,
		})
	}
	return out, nil
}

// AddLLMProvider adds a new LLM provider.
func (h *AdminRequesthandler) AddLLMProvider(c fuego.ContextWithBody[addLLMProviderRequest]) (llmProviderResponse, error) {
	body, err := c.Body()
	if err != nil {
		return llmProviderResponse{}, fuego.BadRequestError{Detail: "invalid request body"}
	}
	if body.Name == "" {
		return llmProviderResponse{}, fuego.BadRequestError{Detail: "name is required"}
	}
	if body.ProviderType == "" {
		return llmProviderResponse{}, fuego.BadRequestError{Detail: "provider_type is required"}
	}

	p := storellm.LLMConfig{
		Name:         body.Name,
		ProviderType: body.ProviderType,
		APIKey:       body.APIKey,
		BaseURL:      body.BaseURL,
		MaxTokens:    body.MaxTokens,
		DefaultModel: body.DefaultModel,
	}

	if err := h.store.LLM.UpsertLLMProvider(context.Background(), p); err != nil {
		return llmProviderResponse{}, fuego.InternalServerError{Detail: fmt.Sprintf("saving LLM provider: %v", err)}
	}

	h.modelRegistry.MarkConfigured(p.ProviderType)
	if h.llmRebuild != nil {
		h.llmRebuild()
	}
	log.WithField("name", body.Name).Info("LLM provider added via admin API")
	return llmProviderResponse{
		Name:         p.Name,
		ProviderType: p.ProviderType,
		APIKey:       obfuscateAPIKey(p.APIKey),
		BaseURL:      p.BaseURL,
		MaxTokens:    p.MaxTokens,
		DefaultModel: p.DefaultModel,
	}, nil
}

// UpdateLLMProvider updates an existing LLM provider.
func (h *AdminRequesthandler) UpdateLLMProvider(c fuego.ContextWithBody[updateLLMProviderRequest]) (llmProviderResponse, error) {
	name := ginCtxBody(c).Param("name")
	if name == "" {
		return llmProviderResponse{}, fuego.BadRequestError{Detail: "name is required"}
	}

	body, err := c.Body()
	if err != nil {
		return llmProviderResponse{}, fuego.BadRequestError{Detail: "invalid request body"}
	}

	existing, err := h.store.LLM.GetLLMProvider(context.Background(), name)
	if err != nil {
		return llmProviderResponse{}, fuego.InternalServerError{Detail: fmt.Sprintf("loading LLM provider: %v", err)}
	}
	if existing == nil {
		return llmProviderResponse{}, fuego.NotFoundError{Detail: fmt.Sprintf("LLM provider %q not found", name)}
	}

	if body.ProviderType != "" {
		existing.ProviderType = body.ProviderType
	}
	if body.APIKey != "" {
		existing.APIKey = body.APIKey
	}
	if body.BaseURL != "" {
		existing.BaseURL = body.BaseURL
	}
	if body.MaxTokens != 0 {
		existing.MaxTokens = body.MaxTokens
	}
	if body.DefaultModel != "" {
		existing.DefaultModel = body.DefaultModel
	}

	if err := h.store.LLM.UpsertLLMProvider(context.Background(), *existing); err != nil {
		return llmProviderResponse{}, fuego.InternalServerError{Detail: fmt.Sprintf("saving LLM provider: %v", err)}
	}

	if h.llmRebuild != nil {
		h.llmRebuild()
	}
	log.WithField("name", name).Info("LLM provider updated via admin API")
	return llmProviderResponse{
		Name:         existing.Name,
		ProviderType: existing.ProviderType,
		APIKey:       obfuscateAPIKey(existing.APIKey),
		BaseURL:      existing.BaseURL,
		MaxTokens:    existing.MaxTokens,
		DefaultModel: existing.DefaultModel,
	}, nil
}

// RemoveLLMProvider removes an LLM provider.
func (h *AdminRequesthandler) RemoveLLMProvider(c fuego.ContextNoBody) (messageResponse, error) {
	name := ginCtx(c).Param("name")
	if name == "" {
		return messageResponse{}, fuego.BadRequestError{Detail: "name is required"}
	}

	existing, err := h.store.LLM.GetLLMProvider(context.Background(), name)
	if err != nil {
		return messageResponse{}, fuego.InternalServerError{Detail: fmt.Sprintf("loading LLM provider: %v", err)}
	}
	if existing == nil {
		return messageResponse{}, fuego.NotFoundError{Detail: fmt.Sprintf("LLM provider %q not found", name)}
	}

	if err := h.store.LLM.DeleteLLMProvider(context.Background(), name); err != nil {
		return messageResponse{}, fuego.InternalServerError{Detail: fmt.Sprintf("deleting LLM provider: %v", err)}
	}

	// Unmark if no other provider of the same type remains
	remaining, _ := h.store.LLM.ListLLMProviders(context.Background())
	stillConfigured := false
	for _, p := range remaining {
		if p.ProviderType == existing.ProviderType {
			stillConfigured = true
			break
		}
	}
	if !stillConfigured {
		h.modelRegistry.UnmarkConfigured(existing.ProviderType)
	}
	if h.llmRebuild != nil {
		h.llmRebuild()
	}

	log.WithField("name", name).Info("LLM provider removed via admin API")
	return messageResponse{Message: fmt.Sprintf("LLM provider %q removed", name)}, nil
}

// --- Search Provider Management ---

type searchProviderResponse struct {
	Name         string            `json:"name"`
	ProviderType string            `json:"provider_type"`
	Config       map[string]string `json:"config"`
}

type addSearchProviderRequest struct {
	Name         string            `json:"name" validate:"required"`
	ProviderType string            `json:"provider_type" validate:"required"`
	Config       map[string]string `json:"config"`
}

type updateSearchProviderRequest struct {
	ProviderType string            `json:"provider_type"`
	Config       map[string]string `json:"config"`
}

func obfuscateSearchConfig(cfg map[string]string) map[string]string {
	out := make(map[string]string, len(cfg))
	for k, v := range cfg {
		if (k == "api_key" || k == "embedding_api_key" || k == "password") && v != "" {
			out[k] = obfuscateAPIKey(v)
		} else {
			out[k] = v
		}
	}
	return out
}

// GetSupportedSearchProviders returns metadata about all supported search provider types.
func (h *AdminRequesthandler) GetSupportedSearchProviders(c fuego.ContextNoBody) ([]backend.ProviderInfo, error) {
	return backend.SupportedProviders(), nil
}

// GetSearchProviders returns all search providers.
func (h *AdminRequesthandler) GetSearchProviders(c fuego.ContextNoBody) ([]searchProviderResponse, error) {
	providers, err := h.store.Search.ListSearchProviders(context.Background())
	if err != nil {
		return nil, fuego.InternalServerError{Detail: fmt.Sprintf("listing search providers: %v", err)}
	}

	var out []searchProviderResponse
	for _, p := range providers {
		out = append(out, searchProviderResponse{
			Name:         p.Name,
			ProviderType: p.ProviderType,
			Config:       obfuscateSearchConfig(p.Config),
		})
	}
	if out == nil {
		out = []searchProviderResponse{}
	}
	return out, nil
}

// checkSearchProviderDuplicate checks whether a new or updated search provider
// would conflict with an existing one. Rules:
//   - Only one pgvector provider may use the internal DB (external_db != "true").
//   - Each pgvector provider with an external DB must target a unique host:port/database.
//
// excludeName is the provider being created/updated and is excluded from the comparison.
func (h *AdminRequesthandler) checkSearchProviderDuplicate(providerType string, cfg map[string]string, excludeName string) error {
	existing, err := h.store.Search.ListSearchProviders(context.Background())
	if err != nil {
		return fmt.Errorf("listing providers: %w", err)
	}

	for _, p := range existing {
		if p.Name == excludeName || p.ProviderType != providerType {
			continue
		}

		switch providerType {
		case "pgvector":
			newExternal := cfg["external_db"] == "true"
			existingExternal := p.Config["external_db"] == "true"

			if !newExternal && !existingExternal {
				return fmt.Errorf("a pgvector provider using the internal database already exists (%q)", p.Name)
			}

			if newExternal && existingExternal {
				newPort := cfg["port"]
				if newPort == "" {
					newPort = "5432"
				}
				existingPort := p.Config["port"]
				if existingPort == "" {
					existingPort = "5432"
				}

				if cfg["host"] == p.Config["host"] && newPort == existingPort && cfg["database"] == p.Config["database"] {
					return fmt.Errorf("a pgvector provider targeting the same database already exists (%q)", p.Name)
				}
			}
		}
	}
	return nil
}

// ensureSearchBackend lazily initializes the search backend if none is active.
// It loads the first configured provider from the DB and uses the factory to create
// a backend, then swaps it into the holder.
func (h *AdminRequesthandler) ensureSearchBackend() {
	if h.backendHolder == nil || h.searchBackendFactory == nil {
		return
	}
	if h.backendHolder.Get() != nil {
		return // already active
	}

	providers, err := h.store.Search.ListSearchProviders(context.Background())
	if err != nil || len(providers) == 0 {
		return
	}

	active := providers[0]
	newBackend, err := h.searchBackendFactory(active.ProviderType, active.Config)
	if err != nil {
		log.WithError(err).WithField("provider", active.Name).Error("failed to lazily initialize search backend")
		return
	}

	old := h.backendHolder.Swap(newBackend)
	if old != nil {
		old.Close()
	}
	log.WithField("provider", active.Name).Info("search backend initialized at runtime")
}

// AddSearchProvider adds a new search provider.
func (h *AdminRequesthandler) AddSearchProvider(c fuego.ContextWithBody[addSearchProviderRequest]) (searchProviderResponse, error) {
	body, err := c.Body()
	if err != nil {
		return searchProviderResponse{}, fuego.BadRequestError{Detail: "invalid request body"}
	}
	if body.Name == "" || body.ProviderType == "" {
		return searchProviderResponse{}, fuego.BadRequestError{Detail: "name and provider_type are required"}
	}

	existing, _ := h.store.Search.GetSearchProvider(context.Background(), body.Name)
	if existing != nil {
		return searchProviderResponse{}, fuego.ConflictError{Detail: fmt.Sprintf("search provider %q already exists", body.Name)}
	}

	cfg := body.Config
	if cfg == nil {
		cfg = map[string]string{}
	}

	if body.ProviderType == "pgvector" && cfg["external_db"] == "true" {
		for _, key := range []string{"host", "database", "username", "password"} {
			if cfg[key] == "" {
				return searchProviderResponse{}, fuego.BadRequestError{Detail: fmt.Sprintf("%s is required when external_db is true", key)}
			}
		}
	}

	if err := h.checkSearchProviderDuplicate(body.ProviderType, cfg, body.Name); err != nil {
		return searchProviderResponse{}, fuego.ConflictError{Detail: err.Error()}
	}

	p := storesearch.SearchProviderConfig{
		Name:         body.Name,
		ProviderType: body.ProviderType,
		Config:       cfg,
	}

	if err := h.store.Search.UpsertSearchProvider(context.Background(), p); err != nil {
		return searchProviderResponse{}, fuego.InternalServerError{Detail: fmt.Sprintf("creating search provider: %v", err)}
	}

	// Lazily initialize the search backend if this is the first provider
	h.ensureSearchBackend()

	log.WithField("name", body.Name).Info("search provider added via admin API")
	return searchProviderResponse{
		Name:         p.Name,
		ProviderType: p.ProviderType,
		Config:       obfuscateSearchConfig(cfg),
	}, nil
}

// UpdateSearchProvider updates an existing search provider.
func (h *AdminRequesthandler) UpdateSearchProvider(c fuego.ContextWithBody[updateSearchProviderRequest]) (searchProviderResponse, error) {
	name := ginCtxBody(c).Param("name")
	if name == "" {
		return searchProviderResponse{}, fuego.BadRequestError{Detail: "name is required"}
	}

	existing, err := h.store.Search.GetSearchProvider(context.Background(), name)
	if err != nil {
		return searchProviderResponse{}, fuego.InternalServerError{Detail: fmt.Sprintf("loading search provider: %v", err)}
	}
	if existing == nil {
		return searchProviderResponse{}, fuego.NotFoundError{Detail: fmt.Sprintf("search provider %q not found", name)}
	}

	body, err := c.Body()
	if err != nil {
		return searchProviderResponse{}, fuego.BadRequestError{Detail: "invalid request body"}
	}

	if body.ProviderType != "" {
		existing.ProviderType = body.ProviderType
	}
	if body.Config != nil {
		existing.Config = body.Config
	}

	if existing.ProviderType == "pgvector" && existing.Config["external_db"] == "true" {
		for _, key := range []string{"host", "database", "username", "password"} {
			if existing.Config[key] == "" {
				return searchProviderResponse{}, fuego.BadRequestError{Detail: fmt.Sprintf("%s is required when external_db is true", key)}
			}
		}
	}

	if err := h.checkSearchProviderDuplicate(existing.ProviderType, existing.Config, name); err != nil {
		return searchProviderResponse{}, fuego.ConflictError{Detail: err.Error()}
	}

	if err := h.store.Search.UpsertSearchProvider(context.Background(), *existing); err != nil {
		return searchProviderResponse{}, fuego.InternalServerError{Detail: fmt.Sprintf("updating search provider: %v", err)}
	}

	// Re-initialize backend with updated config: swap out the old one and create a new one
	if h.backendHolder != nil && h.searchBackendFactory != nil {
		newBackend, err := h.searchBackendFactory(existing.ProviderType, existing.Config)
		if err != nil {
			log.WithError(err).WithField("provider", name).Warn("failed to re-initialize search backend after update")
		} else {
			old := h.backendHolder.Swap(newBackend)
			if old != nil {
				old.Close()
			}
			log.WithField("provider", name).Info("search backend re-initialized after config update")
		}
	}

	log.WithField("name", name).Info("search provider updated via admin API")
	return searchProviderResponse{
		Name:         existing.Name,
		ProviderType: existing.ProviderType,
		Config:       obfuscateSearchConfig(existing.Config),
	}, nil
}

// RemoveSearchProvider removes a search provider.
func (h *AdminRequesthandler) RemoveSearchProvider(c fuego.ContextNoBody) (messageResponse, error) {
	name := ginCtx(c).Param("name")
	if name == "" {
		return messageResponse{}, fuego.BadRequestError{Detail: "name is required"}
	}

	existing, err := h.store.Search.GetSearchProvider(context.Background(), name)
	if err != nil {
		return messageResponse{}, fuego.InternalServerError{Detail: fmt.Sprintf("loading search provider: %v", err)}
	}
	if existing == nil {
		return messageResponse{}, fuego.NotFoundError{Detail: fmt.Sprintf("search provider %q not found", name)}
	}

	inUse, err := h.store.Search.IsSearchProviderInUse(context.Background(), name)
	if err != nil {
		return messageResponse{}, fuego.InternalServerError{Detail: fmt.Sprintf("checking provider usage: %v", err)}
	}
	if inUse {
		return messageResponse{}, fuego.BadRequestError{Detail: fmt.Sprintf("cannot delete search provider %q — it is still assigned to one or more repos", name)}
	}

	if err := h.store.Search.DeleteSearchProvider(context.Background(), name); err != nil {
		return messageResponse{}, fuego.InternalServerError{Detail: fmt.Sprintf("deleting search provider: %v", err)}
	}

	// If no providers remain, tear down the active backend
	if h.backendHolder != nil {
		remaining, _ := h.store.Search.ListSearchProviders(context.Background())
		if len(remaining) == 0 {
			old := h.backendHolder.Swap(nil)
			if old != nil {
				old.Close()
				log.Info("search backend torn down — no providers remain")
			}
		}
	}

	log.WithField("name", name).Info("search provider removed via admin API")
	return messageResponse{Message: fmt.Sprintf("search provider %q removed", name)}, nil
}
