package requesthandler

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/christianfischer/md-wiki-server/internal/draft"
	"github.com/christianfischer/md-wiki-server/internal/importer/confluence"
	"github.com/christianfischer/md-wiki-server/internal/permissions"
	"github.com/christianfischer/md-wiki-server/internal/pg_vector/indexer"
	"github.com/christianfischer/md-wiki-server/internal/repo"
	"github.com/christianfischer/md-wiki-server/internal/repoactions"
	"github.com/christianfischer/md-wiki-server/internal/store/git_repo"
	"github.com/christianfischer/md-wiki-server/internal/store/users"
	"github.com/gin-gonic/gin"
	"github.com/go-fuego/fuego"
	log "github.com/sirupsen/logrus"
)

// errNotFound is returned by checkPermission to signal insufficient permissions.
// Mapped to 404 to avoid revealing the existence of restricted paths.
var errNotFound = errors.New("not found")

type repoSettings struct {
	Slug         string `json:"slug"`
	Name         string `json:"name"`
	PublicAccess bool   `json:"public_access"`
	ReadOnly     bool   `json:"read_only"`
}

type repoListEntry struct {
	Slug              string `json:"slug"`
	Name              string `json:"name"`
	Role              string `json:"role,omitempty"`
	LFSEnabled        bool   `json:"lfs_enabled"`
	SearchProvider    string `json:"search_provider"`
	SearchIndexStatus string `json:"search_index_status"`
	Ready             bool   `json:"ready"`
	Status            string `json:"status"`
}

type fileEntry struct {
	name string
	size int64
}

// orderedMap is a JSON object that preserves key insertion order.
type orderedMap struct {
	keys   []string
	values map[string]any
}

func newOrderedMap() *orderedMap {
	return &orderedMap{values: map[string]any{}}
}

func (o *orderedMap) Set(key string, value any) {
	if _, exists := o.values[key]; !exists {
		o.keys = append(o.keys, key)
	}
	o.values[key] = value
}

func (o *orderedMap) MarshalJSON() ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, key := range o.keys {
		if i > 0 {
			buf.WriteByte(',')
		}
		keyJSON, _ := json.Marshal(key)
		buf.Write(keyJSON)
		buf.WriteByte(':')
		valJSON, err := json.Marshal(o.values[key])
		if err != nil {
			return nil, err
		}
		buf.Write(valJSON)
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

// dirNode is an internal helper for building the tree.
type dirNode struct {
	files    []fileEntry
	children map[string]*dirNode
}

type folderRequest struct {
	Name string `json:"name" validate:"required"`
}

type folderMoveRequest struct {
	Destination string `json:"destination" validate:"required"`
}

type folderResponse struct {
	Name       string `json:"name"`
	Path       string `json:"path"`
	Permission string `json:"permission,omitempty"`
}

type fileMoveRequest struct {
	Destination string `json:"destination"`
}

type fileRenameRequest struct {
	Name string `json:"name" validate:"required"`
}

type fileRequest struct {
	Content string `json:"content"`
}

type fileResponse struct {
	Name            string `json:"name"`
	Path            string `json:"path"`
	IsDir           bool   `json:"is_dir,omitempty"`
	Size            int64  `json:"size,omitempty"`
	Content         string `json:"content,omitempty"`
	LastCommitHash  string `json:"last_commit_hash,omitempty"`
	LastAuthor      string `json:"last_author,omitempty"`
	LastAuthorEmail string `json:"last_author_email,omitempty"`
	LastModified    string `json:"last_modified,omitempty"`
	LastMessage     string `json:"last_message,omitempty"`
	CreatedAt       string `json:"created_at,omitempty"`
	IsDraft         bool   `json:"is_draft,omitempty"`
	DraftCreatedAt  string `json:"draft_created_at,omitempty"`
	DraftUpdatedAt  string `json:"draft_updated_at,omitempty"`
	Permission      string `json:"permission,omitempty"`
}

type fileHistoryEntry struct {
	CommitHash  string `json:"commit_hash"`
	Author      string `json:"author"`
	AuthorEmail string `json:"author_email"`
	Date        string `json:"date"`
	Message     string `json:"message"`
}

type fileHistoryResponse struct {
	Entries []fileHistoryEntry `json:"entries"`
}

type fileDiffResponse struct {
	Diff string `json:"diff"`
}

type fileAtCommitResponse struct {
	CommitHash string `json:"commit_hash"`
	Content    string `json:"content"`
}

type draftRequest struct {
	Content string `json:"content"`
}

type publishResponse struct {
	Success   bool   `json:"success"`
	Merged    string `json:"merged,omitempty"`
	Conflicts string `json:"conflicts,omitempty"`
	Path      string `json:"path"`
}

type RepoRequesthandler struct {
	backends           map[string]repo.RepoBackend
	draftManagers      map[string]*draft.Manager
	indexer            *indexer.Indexer
	docPaths           map[string]string            // repo slug -> doc_path prefix
	repoConfigs        map[string]*git_repo.GitRepo // repo slug -> config
	repoReady          *sync.Map                    // repo slug -> bool (false while cloning)
	permResolvers      map[string]*permissions.Resolver
	isSystemAdmin      func(string) bool
	searchProviderName func(int) string // resolves search provider ID to name
	importPath         string           // default target path for imports
}

func NewRepoRequesthandler(backends map[string]repo.RepoBackend, draftManagers map[string]*draft.Manager, docPaths map[string]string, repoConfigs map[string]*git_repo.GitRepo, repoReady *sync.Map) *RepoRequesthandler {
	return &RepoRequesthandler{
		backends:      backends,
		draftManagers: draftManagers,
		docPaths:      docPaths,
		repoConfigs:   repoConfigs,
		repoReady:     repoReady,
		permResolvers: make(map[string]*permissions.Resolver),
	}
}

// SetSearchProviderNameResolver sets the function used to resolve search provider IDs to names.
func (h *RepoRequesthandler) SetSearchProviderNameResolver(fn func(int) string) {
	h.searchProviderName = fn
}

// SetSystemAdminCheck sets the function used to check if a user is a system admin.
func (h *RepoRequesthandler) SetSystemAdminCheck(fn func(string) bool) {
	h.isSystemAdmin = fn
}

// SetPermissionResolver sets the permission resolver for a repo.
func (h *RepoRequesthandler) SetPermissionResolver(slug string, r *permissions.Resolver) {
	h.permResolvers[slug] = r
}

// GetPermissionResolver returns the permission resolver for a repo, or nil.
func (h *RepoRequesthandler) GetPermissionResolver(slug string) *permissions.Resolver {
	return h.permResolvers[slug]
}

// PermResolvers returns the shared permission resolvers map (for MCP server integration).
func (h *RepoRequesthandler) PermResolvers() map[string]*permissions.Resolver {
	return h.permResolvers
}

// isReadOnly returns true if the repo is configured as read-only.
func (h *RepoRequesthandler) isReadOnly(repoSlug string) bool {
	rc, ok := h.repoConfigs[repoSlug]
	return ok && rc.ReadOnly
}

// effectivePermissionLevel returns the effective permission level string for a user on a path.
func (h *RepoRequesthandler) effectivePermissionLevel(repoSlug, userEmail, path string) string {
	// System admins bypass permission checks (see checkPermission), so report
	// Manager regardless of the permissions file to keep the UI consistent
	// with what is actually enforced.
	isAdmin := userEmail != "" && h.isSystemAdmin != nil && h.isSystemAdmin(userEmail)

	readOnly := h.isReadOnly(repoSlug)

	resolver := h.permResolvers[repoSlug]
	level := permissions.None
	if resolver != nil {
		level = resolver.EffectiveLevel(userEmail, path)
	}
	if isAdmin {
		level = permissions.Manager
	}
	if readOnly && level > permissions.Viewer {
		level = permissions.Viewer
	}
	return level.String()
}

// checkPermission checks if a user has the required permission level on a path.
// Returns nil if allowed, or an appropriate error if denied.
func (h *RepoRequesthandler) checkPermission(repoSlug, userEmail, path string, required permissions.Level) error {
	// Read-only repos block all write operations for everyone
	if h.isReadOnly(repoSlug) && required > permissions.Viewer {
		return errNotFound
	}

	// System admins bypass all checks
	if userEmail != "" && h.isSystemAdmin != nil && h.isSystemAdmin(userEmail) {
		return nil
	}

	// Dotfiles and git internals are only accessible to repo admins
	if isDotPath(path) {
		return errNotFound
	}

	resolver := h.permResolvers[repoSlug]
	if resolver == nil {
		// No permissions file → only unauthenticated read on public repos (handled by RepoAccessCheck)
		if userEmail == "" && required <= permissions.Viewer {
			return nil
		}
		if userEmail == "" {
			return fmt.Errorf("authentication required")
		}
		return errNotFound
	}

	if !resolver.Check(userEmail, path, required) {
		if userEmail == "" {
			return fmt.Errorf("authentication required")
		}
		return errNotFound
	}
	return nil
}

// permError converts a checkPermission error to the appropriate fuego error.
func permError(err error) error {
	if errors.Is(err, errNotFound) {
		return fuego.NotFoundError{Detail: "not found"}
	}
	return fuego.UnauthorizedError{Detail: err.Error()}
}

// isDotPath returns true if the path contains any dotfile or dot-directory component.
func isDotPath(filePath string) bool { return repoactions.IsDotPath(filePath) }

// DocPath returns the configured doc_path for a repo, or "" if not set.
func (h *RepoRequesthandler) DocPath(repoSlug string) string {
	return h.docPaths[repoSlug]
}

// SetImportPath sets the default target path for imports.
func (h *RepoRequesthandler) SetImportPath(p string) {
	h.importPath = p
}

// SetIndexer sets the optional search indexer for triggering index updates on mutations.
func (h *RepoRequesthandler) SetIndexer(idx *indexer.Indexer) {
	h.indexer = idx
}

// shouldIndex returns true if the indexer is set and the repo has a search provider assigned.
func (h *RepoRequesthandler) shouldIndex(repoSlug string) bool {
	if h.indexer == nil {
		return false
	}
	rc, ok := h.repoConfigs[repoSlug]
	return ok && rc.SearchProviderID != nil
}

// --- Helpers ---

func (h *RepoRequesthandler) loadRepoSettings(slug string) (*repoSettings, error) {
	rc, ok := h.repoConfigs[slug]
	if !ok {
		return nil, fmt.Errorf("repository not found")
	}

	name := rc.Name
	if name == "" {
		name = slug
	}
	return &repoSettings{
		Slug:         slug,
		Name:         name,
		PublicAccess: rc.PublicAccess,
		ReadOnly:     rc.ReadOnly,
	}, nil
}

func ginCtx(c fuego.ContextNoBody) *gin.Context {
	return c.Context().(*gin.Context)
}

func ginCtxBody[B any](c fuego.ContextWithBody[B]) *gin.Context {
	return c.Context().(*gin.Context)
}

func getIdentity(gc *gin.Context) (userName string, userIdentity string) {
	identity, _ := gc.Get("user_identity")
	userIdentity, _ = identity.(string)
	name, _ := gc.Get("user_name")
	userName, _ = name.(string)
	return
}

// --- Gin Middleware ---

func (h *RepoRequesthandler) RepoAccessCheck() gin.HandlerFunc {
	return func(c *gin.Context) {
		repoSlug := c.Param("repo")

		settings, err := h.loadRepoSettings(repoSlug)
		if err != nil {
			log.WithError(err).WithField("repo", repoSlug).Warn("repo access check failed")
			c.AbortWithStatusJSON(http.StatusNotFound, gin.H{"error": "repository not found"})
			return
		}

		if settings.PublicAccess {
			c.Next()
			return
		}

		_, exists := c.Get("user_identity")
		if !exists {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "authentication required"})
			return
		}

		c.Next()
	}
}

