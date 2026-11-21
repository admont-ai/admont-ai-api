package repoactions

import (
	"errors"
	"sort"
	"testing"

	"github.com/christianfischer/md-wiki-server/internal/permissions"
	"github.com/christianfischer/md-wiki-server/internal/repo/repotest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func exists(b *repotest.FakeBackend, p string) bool {
	d, f := SplitPath(p)
	_, err := b.GetFile(d, f)
	return err == nil
}

func TestMoveAcrossRepos_PageWithAssets(t *testing.T) {
	src := repotest.NewFakeBackend().
		Seed("docs/page.md", []byte("# Page\n![img](assets/a.png) ![ext](https://x/y.png) [other](other.md) <img src=\"./assets/b.svg\">")).
		Seed("docs/assets/a.png", []byte("A")).
		Seed("docs/assets/b.svg", []byte("B")).
		Seed("docs/.page/diagram.drawio", []byte("D")).
		Seed("docs/other.md", []byte("other"))
	dst := repotest.NewFakeBackend().Seed("guides/.gitkeep", nil)

	res, err := MoveAcrossRepos(
		Deps{Backend: src, RepoSlug: "src"},
		Deps{Backend: dst, RepoSlug: "dst"},
		CrossRepoMove{SourcePath: "docs/page.md", TargetFolder: "guides", AuthorName: "Al", AuthorEmail: "al@x"},
	)
	require.NoError(t, err)
	assert.Equal(t, "guides/page.md", res.NewPath)

	// Page and its private asset folder moved.
	assert.True(t, exists(dst, "guides/page.md"))
	assert.True(t, exists(dst, "guides/.page/diagram.drawio"))
	assert.False(t, exists(src, "docs/page.md"))
	assert.False(t, exists(src, "docs/.page/diagram.drawio"))

	// Shared assets copied, not removed; linked pages untouched.
	assert.True(t, exists(dst, "guides/assets/a.png"))
	assert.True(t, exists(dst, "guides/assets/b.svg"))
	assert.True(t, exists(src, "docs/assets/a.png"))
	assert.False(t, exists(dst, "guides/other.md"))
	assert.True(t, exists(src, "docs/other.md"))

	// Both repos committed synchronously, with the author.
	require.Len(t, dst.SaveCalls, 1)
	require.Len(t, src.SaveCalls, 1)
	assert.Equal(t, "al@x", dst.SaveCalls[0].AuthorEmail)
	assert.Contains(t, src.SaveCalls[0].Message, "dst:guides/page.md")
}

func TestMoveAcrossRepos_FolderRecursiveSkipsDrafts(t *testing.T) {
	src := repotest.NewFakeBackend().
		Seed("team/handbook/intro.md", []byte("i")).
		Seed("team/handbook/.order", []byte("intro.md\n")).
		Seed("team/handbook/sub/deep.md", []byte("d")).
		Seed("team/handbook/.drafts/.intro.md.draft.al@x", []byte("draft")).
		Seed("team/keep.md", []byte("k"))
	dst := repotest.NewFakeBackend()

	res, err := MoveAcrossRepos(
		Deps{Backend: src, RepoSlug: "src"},
		Deps{Backend: dst, RepoSlug: "dst"},
		CrossRepoMove{SourcePath: "team/handbook", IsFolder: true, TargetFolder: ""},
	)
	require.NoError(t, err)
	assert.Equal(t, "handbook", res.NewPath)

	assert.True(t, exists(dst, "handbook/intro.md"))
	assert.True(t, exists(dst, "handbook/.order"))
	assert.True(t, exists(dst, "handbook/sub/deep.md"))
	assert.False(t, exists(dst, "handbook/.drafts/.intro.md.draft.al@x"))

	assert.False(t, exists(src, "team/handbook/intro.md"))
	assert.True(t, exists(src, "team/keep.md"))

	sort.Strings(res.Moved)
	assert.Equal(t, []string{"team/handbook/.order", "team/handbook/intro.md", "team/handbook/sub/deep.md"}, res.Moved)
}

