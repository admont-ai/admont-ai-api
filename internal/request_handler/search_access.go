package requesthandler

import (
	"github.com/christianfischer/md-wiki-server/internal/permissions"
	"github.com/christianfischer/md-wiki-server/internal/pg_vector/backend"
)

// searchPermOverfetch is how many times topK extra results to request from the
// search backend before per-file permission filtering, so that dropping a few
// restricted hits does not shrink the final result set below topK.
const searchPermOverfetch = 4

// overfetchK returns how many results to request from the search backend so that
// topK accessible results can still be returned after permission filtering. The
// result is capped to avoid pathological scans.
func overfetchK(topK int) int {
	if topK <= 0 {
		return topK
	}
	k := topK * searchPermOverfetch
	if k > 400 {
		k = 400
	}
	return k
}

// filterAccessibleResults returns the subset of search results the user is
// permitted to view, capped at topK. It mirrors the per-file checks used by the
// file-read paths:
//   - System admins bypass all checks.
//   - Dot-paths are never returned to non-admins.
//   - A nil resolver for a repo means there are no file-level permissions; the
//     repo-level gate has already authorized access, so results are kept.
//   - Otherwise a chunk is kept only if the user has at least Viewer on its path.
func filterAccessibleResults(results []backend.SearchResult, resolvers map[string]*permissions.Resolver, isAdmin func(string) bool, identity string, topK int) []backend.SearchResult {
	admin := identity != "" && isAdmin != nil && isAdmin(identity)

	filtered := make([]backend.SearchResult, 0, len(results))
	for _, r := range results {
		if !admin {
			if isDotPath(r.FilePath) {
				continue
			}
			if resolver := resolvers[r.RepoSlug]; resolver != nil && !resolver.Check(identity, r.FilePath, permissions.Viewer) {
				continue
			}
		}
		filtered = append(filtered, r)
		if topK > 0 && len(filtered) >= topK {
			break
		}
	}
	return filtered
}