// RepoWriteCheck requires authentication for write operations.
func (h *RepoRequesthandler) RepoWriteCheck() gin.HandlerFunc {
	return func(c *gin.Context) {
		_, exists := c.Get("user_identity")
		if !exists {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "authentication required"})
			return
		}
		c.Next()
	}
}

// abortPermission sends the appropriate error response for a permission failure.
// Returns 404 instead of 403 to avoid revealing the existence of restricted paths.
func abortPermission(gc *gin.Context, err error) {
	if errors.Is(err, errNotFound) {
		gc.JSON(http.StatusNotFound, gin.H{"error": "not found"})
	} else {
		gc.JSON(http.StatusUnauthorized, gin.H{"error": err.Error()})
	}
}

// --- Fuego Handlers ---

func (h *RepoRequesthandler) GetRepos(c fuego.ContextNoBody) ([]repoListEntry, error) {
	gc := ginCtx(c)
	email, authenticated := gc.Get("user_identity")
	userEmail, _ := email.(string)

	var repos []repoListEntry
	for slug := range h.backends {
		settings, err := h.loadRepoSettings(slug)
		if err != nil {
			log.WithError(err).WithField("repo", slug).Warn("failed to load repo settings")
			continue
		}

		if !authenticated && !settings.PublicAccess {
			continue
		}

		// Hide non-public repos from users who have no permissions.
		if authenticated && !settings.PublicAccess {
			isAdmin := h.isSystemAdmin != nil && h.isSystemAdmin(userEmail)
			if !isAdmin {
				resolver := h.permResolvers[slug]
				if resolver == nil {
					// No permissions file — skip repo for non-admins
					continue
				}
				if !resolver.HasAnyAccess(userEmail, permissions.Viewer) {
					continue
				}
			}
		}

		ready := true
		if v, ok := h.repoReady.Load(slug); ok {
			ready, _ = v.(bool)
		}

		var status string
		if !ready {
			status = "cloning"
		} else if backend, ok := h.backends[slug]; ok {
			st, err := backend.Status()
			if err != nil {
				log.WithError(err).WithField("repo", slug).Warn("failed to get git status")
				status = "error"
			} else if len(st) == 0 {
				status = "clean"
			} else {
				status = fmt.Sprintf("%d changed", len(st))
			}
		}

		rc := h.repoConfigs[slug]
		var searchProviderStr string
		if rc != nil && rc.SearchProviderID != nil && h.searchProviderName != nil {
			searchProviderStr = h.searchProviderName(*rc.SearchProviderID)
		}
		searchStatus := "disabled"
		if rc != nil && rc.SearchProviderID != nil && h.indexer != nil {
			searchStatus = h.indexer.Status(slug)
		}
		entry := repoListEntry{
			Slug:              settings.Slug,
			Name:              settings.Name,
			LFSEnabled:        rc != nil && rc.LFSEnabled,
			SearchProvider:    searchProviderStr,
			SearchIndexStatus: searchStatus,
			Ready:             ready,
			Status:            status,
		}

		// Determine role from file permissions
		if userEmail != "" {
			isAdmin := h.isSystemAdmin != nil && h.isSystemAdmin(userEmail)
			if isAdmin {
				entry.Role = string(users.RoleRepoAdmin)
			} else {
				entry.Role = "reader"
			}
		} else {
			entry.Role = "reader"
		}

		repos = append(repos, entry)
	}

	return repos, nil
}

func (h *RepoRequesthandler) ListFiles(c fuego.ContextNoBody) (any, error) {
	repoName := c.PathParam("repo")
	gc := ginCtx(c)

	backend, ok := h.backends[repoName]
	if !ok {
		return nil, fuego.NotFoundError{Detail: "repository not found"}
	}

	root := h.docPaths[repoName]
	entries, err := backend.ListEntries(root)
	if err != nil {
		log.WithError(err).WithField("repo", repoName).Warn("failed to list files")
		return nil, fuego.HTTPError{Detail: "failed to list files"}
	}

	_, userEmail := getIdentity(gc)
	isAdmin := userEmail != "" && h.isSystemAdmin != nil && h.isSystemAdmin(userEmail)

	if !isAdmin {
		// Filter dotfiles and git internal files for non-admin users
		var visible []repo.FileEntry
		for _, e := range entries {
			if strings.HasPrefix(e.Name, ".") {
				continue
			}
			// Also filter entries inside dotfile directories
			if e.Path != "" {
				skip := false
				for _, part := range strings.Split(e.Path, string(filepath.Separator)) {
					if strings.HasPrefix(part, ".") {
						skip = true
						break
					}
				}
				if skip {
					continue
				}
			}
			visible = append(visible, e)
		}
		entries = visible

		// Filter entries by read permission.
		// Folders are shown if the user can read them directly OR if they contain accessible descendants.
		resolver := h.permResolvers[repoName]
		if resolver != nil {
			var filtered []repo.FileEntry
			for _, e := range entries {
				var entryPath string
				if e.IsDir {
					entryPath = filepath.Join(e.Path, e.Name) + "/"
				} else {
					entryPath = filepath.Join(e.Path, e.Name)
				}
				if resolver.Check(userEmail, entryPath, permissions.Viewer) {
					filtered = append(filtered, e)
				} else if e.IsDir && resolver.HasAccessibleDescendant(userEmail, strings.TrimSuffix(entryPath, "/"), permissions.Viewer) {
					filtered = append(filtered, e)
				}
			}
			entries = filtered
		}
	}

	return buildTree(entries, backend, root), nil
}

func (h *RepoRequesthandler) GetFile() gin.HandlerFunc {
	return func(c *gin.Context) {
		repoName := c.Param("repo")
		filePath := strings.TrimPrefix(c.Param("path"), "/")

		backend, ok := h.backends[repoName]
		if !ok {
			c.JSON(http.StatusNotFound, gin.H{"error": "repository not found"})
			return
		}

		_, userEmail := getIdentity(c)
		if err := h.checkPermission(repoName, userEmail, filePath, permissions.Viewer); err != nil {
			abortPermission(c, err)
			return
		}

		subfolder := filepath.Dir(filePath)
		if subfolder == "." {
			subfolder = ""
		}
		filename := filepath.Base(filePath)
		if userEmail != "" {
			if dm, ok := h.draftManagers[repoName]; ok && dm.HasDraft(subfolder, filename, userEmail) {
				draftContent, _, err := dm.GetDraft(subfolder, filename, userEmail)
				if err == nil {
					c.Header("X-Is-Draft", "true")
					contentType := mime.TypeByExtension(filepath.Ext(filename))
					if contentType == "" {
						contentType = http.DetectContentType(draftContent)
					}
					c.Data(http.StatusOK, contentType, draftContent)
					return
				}
			}
		}

		content, err := backend.GetFile(subfolder, filename)
		if err != nil {
			log.WithError(err).WithField("repo", repoName).WithField("path", filePath).Warn("failed to get file")
			c.JSON(http.StatusNotFound, gin.H{"error": "file not found"})
			return
		}

		contentType := mime.TypeByExtension(filepath.Ext(filename))
		if contentType == "" {
			contentType = http.DetectContentType(content)
		}
		c.Data(http.StatusOK, contentType, content)
	}
}

