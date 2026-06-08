package store

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"strings"

	"github.com/christianfischer/md-wiki-server/internal/store/ai_conversation"
	"github.com/christianfischer/md-wiki-server/internal/store/auth_provider"
	"github.com/christianfischer/md-wiki-server/internal/store/git_repo"
	"github.com/christianfischer/md-wiki-server/internal/store/llm_provider"
	"github.com/christianfischer/md-wiki-server/internal/store/migrations"
	"github.com/christianfischer/md-wiki-server/internal/store/search_provider"
	"github.com/christianfischer/md-wiki-server/internal/store/users"
	"github.com/golang-migrate/migrate/v4"
	pgxdriver "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
)

type Store struct {
	pool                *pgxpool.Pool
	dsn                 string
	encryptionKey       []byte
	legacyEncryptionKey []byte

	Repos         *git_repo.Store
	Users         *users.Store
	Auth          *auth_provider.Store
	LLM           *llm_provider.Store
	Search        *search_provider.Store
	Conversations *ai_conversation.Store
}

// isValidIdentifier reports whether s is a safe Postgres identifier
// (letters, digits, underscores, and hyphens only). Used to guard against
// injection when a database/schema name must be interpolated into DDL.
func isValidIdentifier(s string) bool {
	if s == "" || len(s) > 63 {
		return false
	}
	for _, c := range s {
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_' || c == '-') {
			return false
		}
	}
	return true
}

// EnsureDatabase creates the target database if it does not already exist.
func EnsureDatabase(ctx context.Context, dsn string) error {
	u, err := url.Parse(dsn)
	if err != nil {
		return fmt.Errorf("parsing DSN: %w", err)
	}

	dbName := strings.TrimPrefix(u.Path, "/")
	if dbName == "" {
		return nil
	}
	if !isValidIdentifier(dbName) {
		return fmt.Errorf("invalid database name %q in DSN", dbName)
	}

	adminURL := *u
	adminURL.Path = "/postgres"
	adminDSN := adminURL.String()

	conn, err := pgx.Connect(ctx, adminDSN)
	if err != nil {
		return fmt.Errorf("connecting to postgres database: %w", err)
	}
	defer conn.Close(ctx)

	var exists bool
	err = conn.QueryRow(ctx, "SELECT true FROM pg_database WHERE datname = $1", dbName).Scan(&exists)
	if err != nil && err != pgx.ErrNoRows {
		return fmt.Errorf("checking database existence: %w", err)
	}

	if !exists {
		_, err = conn.Exec(ctx, fmt.Sprintf(`CREATE DATABASE "%s"`, dbName))
		if err != nil {
			return fmt.Errorf("creating database %q: %w", dbName, err)
		}
	}
	conn.Close(ctx)

	// Connect to the target database and create schemas.
	targetConn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return fmt.Errorf("connecting to target database: %w", err)
	}
	defer targetConn.Close(ctx)

	for _, schema := range []string{"admont_ai"} {
		if _, err := targetConn.Exec(ctx, fmt.Sprintf(`CREATE SCHEMA IF NOT EXISTS "%s"`, schema)); err != nil {
			return fmt.Errorf("creating schema %q: %w", schema, err)
		}
	}

	return nil
}

func New(ctx context.Context, dsn string) (*Store, error) {
	// Ensure the DSN sets search_path to our schema.
	dsn = ensureSearchPath(dsn, "admont_ai")

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("connecting to postgres: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("pinging postgres: %w", err)
	}
	s := &Store{pool: pool, dsn: dsn}
	s.Users = users.NewStore(pool)
	s.Conversations = ai_conversation.NewStore(pool)
	return s, nil
}

// InitSubStores initializes the sub-stores that require encryption.
// Must be called after SetEncryptionKey.
func (s *Store) InitSubStores() {
	s.Repos = git_repo.NewStore(s.pool, s.encryptSecret, s.decryptSecret)
	s.Auth = auth_provider.NewStore(s.pool, s.encryptSecret, s.decryptSecret)
	s.LLM = llm_provider.NewStore(s.pool, s.encryptSecret, s.decryptSecret)
	s.Search = search_provider.NewStore(s.pool, s.encryptSecret, s.decryptSecret)
}

// ensureSearchPath appends search_path to the DSN if not already set.
func ensureSearchPath(dsn, schema string) string {
	u, err := url.Parse(dsn)
	if err != nil {
		return dsn
	}
	q := u.Query()
	if q.Get("search_path") == "" {
		q.Set("search_path", schema)
		u.RawQuery = q.Encode()
	}
	return u.String()
}

func (s *Store) Close() {
	s.pool.Close()
}

// Pool exposes the underlying connection pool for sharing with other components.
func (s *Store) Pool() *pgxpool.Pool {
	return s.pool
}

// RunMigrations applies all pending schema migrations using golang-migrate.
func (s *Store) RunMigrations() error {
	src, err := iofs.New(migrations.FS, ".")
	if err != nil {
		return fmt.Errorf("creating migration source: %w", err)
	}

	db, err := sql.Open("pgx", s.dsn)
	if err != nil {
		return fmt.Errorf("opening sql connection for migrations: %w", err)
	}
	defer db.Close()

	driver, err := pgxdriver.WithInstance(db, &pgxdriver.Config{
		SchemaName: "admont_ai",
	})
	if err != nil {
		return fmt.Errorf("creating migration database driver: %w", err)
	}

	m, err := migrate.NewWithInstance("iofs", src, "pgx5", driver)
	if err != nil {
		return fmt.Errorf("creating migrate instance: %w", err)
	}

	if err := m.Up(); err != nil && err != migrate.ErrNoChange {
		return fmt.Errorf("running migrations: %w", err)
	}

	return nil
}
