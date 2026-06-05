package git_repo

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Store struct {
	pool    *pgxpool.Pool
	encrypt func(string) (string, error)
	decrypt func(string) (string, error)
}

func NewStore(pool *pgxpool.Pool, encrypt, decrypt func(string) (string, error)) *Store {
	return &Store{pool: pool, encrypt: encrypt, decrypt: decrypt}
}

func (s *Store) ListRepos(ctx context.Context) ([]GitRepo, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT slug, repo_url, branch, authenticated, username, auth_token,
		       search_provider_id, lfs_enabled, doc_path, name, public_access, read_only,
		       backend_type, s3_bucket, s3_prefix, s3_region,
		       s3_access_key, s3_secret_key, s3_endpoint
		FROM git_repos ORDER BY slug
	`)
	if err != nil {
		return nil, fmt.Errorf("listing repos: %w", err)
	}
	defer rows.Close()

	var repos []GitRepo
	for rows.Next() {
		var slug string
		var r GitRepo
		if err := rows.Scan(&slug, &r.RepoUrl, &r.Branch, &r.Authenticated,
			&r.Username, &r.AuthToken, &r.SearchProviderID, &r.LFSEnabled,
			&r.DocPath, &r.Name, &r.PublicAccess, &r.ReadOnly,
			&r.BackendType, &r.S3Bucket, &r.S3Prefix, &r.S3Region,
			&r.S3AccessKey, &r.S3SecretKey, &r.S3Endpoint); err != nil {
			return nil, err
		}
		if decrypted, err := s.decrypt(r.AuthToken); err == nil {
			r.AuthToken = decrypted
		}
		if decrypted, err := s.decrypt(r.S3AccessKey); err == nil {
			r.S3AccessKey = decrypted
		}
		if decrypted, err := s.decrypt(r.S3SecretKey); err == nil {
			r.S3SecretKey = decrypted
		}
		repos = append(repos, r)
	}
	return repos, rows.Err()
}

func (s *Store) GetRepo(ctx context.Context, slug string) (*GitRepo, error) {
	var r GitRepo
	err := s.pool.QueryRow(ctx, `
		SELECT repo_url, branch, authenticated, username, auth_token,
		       search_provider_id, lfs_enabled, doc_path, name, public_access, read_only,
		       backend_type, s3_bucket, s3_prefix, s3_region,
		       s3_access_key, s3_secret_key, s3_endpoint
		FROM git_repos WHERE slug = $1
	`, slug).Scan(&r.RepoUrl, &r.Branch, &r.Authenticated,
		&r.Username, &r.AuthToken, &r.SearchProviderID, &r.LFSEnabled,
		&r.DocPath, &r.Name, &r.PublicAccess, &r.ReadOnly,
		&r.BackendType, &r.S3Bucket, &r.S3Prefix, &r.S3Region,
		&r.S3AccessKey, &r.S3SecretKey, &r.S3Endpoint)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("getting repo %q: %w", slug, err)
	}
	if decrypted, err := s.decrypt(r.AuthToken); err == nil {
		r.AuthToken = decrypted
	}
	if decrypted, err := s.decrypt(r.S3AccessKey); err == nil {
		r.S3AccessKey = decrypted
	}
	if decrypted, err := s.decrypt(r.S3SecretKey); err == nil {
		r.S3SecretKey = decrypted
	}
	return &r, nil
}

func (s *Store) UpsertRepo(ctx context.Context, r GitRepo) error {
	return s.UpsertRepoWithSlug(ctx, r.Slug(), r)
}

func (s *Store) UpsertRepoWithSlug(ctx context.Context, slug string, r GitRepo) error {
	encToken, err := s.encrypt(r.AuthToken)
	if err != nil {
		return fmt.Errorf("encrypting auth_token for repo: %w", err)
	}
	encS3AccessKey, err := s.encrypt(r.S3AccessKey)
	if err != nil {
		return fmt.Errorf("encrypting s3_access_key for repo: %w", err)
	}
	encS3SecretKey, err := s.encrypt(r.S3SecretKey)
	if err != nil {
		return fmt.Errorf("encrypting s3_secret_key for repo: %w", err)
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO git_repos (slug, repo_url, branch, authenticated, username, auth_token,
		                    search_provider_id, lfs_enabled, doc_path, name, public_access, read_only,
		                    backend_type, s3_bucket, s3_prefix, s3_region,
		                    s3_access_key, s3_secret_key, s3_endpoint)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12,
		        $13, $14, $15, $16, $17, $18, $19)
		ON CONFLICT (slug) DO UPDATE SET
			repo_url = EXCLUDED.repo_url,
			branch = EXCLUDED.branch,
			authenticated = EXCLUDED.authenticated,
			username = EXCLUDED.username,
			auth_token = EXCLUDED.auth_token,
			search_provider_id = EXCLUDED.search_provider_id,
			lfs_enabled = EXCLUDED.lfs_enabled,
			doc_path = EXCLUDED.doc_path,
			name = EXCLUDED.name,
			public_access = EXCLUDED.public_access,
			read_only = EXCLUDED.read_only,
			backend_type = EXCLUDED.backend_type,
			s3_bucket = EXCLUDED.s3_bucket,
			s3_prefix = EXCLUDED.s3_prefix,
			s3_region = EXCLUDED.s3_region,
			s3_access_key = EXCLUDED.s3_access_key,
			s3_secret_key = EXCLUDED.s3_secret_key,
			s3_endpoint = EXCLUDED.s3_endpoint
	`, slug, r.RepoUrl, r.Branch, r.Authenticated, r.Username, encToken,
		r.SearchProviderID, r.LFSEnabled, r.DocPath, r.Name, r.PublicAccess, r.ReadOnly,
		r.BackendType, r.S3Bucket, r.S3Prefix, r.S3Region,
		encS3AccessKey, encS3SecretKey, r.S3Endpoint)
	if err != nil {
		return fmt.Errorf("upserting repo %q: %w", slug, err)
	}
	return nil
}

func (s *Store) DeleteRepo(ctx context.Context, slug string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM git_repos WHERE slug = $1`, slug)
	if err != nil {
		return fmt.Errorf("deleting repo %q: %w", slug, err)
	}
	return nil
}