func (h *RepoRequesthandler) GetFileInfo(c fuego.ContextNoBody) (fileResponse, error) {
	repoName := c.PathParam("repo")
	filePath := strings.TrimPrefix(c.PathParam("path"), "/")
	gc := ginCtx(c)

	backend, ok := h.backends[repoName]
	if !ok {
		return fileResponse{}, fuego.NotFoundError{Detail: "repository not found"}
	}

	_, userEmail := getIdentity(gc)
	if err := h.checkPermission(repoName, userEmail, filePath, permissions.Viewer); err != nil {
		return fileResponse{}, permError(err)
	}

	// Check if path is a directory
	cleanPath := strings.TrimSuffix(filePath, "/")
	info, err := backend.Stat(cleanPath)
	if err != nil {
		log.WithError(err).WithField("repo", repoName).WithField("path", filePath).Warn("failed to get file info")
		return fileResponse{}, fuego.NotFoundError{Detail: "not found"}
	}

	name := filepath.Base(cleanPath)
	permPath := filePath
	if info.IsDir {
		permPath = cleanPath + "/"
	}

	resp := fileResponse{
		Name:       name,
		Path:       filePath,
		IsDir:      info.IsDir,
		Permission: h.effectivePermissionLevel(repoName, userEmail, permPath),
	}

	if !info.IsDir {
		resp.Size = info.Size
	}

	// Populate git history info
	subfolder := filepath.Dir(cleanPath)
	if subfolder == "." {
		subfolder = ""
	}
	filename := filepath.Base(cleanPath)

	history, err := backend.GetFileHistory(subfolder, filename)
	if err == nil && len(history) > 0 {
		latest := history[0]
		resp.LastCommitHash = latest.CommitHash
		resp.LastAuthor = latest.Author
		resp.LastAuthorEmail = latest.AuthorEmail
		resp.LastModified = latest.Date.Format("2006-01-02T15:04:05Z07:00")
		resp.LastMessage = latest.Message

		oldest := history[len(history)-1]
		resp.CreatedAt = oldest.Date.Format("2006-01-02T15:04:05Z07:00")
	}

	// Check if authenticated user has a draft (files only)
	if !info.IsDir && userEmail != "" {
		if dm, ok := h.draftManagers[repoName]; ok {
			if meta, err := dm.GetDraftMeta(subfolder, filename, userEmail); err == nil {
				resp.IsDraft = true
				resp.DraftCreatedAt = meta.CreatedAt.Format("2006-01-02T15:04:05Z07:00")
				resp.DraftUpdatedAt = meta.UpdatedAt.Format("2006-01-02T15:04:05Z07:00")
			}
		}
	}

	return resp, nil
}

func (h *RepoRequesthandler) GetFileHistory(c fuego.ContextNoBody) (fileHistoryResponse, error) {
	repoName := c.PathParam("repo")
	filePath := strings.TrimPrefix(c.PathParam("path"), "/")
	gc := ginCtx(c)

	backend, ok := h.backends[repoName]
	if !ok {
		return fileHistoryResponse{}, fuego.NotFoundError{Detail: "repository not found"}
	}
	if !backend.SupportsHistory() {
		return fileHistoryResponse{}, fuego.BadRequestError{Detail: "history not supported for this repository backend"}
	}

	_, userEmail := getIdentity(gc)
	if err := h.checkPermission(repoName, userEmail, filePath, permissions.Viewer); err != nil {
		return fileHistoryResponse{}, permError(err)
	}

	subfolder := filepath.Dir(filePath)
	if subfolder == "." {
		subfolder = ""
	}
	filename := filepath.Base(filePath)

	history, err := backend.GetFileHistory(subfolder, filename)
	if err != nil {
		log.WithError(err).WithField("repo", repoName).WithField("path", filePath).Warn("failed to get file history")
		return fileHistoryResponse{}, fuego.NotFoundError{Detail: "file not found"}
	}

	entries := make([]fileHistoryEntry, len(history))
	for i, h := range history {
		entries[i] = fileHistoryEntry{
			CommitHash:  h.CommitHash,
			Author:      h.Author,
			AuthorEmail: h.AuthorEmail,
			Date:        h.Date.Format("2006-01-02T15:04:05Z07:00"),
			Message:     h.Message,
		}
	}

	return fileHistoryResponse{Entries: entries}, nil
}

func (h *RepoRequesthandler) GetFileDiff(c fuego.ContextNoBody) (fileDiffResponse, error) {
	repoName := c.PathParam("repo")
	filePath := strings.TrimPrefix(c.PathParam("path"), "/")
	commitHash := c.QueryParam("commit")

	if commitHash == "" {
		return fileDiffResponse{}, fuego.BadRequestError{Detail: "commit query parameter is required"}
	}

	gc := ginCtx(c)
	backend, ok := h.backends[repoName]
	if !ok {
		return fileDiffResponse{}, fuego.NotFoundError{Detail: "repository not found"}
	}
	if !backend.SupportsHistory() {
		return fileDiffResponse{}, fuego.BadRequestError{Detail: "history not supported for this repository backend"}
	}

	_, userEmail := getIdentity(gc)
	if err := h.checkPermission(repoName, userEmail, filePath, permissions.Viewer); err != nil {
		return fileDiffResponse{}, permError(err)
	}

	subfolder := filepath.Dir(filePath)
	if subfolder == "." {
		subfolder = ""
	}
	filename := filepath.Base(filePath)

	diff, err := backend.GetFileDiffWithCommit(commitHash, subfolder, filename)
	if err != nil {
		log.WithError(err).WithField("repo", repoName).WithField("path", filePath).WithField("commit", commitHash).Warn("failed to get file diff")
		return fileDiffResponse{}, fuego.NotFoundError{Detail: "file or commit not found"}
	}

	return fileDiffResponse{Diff: diff}, nil
}

func (h *RepoRequesthandler) GetFileAtCommit(c fuego.ContextNoBody) (fileAtCommitResponse, error) {
	repoName := c.PathParam("repo")
	filePath := strings.TrimPrefix(c.PathParam("path"), "/")
	commitHash := c.QueryParam("commit")

	if commitHash == "" {
		return fileAtCommitResponse{}, fuego.BadRequestError{Detail: "commit query parameter is required"}
	}

	gc := ginCtx(c)
	backend, ok := h.backends[repoName]
	if !ok {
		return fileAtCommitResponse{}, fuego.NotFoundError{Detail: "repository not found"}
	}
	if !backend.SupportsHistory() {
		return fileAtCommitResponse{}, fuego.BadRequestError{Detail: "history not supported for this repository backend"}
	}

	_, userEmail := getIdentity(gc)
	if err := h.checkPermission(repoName, userEmail, filePath, permissions.Viewer); err != nil {
		return fileAtCommitResponse{}, permError(err)
	}

	subfolder := filepath.Dir(filePath)
	if subfolder == "." {
		subfolder = ""
	}
	filename := filepath.Base(filePath)

	content, err := backend.GetFileAtCommit(commitHash, subfolder, filename)
	if err != nil {
		log.WithError(err).WithField("repo", repoName).WithField("path", filePath).WithField("commit", commitHash).Warn("failed to get file at commit")
		return fileAtCommitResponse{}, fuego.NotFoundError{Detail: "file or commit not found"}
	}

	return fileAtCommitResponse{CommitHash: commitHash, Content: content}, nil
}

func (h *RepoRequesthandler) CreateFile(c fuego.ContextWithBody[fileRequest]) (fileResponse, error) {
	repoName := c.PathParam("repo")
	filePath := strings.TrimPrefix(c.PathParam("path"), "/")
	gc := ginCtxBody(c)

	body, err := c.Body()
	if err != nil {
		return fileResponse{}, err
	}

	backend, ok := h.backends[repoName]
	if !ok {
		return fileResponse{}, fuego.NotFoundError{Detail: "repository not found"}
	}

	subfolder := filepath.Dir(filePath)
	if subfolder == "." {
		subfolder = ""
	}
	filename := filepath.Base(filePath)
	userName, userEmail := getIdentity(gc)

	// Check Update permission on parent folder
	parentFolder := subfolder
	if parentFolder == "" {
		parentFolder = "."
	}
	if err := h.checkPermission(repoName, userEmail, parentFolder+"/", permissions.Contributor); err != nil {
		return fileResponse{}, permError(err)
	}

	if err := backend.AddFile(subfolder, filename, []byte(body.Content)); err != nil {
		log.WithError(err).WithField("repo", repoName).WithField("path", filePath).Warn("failed to create file")
		return fileResponse{}, fuego.HTTPError{Detail: "failed to create file"}
	}

	backend.SaveChangesAsync("create "+filePath, userName, userEmail)
	if h.shouldIndex(repoName) {
		h.indexer.IndexFile(repoName, filePath)
	}
	return fileResponse{
		Name:    filename,
		Path:    filePath,
		Content: body.Content,
	}, nil
}

func (h *RepoRequesthandler) UpdateFile(c fuego.ContextWithBody[fileRequest]) (fileResponse, error) {
	repoName := c.PathParam("repo")
	filePath := strings.TrimPrefix(c.PathParam("path"), "/")
	gc := ginCtxBody(c)

	body, err := c.Body()
	if err != nil {
		return fileResponse{}, err
	}

	backend, ok := h.backends[repoName]
	if !ok {
		return fileResponse{}, fuego.NotFoundError{Detail: "repository not found"}
	}

	subfolder := filepath.Dir(filePath)
	if subfolder == "." {
		subfolder = ""
	}
	filename := filepath.Base(filePath)
	userName, userEmail := getIdentity(gc)

	if err := h.checkPermission(repoName, userEmail, filePath, permissions.Contributor); err != nil {
		return fileResponse{}, permError(err)
	}

	if err := backend.AddFile(subfolder, filename, []byte(body.Content)); err != nil {
		log.WithError(err).WithField("repo", repoName).WithField("path", filePath).Warn("failed to update file")
		return fileResponse{}, fuego.HTTPError{Detail: "failed to update file"}
	}

	backend.SaveChangesAsync("update "+filePath, userName, userEmail)
	if h.shouldIndex(repoName) {
		h.indexer.IndexFile(repoName, filePath)
	}
	return fileResponse{
		Name:    filename,
		Path:    filePath,
		Content: body.Content,
	}, nil
}

func (h *RepoRequesthandler) DeleteFile(c fuego.ContextNoBody) (any, error) {
	repoName := c.PathParam("repo")
	filePath := strings.TrimPrefix(c.PathParam("path"), "/")
	gc := ginCtx(c)

	backend, ok := h.backends[repoName]
	if !ok {
		return nil, fuego.NotFoundError{Detail: "repository not found"}
	}

	subfolder := filepath.Dir(filePath)
	if subfolder == "." {
		subfolder = ""
	}
	filename := filepath.Base(filePath)
	userName, userEmail := getIdentity(gc)

	if err := h.checkPermission(repoName, userEmail, filePath, permissions.ContentManager); err != nil {
		return nil, permError(err)
	}

	if err := backend.DeleteFile(subfolder, filename); err != nil {
		log.WithError(err).WithField("repo", repoName).WithField("path", filePath).Warn("failed to delete file")
		return nil, fuego.HTTPError{Detail: "failed to delete file"}
	}

	// Remove permission entry
	if resolver := h.permResolvers[repoName]; resolver != nil {
		resolver.RemoveEntry(filePath)
		h.savePermissions(repoName)
	}

	backend.SaveChangesAsync("delete "+filePath, userName, userEmail)
	if h.shouldIndex(repoName) {
		h.indexer.DeleteFileIndex(repoName, filePath)
	}
	return nil, nil
}

