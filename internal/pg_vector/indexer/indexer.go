package indexer

import (
	"context"
	"path/filepath"
	"strings"
	"sync"

	"github.com/christianfischer/md-wiki-server/internal/pg_vector/backend"
	"github.com/christianfischer/md-wiki-server/internal/pg_vector/chunker"
	"github.com/christianfischer/md-wiki-server/internal/repo"
	log "github.com/sirupsen/logrus"
)

// Search index status constants.
const (
	StatusReady      = "ready"
	StatusRebuilding = "rebuilding"
	StatusUpdating   = "updating"
	StatusError      = "error"
)

// Indexer orchestrates chunking and storage of markdown documents via a pluggable SearchBackend.
type Indexer struct {
	backend   *backend.Holder
	repoState backend.RepoStateStore
	backends  map[string]repo.RepoBackend
	docPaths  map[string]string // repo slug -> doc_path prefix
	statusMap sync.Map          // repo slug -> string (StatusReady, StatusRebuilding, StatusError)
}

// New creates a new Indexer.
func New(b *backend.Holder, rs backend.RepoStateStore, backends map[string]repo.RepoBackend, docPaths map[string]string) *Indexer {
	return &Indexer{
		backend:   b,
		repoState: rs,
		backends:  backends,
		docPaths:  docPaths,
	}
}

// Status returns the current index status for a repo.
func (idx *Indexer) Status(repoSlug string) string {
	if v, ok := idx.statusMap.Load(repoSlug); ok {
		return v.(string)
	}
	return StatusReady
}

// IndexFile reads, chunks, and upserts a single file via the active backend. Runs asynchronously.
func (idx *Indexer) IndexFile(repoSlug, filePath string) {
	go func() {
		if err := idx.indexFile(context.Background(), repoSlug, filePath); err != nil {
			log.WithError(err).WithFields(log.Fields{
				"repo": repoSlug, "file": filePath,
			}).Error("failed to index file")
		}
	}()
}

func (idx *Indexer) indexFile(ctx context.Context, repoSlug, filePath string) error {
	rb, ok := idx.backends[repoSlug]
	if !ok {
		return nil
	}

	// Only index .md files
	if filepath.Ext(filePath) != ".md" {
		return nil
	}

	// Only index files under doc_path if configured
	if dp := idx.docPaths[repoSlug]; dp != "" {
		if !strings.HasPrefix(filePath, dp+"/") && filePath != dp {
			return nil
		}
	}

	b := idx.backend.Get()
	if b == nil {
		return nil
	}

	subfolder := filepath.Dir(filePath)
	if subfolder == "." {
		subfolder = ""
	}
	filename := filepath.Base(filePath)

	content, err := rb.GetFile(subfolder, filename)
	if err != nil {
		return err
	}

	chunks := chunker.ChunkMarkdown(content)
	if len(chunks) == 0 {
		return b.DeleteFileChunks(ctx, repoSlug, filePath)
	}

	// Build backend-agnostic chunks (text only — backend handles embedding)
	backendChunks := make([]backend.Chunk, len(chunks))
	for i, c := range chunks {
		backendChunks[i] = backend.Chunk{
			RepoSlug:    repoSlug,
			FilePath:    filePath,
			ChunkIndex:  c.Index,
			HeadingPath: c.HeadingPath,
			Content:     c.Content,
		}
	}

	return b.UpsertChunks(ctx, backendChunks)
}

// DeleteFileIndex removes all chunks for a file. Runs asynchronously.
func (idx *Indexer) DeleteFileIndex(repoSlug, filePath string) {
	go func() {
		b := idx.backend.Get()
		if b == nil {
			return
		}
		if err := b.DeleteFileChunks(context.Background(), repoSlug, filePath); err != nil {
			log.WithError(err).WithFields(log.Fields{
				"repo": repoSlug, "file": filePath,
			}).Error("failed to delete file index")
		}
	}()
}

// DeleteFolderIndex removes all chunks for files under a folder prefix. Runs asynchronously.
func (idx *Indexer) DeleteFolderIndex(repoSlug, folderPath string) {
	go func() {
		b := idx.backend.Get()
		if b == nil {
			return
		}
		rb, ok := idx.backends[repoSlug]
		if !ok {
			return
		}
		files, err := rb.ListAllMdFiles(folderPath)
		if err != nil {
			log.WithError(err).WithFields(log.Fields{
				"repo": repoSlug, "folder": folderPath,
			}).Error("failed to list md files for folder index delete")
			return
		}
		ctx := context.Background()
		for _, f := range files {
			if err := b.DeleteFileChunks(ctx, repoSlug, f); err != nil {
				log.WithError(err).WithFields(log.Fields{
					"repo": repoSlug, "file": f,
				}).Warn("failed to delete file chunks during folder index delete")
			}
		}
	}()
}

// ReindexFolder re-indexes all .md files under a folder prefix. Runs asynchronously.
func (idx *Indexer) ReindexFolder(repoSlug, folderPath string) {
	go func() {
		rb, ok := idx.backends[repoSlug]
		if !ok {
			return
		}
		files, err := rb.ListAllMdFiles(folderPath)
		if err != nil {
			log.WithError(err).WithFields(log.Fields{
				"repo": repoSlug, "folder": folderPath,
			}).Error("failed to list md files for folder reindex")
			return
		}
		ctx := context.Background()
		for _, f := range files {
			if err := idx.indexFile(ctx, repoSlug, f); err != nil {
				log.WithError(err).WithFields(log.Fields{
					"repo": repoSlug, "file": f,
				}).Warn("failed to index file during folder reindex")
			}
		}
	}()
}

