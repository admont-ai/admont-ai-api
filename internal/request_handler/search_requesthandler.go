package requesthandler

import (
	"net/http"

	"github.com/christianfischer/md-wiki-server/internal/permissions"
	"github.com/christianfischer/md-wiki-server/internal/pg_vector/backend"
	"github.com/christianfischer/md-wiki-server/internal/repo"
	"github.com/christianfischer/md-wiki-server/internal/store/git_repo"
	"github.com/gin-gonic/gin"
	"github.com/go-fuego/fuego"
	log "github.com/sirupsen/logrus"
)

type searchRepoSettings struct {
	Name         string
	PublicAccess bool
}

type searchRepoRef struct {
	Name string `json:"name" validate:"required"`
	Path string `json:"path,omitempty"`
}

type searchRequest struct {
	Repos     []searchRepoRef `json:"repos" validate:"required"`
	Query     string          `json:"query" validate:"required"`
	Mode      string          `json:"mode,omitempty"`
	TopK      int             `json:"top_k,omitempty"`
	Threshold float64         `json:"threshold,omitempty"`
}

type searchResponse struct {
	Results []backend.SearchResult `json:"results"`
}

type statusResponse struct {
	Repos []backend.RepoState `json:"repos"`
}

type SearchRequesthandler struct {
	backend       *backend.Holder
	repoState     backend.RepoStateStore
	backends      map[string]repo.RepoBackend
	repoConfigs   map[string]*git_repo.GitRepo
	permResolvers map[string]*permissions.Resolver
	isSystemAdmin func(string) bool
}

func NewSearchRequesthandler(b *backend.Holder, rs backend.RepoStateStore, backends map[string]repo.RepoBackend, repoConfigs map[string]*git_repo.GitRepo, permResolvers map[string]*permissions.Resolver) *SearchRequesthandler {
	return &SearchRequesthandler{backend: b, repoState: rs, backends: backends, repoConfigs: repoConfigs, permResolvers: permResolvers}
}

// SetSystemAdminCheck wires the system-admin predicate used to bypass per-file
// permission filtering (mirrors the repo and agent handlers).
func (h *SearchRequesthandler) SetSystemAdminCheck(fn func(string) bool) {
	h.isSystemAdmin = fn
}

func (h *SearchRequesthandler) Search(c fuego.ContextWithBody[searchRequest]) (searchResponse, error) {
	gc := c.Context().(*gin.Context)

	body, err := c.Body()
	if err != nil {
		return searchResponse{}, err
	}

	if body.Query == "" {
		return searchResponse{}, fuego.BadRequestError{Detail: "query is required"}
	}

	mode := body.Mode
	if mode == "" {
		mode = "hybrid"
	}
	if mode != "fulltext" && mode != "semantic" && mode != "hybrid" {
		return searchResponse{}, fuego.BadRequestError{Detail: "mode must be fulltext, semantic, or hybrid"}
	}

	topK := body.TopK
	if topK <= 0 {
		topK = 10
	}
	if topK > 100 {
		topK = 100
	}

	threshold := body.Threshold
	if threshold < 0 {
		threshold = 0
	}

	// Filter repos by auth
	identity, _ := gc.Get("user_identity")
	userEmail, _ := identity.(string)

	var allowedRepos []string
	pathPrefixes := map[string]string{} // repo -> path prefix

	for _, ref := range body.Repos {
		slug := h.resolveRepoSlug(ref.Name)
		if slug == "" {
			continue
		}
		rc, ok := h.repoConfigs[slug]
		if !ok || rc.SearchProviderID == nil {
			continue
		}
		if !h.canAccessRepo(slug, userEmail) {
			continue
		}
		allowedRepos = append(allowedRepos, slug)
		if ref.Path != "" {
			pathPrefixes[slug] = ref.Path
		}
	}

	if len(allowedRepos) == 0 {
		return searchResponse{Results: []backend.SearchResult{}}, nil
	}

	// For simplicity, use a single path prefix (first one found) for all repos
	pathPrefix := ""
	for _, p := range pathPrefixes {
		pathPrefix = p
		break
	}

	b := h.backend.Get()
	if b == nil {
		return searchResponse{}, fuego.HTTPError{
			Status: http.StatusServiceUnavailable,
			Detail: "search backend not available",
		}
	}

	var results []backend.SearchResult

	// Over-fetch so per-file permission filtering can still return topK results.
	fetchK := overfetchK(topK)

	switch mode {
	case "fulltext":
		results, err = b.FulltextSearch(gc.Request.Context(), allowedRepos, body.Query, pathPrefix, fetchK, threshold)
	case "semantic":
		results, err = b.SemanticSearch(gc.Request.Context(), allowedRepos, body.Query, pathPrefix, fetchK, threshold)
	case "hybrid":
		results, err = b.HybridSearch(gc.Request.Context(), allowedRepos, body.Query, pathPrefix, fetchK, threshold)
	}

	if err != nil {
		log.WithError(err).Warn("search failed")
		return searchResponse{}, fuego.HTTPError{
			Status: http.StatusInternalServerError,
			Detail: "search failed",
		}
	}

	// Enforce per-document access: drop chunks from files the user cannot read.
	results = filterAccessibleResults(results, h.permResolvers, h.isSystemAdmin, userEmail, topK)

	if results == nil {
		results = []backend.SearchResult{}
	}

	return searchResponse{Results: results}, nil
}

func (h *SearchRequesthandler) Status(c fuego.ContextNoBody) (statusResponse, error) {
	states, err := h.repoState.GetAllSearchRepoStates(c.Context().(*gin.Context).Request.Context())
	if err != nil {
		return statusResponse{}, fuego.HTTPError{
			Status: http.StatusInternalServerError,
			Detail: "failed to get status",
		}
	}
	if states == nil {
		states = []backend.RepoState{}
	}
	return statusResponse{Repos: states}, nil
}

func (h *SearchRequesthandler) loadRepoSettings(slug string) *searchRepoSettings {
	rc, ok := h.repoConfigs[slug]
	if !ok {
		return nil
	}

	name := rc.Name
	if name == "" {
		name = slug
	}
	return &searchRepoSettings{
		Name:         name,
		PublicAccess: rc.PublicAccess,
	}
}

// resolveRepoSlug accepts either a slug or a display name and returns the slug.
func (h *SearchRequesthandler) resolveRepoSlug(nameOrSlug string) string {
	if _, ok := h.backends[nameOrSlug]; ok {
		return nameOrSlug
	}

	for slug := range h.backends {
		s := h.loadRepoSettings(slug)
		if s != nil && s.Name == nameOrSlug {
			return slug
		}
	}

	return ""
}

func (h *SearchRequesthandler) canAccessRepo(repoSlug, userEmail string) bool {
	settings := h.loadRepoSettings(repoSlug)
	if settings == nil {
		return false
	}
	if settings.PublicAccess {
		return true
	}
	if userEmail == "" {
		return false
	}
	if h.isSystemAdmin != nil && h.isSystemAdmin(userEmail) {
		return true
	}
	resolver := h.permResolvers[repoSlug]
	if resolver == nil {
		// No permissions file on a private repo: hidden from the file/list API
		// for non-admins, so it must not be searchable either.
		return false
	}
	// Accessible if the user has Viewer on the root OR any path entry, matching
	// repo listing (GetRepos). Per-file filtering then removes restricted chunks.
	return resolver.HasAnyAccess(userEmail, permissions.Viewer)
}
