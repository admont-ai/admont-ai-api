package git_repo

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestGitRepo_Slug(t *testing.T) {
	tests := []struct {
		name string
		repo GitRepo
		want string
	}{
		{
			"remote git with .git suffix",
			GitRepo{BackendType: "remote_git", RepoUrl: "https://github.com/org/wiki.git"},
			"wiki",
		},
		{
			"remote git without .git suffix",
			GitRepo{BackendType: "remote_git", RepoUrl: "https://github.com/org/my-wiki"},
			"my-wiki",
		},
		{
			"default backend type (empty = remote_git)",
			GitRepo{BackendType: "", RepoUrl: "https://github.com/org/repo.git"},
			"repo",
		},
		{
			"local git returns empty",
			GitRepo{BackendType: "local_git"},
			"",
		},
		{
			"s3_git with bucket only",
			GitRepo{BackendType: "s3_git", S3Bucket: "my-wiki-bucket"},
			"my-wiki-bucket",
		},
		{
			"s3_git with bucket and prefix",
			GitRepo{BackendType: "s3_git", S3Bucket: "bucket", S3Prefix: "docs/"},
			"bucket-docs",
		},
		{
			"s3_store with bucket and prefix",
			GitRepo{BackendType: "s3_store", S3Bucket: "bucket", S3Prefix: "path/to/docs/"},
			"bucket-path-to-docs",
		},
		{
			"s3 with trailing slash prefix",
			GitRepo{BackendType: "s3_git", S3Bucket: "b", S3Prefix: "prefix/"},
			"b-prefix",
		},
		{
			"s3 with no prefix",
			GitRepo{BackendType: "s3_store", S3Bucket: "standalone"},
			"standalone",
		},
		{
			"complex repo URL",
			GitRepo{BackendType: "remote_git", RepoUrl: "https://gitlab.company.com/group/subgroup/project.git"},
			"project",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.repo.Slug())
		})
	}
}
