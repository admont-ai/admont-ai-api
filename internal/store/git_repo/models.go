package git_repo

import (
	"path"
	"strings"
)

type GitRepo struct {
	RepoUrl          string `mapstructure:"repo_url" yaml:"repo_url"`
	Branch           string `mapstructure:"branch" yaml:"branch"`
	Authenticated    bool   `mapstructure:"authenticated" yaml:"authenticated"`
	Username         string `mapstructure:"username" yaml:"username,omitempty"`
	AuthToken        string `mapstructure:"auth_token" yaml:"auth_token,omitempty"`
	SearchProviderID *int   `mapstructure:"search_provider_id" yaml:"search_provider_id"`
	LFSEnabled       bool   `mapstructure:"lfs_enabled" yaml:"lfs_enabled"`
	DocPath          string `mapstructure:"doc_path" yaml:"doc_path"`
	Name             string `mapstructure:"name" yaml:"name"`
	PublicAccess     bool   `mapstructure:"public_access" yaml:"public_access"`
	ReadOnly         bool   `mapstructure:"read_only" yaml:"read_only"`

	// Backend configuration
	BackendType string `mapstructure:"backend_type" yaml:"backend_type"` // "remote_git" (default/empty), "local_git", "s3_git", "s3_store"
	S3Bucket    string `mapstructure:"s3_bucket" yaml:"s3_bucket"`
	S3Prefix    string `mapstructure:"s3_prefix" yaml:"s3_prefix"`
	S3Region    string `mapstructure:"s3_region" yaml:"s3_region"`
	S3AccessKey string `mapstructure:"s3_access_key" yaml:"s3_access_key"` // encrypted in DB, optional (IAM role fallback)
	S3SecretKey string `mapstructure:"s3_secret_key" yaml:"s3_secret_key"` // encrypted in DB, optional (IAM role fallback)
	S3Endpoint  string `mapstructure:"s3_endpoint" yaml:"s3_endpoint"`     // for S3-compatible services (MinIO)
}

// Slug derives the repo slug from the URL, local path, or S3 bucket.
func (r *GitRepo) Slug() string {
	switch r.BackendType {
	case "local_git":
		return ""
	case "s3_git", "s3_store":
		slug := r.S3Bucket
		if r.S3Prefix != "" {
			slug += "-" + strings.Trim(strings.ReplaceAll(r.S3Prefix, "/", "-"), "-")
		}
		return slug
	}
	return strings.TrimSuffix(path.Base(r.RepoUrl), ".git")
}