func (h *RepoRequesthandler) MoveFile(c fuego.ContextWithBody[fileMoveRequest]) (fileResponse, error) {
	repoName := c.PathParam("repo")
	filePath := strings.TrimPrefix(c.PathParam("path"), "/")
	gc := ginCtxBody(c)

	body, err := c.Body()
	if err != nil {
		return fileResponse{}, err
	}

	backend, ok := h.backends[repoName]
	if !ok {
		return fileResponse{}, fuego.NotFoundError{Detail: "repository not found"}
	}

	oldSubfolder := filepath.Dir(filePath)
	if oldSubfolder == "." {
		oldSubfolder = ""
	}
	filename := filepath.Base(filePath)
	dest := strings.TrimPrefix(body.Destination, "/")
	newFilePath := filepath.Join(dest, filename)
	userName, userEmail := getIdentity(gc)

	// Check delete on source, update on destination folder
	if err := h.checkPermission(repoName, userEmail, filePath, permissions.ContentManager); err != nil {
		return fileResponse{}, permError(err)
	}
	destFolder := dest
	if destFolder == "" {
		destFolder = "."
	}
	if err := h.checkPermission(repoName, userEmail, destFolder+"/", permissions.Contributor); err != nil {
		return fileResponse{}, permError(err)
	}

	if err := backend.MoveFile(oldSubfolder, filename, dest, filename); err != nil {
		log.WithError(err).WithField("repo", repoName).WithField("path", filePath).Warn("failed to move file")
		return fileResponse{}, fuego.HTTPError{Detail: "failed to move file"}
	}

	// Update permission entries
	if resolver := h.permResolvers[repoName]; resolver != nil {
		resolver.RenamePath(filePath, newFilePath)
		h.savePermissions(repoName)
	}

	backend.SaveChangesAsync("move "+filePath+" to "+newFilePath, userName, userEmail)
	if h.shouldIndex(repoName) {
		h.indexer.DeleteFileIndex(repoName, filePath)
		h.indexer.IndexFile(repoName, newFilePath)
	}
	return fileResponse{
		Name: filename,
		Path: newFilePath,
	}, nil
}

func (h *RepoRequesthandler) RenameFile(c fuego.ContextWithBody[fileRenameRequest]) (fileResponse, error) {
	repoName := c.PathParam("repo")
	filePath := strings.TrimPrefix(c.PathParam("path"), "/")
	gc := ginCtxBody(c)

	body, err := c.Body()
	if err != nil {
		return fileResponse{}, err
	}

	if body.Name == "" {
		return fileResponse{}, fuego.BadRequestError{Detail: "name is required"}
	}

	backend, ok := h.backends[repoName]
	if !ok {
		return fileResponse{}, fuego.NotFoundError{Detail: "repository not found"}
	}

	subfolder := filepath.Dir(filePath)
	if subfolder == "." {
		subfolder = ""
	}
	oldName := filepath.Base(filePath)
	newFilePath := filepath.Join(subfolder, body.Name)
	userName, userEmail := getIdentity(gc)

	if err := h.checkPermission(repoName, userEmail, filePath, permissions.Contributor); err != nil {
		return fileResponse{}, permError(err)
	}

	if err := backend.MoveFile(subfolder, oldName, subfolder, body.Name); err != nil {
		log.WithError(err).WithField("repo", repoName).WithField("path", filePath).Warn("failed to rename file")
		return fileResponse{}, fuego.HTTPError{Detail: "failed to rename file"}
	}

	// Update permission entries
	if resolver := h.permResolvers[repoName]; resolver != nil {
		resolver.RenamePath(filePath, newFilePath)
		h.savePermissions(repoName)
	}

	backend.SaveChangesAsync("rename "+filePath+" to "+newFilePath, userName, userEmail)
	if h.shouldIndex(repoName) {
		h.indexer.DeleteFileIndex(repoName, filePath)
		h.indexer.IndexFile(repoName, newFilePath)
	}
	return fileResponse{
		Name: body.Name,
		Path: newFilePath,
	}, nil
}

func (h *RepoRequesthandler) UploadFile() gin.HandlerFunc {
	return func(c *gin.Context) {
		repoName := c.Param("repo")
		rawPath := strings.TrimPrefix(c.Param("path"), "/")
		folderPath := filepath.Dir(rawPath)
		if folderPath == "." {
			folderPath = ""
		}

		backend, ok := h.backends[repoName]
		if !ok {
			c.JSON(http.StatusNotFound, gin.H{"error": "repository not found"})
			return
		}

		_, userEmail := getIdentity(c)
		parentFolder := folderPath
		if parentFolder == "" {
			parentFolder = "."
		}
		if err := h.checkPermission(repoName, userEmail, parentFolder+"/", permissions.Contributor); err != nil {
			abortPermission(c, err)
			return
		}

		file, header, err := c.Request.FormFile("file")
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "file is required"})
			return
		}
		defer file.Close()

		data, err := io.ReadAll(file)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("failed to read file: %v", err)})
			return
		}

		filename := header.Filename
		userName, userEmail := getIdentity(c)

		if err := backend.AddFile(folderPath, filename, data); err != nil {
			log.WithError(err).WithField("repo", repoName).WithField("path", folderPath).WithField("filename", filename).Warn("failed to upload file")
			c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("failed to upload file: %v", err)})
			return
		}

		docPath := filepath.Join(folderPath, filename)
		backend.SaveChangesAsync("upload "+docPath, userName, userEmail)
		if h.shouldIndex(repoName) {
			h.indexer.IndexFile(repoName, docPath)
		}

		scheme := "http"
		if c.Request.TLS != nil || c.GetHeader("X-Forwarded-Proto") == "https" {
			scheme = "https"
		}
		url := fmt.Sprintf("%s://%s/repos/%s/file/%s", scheme, c.Request.Host, repoName, docPath)

		c.JSON(http.StatusCreated, gin.H{
			"name": filename,
			"path": docPath,
			"url":  url,
		})
	}
}

func (h *RepoRequesthandler) ImportConfluence() gin.HandlerFunc {
	return func(c *gin.Context) {
		repoName := c.Param("repo")

		backend, ok := h.backends[repoName]
		if !ok {
			c.JSON(http.StatusNotFound, gin.H{"error": "repository not found"})
			return
		}

		userName, userEmail := getIdentity(c)

		// Determine target path inside the repo: form field > repo doc_path > repo root.
		// h.importPath is the filesystem directory for uploaded zips, not a repo path.
		targetPath := strings.TrimSpace(c.PostForm("target_path"))
		if targetPath == "" {
			targetPath = h.docPaths[repoName]
		}

		// Check contributor permission on the target folder.
		permPath := targetPath
		if permPath == "" {
			permPath = "."
		}
		if err := h.checkPermission(repoName, userEmail, permPath+"/", permissions.Contributor); err != nil {
			abortPermission(c, err)
			return
		}

		file, _, err := c.Request.FormFile("file")
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "file is required"})
			return
		}
		defer file.Close()

		const maxConfluenceZipSize = 500 << 20 // 500 MB
		data, err := io.ReadAll(io.LimitReader(file, maxConfluenceZipSize+1))
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("failed to read file: %v", err)})
			return
		}
		if len(data) > maxConfluenceZipSize {
			c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "zip file exceeds 500 MB limit"})
			return
		}

		zipReader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("invalid zip file: %v", err)})
			return
		}

		// Extract zip to the import path so the raw structure can be inspected.
		extractDir := filepath.Join(h.importPath, ".confluence-import")
		cleanExtractDir := filepath.Clean(extractDir) + string(os.PathSeparator)
		if err := os.MkdirAll(extractDir, 0755); err != nil {
			log.WithError(err).Warn("failed to create confluence extract dir")
		} else {
			for _, f := range zipReader.File {
				resolved := filepath.Clean(filepath.Join(extractDir, f.Name))
				if !strings.HasPrefix(resolved, cleanExtractDir) && resolved != filepath.Clean(extractDir) {
					log.WithField("entry", f.Name).Warn("zip slip attempt blocked")
					continue
				}
				if f.FileInfo().IsDir() {
					os.MkdirAll(resolved, 0755)
					continue
				}
				os.MkdirAll(filepath.Dir(resolved), 0755)
				rc, err := f.Open()
				if err != nil {
					continue
				}
				const maxEntrySize = 100 << 20 // 100 MB per entry
				raw, _ := io.ReadAll(io.LimitReader(rc, maxEntrySize))
				rc.Close()
				os.WriteFile(resolved, raw, 0644)
			}
			log.WithField("path", extractDir).Info("extracted confluence zip for inspection")
		}

		result, err := confluence.Import(zipReader, targetPath)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("import failed: %v", err)})
			return
		}

		// Write all pages to the backend.
		var pagePaths []string
		for _, page := range result.Pages {
			dir := filepath.Dir(page.Path)
			name := filepath.Base(page.Path)
			if dir == "." {
				dir = ""
			}
			if err := h.checkPermission(repoName, userEmail, page.Path, permissions.Contributor); err != nil {
				result.Errors = append(result.Errors, confluence.ImportError{File: page.Path, Message: "permission denied"})
				continue
			}
			log.WithField("repo", repoName).WithField("dir", dir).WithField("name", name).Info("writing imported page")
			if err := backend.AddFile(dir, name, page.Content); err != nil {
				log.WithError(err).WithField("repo", repoName).WithField("path", page.Path).Warn("failed to write imported page")
				result.Errors = append(result.Errors, confluence.ImportError{File: page.Path, Message: err.Error()})
			}
			pagePaths = append(pagePaths, page.Path)
		}

		// Write all attachments to the backend.
		var attachPaths []string
		for _, att := range result.Attachments {
			dir := filepath.Dir(att.Path)
			name := filepath.Base(att.Path)
			if dir == "." {
				dir = ""
			}
			if err := h.checkPermission(repoName, userEmail, att.Path, permissions.Contributor); err != nil {
				result.Errors = append(result.Errors, confluence.ImportError{File: att.Path, Message: "permission denied"})
				continue
			}
			log.WithField("repo", repoName).WithField("dir", dir).WithField("name", name).Info("writing imported attachment")
			if err := backend.AddFile(dir, name, att.Content); err != nil {
				log.WithError(err).WithField("repo", repoName).WithField("path", att.Path).Warn("failed to write imported attachment")
				result.Errors = append(result.Errors, confluence.ImportError{File: att.Path, Message: err.Error()})
			}
			attachPaths = append(attachPaths, att.Path)
		}

		// Write .order files to preserve hierarchy ordering from index.html.
		for _, of := range result.OrderFiles {
			if err := backend.WriteOrder(of.Dir, of.Entries); err != nil {
				log.WithError(err).WithField("repo", repoName).WithField("dir", of.Dir).Warn("failed to write .order file")
			}
		}

		// Single synchronous commit.
		commitMsg := fmt.Sprintf("import Confluence space (%d pages, %d attachments)", len(result.Pages), len(result.Attachments))
		if err := backend.SaveChanges(commitMsg, userName, userEmail); err != nil {
			log.WithError(err).WithField("repo", repoName).Warn("failed to commit Confluence import")
			c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("failed to commit: %v", err)})
			return
		}

		// Index imported markdown files.
		if h.shouldIndex(repoName) {
			for _, page := range result.Pages {
				h.indexer.IndexFile(repoName, page.Path)
			}
		}

		// Build error strings for response.
		var importErrors []gin.H
		for _, e := range result.Errors {
			importErrors = append(importErrors, gin.H{"file": e.File, "message": e.Message})
		}

		c.JSON(http.StatusOK, gin.H{
			"pages_imported":       len(result.Pages),
			"attachments_imported": len(result.Attachments),
			"page_paths":           pagePaths,
			"attachment_paths":     attachPaths,
			"errors":               importErrors,
			"extract_dir":          extractDir,
		})
	}
}

