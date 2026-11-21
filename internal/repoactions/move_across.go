package repoactions

import (
	"errors"
	"fmt"
	"path"
	"regexp"
	"strings"

	log "github.com/sirupsen/logrus"

	"github.com/christianfischer/md-wiki-server/internal/repo"
)

// ErrTargetCommit is returned by MoveAcrossRepos when the target repo could
// not be committed. The target has been cleaned up and the source is
// untouched, so the move simply did not happen.
var ErrTargetCommit = errors.New("committing target repository failed")

// ErrSourceCommit is returned by MoveAcrossRepos when the item was committed
// to the target but deleting/committing it in the source failed. The item
// then exists in both repositories.
var ErrSourceCommit = errors.New("committing source repository failed")

// CrossRepoMove describes one cross-repo move request. Paths are
// repo-relative without leading or trailing slashes; TargetFolder "" is the
// target repo root.
type CrossRepoMove struct {
	SourcePath   string
	IsFolder     bool
	TargetFolder string

	AuthorName  string
	AuthorEmail string
}

// CrossRepoMoveResult reports what MoveAcrossRepos did.
type CrossRepoMoveResult struct {
	NewPath     string   // the moved item's path in the target repo
	Moved       []string // source files removed from the source repo
	Copied      []string // target files written (moved files + copied shared assets)
	SharedAsset []string // source files referenced by the page and copied (not removed)
}

// skipOnCopy reports whether a path segment must never be carried across
// repos: pending drafts belong to the source repo's history, and the rest is
// VCS/OS noise.
func skipOnCopy(name string) bool {
	switch name {
	case ".drafts", ".git", ".DS_Store":
		return true
	}
	return false
}

// listTree returns every file under folder (repo-relative paths), walking
// with ListFolders/ListFiles, which behave the same on every backend (unlike
// ListEntries, which is one level deep on S3).
func listTree(b repo.RepoBackend, folder string) ([]string, error) {
	var out []string
	files, err := b.ListFiles(folder)
	if err != nil {
		return nil, err
	}
	for _, f := range files {
		if skipOnCopy(f) {
			continue
		}
		out = append(out, path.Join(folder, f))
	}
	dirs, err := b.ListFolders(folder)
	if err != nil {
		return nil, err
	}
	for _, d := range dirs {
		if skipOnCopy(d) {
			continue
		}
		sub, err := listTree(b, path.Join(folder, d))
		if err != nil {
			return nil, err
		}
		out = append(out, sub...)
	}
	return out, nil
}

// assetFolderFor returns the hidden per-page asset folder for a page,
// matching the UI's assetFolderPath convention: "a/x.md" -> "a/.x".
func assetFolderFor(pagePath string) string {
	dir, file := SplitPath(pagePath)
	name := strings.TrimSuffix(file, path.Ext(file))
	return path.Join(dir, "."+name)
}

// relLinkRe matches markdown link/image targets "](target)" and HTML
// src/href attributes.
var relLinkRe = regexp.MustCompile(`\]\(\s*<?([^)\s>]+)>?(?:\s+"[^"]*")?\s*\)|(?:src|href)\s*=\s*["']([^"']+)["']`)

// referencedAssets returns repo-relative paths of non-page files a markdown
// page links to via relative URLs (e.g. "assets/x.png", "../img/y.svg").
// Absolute URLs, anchors, data URIs and links to other markdown pages are
// ignored, as are paths escaping the repo root.
func referencedAssets(pagePath string, content []byte) []string {
	dir, _ := SplitPath(pagePath)
	seen := map[string]bool{}
	var out []string
	for _, m := range relLinkRe.FindAllSubmatch(content, -1) {
		target := string(m[1])
		if target == "" {
			target = string(m[2])
		}
		if i := strings.IndexAny(target, "#?"); i >= 0 {
			target = target[:i]
		}
		if target == "" || strings.HasPrefix(target, "/") || strings.Contains(target, ":") {
			continue
		}
		if strings.EqualFold(path.Ext(target), ".md") {
			continue
		}
		resolved := path.Clean(path.Join(dir, target))
		if resolved == "." || strings.HasPrefix(resolved, "../") || resolved == ".." {
			continue
		}
		if !seen[resolved] {
			seen[resolved] = true
			out = append(out, resolved)
		}
	}
	return out
}

// rebase maps a path under fromDir onto toDir ("" = repo root).
func rebase(p, fromDir, toDir string) string {
	rel := p
	if fromDir != "" {
		rel = strings.TrimPrefix(p, fromDir+"/")
	}
	return path.Join(toDir, rel)
}

