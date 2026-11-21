package requesthandler

import (
	"errors"
	"fmt"
	"strings"

	"github.com/go-fuego/fuego"
	log "github.com/sirupsen/logrus"

	"github.com/christianfischer/md-wiki-server/internal/permissions"
	"github.com/christianfischer/md-wiki-server/internal/repoactions"
)

type crossRepoMoveRequest struct {
	TargetRepo   string `json:"target_repo" validate:"required"`
	TargetFolder string `json:"target_folder"`
}

type crossRepoMoveResponse struct {
	Repo string `json:"repo"`
	Path string `json:"path"`
	Type string `json:"type"` // "file" or "folder"
}

// repoDeps builds the repoactions.Deps for one repo/identity pair.
func (h *RepoRequesthandler) repoDeps(repoSlug, identity string) repoactions.Deps {
	rc, ok := h.repoConfigs[repoSlug]
	return repoactions.Deps{
		Backend:         h.backends[repoSlug],
		RepoSlug:        repoSlug,
		Resolver:        h.permResolvers[repoSlug],
		ReadOnly:        ok && rc.ReadOnly,
		PublicAccess:    ok && rc.PublicAccess,
		IsAdmin:         identity != "" && h.isSystemAdmin != nil && h.isSystemAdmin(identity),
		Indexer:         h.indexer,
		ShouldIndex:     h.shouldIndex(repoSlug),
		SavePermissions: func() { h.savePermissions(repoSlug) },
	}
}

// anyDraftError rejects a cross-repo move while any draft (the caller's own
// included) is pending on the item: drafts reference the source repo's
// history and cannot be carried over.
func (h *RepoRequesthandler) anyDraftError(repoName, itemPath string, isFolder bool) error {
	dm, ok := h.draftManagers[repoName]
	if !ok {
		return nil
	}
	// An empty caller email excludes nobody, so these return any draft.
	where := itemPath
	if isFolder {
		m, p, err := dm.OtherUsersDraftUnderFolder(itemPath, "")
		if err != nil || m == nil {
			return nil
		}
		where = p
	} else {
		sub, fn := repoactions.SplitPath(itemPath)
		if m, err := dm.OtherUsersDraft(sub, fn, ""); err != nil || m == nil {
			return nil
		}
	}
	return fuego.ConflictError{Detail: fmt.Sprintf(
		"%s has a pending draft — publish or discard pending drafts before moving it to another repository", where)}
}

// MoveAcrossRepos moves a file or folder into another repository and commits
// both repositories immediately. Requires edit+delete (ContentManager) on the
// source item and on the target folder.
func (h *RepoRequesthandler) MoveAcrossRepos(c fuego.ContextWithBody[crossRepoMoveRequest]) (crossRepoMoveResponse, error) {
	repoName := c.PathParam("repo")
	itemPath := strings.Trim(c.PathParam("path"), "/")
	gc := ginCtxBody(c)

	body, err := c.Body()
	if err != nil {
		return crossRepoMoveResponse{}, err
	}
	targetRepo := strings.TrimSpace(body.TargetRepo)
	targetFolder := strings.Trim(strings.TrimSpace(body.TargetFolder), "/")

	if itemPath == "" {
		return crossRepoMoveResponse{}, fuego.BadRequestError{Detail: "the repository root cannot be moved"}
	}
	if targetRepo == repoName {
		return crossRepoMoveResponse{}, fuego.BadRequestError{Detail: "target repository equals source; use the in-repository move"}
	}
	if strings.Contains("/"+targetFolder+"/", "/../") {
		return crossRepoMoveResponse{}, fuego.BadRequestError{Detail: "invalid target folder"}
	}

	srcBackend, ok := h.backends[repoName]
	if !ok {
		return crossRepoMoveResponse{}, fuego.NotFoundError{Detail: "repository not found"}
	}
	dstBackend, ok := h.backends[targetRepo]
	if !ok {
		return crossRepoMoveResponse{}, fuego.NotFoundError{Detail: "target repository not found"}
	}
	if h.repoReady != nil {
		if v, ok := h.repoReady.Load(targetRepo); ok && v == false {
			return crossRepoMoveResponse{}, fuego.HTTPError{Status: 503, Detail: "target repository is not ready yet"}
		}
	}

	userName, userEmail := getIdentity(gc)

	info, err := srcBackend.Stat(itemPath)
	if err != nil {
		return crossRepoMoveResponse{}, fuego.NotFoundError{Detail: "not found"}
	}
	isFolder := info.IsDir

	// Edit+delete on the source item and on the target folder.
	srcPermPath := itemPath
	if isFolder {
		srcPermPath += "/"
	}
	if err := h.checkPermission(repoName, userEmail, srcPermPath, permissions.ContentManager); err != nil {
		return crossRepoMoveResponse{}, permError(err)
	}
	dstPermPath := "./"
	if targetFolder != "" {
		dstPermPath = targetFolder + "/"
	}
	if err := h.checkPermission(targetRepo, userEmail, dstPermPath, permissions.ContentManager); err != nil {
		return crossRepoMoveResponse{}, permError(err)
	}

	if targetFolder != "" {
		if ti, err := dstBackend.Stat(targetFolder); err != nil || !ti.IsDir {
			return crossRepoMoveResponse{}, fuego.NotFoundError{Detail: "target folder not found"}
		}
	}
	_, name := repoactions.SplitPath(itemPath)
	newPath := name
	if targetFolder != "" {
		newPath = targetFolder + "/" + name
	}
	if _, err := dstBackend.Stat(newPath); err == nil {
		return crossRepoMoveResponse{}, fuego.ConflictError{Detail: fmt.Sprintf("%s already exists in %s", newPath, targetRepo)}
	}
	if err := h.anyDraftError(repoName, itemPath, isFolder); err != nil {
		return crossRepoMoveResponse{}, err
	}

	res, err := repoactions.MoveAcrossRepos(
		h.repoDeps(repoName, userEmail),
		h.repoDeps(targetRepo, userEmail),
		repoactions.CrossRepoMove{
			SourcePath:   itemPath,
			IsFolder:     isFolder,
			TargetFolder: targetFolder,
			AuthorName:   userName,
			AuthorEmail:  userEmail,
		},
	)
	if err != nil {
		logger := log.WithError(err).WithFields(log.Fields{"repo": repoName, "path": itemPath, "target_repo": targetRepo, "target": newPath})
		switch {
		case errors.Is(err, repoactions.ErrSourceCommit):
			logger.Error("cross-repo move: copied to target but removing from source failed")
			return crossRepoMoveResponse{}, fuego.HTTPError{Status: 500, Detail: fmt.Sprintf(
				"%s was moved to %s but could not be removed from %s; it now exists in both repositories", itemPath, targetRepo, repoName)}
		case errors.Is(err, repoactions.ErrTargetCommit):
			logger.Warn("cross-repo move: target commit failed, nothing moved")
			return crossRepoMoveResponse{}, fuego.HTTPError{Status: 500, Detail: "failed to commit to the target repository; nothing was moved"}
		default:
			logger.Warn("cross-repo move failed")
			return crossRepoMoveResponse{}, fuego.HTTPError{Status: 500, Detail: "failed to move"}
		}
	}

	typ := "file"
	if isFolder {
		typ = "folder"
	}
	return crossRepoMoveResponse{Repo: targetRepo, Path: res.NewPath, Type: typ}, nil
}