// DeleteRepoIndex removes all chunks and state for a repo. Runs asynchronously.
func (idx *Indexer) DeleteRepoIndex(repoSlug string) {
	idx.statusMap.Delete(repoSlug)
	go func() {
		ctx := context.Background()
		b := idx.backend.Get()
		if b != nil {
			if err := b.DeleteRepoChunks(ctx, repoSlug); err != nil {
				log.WithError(err).WithField("repo", repoSlug).Error("failed to delete repo index")
			}
		}
		_ = idx.repoState.DeleteSearchRepoState(ctx, repoSlug)
		log.WithField("repo", repoSlug).Info("repo search index deleted")
	}()
}

// FullReindex deletes all chunks for a repo and re-indexes every .md file. Runs asynchronously.
func (idx *Indexer) FullReindex(repoSlug string) {
	idx.statusMap.Store(repoSlug, StatusRebuilding)
	go func() {
		if err := idx.fullReindex(context.Background(), repoSlug); err != nil {
			log.WithError(err).WithField("repo", repoSlug).Error("failed to full reindex")
			idx.statusMap.Store(repoSlug, StatusError)
			return
		}
		idx.statusMap.Store(repoSlug, StatusReady)
	}()
}

func (idx *Indexer) fullReindex(ctx context.Context, repoSlug string) error {
	rb, ok := idx.backends[repoSlug]
	if !ok {
		return nil
	}

	b := idx.backend.Get()
	if b == nil {
		return nil
	}

	log.WithField("repo", repoSlug).Info("starting full reindex")

	if err := b.DeleteRepoChunks(ctx, repoSlug); err != nil {
		return err
	}

	files, err := rb.ListAllMdFiles(idx.docPaths[repoSlug])
	if err != nil {
		return err
	}

	for _, f := range files {
		if err := idx.indexFile(ctx, repoSlug, f); err != nil {
			log.WithError(err).WithFields(log.Fields{
				"repo": repoSlug, "file": f,
			}).Warn("failed to index file during full reindex")
		}
	}

	// Update state
	headSHA, err := rb.HeadHash()
	if err == repo.ErrNotSupported {
		log.WithField("repo", repoSlug).Info("full reindex completed (no history support)")
		return nil
	}
	if err != nil {
		return err
	}
	if err := idx.repoState.UpdateSearchRepoState(ctx, repoSlug, headSHA); err != nil {
		return err
	}

	log.WithFields(log.Fields{
		"repo": repoSlug, "files": len(files), "sha": headSHA[:8],
	}).Info("full reindex completed")

	return nil
}

// IncrementalReindex uses git diff to only process changed files. Runs asynchronously.
func (idx *Indexer) IncrementalReindex(repoSlug string) {
	idx.statusMap.Store(repoSlug, StatusUpdating)
	go func() {
		if err := idx.incrementalReindex(context.Background(), repoSlug); err != nil {
			log.WithError(err).WithField("repo", repoSlug).Error("failed to incremental reindex")
			idx.statusMap.Store(repoSlug, StatusError)
			return
		}
		idx.statusMap.Store(repoSlug, StatusReady)
	}()
}

func (idx *Indexer) incrementalReindex(ctx context.Context, repoSlug string) error {
	rb, ok := idx.backends[repoSlug]
	if !ok {
		return nil
	}

	if !rb.SupportsHistory() {
		return idx.fullReindex(ctx, repoSlug)
	}

	b := idx.backend.Get()
	if b == nil {
		return nil
	}

	headSHA, err := rb.HeadHash()
	if err != nil {
		return err
	}

	state, err := idx.repoState.GetSearchRepoState(ctx, repoSlug)
	if err != nil {
		return err
	}

	// If no previous state, do a full reindex
	if state == nil || state.LastIndexedSHA == "" {
		return idx.fullReindex(ctx, repoSlug)
	}

	// If already up to date, nothing to do
	if state.LastIndexedSHA == headSHA {
		log.WithField("repo", repoSlug).Debug("index already up to date")
		return nil
	}

	log.WithFields(log.Fields{
		"repo": repoSlug,
		"from": state.LastIndexedSHA[:minInt(8, len(state.LastIndexedSHA))],
		"to":   headSHA[:8],
	}).Info("starting incremental reindex")

	changed, deleted, err := rb.DiffChangedFiles(state.LastIndexedSHA, headSHA)
	if err != nil {
		// If diff fails (e.g., force-pushed), fall back to full reindex
		log.WithError(err).WithField("repo", repoSlug).Warn("diff failed, falling back to full reindex")
		return idx.fullReindex(ctx, repoSlug)
	}

	// Process deletions
	for _, f := range deleted {
		if strings.HasSuffix(f, ".md") {
			if err := b.DeleteFileChunks(ctx, repoSlug, f); err != nil {
				log.WithError(err).WithFields(log.Fields{
					"repo": repoSlug, "file": f,
				}).Warn("failed to delete file chunks during incremental reindex")
			}
		}
	}

	// Process additions/modifications
	for _, f := range changed {
		if strings.HasSuffix(f, ".md") {
			if err := idx.indexFile(ctx, repoSlug, f); err != nil {
				log.WithError(err).WithFields(log.Fields{
					"repo": repoSlug, "file": f,
				}).Warn("failed to index file during incremental reindex")
			}
		}
	}

	// Update state
	if err := idx.repoState.UpdateSearchRepoState(ctx, repoSlug, headSHA); err != nil {
		return err
	}

	log.WithFields(log.Fields{
		"repo":    repoSlug,
		"changed": len(changed),
		"deleted": len(deleted),
		"sha":     headSHA[:8],
	}).Info("incremental reindex completed")

	return nil
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