func (h *RepoRequesthandler) BulkDelete() gin.HandlerFunc {
	return func(c *gin.Context) {
		repoName := c.Param("repo")

		backend, ok := h.backends[repoName]
		if !ok {
			c.JSON(http.StatusNotFound, gin.H{"error": "repository not found"})
			return
		}

		var body struct {
			Paths []string `json:"paths"`
		}
		if err := c.ShouldBindJSON(&body); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
			return
		}
		if len(body.Paths) == 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "paths list must not be empty"})
			return
		}

		userName, userEmail := getIdentity(c)

		var (
			deletedFiles   int
			deletedFolders int
			errList        []gin.H
			permChanged    bool
		)

		resolver := h.permResolvers[repoName]

		for _, p := range body.Paths {
			p = strings.TrimPrefix(p, "/")
			if p == "" {
				errList = append(errList, gin.H{"path": p, "message": "empty path"})
				continue
			}

			info, err := backend.Stat(p)
			if err != nil {
				errList = append(errList, gin.H{"path": p, "message": "not found"})
				continue
			}

			if info.IsDir {
				if err := h.checkPermission(repoName, userEmail, p+"/", permissions.ContentManager); err != nil {
					errList = append(errList, gin.H{"path": p, "message": "permission denied"})
					continue
				}
				if err := backend.DeleteFolder(p); err != nil {
					errList = append(errList, gin.H{"path": p, "message": err.Error()})
					continue
				}
				if resolver != nil {
					resolver.RemoveEntriesUnder(p)
					permChanged = true
				}
				if h.shouldIndex(repoName) {
					h.indexer.DeleteFolderIndex(repoName, p)
				}
				deletedFolders++
			} else {
				if err := h.checkPermission(repoName, userEmail, p, permissions.ContentManager); err != nil {
					errList = append(errList, gin.H{"path": p, "message": "permission denied"})
					continue
				}
				subfolder := filepath.Dir(p)
				if subfolder == "." {
					subfolder = ""
				}
				filename := filepath.Base(p)
				if err := backend.DeleteFile(subfolder, filename); err != nil {
					errList = append(errList, gin.H{"path": p, "message": err.Error()})
					continue
				}
				if resolver != nil {
					resolver.RemoveEntry(p)
					permChanged = true
				}
				if h.shouldIndex(repoName) {
					h.indexer.DeleteFileIndex(repoName, p)
				}
				deletedFiles++
			}
		}

		if permChanged {
			h.savePermissions(repoName)
		}

		if deletedFiles+deletedFolders > 0 {
			commitMsg := fmt.Sprintf("bulk delete (%d files, %d folders)", deletedFiles, deletedFolders)
			if err := backend.SaveChanges(commitMsg, userName, userEmail); err != nil {
				log.WithError(err).WithField("repo", repoName).Warn("failed to commit bulk delete")
				c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("failed to commit: %v", err)})
				return
			}
		}

		c.JSON(http.StatusOK, gin.H{
			"deleted_files":   deletedFiles,
			"deleted_folders": deletedFolders,
			"errors":          errList,
		})
	}
}

func (h *RepoRequesthandler) CreateFolder(c fuego.ContextWithBody[folderRequest]) (folderResponse, error) {
	repoName := c.PathParam("repo")
	folderPath := strings.TrimPrefix(c.PathParam("path"), "/")
	gc := ginCtxBody(c)

	body, err := c.Body()
	if err != nil {
		return folderResponse{}, err
	}

	backend, ok := h.backends[repoName]
	if !ok {
		return folderResponse{}, fuego.NotFoundError{Detail: "repository not found"}
	}

	subfolder := filepath.Join(folderPath, body.Name)
	userName, userEmail := getIdentity(gc)

	// Check Update permission on parent folder
	parentFolder := folderPath
	if parentFolder == "" {
		parentFolder = "."
	}
	if err := h.checkPermission(repoName, userEmail, parentFolder+"/", permissions.Contributor); err != nil {
		return folderResponse{}, permError(err)
	}

	if err := backend.AddFile(subfolder, ".gitkeep", []byte{}); err != nil {
		log.WithError(err).WithField("repo", repoName).WithField("folder", folderPath).Warn("failed to create folder")
		return folderResponse{}, fuego.HTTPError{Detail: "failed to create folder"}
	}

	backend.SaveChangesAsync("create folder "+subfolder, userName, userEmail)
	return folderResponse{Name: body.Name, Path: subfolder, Permission: h.effectivePermissionLevel(repoName, userEmail, subfolder+"/")}, nil
}

func (h *RepoRequesthandler) UpdateFolder(c fuego.ContextWithBody[folderRequest]) (folderResponse, error) {
	repoName := c.PathParam("repo")
	folderPath := strings.TrimPrefix(c.PathParam("path"), "/")
	gc := ginCtxBody(c)

	body, err := c.Body()
	if err != nil {
		return folderResponse{}, err
	}

	backend, ok := h.backends[repoName]
	if !ok {
		return folderResponse{}, fuego.NotFoundError{Detail: "repository not found"}
	}

	parentDir := filepath.Dir(folderPath)
	if parentDir == "." {
		parentDir = ""
	}
	newRelPath := filepath.Join(parentDir, body.Name)
	userName, userEmail := getIdentity(gc)

	if err := h.checkPermission(repoName, userEmail, folderPath+"/", permissions.Contributor); err != nil {
		return folderResponse{}, permError(err)
	}

	if err := backend.RenameFolder(folderPath, newRelPath); err != nil {
		log.WithError(err).WithField("repo", repoName).WithField("folder", folderPath).Warn("failed to rename folder")
		return folderResponse{}, fuego.HTTPError{Detail: "failed to rename folder"}
	}

	// Update permission entries
	if resolver := h.permResolvers[repoName]; resolver != nil {
		resolver.RenamePath(folderPath+"/", newRelPath+"/")
		h.savePermissions(repoName)
	}

	backend.SaveChangesAsync("rename folder "+folderPath+" to "+newRelPath, userName, userEmail)
	if h.shouldIndex(repoName) {
		h.indexer.DeleteFolderIndex(repoName, folderPath)
		h.indexer.ReindexFolder(repoName, newRelPath)
	}
	return folderResponse{Name: body.Name, Path: newRelPath, Permission: h.effectivePermissionLevel(repoName, userEmail, newRelPath+"/")}, nil
}

func (h *RepoRequesthandler) DeleteFolder(c fuego.ContextNoBody) (any, error) {
	repoName := c.PathParam("repo")
	folderPath := strings.TrimPrefix(c.PathParam("path"), "/")
	gc := ginCtx(c)

	backend, ok := h.backends[repoName]
	if !ok {
		return nil, fuego.NotFoundError{Detail: "repository not found"}
	}

	userName, userEmail := getIdentity(gc)

	if err := h.checkPermission(repoName, userEmail, folderPath+"/", permissions.ContentManager); err != nil {
		return nil, permError(err)
	}

	if err := backend.DeleteFolder(folderPath); err != nil {
		log.WithError(err).WithField("repo", repoName).WithField("folder", folderPath).Warn("failed to delete folder")
		return nil, fuego.HTTPError{Detail: "failed to delete folder"}
	}

	// Remove permission entries for folder and children
	if resolver := h.permResolvers[repoName]; resolver != nil {
		resolver.RemoveEntriesUnder(folderPath)
		h.savePermissions(repoName)
	}

	backend.SaveChangesAsync("delete folder "+folderPath, userName, userEmail)
	if h.shouldIndex(repoName) {
		h.indexer.DeleteFolderIndex(repoName, folderPath)
	}
	return nil, nil
}