// MoveAcrossRepos moves a page or folder from src to dst and commits both
// repos synchronously: target first (so a failure leaves the source intact),
// then the source. Permission and draft checks are the caller's job.
//
// Per-item permission entries are dropped — the moved item inherits the
// target folder's permissions. For a single page, its hidden asset folder
// moves with it and relatively-linked files (e.g. a shared "assets/" folder)
// are copied, but not removed from the source since sibling pages may use
// them.
func MoveAcrossRepos(src, dst Deps, m CrossRepoMove) (CrossRepoMoveResult, error) {
	var res CrossRepoMoveResult
	srcParent, name := SplitPath(m.SourcePath)
	res.NewPath = path.Join(m.TargetFolder, name)

	// copies maps source path -> target path. moved lists source paths that
	// are removed afterwards; shared lists source files only copied.
	copies := map[string]string{}
	var moved, shared []string
	var assetFolder string

	if m.IsFolder {
		files, err := listTree(src.Backend, m.SourcePath)
		if err != nil {
			return res, fmt.Errorf("listing %s: %w", m.SourcePath, err)
		}
		for _, f := range files {
			copies[f] = rebase(f, srcParent, m.TargetFolder)
			moved = append(moved, f)
		}
		if len(files) == 0 {
			// Empty folder: keep it visible in the target.
			copies[""] = path.Join(res.NewPath, ".gitkeep")
		}
	} else {
		copies[m.SourcePath] = res.NewPath
		moved = append(moved, m.SourcePath)

		assetFolder = assetFolderFor(m.SourcePath)
		if info, err := src.Backend.Stat(assetFolder); err == nil && info.IsDir {
			files, err := listTree(src.Backend, assetFolder)
			if err != nil {
				return res, fmt.Errorf("listing %s: %w", assetFolder, err)
			}
			for _, f := range files {
				copies[f] = rebase(f, srcParent, m.TargetFolder)
				moved = append(moved, f)
			}
		} else {
			assetFolder = ""
		}

		if strings.EqualFold(path.Ext(name), ".md") {
			content, err := src.Backend.GetFile(srcParent, name)
			if err != nil {
				return res, fmt.Errorf("reading %s: %w", m.SourcePath, err)
			}
			for _, a := range referencedAssets(m.SourcePath, content) {
				if _, dup := copies[a]; dup {
					continue
				}
				if assetFolder != "" && strings.HasPrefix(a, assetFolder+"/") {
					continue
				}
				if srcParent != "" && !strings.HasPrefix(a, srcParent+"/") {
					continue // points outside the page's folder; no stable target location
				}
				aDir, aName := SplitPath(a)
				if _, err := src.Backend.GetFile(aDir, aName); err != nil {
					continue // dangling link
				}
				t := rebase(a, srcParent, m.TargetFolder)
				tDir, tName := SplitPath(t)
				if _, err := dst.Backend.GetFile(tDir, tName); err == nil {
					continue // already present in target; don't overwrite
				}
				copies[a] = t
				shared = append(shared, a)
			}
		}
	}

	// 1. Write everything into the target.
	var written []string
	rollback := func() {
		for _, t := range written {
			d, f := SplitPath(t)
			if err := dst.Backend.DeleteFile(d, f); err != nil {
				log.WithError(err).WithField("repo", dst.RepoSlug).WithField("path", t).Warn("cross-repo move: rollback delete failed")
			}
		}
	}
	for s, t := range copies {
		var data []byte
		if s != "" {
			sDir, sName := SplitPath(s)
			b, err := src.Backend.GetFile(sDir, sName)
			if err != nil {
				rollback()
				return res, fmt.Errorf("reading %s: %w", s, err)
			}
			data = b
		}
		tDir, tName := SplitPath(t)
		if err := dst.Backend.AddFile(tDir, tName, data); err != nil {
			rollback()
			return res, fmt.Errorf("writing %s: %w", t, err)
		}
		written = append(written, t)
	}

	// 2. Commit the target.
	if err := dst.Backend.SaveChanges(
		fmt.Sprintf("move %s from %s to %s", m.SourcePath, src.RepoSlug, res.NewPath),
		m.AuthorName, m.AuthorEmail); err != nil {
		rollback()
		return res, fmt.Errorf("%w: %v", ErrTargetCommit, err)
	}
	res.Copied = written
	res.SharedAsset = shared
	res.Moved = moved

	// 3. Remove from the source and commit it.
	var delErr error
	if m.IsFolder {
		delErr = src.Backend.DeleteFolder(m.SourcePath)
	} else {
		delErr = src.Backend.DeleteFile(srcParent, name)
		if delErr == nil && assetFolder != "" {
			delErr = src.Backend.DeleteFolder(assetFolder)
		}
	}
	if delErr != nil {
		return res, fmt.Errorf("%w: deleting %s: %v", ErrSourceCommit, m.SourcePath, delErr)
	}
	if src.Resolver != nil {
		if m.IsFolder {
			src.Resolver.RemoveEntriesUnder(m.SourcePath)
		} else {
			src.Resolver.RemoveEntry(m.SourcePath)
		}
		if src.SavePermissions != nil {
			src.SavePermissions()
		}
	}
	if err := src.Backend.SaveChanges(
		fmt.Sprintf("move %s to %s:%s", m.SourcePath, dst.RepoSlug, res.NewPath),
		m.AuthorName, m.AuthorEmail); err != nil {
		return res, fmt.Errorf("%w: %v", ErrSourceCommit, err)
	}

	// 4. Search index: explicit per-file calls (the folder variants list
	// files asynchronously and would race with the delete above).
	if src.ShouldIndex && src.Indexer != nil {
		for _, p := range moved {
			if repo.IndexableFile(p) {
				src.Indexer.DeleteFileIndex(src.RepoSlug, p)
			}
		}
	}
	if dst.ShouldIndex && dst.Indexer != nil {
		for _, p := range written {
			if repo.IndexableFile(p) {
				dst.Indexer.IndexFile(dst.RepoSlug, p)
			}
		}
	}
	return res, nil
}
