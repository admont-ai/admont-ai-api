package requesthandler

import (
	"testing"

	"github.com/christianfischer/md-wiki-server/internal/permissions"
	"github.com/christianfischer/md-wiki-server/internal/pg_vector/backend"
)

// resolver granting Viewer to everyone on teamA/** but nothing elsewhere.
func teamAResolver() *permissions.Resolver {
	return permissions.NewResolver(permissions.PermissionsFile{
		Root: &permissions.PathEntry{Default: permissions.None},
		Paths: map[string]permissions.PathEntry{
			"teamA/doc.md": {Default: permissions.Viewer},
		},
	})
}

func paths(results []backend.SearchResult) []string {
	out := make([]string, len(results))
	for i, r := range results {
		out[i] = r.FilePath
	}
	return out
}

func eq(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestFilterAccessibleResults(t *testing.T) {
	results := []backend.SearchResult{
		{RepoSlug: "r1", FilePath: "teamA/doc.md"},
		{RepoSlug: "r1", FilePath: "teamB/secret.md"},
		{RepoSlug: "open", FilePath: "anything.md"},
		{RepoSlug: "r1", FilePath: ".hidden/notes.md"},
	}
	resolvers := map[string]*permissions.Resolver{"r1": teamAResolver()}
	notAdmin := func(string) bool { return false }
	isAdmin := func(id string) bool { return id == "admin@x" }

	t.Run("non-admin sees only permitted and nil-resolver repos", func(t *testing.T) {
		got := filterAccessibleResults(results, resolvers, notAdmin, "user@x", 10)
		// teamA permitted; teamB denied; "open" repo has no resolver (kept);
		// dot-path dropped.
		if want := []string{"teamA/doc.md", "anything.md"}; !eq(paths(got), want) {
			t.Fatalf("got %v, want %v", paths(got), want)
		}
	})

	t.Run("admin bypasses all checks", func(t *testing.T) {
		got := filterAccessibleResults(results, resolvers, isAdmin, "admin@x", 10)
		want := []string{"teamA/doc.md", "teamB/secret.md", "anything.md", ".hidden/notes.md"}
		if !eq(paths(got), want) {
			t.Fatalf("got %v, want %v", paths(got), want)
		}
	})

	t.Run("topK caps after filtering", func(t *testing.T) {
		got := filterAccessibleResults(results, resolvers, notAdmin, "user@x", 1)
		if want := []string{"teamA/doc.md"}; !eq(paths(got), want) {
			t.Fatalf("got %v, want %v", paths(got), want)
		}
	})

	t.Run("nil resolver map keeps non-dot results", func(t *testing.T) {
		got := filterAccessibleResults(results, nil, notAdmin, "user@x", 10)
		// No resolvers configured at all → only the dot-path is dropped.
		want := []string{"teamA/doc.md", "teamB/secret.md", "anything.md"}
		if !eq(paths(got), want) {
			t.Fatalf("got %v, want %v", paths(got), want)
		}
	})

	t.Run("unauthenticated denied private, allowed public default", func(t *testing.T) {
		publicResolver := permissions.NewResolver(permissions.PermissionsFile{
			Root: &permissions.PathEntry{Default: permissions.Viewer},
		})
		res := []backend.SearchResult{
			{RepoSlug: "r1", FilePath: "teamA/doc.md"},   // teamA grants everyone Viewer
			{RepoSlug: "r1", FilePath: "teamB/secret.md"}, // root None → denied
			{RepoSlug: "pub", FilePath: "readme.md"},      // root default Viewer → allowed
		}
		rs := map[string]*permissions.Resolver{"r1": teamAResolver(), "pub": publicResolver}
		got := filterAccessibleResults(res, rs, notAdmin, "", 10)
		if want := []string{"teamA/doc.md", "readme.md"}; !eq(paths(got), want) {
			t.Fatalf("got %v, want %v", paths(got), want)
		}
	})
}

func TestOverfetchK(t *testing.T) {
	cases := map[int]int{0: 0, -5: -5, 10: 40, 100: 400, 200: 400}
	for in, want := range cases {
		if got := overfetchK(in); got != want {
			t.Errorf("overfetchK(%d) = %d, want %d", in, got, want)
		}
	}
}