func (h *RepoRequesthandler) MoveFolder(c fuego.ContextWithBody[folderMoveRequest]) (folderResponse, error) {
	repoName := c.PathParam("repo")
	folderPath := strings.TrimPrefix(c.PathParam("path"), "/")
	gc := ginCtxBody(c)

	body, err := c.Body()
	if err != nil {
		return folderResponse{}, err
	}

	backend, ok := h.backends[repoName]
	if !ok {
		return folderResponse{}, fuego.NotFoundError{Detail: "repository not found"}
	}

	dest := strings.TrimPrefix(body.Destination, "/")
	folderName := filepath.Base(folderPath)
	newRelPath := filepath.Join(dest, folderName)
	userName, userEmail := getIdentity(gc)

	// Check delete on source, update on destination parent
	if err := h.checkPermission(repoName, userEmail, folderPath+"/", permissions.ContentManager); err != nil {
		return folderResponse{}, permError(err)
	}
	destParent := dest
	if destParent == "" {
		destParent = "."
	}
	if err := h.checkPermission(repoName, userEmail, destParent+"/", permissions.Contributor); err != nil {
		return folderResponse{}, permError(err)
	}

	if err := backend.RenameFolder(folderPath, newRelPath); err != nil {
		log.WithError(err).WithField("repo", repoName).WithField("folder", folderPath).Warn("failed to move folder")
		return folderResponse{}, fuego.HTTPError{Detail: "failed to move folder"}
	}

	// Update permission entries
	if resolver := h.permResolvers[repoName]; resolver != nil {
		resolver.RenamePath(folderPath+"/", newRelPath+"/")
		h.savePermissions(repoName)
	}

	backend.SaveChangesAsync("move folder "+folderPath+" to "+newRelPath, userName, userEmail)
	if h.shouldIndex(repoName) {
		h.indexer.DeleteFolderIndex(repoName, folderPath)
		h.indexer.ReindexFolder(repoName, newRelPath)
	}
	return folderResponse{Name: folderName, Path: newRelPath, Permission: h.effectivePermissionLevel(repoName, userEmail, newRelPath+"/")}, nil
}

// --- Order Handlers ---

type orderRequest struct {
	Order []string `json:"order" validate:"required"`
}

type orderResponse struct {
	Path  string   `json:"path"`
	Order []string `json:"order"`
}

func (h *RepoRequesthandler) SetOrder(c fuego.ContextWithBody[orderRequest]) (orderResponse, error) {
	repoName := c.PathParam("repo")
	dirPath := strings.TrimPrefix(c.PathParam("path"), "/")
	gc := ginCtxBody(c)

	body, err := c.Body()
	if err != nil {
		return orderResponse{}, err
	}

	backend, ok := h.backends[repoName]
	if !ok {
		return orderResponse{}, fuego.NotFoundError{Detail: "repository not found"}
	}

	userName, userEmail := getIdentity(gc)
	if err := h.checkPermission(repoName, userEmail, dirPath+"/", permissions.Contributor); err != nil {
		return orderResponse{}, permError(err)
	}

	root := h.docPaths[repoName]
	fullDir := filepath.Join(root, dirPath)

	if err := backend.WriteOrder(fullDir, body.Order); err != nil {
		return orderResponse{}, fuego.HTTPError{Detail: "failed to write order"}
	}

	commitPath := dirPath
	if commitPath == "" {
		commitPath = "root"
	}
	backend.SaveChangesAsync("update order for "+commitPath, userName, userEmail)

	return orderResponse{Path: dirPath, Order: body.Order}, nil
}

// --- Draft Handlers ---

func (h *RepoRequesthandler) SaveDraft(c fuego.ContextWithBody[draftRequest]) (fileResponse, error) {
	repoName := c.PathParam("repo")
	filePath := strings.TrimPrefix(c.PathParam("path"), "/")
	gc := ginCtxBody(c)

	body, err := c.Body()
	if err != nil {
		return fileResponse{}, err
	}

	backend, ok := h.backends[repoName]
	if !ok {
		return fileResponse{}, fuego.NotFoundError{Detail: "repository not found"}
	}

	dm, ok := h.draftManagers[repoName]
	if !ok {
		return fileResponse{}, fuego.HTTPError{Detail: "drafts not available for this repository"}
	}

	subfolder := filepath.Dir(filePath)
	if subfolder == "." {
		subfolder = ""
	}
	filename := filepath.Base(filePath)
	userName, userEmail := getIdentity(gc)

	if err := h.checkPermission(repoName, userEmail, filePath, permissions.Contributor); err != nil {
		return fileResponse{}, permError(err)
	}

	// Get the current commit hash as the base for future merge
	baseCommitHash := ""
	if hash, err := backend.GetFileCommitHash(subfolder, filename); err == nil {
		baseCommitHash = hash
	}

	if err := dm.SaveDraft(subfolder, filename, userEmail, userName, baseCommitHash, []byte(body.Content)); err != nil {
		log.WithError(err).WithField("repo", repoName).WithField("path", filePath).Warn("failed to save draft")
		return fileResponse{}, fuego.HTTPError{Detail: "failed to save draft"}
	}

	meta, _ := dm.GetDraftMeta(subfolder, filename, userEmail)
	resp := fileResponse{
		Name:    filename,
		Path:    filePath,
		Content: body.Content,
		IsDraft: true,
	}
	if meta != nil {
		resp.DraftCreatedAt = meta.CreatedAt.Format("2006-01-02T15:04:05Z07:00")
		resp.DraftUpdatedAt = meta.UpdatedAt.Format("2006-01-02T15:04:05Z07:00")
	}

	return resp, nil
}

func (h *RepoRequesthandler) PublishDraft(c fuego.ContextNoBody) (publishResponse, error) {
	repoName := c.PathParam("repo")
	filePath := strings.TrimPrefix(c.PathParam("path"), "/")
	gc := ginCtx(c)

	backend, ok := h.backends[repoName]
	if !ok {
		return publishResponse{}, fuego.NotFoundError{Detail: "repository not found"}
	}

	dm, ok := h.draftManagers[repoName]
	if !ok {
		return publishResponse{}, fuego.HTTPError{Detail: "drafts not available for this repository"}
	}

	subfolder := filepath.Dir(filePath)
	if subfolder == "." {
		subfolder = ""
	}
	filename := filepath.Base(filePath)
	userName, userEmail := getIdentity(gc)

	if err := h.checkPermission(repoName, userEmail, filePath, permissions.Contributor); err != nil {
		return publishResponse{}, permError(err)
	}

	// Get the draft
	draftContent, meta, err := dm.GetDraft(subfolder, filename, userEmail)
	if err != nil {
		return publishResponse{}, fuego.NotFoundError{Detail: "no draft found"}
	}

	// Get the base version from when the draft was created
	baseContent := ""
	if meta != nil && meta.BaseCommitHash != "" {
		if content, err := backend.GetFileAtCommit(meta.BaseCommitHash, subfolder, filename); err == nil {
			baseContent = content
		}
	}

	// Get the current version on disk
	currentBytes, err := backend.GetFile(subfolder, filename)
	if err != nil {
		// File doesn't exist yet — treat as empty base/current
		currentBytes = []byte{}
	}
	currentContent := string(currentBytes)

	// Three-way merge
	merged, hasConflicts, conflictText := draft.ThreeWayMerge(baseContent, currentContent, string(draftContent))
	if hasConflicts {
		return publishResponse{
			Success:   false,
			Conflicts: conflictText,
			Path:      filePath,
		}, nil
	}

	// Write the merged content to the actual file
	if err := backend.AddFile(subfolder, filename, []byte(merged)); err != nil {
		log.WithError(err).WithField("repo", repoName).WithField("path", filePath).Warn("failed to publish draft")
		return publishResponse{}, fuego.HTTPError{Detail: "failed to write file"}
	}

	backend.SaveChangesAsync("publish draft "+filePath, userName, userEmail)
	if h.shouldIndex(repoName) {
		h.indexer.IndexFile(repoName, filePath)
	}

	// Delete the draft
	if err := dm.DeleteDraft(subfolder, filename, userEmail); err != nil {
		log.WithError(err).Warn("failed to clean up draft after publish")
	}

	return publishResponse{
		Success: true,
		Merged:  merged,
		Path:    filePath,
	}, nil
}

func (h *RepoRequesthandler) DiscardDraft(c fuego.ContextNoBody) (any, error) {
	repoName := c.PathParam("repo")
	filePath := strings.TrimPrefix(c.PathParam("path"), "/")
	gc := ginCtx(c)

	dm, ok := h.draftManagers[repoName]
	if !ok {
		return nil, fuego.HTTPError{Detail: "drafts not available for this repository"}
	}

	subfolder := filepath.Dir(filePath)
	if subfolder == "." {
		subfolder = ""
	}
	filename := filepath.Base(filePath)
	_, userEmail := getIdentity(gc)

	if err := h.checkPermission(repoName, userEmail, filePath, permissions.Contributor); err != nil {
		return nil, permError(err)
	}

	if !dm.HasDraft(subfolder, filename, userEmail) {
		return nil, fuego.NotFoundError{Detail: "no draft found"}
	}

	if err := dm.DeleteDraft(subfolder, filename, userEmail); err != nil {
		log.WithError(err).WithField("repo", repoName).WithField("path", filePath).Warn("failed to discard draft")
		return nil, fuego.HTTPError{Detail: "failed to discard draft"}
	}

	return nil, nil
}

// savePermissions persists the permissions file for a repo via the backend.
func (h *RepoRequesthandler) savePermissions(repoSlug string) {
	resolver := h.permResolvers[repoSlug]
	if resolver == nil {
		return
	}
	backend, ok := h.backends[repoSlug]
	if !ok {
		return
	}
	data, err := permissions.Marshal(resolver)
	if err != nil {
		log.WithError(err).WithField("repo", repoSlug).Warn("failed to marshal permissions file")
		return
	}
	if err := backend.AddFile("", permissions.PermissionsFileName, data); err != nil {
		log.WithError(err).WithField("repo", repoSlug).Warn("failed to save permissions file")
	}
}

// --- Permission API Handlers ---

type permissionRequest struct {
	Default string            `json:"default,omitempty"`
	Users   map[string]string `json:"users,omitempty"`
	Groups  map[string]string `json:"groups,omitempty"`
}