func TestMoveAcrossRepos_DropsSourcePermissionEntries(t *testing.T) {
	pf := permissions.PermissionsFile{
		Root: &permissions.PathEntry{Default: permissions.Viewer},
		Paths: map[string]permissions.PathEntry{
			"secret/":        {Default: permissions.None},
			"secret/plan.md": {Default: permissions.None},
		},
	}
	resolver := permissions.NewResolver(pf)
	saved := 0
	src := repotest.NewFakeBackend().Seed("secret/plan.md", []byte("p"))
	dst := repotest.NewFakeBackend()

	_, err := MoveAcrossRepos(
		Deps{Backend: src, RepoSlug: "src", Resolver: resolver, SavePermissions: func() { saved++ }},
		Deps{Backend: dst, RepoSlug: "dst"},
		CrossRepoMove{SourcePath: "secret", IsFolder: true},
	)
	require.NoError(t, err)
	assert.Equal(t, 1, saved)
	_, ok := resolver.GetEntry("secret/plan.md")
	assert.False(t, ok)
}

// failingBackend fails SaveChanges to simulate a commit/push error.
type failingBackend struct{ *repotest.FakeBackend }

func (f failingBackend) SaveChanges(string, string, string) error { return errors.New("push rejected") }

func TestMoveAcrossRepos_TargetCommitFailureLeavesSourceIntact(t *testing.T) {
	src := repotest.NewFakeBackend().
		Seed("a/page.md", []byte("x")).
		Seed("a/.page/img.png", []byte("i"))
	dstFake := repotest.NewFakeBackend()

	_, err := MoveAcrossRepos(
		Deps{Backend: src, RepoSlug: "src"},
		Deps{Backend: failingBackend{dstFake}, RepoSlug: "dst"},
		CrossRepoMove{SourcePath: "a/page.md", TargetFolder: "b"},
	)
	require.ErrorIs(t, err, ErrTargetCommit)

	assert.True(t, exists(src, "a/page.md"))
	assert.True(t, exists(src, "a/.page/img.png"))
	assert.False(t, exists(dstFake, "b/page.md"), "target writes are rolled back")
	assert.False(t, exists(dstFake, "b/.page/img.png"))
	assert.Empty(t, src.SaveCalls)
}

func TestMoveAcrossRepos_DoesNotOverwriteExistingSharedAsset(t *testing.T) {
	src := repotest.NewFakeBackend().
		Seed("page.md", []byte("![i](assets/logo.png)")).
		Seed("assets/logo.png", []byte("new"))
	dst := repotest.NewFakeBackend().Seed("assets/logo.png", []byte("existing"))

	_, err := MoveAcrossRepos(
		Deps{Backend: src, RepoSlug: "src"},
		Deps{Backend: dst, RepoSlug: "dst"},
		CrossRepoMove{SourcePath: "page.md"},
	)
	require.NoError(t, err)
	got, _ := dst.GetFile("assets", "logo.png")
	assert.Equal(t, "existing", string(got))
}

func TestReferencedAssets(t *testing.T) {
	md := []byte(`![a](assets/a.png "t") ![b](<./b c.png>) [up](../x.pdf) [abs](/repos/r/file/z.png)
[anchor](#h) [page](sib.md) ![data](data:image/png;base64,xx) <img src='img/q.jpg'> [qs](f.pdf?x=1#y)`)
	got := referencedAssets("docs/page.md", md)
	assert.ElementsMatch(t, []string{"docs/assets/a.png", "x.pdf", "docs/img/q.jpg", "docs/f.pdf"}, got)
}

func TestAssetFolderFor(t *testing.T) {
	assert.Equal(t, "a/.x", assetFolderFor("a/x.md"))
	assert.Equal(t, ".x", assetFolderFor("x.md"))
}
