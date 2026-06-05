package repofactory

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/christianfischer/md-wiki-server/internal/git"
	"github.com/christianfischer/md-wiki-server/internal/repo"
	"github.com/christianfischer/md-wiki-server/internal/repo/localgit"
	"github.com/christianfischer/md-wiki-server/internal/repo/remotegit"
	"github.com/christianfischer/md-wiki-server/internal/repo/s3backend"
	"github.com/christianfischer/md-wiki-server/internal/repo/s3git"
	"github.com/christianfischer/md-wiki-server/internal/store/git_repo"
)

// NewBackend creates a RepoBackend based on the config's BackendType.
func NewBackend(cfg *git_repo.GitRepo, basePath string) (repo.RepoBackend, error) {
	switch cfg.BackendType {
	case "", "remote_git":
		helper := git.NewHelper(basePath, cfg.RepoUrl, cfg.Branch, cfg.Username, cfg.AuthToken, cfg.LFSEnabled)
		return remotegit.New(helper), nil

	case "local_git":
		return localgit.New(basePath, cfg.Branch), nil

	case "s3_git":
		if cfg.S3Bucket == "" {
			return nil, fmt.Errorf("s3_git backend requires s3_bucket")
		}
		return s3git.New(cfg.S3Bucket, cfg.S3Prefix, cfg.S3Region,
			cfg.S3AccessKey, cfg.S3SecretKey, cfg.S3Endpoint, basePath, cfg.Branch)

	case "s3_store":
		if cfg.S3Bucket == "" {
			return nil, fmt.Errorf("s3_store backend requires s3_bucket")
		}
		// Build a cache directory name from bucket+prefix
		cacheName := cfg.S3Bucket
		if cfg.S3Prefix != "" {
			cacheName += "-" + strings.Trim(cfg.S3Prefix, "/")
		}
		cacheDir := filepath.Join(basePath, cacheName)
		return s3backend.New(cfg.S3Bucket, cfg.S3Prefix, cfg.S3Region,
			cfg.S3AccessKey, cfg.S3SecretKey, cfg.S3Endpoint, cacheDir)

	default:
		return nil, fmt.Errorf("unknown backend type: %q", cfg.BackendType)
	}
}