type permissionResponse struct {
	Owner          string            `json:"owner"`
	Default        string            `json:"default"`
	Users          map[string]string `json:"users,omitempty"`
	Groups         map[string]string `json:"groups,omitempty"`
	EffectiveLevel string            `json:"effective_level"`
	Source         string            `json:"source"`
}

func (h *RepoRequesthandler) GetPermissions(c fuego.ContextNoBody) (permissionResponse, error) {
	repoName := c.PathParam("repo")
	filePath := strings.TrimPrefix(c.PathParam("path"), "/")
	gc := ginCtx(c)

	_, userEmail := getIdentity(gc)
	isAdmin := userEmail != "" && h.isSystemAdmin != nil && h.isSystemAdmin(userEmail)

	resolver := h.permResolvers[repoName]
	if resolver == nil {
		if isAdmin {
			return permissionResponse{Default: "none", EffectiveLevel: permissions.Manager.String(), Source: "admin"}, nil
		}
		return permissionResponse{}, nil
	}

	level, source := resolver.EffectiveSource(userEmail, filePath)
	if isAdmin && level < permissions.Manager {
		level, source = permissions.Manager, "admin"
	}

	resp := permissionResponse{
		Default:        "none",
		EffectiveLevel: level.String(),
		Source:         source,
	}

	// Try to get the exact entry for this path
	pathKey := filePath
	entry, ok := resolver.GetEntry(pathKey)
	if !ok {
		// Try folder key
		entry, ok = resolver.GetEntry(pathKey + "/")
	}
	if ok {
		resp.Owner = entry.Owner
		resp.Default = entry.Default.String()
		if len(entry.Users) > 0 {
			resp.Users = make(map[string]string, len(entry.Users))
			for email, lvl := range entry.Users {
				resp.Users[email] = lvl.String()
			}
		}
		if len(entry.Groups) > 0 {
			resp.Groups = make(map[string]string, len(entry.Groups))
			for group, lvl := range entry.Groups {
				resp.Groups[group] = lvl.String()
			}
		}
	} else if pf := resolver.File(); pf.Root != nil {
		resp.Default = pf.Root.Default.String()
	}

	return resp, nil
}

func (h *RepoRequesthandler) SetPermissions(c fuego.ContextWithBody[permissionRequest]) (permissionResponse, error) {
	repoName := c.PathParam("repo")
	if h.isReadOnly(repoName) {
		return permissionResponse{}, fuego.ForbiddenError{Detail: "repository is read-only"}
	}
	filePath := strings.TrimPrefix(c.PathParam("path"), "/")
	gc := ginCtxBody(c)

	body, err := c.Body()
	if err != nil {
		return permissionResponse{}, fuego.BadRequestError{Detail: "invalid request body"}
	}

	userName, userEmail := getIdentity(gc)
	log.WithFields(log.Fields{"repo": repoName, "path": filePath, "user": userEmail, "body": body}).Info("set permissions request")
	if userEmail == "" {
		return permissionResponse{}, fuego.UnauthorizedError{Detail: "authentication required"}
	}

	// Root folder requires repo admin; other paths require owner or system admin
	isAdmin := h.isSystemAdmin != nil && h.isSystemAdmin(userEmail)

	resolver := h.permResolvers[repoName]
	if resolver == nil {
		if h.isReadOnly(repoName) {
			return permissionResponse{}, fuego.ForbiddenError{Detail: "repository is read-only"}
		}
		if !isAdmin {
			return permissionResponse{}, fuego.ForbiddenError{Detail: "only a repo admin can initialize permissions"}
		}
		resolver = permissions.Initialize(permissions.None)
		h.permResolvers[repoName] = resolver
	}
	isRoot := filePath == "" || filePath == "/" || filePath == "."
	if isRoot {
		if !isAdmin {
			return permissionResponse{}, fuego.ForbiddenError{Detail: "only a repo admin can set permissions on the root folder"}
		}
	} else if !isAdmin && !resolver.IsOwner(userEmail, filePath) {
		return permissionResponse{}, fuego.ForbiddenError{Detail: "only the owner or a system admin can set permissions"}
	}

	pathKey := filePath
	// Determine if this is a folder entry
	if strings.HasSuffix(filePath, "/") {
		pathKey = filePath
	}

	if body.Default != "" {
		lvl, err := permissions.ParseLevel(body.Default)
		if err != nil {
			return permissionResponse{}, fuego.BadRequestError{Detail: err.Error()}
		}
		if lvl == permissions.Manager && !isRoot {
			return permissionResponse{}, fuego.BadRequestError{Detail: "manager role can only be assigned at the repo root level"}
		}
		resolver.SetDefault(pathKey, lvl)
	}

	// Replace user permissions entirely — users not in the request are removed.
	if body.Users != nil {
		newUsers := make(map[string]permissions.Level, len(body.Users))
		for email, levelStr := range body.Users {
			lvl, err := permissions.ParseLevel(levelStr)
			if err != nil {
				return permissionResponse{}, fuego.BadRequestError{Detail: fmt.Sprintf("invalid level for user %s: %v", email, err)}
			}
			if lvl == permissions.Manager && !isRoot {
				return permissionResponse{}, fuego.BadRequestError{Detail: "manager role can only be assigned at the repo root level"}
			}
			newUsers[email] = lvl
		}
		resolver.ReplaceUsers(pathKey, newUsers)
	}

	// Replace group permissions entirely — groups not in the request are removed.
	if body.Groups != nil {
		newGroups := make(map[string]permissions.Level, len(body.Groups))
		for groupName, levelStr := range body.Groups {
			lvl, err := permissions.ParseLevel(levelStr)
			if err != nil {
				return permissionResponse{}, fuego.BadRequestError{Detail: fmt.Sprintf("invalid level for group %s: %v", groupName, err)}
			}
			if lvl == permissions.Manager && !isRoot {
				return permissionResponse{}, fuego.BadRequestError{Detail: "manager role can only be assigned at the repo root level"}
			}
			newGroups[groupName] = lvl
		}
		resolver.ReplaceGroups(pathKey, newGroups)
	}

	h.savePermissions(repoName)

	// Commit changes synchronously so they survive restarts
	backend := h.backends[repoName]
	if backend != nil {
		if err := backend.SaveChanges("update permissions for "+filePath, userName, userEmail); err != nil {
			log.WithError(err).WithField("repo", repoName).Warn("failed to commit permissions change")
		}
	}

	// Build response
	level, source := resolver.EffectiveSource(userEmail, filePath)
	resp := permissionResponse{
		EffectiveLevel: level.String(),
		Source:         source,
	}
	entry, ok := resolver.GetEntry(pathKey)
	if ok {
		resp.Owner = entry.Owner
		resp.Default = entry.Default.String()
		if len(entry.Users) > 0 {
			resp.Users = make(map[string]string, len(entry.Users))
			for e, lvl := range entry.Users {
				resp.Users[e] = lvl.String()
			}
		}
		if len(entry.Groups) > 0 {
			resp.Groups = make(map[string]string, len(entry.Groups))
			for g, lvl := range entry.Groups {
				resp.Groups[g] = lvl.String()
			}
		}
	}
	return resp, nil
}

func (h *RepoRequesthandler) RemovePermissions(c fuego.ContextNoBody) (any, error) {
	repoName := c.PathParam("repo")
	if h.isReadOnly(repoName) {
		return nil, fuego.ForbiddenError{Detail: "repository is read-only"}
	}
	filePath := strings.TrimPrefix(c.PathParam("path"), "/")
	gc := ginCtx(c)

	userName, userEmail := getIdentity(gc)
	log.WithFields(log.Fields{"repo": repoName, "path": filePath, "user": userEmail}).Info("remove permissions request")
	if userEmail == "" {
		return nil, fuego.UnauthorizedError{Detail: "authentication required"}
	}

	resolver := h.permResolvers[repoName]
	if resolver == nil {
		return nil, fuego.BadRequestError{Detail: "no permissions file for this repository"}
	}

	isAdmin := h.isSystemAdmin != nil && h.isSystemAdmin(userEmail)
	isRoot := filePath == "" || filePath == "/" || filePath == "."
	if isRoot {
		if !isAdmin {
			return nil, fuego.ForbiddenError{Detail: "only a repo admin can remove permissions on the root folder"}
		}
	} else if !isAdmin && !resolver.IsOwner(userEmail, filePath) {
		return nil, fuego.ForbiddenError{Detail: "only the owner or a system admin can remove permissions"}
	}

	resolver.RemoveEntry(filePath)
	resolver.RemoveEntry(filePath + "/")
	h.savePermissions(repoName)

	backend := h.backends[repoName]
	if backend != nil {
		if err := backend.SaveChanges("remove permissions for "+filePath, userName, userEmail); err != nil {
			log.WithError(err).WithField("repo", repoName).Warn("failed to commit permissions change")
		}
	}

	return nil, nil
}

type initPermissionsRequest struct {
	Default string `json:"default,omitempty"` // "none"|"read"|"update"|"delete"; defaults to "read"
}

type initPermissionsResponse struct {
	Default string `json:"default"`
	Message string `json:"message"`
}

