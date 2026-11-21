package requesthandler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/go-fuego/fuego"
	"github.com/go-fuego/fuego/extra/fuegogin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/christianfischer/md-wiki-server/internal/draft"
	"github.com/christianfischer/md-wiki-server/internal/permissions"
	"github.com/christianfischer/md-wiki-server/internal/repo"
	"github.com/christianfischer/md-wiki-server/internal/repo/repotest"
	"github.com/christianfischer/md-wiki-server/internal/store/git_repo"
)

type moveFixture struct {
	h        *RepoRequesthandler
	src, dst *repotest.FakeBackend
	router   *gin.Engine
}

func newMoveFixture(t *testing.T, srcLevel, dstLevel permissions.Level) *moveFixture {
	t.Helper()
	gin.SetMode(gin.TestMode)
	src := repotest.NewFakeBackend().
		Seed("docs/page.md", []byte("hello")).
		Seed("docs/team/a.md", []byte("a"))
	dst := repotest.NewFakeBackend().Seed("guides/.gitkeep", nil)

	h := &RepoRequesthandler{
		backends:    map[string]repo.RepoBackend{"src": src, "dst": dst, "ro": repotest.NewFakeBackend()},
		repoConfigs: map[string]*git_repo.GitRepo{"src": {Name: "Src"}, "dst": {Name: "Dst"}, "ro": {Name: "RO", ReadOnly: true}},
		permResolvers: map[string]*permissions.Resolver{
			"src": resolverWithRoot(srcLevel),
			"dst": resolverWithRoot(dstLevel),
			"ro":  resolverWithRoot(permissions.Manager),
		},
		draftManagers: map[string]*draft.Manager{"src": draft.NewManager(t.TempDir())},
	}

	r := gin.New()
	g := r.Group("/repos/:repo", func(c *gin.Context) {
		c.Set("user_identity", "alice@example.com")
		c.Set("user_name", "Alice")
	})
	fuegogin.Post(fuego.NewEngine(), g, "/move/*path", h.MoveAcrossRepos)
	return &moveFixture{h: h, src: src, dst: dst, router: r}
}

func (f *moveFixture) move(path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/repos/src/move/"+path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	f.router.ServeHTTP(w, req)
	return w
}

func TestMoveAcrossRepos_FileSucceedsWithContentManagerOnBoth(t *testing.T) {
	f := newMoveFixture(t, permissions.ContentManager, permissions.ContentManager)
	w := f.move("docs/page.md", `{"target_repo":"dst","target_folder":"guides"}`)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.JSONEq(t, `{"repo":"dst","path":"guides/page.md","type":"file"}`, w.Body.String())

	got, err := f.dst.GetFile("guides", "page.md")
	require.NoError(t, err)
	assert.Equal(t, "hello", string(got))
	_, err = f.src.GetFile("docs", "page.md")
	assert.Error(t, err)
	require.Len(t, f.dst.SaveCalls, 1)
	assert.Equal(t, "alice@example.com", f.dst.SaveCalls[0].AuthorEmail)
}

func TestMoveAcrossRepos_FolderToRoot(t *testing.T) {
	f := newMoveFixture(t, permissions.ContentManager, permissions.ContentManager)
	w := f.move("docs/team", `{"target_repo":"dst","target_folder":""}`)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.JSONEq(t, `{"repo":"dst","path":"team","type":"folder"}`, w.Body.String())
	_, err := f.dst.GetFile("team", "a.md")
	assert.NoError(t, err)
}

func TestMoveAcrossRepos_RequiresContentManagerOnBothSides(t *testing.T) {
	cases := []struct {
		name     string
		src, dst permissions.Level
	}{
		{"contributor on source", permissions.Contributor, permissions.ContentManager},
		{"contributor on target", permissions.ContentManager, permissions.Contributor},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newMoveFixture(t, tc.src, tc.dst)
			w := f.move("docs/page.md", `{"target_repo":"dst","target_folder":"guides"}`)
			assert.Equal(t, http.StatusNotFound, w.Code)
			_, err := f.src.GetFile("docs", "page.md")
			assert.NoError(t, err, "source untouched")
			assert.Empty(t, f.dst.SaveCalls)
		})
	}
}

func TestMoveAcrossRepos_Rejections(t *testing.T) {
	cases := []struct {
		name, path, body string
		code             int
	}{
		{"same repo", "docs/page.md", `{"target_repo":"src"}`, http.StatusBadRequest},
		{"unknown target", "docs/page.md", `{"target_repo":"nope"}`, http.StatusNotFound},
		{"read-only target", "docs/page.md", `{"target_repo":"ro"}`, http.StatusNotFound},
		{"missing source", "docs/missing.md", `{"target_repo":"dst"}`, http.StatusNotFound},
		{"missing target folder", "docs/page.md", `{"target_repo":"dst","target_folder":"nope"}`, http.StatusNotFound},
		{"path traversal", "docs/page.md", `{"target_repo":"dst","target_folder":"../x"}`, http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newMoveFixture(t, permissions.ContentManager, permissions.ContentManager)
			w := f.move(tc.path, tc.body)
			assert.Equal(t, tc.code, w.Code, w.Body.String())
		})
	}
}

func TestMoveAcrossRepos_NameConflict(t *testing.T) {
	f := newMoveFixture(t, permissions.ContentManager, permissions.ContentManager)
	f.dst.Seed("guides/page.md", []byte("existing"))
	w := f.move("docs/page.md", `{"target_repo":"dst","target_folder":"guides"}`)
	assert.Equal(t, http.StatusConflict, w.Code)
	got, _ := f.dst.GetFile("guides", "page.md")
	assert.Equal(t, "existing", string(got))
}

func TestMoveAcrossRepos_PendingOwnDraftBlocks(t *testing.T) {
	f := newMoveFixture(t, permissions.ContentManager, permissions.ContentManager)
	require.NoError(t, f.h.draftManagers["src"].SaveDraft("docs", "page.md", "alice@example.com", "Alice", "", []byte("wip")))
	w := f.move("docs/page.md", `{"target_repo":"dst","target_folder":"guides"}`)
	assert.Equal(t, http.StatusConflict, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), "pending draft")
}