// InitPermissions creates or resets the .wiki-permissions.yaml for a repository.
// Only system admins can call this.
func (h *RepoRequesthandler) InitPermissions(c fuego.ContextWithBody[initPermissionsRequest]) (initPermissionsResponse, error) {
	repoName := c.PathParam("repo")
	if h.isReadOnly(repoName) {
		return initPermissionsResponse{}, fuego.ForbiddenError{Detail: "repository is read-only"}
	}
	gc := ginCtxBody(c)

	userName, userEmail := getIdentity(gc)
	if userEmail == "" {
		return initPermissionsResponse{}, fuego.UnauthorizedError{Detail: "authentication required"}
	}

	isAdmin := h.isSystemAdmin != nil && h.isSystemAdmin(userEmail)
	if !isAdmin {
		return initPermissionsResponse{}, fuego.ForbiddenError{Detail: "only a system admin can initialize permissions"}
	}

	backend, ok := h.backends[repoName]
	if !ok {
		return initPermissionsResponse{}, fuego.NotFoundError{Detail: "repository not found"}
	}

	body, err := c.Body()
	if err != nil {
		return initPermissionsResponse{}, fuego.BadRequestError{Detail: "invalid request body"}
	}

	log.WithFields(log.Fields{"repo": repoName, "user": userEmail, "body": body}).Info("init permissions request")

	defaultLevel := permissions.None
	if body.Default != "" {
		lvl, err := permissions.ParseLevel(body.Default)
		if err != nil {
			return initPermissionsResponse{}, fuego.BadRequestError{Detail: err.Error()}
		}
		defaultLevel = lvl
	}

	resolver := permissions.Initialize(defaultLevel)
	h.permResolvers[repoName] = resolver

	data, err := permissions.Marshal(resolver)
	if err != nil {
		return initPermissionsResponse{}, fuego.InternalServerError{Detail: "failed to marshal permissions file"}
	}
	if err := backend.AddFile("", permissions.PermissionsFileName, data); err != nil {
		return initPermissionsResponse{}, fuego.InternalServerError{Detail: "failed to save permissions file"}
	}

	if err := backend.SaveChanges("initialize permissions", userName, userEmail); err != nil {
		return initPermissionsResponse{}, fuego.InternalServerError{Detail: "failed to push permissions"}
	}

	return initPermissionsResponse{
		Default: defaultLevel.String(),
		Message: fmt.Sprintf("permissions initialized for repo %q", repoName),
	}, nil
}

// --- Permission Group CRUD ---

type permGroupRequest struct {
	Members []string `json:"members" validate:"required"`
}

type permGroupResponse struct {
	Name    string   `json:"name"`
	Members []string `json:"members"`
}

// GetPermissionGroups returns all groups defined in the repo's permissions file.
func (h *RepoRequesthandler) GetPermissionGroups(c fuego.ContextNoBody) ([]permGroupResponse, error) {
	repoName := c.PathParam("repo")

	resolver := h.permResolvers[repoName]
	if resolver == nil {
		return []permGroupResponse{}, nil
	}

	groups := resolver.GetGroups()
	out := make([]permGroupResponse, 0, len(groups))
	for name, members := range groups {
		out = append(out, permGroupResponse{Name: name, Members: members})
	}
	return out, nil
}

// AddPermissionGroup creates a new group in the repo's permissions file.
func (h *RepoRequesthandler) AddPermissionGroup(c fuego.ContextWithBody[permGroupRequest]) (permGroupResponse, error) {
	repoName := c.PathParam("repo")
	if h.isReadOnly(repoName) {
		return permGroupResponse{}, fuego.ForbiddenError{Detail: "repository is read-only"}
	}
	groupName := ginCtxBody(c).Param("name")
	gc := ginCtxBody(c)

	userName, userEmail := getIdentity(gc)
	if userEmail == "" {
		return permGroupResponse{}, fuego.UnauthorizedError{Detail: "authentication required"}
	}

	isAdmin := h.isSystemAdmin != nil && h.isSystemAdmin(userEmail)
	if !isAdmin {
		return permGroupResponse{}, fuego.ForbiddenError{Detail: "only a repo admin can manage permission groups"}
	}

	resolver := h.permResolvers[repoName]
	if resolver == nil {
		return permGroupResponse{}, fuego.BadRequestError{Detail: "no permissions file for this repository"}
	}

	body, err := c.Body()
	if err != nil {
		return permGroupResponse{}, fuego.BadRequestError{Detail: "invalid request body"}
	}

	log.WithFields(log.Fields{"repo": repoName, "group": groupName, "user": userEmail, "body": body}).Info("add permission group request")

	if err := resolver.AddGroup(groupName, body.Members); err != nil {
		return permGroupResponse{}, fuego.ConflictError{Detail: err.Error()}
	}

	h.savePermissions(repoName)
	backend := h.backends[repoName]
	if backend != nil {
		if err := backend.SaveChanges("add permission group "+groupName, userName, userEmail); err != nil {
			log.WithError(err).WithField("repo", repoName).Warn("failed to commit permissions change")
		}
	}

	return permGroupResponse{Name: groupName, Members: body.Members}, nil
}

// UpdatePermissionGroup updates the members of an existing group.
func (h *RepoRequesthandler) UpdatePermissionGroup(c fuego.ContextWithBody[permGroupRequest]) (permGroupResponse, error) {
	repoName := c.PathParam("repo")
	if h.isReadOnly(repoName) {
		return permGroupResponse{}, fuego.ForbiddenError{Detail: "repository is read-only"}
	}
	groupName := ginCtxBody(c).Param("name")
	gc := ginCtxBody(c)

	userName, userEmail := getIdentity(gc)
	if userEmail == "" {
		return permGroupResponse{}, fuego.UnauthorizedError{Detail: "authentication required"}
	}

	isAdmin := h.isSystemAdmin != nil && h.isSystemAdmin(userEmail)
	if !isAdmin {
		return permGroupResponse{}, fuego.ForbiddenError{Detail: "only a repo admin can manage permission groups"}
	}

	resolver := h.permResolvers[repoName]
	if resolver == nil {
		return permGroupResponse{}, fuego.BadRequestError{Detail: "no permissions file for this repository"}
	}

	body, err := c.Body()
	if err != nil {
		return permGroupResponse{}, fuego.BadRequestError{Detail: "invalid request body"}
	}

	log.WithFields(log.Fields{"repo": repoName, "group": groupName, "user": userEmail, "body": body}).Info("update permission group request")

	if err := resolver.UpdateGroup(groupName, body.Members); err != nil {
		return permGroupResponse{}, fuego.NotFoundError{Detail: err.Error()}
	}

	h.savePermissions(repoName)
	backend := h.backends[repoName]
	if backend != nil {
		if err := backend.SaveChanges("update permission group "+groupName, userName, userEmail); err != nil {
			log.WithError(err).WithField("repo", repoName).Warn("failed to commit permissions change")
		}
	}

	return permGroupResponse{Name: groupName, Members: body.Members}, nil
}

// DeletePermissionGroup removes a group and its references from all path entries.
func (h *RepoRequesthandler) DeletePermissionGroup(c fuego.ContextNoBody) (any, error) {
	repoName := c.PathParam("repo")
	if h.isReadOnly(repoName) {
		return nil, fuego.ForbiddenError{Detail: "repository is read-only"}
	}
	groupName := ginCtx(c).Param("name")
	gc := ginCtx(c)

	userName, userEmail := getIdentity(gc)
	if userEmail == "" {
		return nil, fuego.UnauthorizedError{Detail: "authentication required"}
	}

	isAdmin := h.isSystemAdmin != nil && h.isSystemAdmin(userEmail)
	if !isAdmin {
		return nil, fuego.ForbiddenError{Detail: "only a repo admin can manage permission groups"}
	}

	resolver := h.permResolvers[repoName]
	if resolver == nil {
		return nil, fuego.BadRequestError{Detail: "no permissions file for this repository"}
	}

	log.WithFields(log.Fields{"repo": repoName, "group": groupName, "user": userEmail}).Info("delete permission group request")

	if err := resolver.RemoveGroup(groupName); err != nil {
		return nil, fuego.NotFoundError{Detail: err.Error()}
	}

	h.savePermissions(repoName)
	backend := h.backends[repoName]
	if backend != nil {
		if err := backend.SaveChanges("delete permission group "+groupName, userName, userEmail); err != nil {
			log.WithError(err).WithField("repo", repoName).Warn("failed to commit permissions change")
		}
	}

	return nil, nil
}

func buildTree(entries []repo.FileEntry, backend repo.RepoBackend, docPath string) *orderedMap {
	root := &dirNode{children: map[string]*dirNode{}}

	for _, f := range entries {
		parts := []string{}
		if f.Path != "" {
			parts = strings.Split(f.Path, string(filepath.Separator))
		}

		current := root
		for _, part := range parts {
			if current.children == nil {
				current.children = map[string]*dirNode{}
			}
			child, ok := current.children[part]
			if !ok {
				child = &dirNode{children: map[string]*dirNode{}}
				current.children[part] = child
			}
			current = child
		}

		if f.IsDir {
			if current.children == nil {
				current.children = map[string]*dirNode{}
			}
			if _, ok := current.children[f.Name]; !ok {
				current.children[f.Name] = &dirNode{children: map[string]*dirNode{}}
			}
		} else {
			current.files = append(current.files, fileEntry{name: f.Name, size: f.Size})
		}
	}

	return serializeDir(root, backend, docPath)
}

func serializeDir(d *dirNode, backend repo.RepoBackend, dirPath string) *orderedMap {
	// Collect file and folder keys
	fileValues := map[string]int64{}
	for _, f := range d.files {
		fileValues[f.name] = f.size
	}
	childMaps := map[string]*orderedMap{}
	for name, child := range d.children {
		childMaps[name] = serializeDir(child, backend, filepath.Join(dirPath, name))
	}

	// Build set of all keys (files plain, folders with / prefix)
	existing := map[string]bool{}
	for name := range fileValues {
		existing[name] = true
	}
	for name := range childMaps {
		existing["/"+name] = true
	}

	// Read .order file for custom ordering; fall back to alphabetical
	order, _ := backend.ReadOrder(dirPath)

	var ordered []string
	seen := map[string]bool{}
	for _, name := range order {
		if existing[name] {
			ordered = append(ordered, name)
			seen[name] = true
		} else if existing["/"+name] {
			ordered = append(ordered, "/"+name)
			seen["/"+name] = true
		}
	}
	// Append remaining entries not in .order, sorted alphabetically
	var remaining []string
	for key := range existing {
		if !seen[key] {
			remaining = append(remaining, key)
		}
	}
	sort.Strings(remaining)
	ordered = append(ordered, remaining...)

	// Build ordered result
	result := newOrderedMap()
	for _, key := range ordered {
		if strings.HasPrefix(key, "/") {
			result.Set(key, childMaps[key[1:]])
		} else {
			result.Set(key, fileValues[key])
		}
	}
	return result
}
