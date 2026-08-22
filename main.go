package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image/png"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/christianfischer/md-wiki-server/config"
	"github.com/christianfischer/md-wiki-server/internal/auth"
	"github.com/christianfischer/md-wiki-server/internal/checker"
	"github.com/christianfischer/md-wiki-server/internal/draft"
	"github.com/christianfischer/md-wiki-server/internal/llm"
	wikimcp "github.com/christianfischer/md-wiki-server/internal/mcp"
	"github.com/christianfischer/md-wiki-server/internal/middleware"
	"github.com/christianfischer/md-wiki-server/internal/permissions"
	"github.com/christianfischer/md-wiki-server/internal/pg_vector/backend"
	pgvectorbackend "github.com/christianfischer/md-wiki-server/internal/pg_vector/backend/pgvector"
	"github.com/christianfischer/md-wiki-server/internal/pg_vector/embedder"
	"github.com/christianfischer/md-wiki-server/internal/pg_vector/indexer"
	"github.com/christianfischer/md-wiki-server/internal/repo"
	"github.com/christianfischer/md-wiki-server/internal/repo/repofactory"
	"github.com/christianfischer/md-wiki-server/internal/repo/s3backend"
	requesthandler "github.com/christianfischer/md-wiki-server/internal/request_handler"
	"github.com/christianfischer/md-wiki-server/internal/store"
	storeauth "github.com/christianfischer/md-wiki-server/internal/store/auth_provider"
	"github.com/christianfischer/md-wiki-server/internal/store/git_repo"
	llm_provider "github.com/christianfischer/md-wiki-server/internal/store/llm_provider"
	storesearch "github.com/christianfischer/md-wiki-server/internal/store/search_provider"
	"github.com/christianfischer/md-wiki-server/internal/usage"
	"github.com/pquerna/otp/totp"
	"golang.org/x/crypto/bcrypt"

	"github.com/gin-contrib/cors"
	"github.com/gin-contrib/gzip"
	"github.com/gin-gonic/gin"
	"github.com/go-fuego/fuego"
	"github.com/go-fuego/fuego/extra/fuegogin"
	log "github.com/sirupsen/logrus"
)

type healthResponse struct {
	Status string `json:"status"`
}

type meDetailsResponse struct {
	Email           string   `json:"email"`
	Name            string   `json:"name"`
	Provider        string   `json:"provider"`
	Roles           []string `json:"roles"`
	PasswordExpired bool     `json:"password_expired,omitempty"`
}

type messageResponse struct {
	Message string `json:"message"`
}

type mcpClientResponse struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	ConnectedAt string `json:"connected_at"`
}

var silentPaths = map[string]bool{
	"/me/mcp-clients": true,
}

func ginLogrus() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		path := c.Request.URL.Path
		if c.Request.URL.RawQuery != "" {
			path = path + "?" + c.Request.URL.RawQuery
		}

		c.Next()

		if silentPaths[c.Request.URL.Path] && c.Writer.Status() < 400 {
			return
		}

		entry := log.WithFields(log.Fields{
			"status":  c.Writer.Status(),
			"method":  c.Request.Method,
			"path":    path,
			"latency": time.Since(start).Round(time.Millisecond),
			"ip":      c.ClientIP(),
		})

		if len(c.Errors) > 0 {
			entry.Error(c.Errors.ByType(gin.ErrorTypePrivate).String())
		} else if c.Writer.Status() >= 500 {
			entry.Error("server error")
		} else if c.Writer.Status() >= 400 {
			entry.Warn("client error")
		} else if c.Request.Method == http.MethodGet {
			// Read-only GETs (document fetches, etc.) are noisy — keep at debug.
			entry.Debug("request")
		} else {
			entry.Info("request")
		}
	}
}

// loadPermissions reads the permissions file through the backend abstraction.
func loadPermissions(backend repo.RepoBackend) (*permissions.Resolver, error) {
	data, err := backend.GetFile("", permissions.PermissionsFileName)
	if err != nil {
		return nil, nil // file doesn't exist
	}
	return permissions.LoadFromData(data)
}

// savePermissionsToBackend writes the permissions file through the backend abstraction.
func savePermissionsToBackend(backend repo.RepoBackend, r *permissions.Resolver) error {
	data, err := permissions.Marshal(r)
	if err != nil {
		return err
	}
	return backend.AddFile("", permissions.PermissionsFileName, data)
}

func main() {
	log.SetFormatter(&log.TextFormatter{
		FullTimestamp:   true,
		TimestampFormat: "2006-01-02 15:04:05",
	})

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("failed to load config: %v", err)
	}

	if lvl, err := log.ParseLevel(cfg.LogLevel); err == nil {
		log.SetLevel(lvl)
	} else if cfg.LogLevel != "" {
		log.WithField("log_level", cfg.LogLevel).Warn("invalid log_level; using info")
	}

	// Log the effective configuration (defaults + config file + env overrides),
	// with secret values redacted.
	log.WithField("config", fmt.Sprintf("%+v", cfg.Redacted())).Info("loaded configuration")

	if len(cfg.AllowedOrigins) == 0 {
		log.Fatal("allowed_origins must be set in config")
	}
	if cfg.RepoClonePath == "" {
		log.Fatal("repo_clone_path must be set in config")
	}
	if !filepath.IsAbs(cfg.RepoClonePath) {
		log.Fatalf("repo_clone_path must be an absolute path, got: %s", cfg.RepoClonePath)
	}
	if cwd, err := os.Getwd(); err == nil {
		absClone, _ := filepath.Abs(cfg.RepoClonePath)
		if absClone == cwd || strings.HasPrefix(cwd, absClone+string(os.PathSeparator)) || strings.HasPrefix(absClone, cwd+string(os.PathSeparator)) {
			log.Fatalf("git_repo.clone_path (%s) must not overlap with working directory (%s)", absClone, cwd)
		}
	}
	if cfg.Database.DB == "" {
		log.Fatal("database.db must be set in config")
	}

	// --- Database setup ---
	ctx := context.Background()
	dsn := cfg.Database.DSN()

	if err := store.EnsureDatabase(ctx, dsn); err != nil {
		log.WithError(err).Fatal("failed to ensure database exists")
	}

	db, err := store.New(ctx, dsn)
	if err != nil {
		log.WithError(err).Fatal("failed to connect to database")
	}
	defer db.Close()

	if err := db.RunMigrations(); err != nil {
		log.WithError(err).Fatal("failed to run database migrations")
	}
	log.Info("database migrations applied")

	// --- JWT secret ---
	jwtSecret := cfg.JWTSecret
	if jwtSecret == "" {
		b := make([]byte, 32)
		if _, err := rand.Read(b); err != nil {
			log.Fatalf("failed to generate jwt_secret: %v", err)
		}
		jwtSecret = hex.EncodeToString(b)
		log.Warn("jwt_secret not set in config — using auto-generated ephemeral secret (tokens will not survive restarts)")
	}

	// Set encryption key: env var > config file > jwt_secret-derived fallback.
	encKeyHex := os.Getenv("ADMONT_ENCRYPTION_KEY")
	if encKeyHex == "" {
		encKeyHex = cfg.EncryptionKey
	}
	if encKeyHex != "" {
		encKey, err := hex.DecodeString(encKeyHex)
		if err != nil || len(encKey) != 32 {
			log.Fatal("encryption_key must be a hex-encoded 32-byte (64 char) key")
		}
		db.SetEncryptionKeyRaw(encKey)
		log.Info("encryption key loaded from configuration")
	} else {
		db.SetEncryptionKey(jwtSecret)
		if os.Getenv("GIN_MODE") == "release" {
			log.Error("SECURITY: ADMONT_ENCRYPTION_KEY not set in release mode — encryption key derived from jwt_secret; set a dedicated encryption key for production")
		} else {
			log.Warn("ADMONT_ENCRYPTION_KEY not set — encryption key derived from jwt_secret; set a dedicated key for production")
		}
	}
	db.InitSubStores()

	// Derive signing key for TOTP pending tokens.
	signingKeyHash := sha256.Sum256([]byte(jwtSecret))
	signingKey := signingKeyHash[:]

	baseURL := cfg.AuthBaseURL
	if baseURL == "" {
		// Derive from service config, replacing 0.0.0.0 with localhost for valid callbacks.
		host := cfg.Hostname
		if host == "" || host == "0.0.0.0" {
			host = "localhost"
		}
		baseURL = fmt.Sprintf("http://%s:%d", host, cfg.Port)
		log.WithField("base_url", baseURL).Info("auth_base_url derived from service config")
	}

	// --- Load auth providers from DB ---
	httpCallbackURL := baseURL + "/auth/callback"
	mcpCallbackURL := baseURL + "/mcp/callback"

	httpRegistry := auth.NewRegistry()
	mcpRegistry := auth.NewRegistry()

	// Internal-user auth is handled natively (see /auth/internal/* and the MCP
	// native login). External IdP providers, if any, are loaded from the DB below.
	dbAuthProviders, err := db.Auth.ListAuthProviders(ctx)
	if err != nil {
		log.WithError(err).Fatal("failed to load auth providers from database")
	}

	allAuthProviders := make([]storeauth.AuthProvider, 0, len(dbAuthProviders))
	for _, p := range dbAuthProviders {
		httpEntry, err := auth.NewProviderFromConfig(p, httpCallbackURL)
		if err != nil {
			log.WithError(err).WithField("provider", p.Name).Error("failed to create HTTP auth provider")
			continue
		}
		httpRegistry.Register(httpEntry)

		mcpEntry, err := auth.NewProviderFromConfig(p, mcpCallbackURL)
		if err != nil {
			log.WithError(err).WithField("provider", p.Name).Error("failed to create MCP auth provider")
			httpRegistry.Unregister(p.Name)
			continue
		}
		mcpRegistry.Register(mcpEntry)

		allAuthProviders = append(allAuthProviders, p)
		log.WithField("provider", p.Name).Info("auth provider registered")
	}

	jwtService := auth.NewJWTService(jwtSecret, 1*time.Hour)
	var authenticator *auth.Authenticator
	if cfg.InternalAuth.Enabled {
		authenticator = auth.NewAuthenticator(db, cfg.InternalAuth.MaxFailedLogin, cfg.InternalAuth.FailedLoginIntervalMin, signingKey)
		authenticator.SetPasswordPolicy(cfg.InternalAuth.PasswordPolicy())
	}
	authHandler := auth.NewHandler(httpRegistry, jwtService, cfg.AllowedOrigins, authenticator, db.Users, cfg.ExternalAuth.SignupMode)

	// Passkey (WebAuthn) support for internal users. A user-verified passkey is
	// multi-factor on its own, so a passkey login bypasses TOTP.
	if authenticator != nil {
		rpID := cfg.InternalAuth.WebAuthnRPID
		if rpID == "" {
			if u, err := url.Parse(baseURL); err == nil {
				rpID = u.Hostname()
			}
		}
		origins := cfg.InternalAuth.WebAuthnOrigins
		if len(origins) == 0 {
			origins = cfg.AllowedOrigins
		}
		if wm, err := auth.NewWebAuthnManager(db, rpID, "Admont", origins); err != nil {
			log.WithError(err).Warn("failed to initialize passkey support — passkeys disabled")
		} else {
			authHandler.SetWebAuthn(wm)
			log.WithFields(log.Fields{"rp_id": rpID, "origins": origins}).Info("passkey (WebAuthn) support enabled")
		}
	}

	// --- Load users and groups from DB ---
	users, err := db.Users.ListAllUsers(ctx)
	if err != nil {
		log.WithError(err).Fatal("failed to load users from database")
	}
	log.WithField("count", len(users)).Info("users loaded from database")

	groups, err := db.Users.ListGroups(ctx)
	if err != nil {
		log.WithError(err).Fatal("failed to load groups from database")
	}
	log.WithField("count", len(groups)).Info("groups loaded from database")

	// --- Load repos from DB ---
	dbRepos, err := db.Repos.ListRepos(ctx)
	if err != nil {
		log.WithError(err).Fatal("failed to load repos from database")
	}

	var repoReady sync.Map
	backends := make(map[string]repo.RepoBackend)
	docPaths := make(map[string]string)
	repoConfigs := make(map[string]*git_repo.GitRepo)
	draftManagers := make(map[string]*draft.Manager)
	for i := range dbRepos {
		r := &dbRepos[i]
		slug := r.Slug()
		var repoPath string
		if r.BackendType == "local_git" {
			repoPath = filepath.Join(cfg.LocalRepoPath, slug)
		} else {
			repoPath = filepath.Join(cfg.RepoClonePath, slug)
		}
		backend, err := repofactory.NewBackend(r, repoPath)
		if err != nil {
			log.WithError(err).WithField("repo", slug).Fatal("failed to create repo backend")
		}

		backends[slug] = backend
		repoConfigs[slug] = r
		if r.DocPath != "" {
			docPaths[slug] = r.DocPath
		}
		var dm *draft.Manager
		if s3b, ok := backend.(*s3backend.Backend); ok {
			dm = draft.NewManagerWithStore(draft.NewS3Store(s3b.S3Client(), s3b.Bucket(), s3b.Prefix()))
		} else {
			dm = draft.NewManager(backend.RepoPath())
		}
		draftManagers[slug] = dm

		repoReady.Store(slug, false)
	}

	// --- Model registry ---
	modelRegistry := llm.NewModelRegistry()
	modelRegistry.RegisterDefault("anthropic", llm.AnthropicDefaultModel)
	modelRegistry.RegisterDefault("bedrock", llm.Model{})
	modelRegistry.RegisterDefault("deepseek", llm.DeepSeekDefaultModel)
	modelRegistry.RegisterDefault("google", llm.GoogleDefaultModel)
	modelRegistry.RegisterDefault("meta", llm.MetaDefaultModel)
	modelRegistry.RegisterDefault("mistral", llm.MistralDefaultModel)
	modelRegistry.RegisterDefault("ollama", llm.OllamaDefaultModel)
	modelRegistry.RegisterDefault("openai", llm.OpenAIDefaultModel)
	modelRegistry.RegisterDefault("perplexity", llm.PerplexityDefaultModel)
	modelRegistry.RegisterDefault("xai", llm.XAIDefaultModel)

	// --- Per-user daily LLM token usage (in-memory, reset 00:00 UTC) ---
	usageTracker := usage.NewTracker()
	usageTracker.StartDailyReset(ctx)

	// --- LLM client from DB ---
	llmClient := buildLLMClient(db, modelRegistry, usageTracker)

	// Fetch models dynamically at startup (with timeout)
	fetchCtx, fetchCancel := context.WithTimeout(ctx, 60*time.Second)
	modelRegistry.FetchAll(fetchCtx)
	fetchCancel()

	// Refresh models periodically
	modelRegistry.StartRefresh(1 * time.Hour)
	defer modelRegistry.Stop()

	llmHandler := requesthandler.NewLLMRequesthandler(llmClient)

	// Grammar & spelling checker (LanguageTool)
	var chk checker.Checker
	if cfg.LanguageToolURL != "" {
		chk = checker.NewLanguageToolChecker(cfg.LanguageToolURL)
		log.WithField("url", cfg.LanguageToolURL).Info("LanguageTool checker configured")
	}
	checkerHandler := requesthandler.NewCheckerRequesthandler(chk)

	repoHandler := requesthandler.NewRepoRequesthandler(backends, draftManagers, docPaths, repoConfigs, &repoReady)

	// Launch repo initialization goroutines (after repoHandler is created so we can set resolvers)
	for slug, backend := range backends {
		dm := draftManagers[slug]
		rc := repoConfigs[slug]
		go func(slug string, rc *git_repo.GitRepo, backend repo.RepoBackend, dm *draft.Manager) {
			// For git-based backends, try to sync an existing clone before falling back to full init.
			if backend.Type() == "remote_git" || backend.Type() == "local_git" || backend.Type() == "s3_git" {
				gitDir := filepath.Join(backend.RepoPath(), ".git")
				if info, err := os.Stat(gitDir); err == nil && info.IsDir() {
					log.WithField("repo", rc.RepoUrl).Info("repository exists, pulling changes")
					if err := backend.Sync(); err != nil {
						log.WithError(err).WithField("repo", rc.RepoUrl).Warn("sync failed, re-initializing")
						if err := backend.Initialize(context.Background()); err != nil {
							log.WithError(err).WithField("repo", rc.RepoUrl).Error("failed to initialize repository")
							return
						}
					}
				} else {
					if err := backend.Initialize(context.Background()); err != nil {
						log.WithError(err).WithField("repo", rc.RepoUrl).Error("failed to initialize repository")
						return
					}
				}
			} else {
				if err := backend.Initialize(context.Background()); err != nil {
					log.WithError(err).WithField("repo", slug).Error("failed to initialize repository")
					return
				}
			}

			if backend.Type() == "remote_git" || backend.Type() == "local_git" || backend.Type() == "s3_git" {
				if err := dm.EnsureGitignore(); err != nil {
					log.WithError(err).WithField("repo", slug).Warn("failed to ensure .drafts in .gitignore")
				}
			}

			// Load file-level permissions
			resolver, err := loadPermissions(backend)
			if err != nil {
				log.WithError(err).WithField("repo", slug).Warn("failed to load permissions file")
			} else if resolver != nil {
				repoHandler.SetPermissionResolver(slug, resolver)
				log.WithField("repo", slug).Info("file permissions loaded")
			}

			repoReady.Store(slug, true)
			log.WithField("repo", rc.RepoUrl).WithField("path", backend.RepoPath()).Info("repository ready")
		}(slug, rc, backend, dm)
	}

	// Search feature — always create holder/indexer so providers can be added at runtime.
	repoStateStore := storesearch.NewRepoStateStore(db.Search)
	backendHolder := backend.NewHolder(nil)
	searchIndexer := indexer.New(backendHolder, repoStateStore, backends, docPaths)
	repoHandler.SetIndexer(searchIndexer)
	searchHandler := requesthandler.NewSearchRequesthandler(backendHolder, repoStateStore, backends, repoConfigs, repoHandler.PermResolvers())
	ragHandler := requesthandler.NewRAGRequesthandler(llmClient, backendHolder, backends, repoConfigs, repoHandler.PermResolvers())

	summarizer := llm.NewSummarizer(llmClient, db.Conversations)
	ragHandler.SetConversationStore(db.Conversations, summarizer)
	convHandler := requesthandler.NewConversationRequesthandler(db.Conversations)

	agentHandler := requesthandler.NewAgentRequesthandler(llmClient, backendHolder, backends, repoConfigs, repoHandler.PermResolvers(), docPaths, searchIndexer)
	agentHandler.SetConversationStore(db.Conversations, summarizer)
	agentHandler.SetDraftManagers(draftManagers)

	// Initialize search backend from existing provider (if any)
	searchProviders, err := db.Search.ListSearchProviders(ctx)
	if err != nil {
		log.WithError(err).Fatal("failed to list search providers")
	}

	if len(searchProviders) > 0 {
		activeProvider := searchProviders[0]
		log.WithField("provider_type", activeProvider.ProviderType).Info("initializing search backend")
		if sb, err := initSearchBackend(activeProvider.ProviderType, activeProvider.Config, dsn, cfg.Search); err != nil {
			log.WithError(err).Warn("failed to initialize search backend at startup — search will be unavailable until backend can be created")
		} else {
			backendHolder.Swap(sb)
			// sb will be closed via backendHolder.Swap or program exit

			// Trigger incremental reindex for repos with a search provider assigned
			for slug, rc := range repoConfigs {
				if rc.SearchProviderID != nil {
					log.WithField("repo", slug).Info("triggering incremental search reindex")
					searchIndexer.IncrementalReindex(slug)
				}
			}

			log.Info("search feature enabled")
		}
	} else {
		log.Info("no search providers configured — search feature disabled")
	}

	// Factory for lazy search backend initialization from admin API
	searchBackendFactory := func(providerType string, providerConfig map[string]string) (backend.SearchBackend, error) {
		return initSearchBackend(providerType, providerConfig, dsn, cfg.Search)
	}

	engine := fuego.NewEngine()

	// Route Gin logs through logrus
	gin.DefaultWriter = log.StandardLogger().WriterLevel(log.InfoLevel)
	gin.DefaultErrorWriter = log.StandardLogger().WriterLevel(log.ErrorLevel)
	if cfg.ReleaseMode {
		gin.SetMode(gin.ReleaseMode)
	}
	// Suppress Gin's built-in route debug printing; we log routes ourselves below.
	gin.DebugPrintRouteFunc = func(string, string, string, int) {}

	r := gin.New()
	// Trusted proxies govern whether client-supplied X-Forwarded-For / -Proto
	// headers are honored. Trusting all proxies (the Gin default) lets clients
	// spoof their IP and bypass IP-based rate limiting and login brute-force
	// protection. Default to trusting none; deployments behind a known reverse
	// proxy must set trusted_proxies explicitly.
	if len(cfg.TrustedProxies) > 0 {
		if err := r.SetTrustedProxies(cfg.TrustedProxies); err != nil {
			log.WithError(err).Fatal("invalid trusted_proxies configuration")
		}
	} else {
		_ = r.SetTrustedProxies(nil)
	}
	r.Use(gin.RecoveryWithWriter(log.StandardLogger().WriterLevel(log.ErrorLevel)))
	r.Use(ginLogrus())

	r.Use(gzip.Gzip(gzip.DefaultCompression, gzip.WithExcludedPaths([]string{"/mcp/"})))
	r.Use(cors.New(cors.Config{
		AllowOrigins:     cfg.AllowedOrigins,
		AllowMethods:     []string{"GET", "POST", "PUT", "DELETE", "PATCH", "OPTIONS"},
		AllowHeaders:     []string{"Origin", "Content-Type", "Authorization"},
		AllowCredentials: true,
	}))
	r.Use(func(c *gin.Context) {
		c.Header("X-Content-Type-Options", "nosniff")
		c.Header("X-Frame-Options", "DENY")
		c.Header("Referrer-Policy", "strict-origin-when-cross-origin")
		if c.Request.TLS != nil || c.GetHeader("X-Forwarded-Proto") == "https" {
			c.Header("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		}
		c.Next()
	})

	fuegogin.Get(engine, r, "/health", func(c fuego.ContextNoBody) (healthResponse, error) {
		return healthResponse{Status: "ok"}, nil
	}, fuego.OptionTags("System"))

	authGroup := r.Group("/auth")
	authGroup.GET("/login", authHandler.Login)
	authGroup.GET("/callback", authHandler.Callback)
	authGroup.GET("/providers", authHandler.Providers)
	refreshLimiter := middleware.NewRateLimiter(20)
	authGroup.POST("/refresh", middleware.RateLimit(refreshLimiter), authHandler.Refresh)
	authGroup.POST("/exchange", authHandler.Exchange)

	// Native internal-user authentication (password + TOTP), no external IdP.
	if cfg.InternalAuth.Enabled {
		loginLimiter := middleware.NewRateLimiter(cfg.InternalAuth.MaxFailedLogin * 4)
		internalGroup := authGroup.Group("/internal")
		internalGroup.GET("/signup-status", authHandler.InternalSignupStatus)
		internalGroup.POST("/login", middleware.RateLimit(loginLimiter), authHandler.InternalLogin)
		internalGroup.POST("/totp", middleware.RateLimit(loginLimiter), authHandler.InternalTOTP)
		internalGroup.POST("/signup", middleware.RateLimit(loginLimiter), authHandler.InternalSignup)
		// Forced password reset (expired password) and the public password policy.
		internalGroup.POST("/reset-password", middleware.RateLimit(loginLimiter), authHandler.ResetPassword)
		passwordPolicy := cfg.InternalAuth.PasswordPolicy()
		internalGroup.GET("/password-policy", func(c *gin.Context) { c.JSON(http.StatusOK, passwordPolicy) })
		// Passkey (discoverable / usernameless) login.
		internalGroup.POST("/webauthn/login/begin", middleware.RateLimit(loginLimiter), authHandler.WebAuthnLoginBegin)
		internalGroup.POST("/webauthn/login/finish", middleware.RateLimit(loginLimiter), authHandler.WebAuthnLoginFinish)
	}

	// Create admin handler for runtime config management
	adminHandler := requesthandler.NewAdminRequesthandler(
		db, backends, draftManagers, docPaths,
		users, groups,
		cfg.RepoClonePath, cfg.LocalRepoPath, repoConfigs, &repoReady,
		allAuthProviders,
		baseURL,
		httpRegistry, mcpRegistry,
		modelRegistry,
	)
	adminHandler.SetIndexer(searchIndexer)
	adminHandler.SetSearchBackend(backendHolder, repoStateStore)
	adminHandler.SetSearchBackendFactory(searchBackendFactory)
	adminHandler.SetUsageTracker(usageTracker)
	adminHandler.SetLLMRebuild(func() {
		newClient := buildLLMClient(db, modelRegistry, usageTracker)
		llmHandler.SetClient(newClient)
		ragHandler.SetClient(newClient)
		agentHandler.SetClient(newClient)
		summarizer.SetClient(newClient)
		fetchCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		modelRegistry.FetchAll(fetchCtx)
		cancel()
		log.Info("LLM providers reloaded")
	})

	adminHandler.SetSessionInvalidator(jwtService.InvalidateSessions)
	adminHandler.SetPasswordPolicy(cfg.InternalAuth.PasswordPolicy())
	// Refresh the admin user cache when social login creates/updates a user.
	authHandler.SetUserChangeHook(adminHandler.ReloadUsers)

	// Wire up system admin check for file permissions
	repoHandler.SetSystemAdminCheck(adminHandler.CanManageRepos)
	agentHandler.SetSystemAdminCheck(adminHandler.CanManageRepos)
	searchHandler.SetSystemAdminCheck(adminHandler.CanManageRepos)
	ragHandler.SetSystemAdminCheck(adminHandler.CanManageRepos)

	// Set default import path for Confluence imports
	repoHandler.SetImportPath(cfg.ImportPath)

	// Wire up search provider name resolver
	repoHandler.SetSearchProviderNameResolver(func(id int) string {
		name, err := db.Search.GetSearchProviderNameByID(context.Background(), id)
		if err != nil {
			return ""
		}
		return name
	})

	// MCP server (SSE + streamable HTTP transports with multi-provider OAuth).
	// Gated by MCP_ENABLED (default true) — mcpServer stays nil when disabled;
	// every use of it below must handle that.
	var mcpServer *wikimcp.Server
	if cfg.MCPEnabled {
		mcpServer = wikimcp.NewServer(
			backends, draftManagers, docPaths, repoConfigs, &repoReady,
			repoHandler.PermResolvers(),
			jwtService,
			mcpRegistry,
			baseURL,
		)
		mcpServer.SetSystemAdminCheck(adminHandler.CanManageRepos)
		mcpServer.SetAuthenticator(authenticator)
		mcpServer.SetIndexer(searchIndexer)
		mcpServer.SetSearch(backendHolder, repoStateStore)
		mcpServer.SetRegisteredClientStore(db.MCPClients)
		mcpServer.RegisterRoutes(r)
		log.Info("MCP server enabled at /mcp")
	} else {
		log.Info("MCP server disabled (MCP_ENABLED=false)")
	}

	meGroup := r.Group("/me")
	meGroup.Use(middleware.JWTAuth(jwtService))

	// Passkey management for the logged-in internal user.
	meGroup.POST("/webauthn/register/begin", authHandler.WebAuthnRegisterBegin)
	meGroup.POST("/webauthn/register/finish", authHandler.WebAuthnRegisterFinish)
	meGroup.GET("/webauthn/credentials", authHandler.WebAuthnList)
	meGroup.PATCH("/webauthn/credentials/:id", authHandler.WebAuthnRename)
	meGroup.DELETE("/webauthn/credentials/:id", authHandler.WebAuthnDelete)
	fuegogin.Get(engine, meGroup, "/details", func(c fuego.ContextNoBody) (meDetailsResponse, error) {
		gc := c.Context().(*gin.Context)
		identity, _ := gc.Get(middleware.CtxUserIdentity)
		userIdentity, _ := identity.(string)
		email, _ := gc.Get(middleware.CtxUserEmail)
		name, _ := gc.Get(middleware.CtxUserName)
		provider, _ := gc.Get(middleware.CtxUserProvider)
		roles := adminHandler.GetUserRoles(userIdentity)
		if roles == nil {
			roles = []string{}
		}
		resp := meDetailsResponse{
			Email:    email.(string),
			Name:     name.(string),
			Provider: provider.(string),
			Roles:    roles,
		}
		if provider.(string) == "internal" {
			if user, err := db.Users.GetInternalUser(gc.Request.Context(), email.(string)); err == nil && user != nil {
				resp.PasswordExpired = user.PasswordExpired
			}
		}
		return resp, nil
	},
		fuego.OptionTags("User"),
		fuego.OptionSummary("Get current user details"),
	)

	type changePasswordRequest struct {
		CurrentPassword string `json:"current_password" validate:"required"`
		NewPassword     string `json:"new_password" validate:"required"`
	}
	type changePasswordResponse struct {
		Message string `json:"message"`
	}
	fuegogin.Put(engine, meGroup, "/password", func(c fuego.ContextWithBody[changePasswordRequest]) (changePasswordResponse, error) {
		gc := c.Context().(*gin.Context)
		provider, _ := gc.Get(middleware.CtxUserProvider)
		if provider.(string) != "internal" {
			return changePasswordResponse{}, fuego.BadRequestError{Detail: "password change is only available for internal users"}
		}
		body, err := c.Body()
		if err != nil {
			return changePasswordResponse{}, fuego.BadRequestError{Detail: "invalid request body"}
		}
		if body.CurrentPassword == "" || body.NewPassword == "" {
			return changePasswordResponse{}, fuego.BadRequestError{Detail: "current_password and new_password are required"}
		}
		if err := cfg.InternalAuth.PasswordPolicy().Validate(body.NewPassword); err != nil {
			return changePasswordResponse{}, fuego.BadRequestError{Detail: err.Error()}
		}
		email, _ := gc.Get(middleware.CtxUserEmail)
		userEmail := email.(string)
		storedHash, err := db.Users.GetPasswordHash(gc.Request.Context(), userEmail)
		if err != nil {
			return changePasswordResponse{}, fmt.Errorf("internal error")
		}
		if err := bcrypt.CompareHashAndPassword([]byte(storedHash), []byte(body.CurrentPassword)); err != nil {
			return changePasswordResponse{}, fuego.ForbiddenError{Detail: "current password is incorrect"}
		}
		if bcrypt.CompareHashAndPassword([]byte(storedHash), []byte(body.NewPassword)) == nil {
			return changePasswordResponse{}, fuego.BadRequestError{Detail: "new password must be different from the current password"}
		}
		hash, err := bcrypt.GenerateFromPassword([]byte(body.NewPassword), bcrypt.DefaultCost)
		if err != nil {
			return changePasswordResponse{}, fmt.Errorf("internal error")
		}
		ctx := gc.Request.Context()
		if err := db.Users.SetPasswordHash(ctx, userEmail, string(hash)); err != nil {
			return changePasswordResponse{}, fmt.Errorf("internal error")
		}
		_ = db.Users.ClearPasswordExpired(ctx, userEmail)
		// Refresh the admin user cache so the cleared password_expired flag is reflected.
		adminHandler.ReloadUsers()
		// Invalidate all existing access/refresh tokens for this user so a
		// previously-issued (or stolen) token cannot outlive the password change.
		if id, ok := gc.Get(middleware.CtxUserIdentity); ok {
			if identity, ok := id.(string); ok {
				jwtService.InvalidateSessions(identity)
			}
		}
		return changePasswordResponse{Message: "password changed successfully"}, nil
	},
		fuego.OptionTags("User"),
		fuego.OptionSummary("Change password for internal user"),
	)

	// --- TOTP self-service endpoints ---
	type totpStatusResponse struct {
		Enabled bool `json:"enabled"`
	}
	fuegogin.Get(engine, meGroup, "/totp/status", func(c fuego.ContextNoBody) (totpStatusResponse, error) {
		gc := c.Context().(*gin.Context)
		provider, _ := gc.Get(middleware.CtxUserProvider)
		if provider.(string) != "internal" {
			return totpStatusResponse{}, fuego.BadRequestError{Detail: "TOTP is only available for internal users"}
		}
		email, _ := gc.Get(middleware.CtxUserEmail)
		enabled, err := db.Users.IsTOTPEnabled(gc.Request.Context(), email.(string))
		if err != nil {
			return totpStatusResponse{}, fmt.Errorf("internal error")
		}
		return totpStatusResponse{Enabled: enabled}, nil
	},
		fuego.OptionTags("User"),
		fuego.OptionSummary("Get TOTP status for current user"),
	)

	type totpSetupResponse struct {
		Secret          string `json:"secret"`
		ProvisioningURI string `json:"provisioning_uri"`
		QRCode          string `json:"qr_code"`
	}
	fuegogin.Post(engine, meGroup, "/totp/setup", func(c fuego.ContextNoBody) (totpSetupResponse, error) {
		gc := c.Context().(*gin.Context)
		provider, _ := gc.Get(middleware.CtxUserProvider)
		if provider.(string) != "internal" {
			return totpSetupResponse{}, fuego.BadRequestError{Detail: "TOTP is only available for internal users"}
		}
		email, _ := gc.Get(middleware.CtxUserEmail)
		userEmail := email.(string)
		reqCtx := gc.Request.Context()

		// Generate a new TOTP key.
		key, err := totp.Generate(totp.GenerateOpts{
			Issuer:      "Admont",
			AccountName: userEmail,
		})
		if err != nil {
			return totpSetupResponse{}, fmt.Errorf("generating TOTP key: %w", err)
		}

		// Encrypt and store the secret (not yet enabled).
		encrypted, err := db.Encrypt(key.Secret())
		if err != nil {
			return totpSetupResponse{}, fmt.Errorf("encrypting TOTP secret: %w", err)
		}
		if err := db.Users.SetTOTPSecret(reqCtx, userEmail, encrypted); err != nil {
			return totpSetupResponse{}, fmt.Errorf("storing TOTP secret: %w", err)
		}

		// Generate QR code as base64 PNG.
		img, err := key.Image(200, 200)
		if err != nil {
			return totpSetupResponse{}, fmt.Errorf("generating QR code: %w", err)
		}
		var buf bytes.Buffer
		if err := png.Encode(&buf, img); err != nil {
			return totpSetupResponse{}, fmt.Errorf("encoding QR code: %w", err)
		}
		qrBase64 := base64.StdEncoding.EncodeToString(buf.Bytes())

		return totpSetupResponse{
			Secret:          key.Secret(),
			ProvisioningURI: key.URL(),
			QRCode:          "data:image/png;base64," + qrBase64,
		}, nil
	},
		fuego.OptionTags("User"),
		fuego.OptionSummary("Generate TOTP secret and QR code"),
	)

	type totpVerifyRequest struct {
		Code string `json:"code" validate:"required"`
	}
	type totpVerifyResponse struct {
		Enabled       bool     `json:"enabled"`
		RecoveryCodes []string `json:"recovery_codes"`
	}
	fuegogin.Post(engine, meGroup, "/totp/verify", func(c fuego.ContextWithBody[totpVerifyRequest]) (totpVerifyResponse, error) {
		gc := c.Context().(*gin.Context)
		provider, _ := gc.Get(middleware.CtxUserProvider)
		if provider.(string) != "internal" {
			return totpVerifyResponse{}, fuego.BadRequestError{Detail: "TOTP is only available for internal users"}
		}
		body, err := c.Body()
		if err != nil || body.Code == "" {
			return totpVerifyResponse{}, fuego.BadRequestError{Detail: "code is required"}
		}
		email, _ := gc.Get(middleware.CtxUserEmail)
		userEmail := email.(string)
		reqCtx := gc.Request.Context()

		// Get the stored (encrypted) secret.
		encryptedSecret, enabled, err := db.Users.GetTOTPSecret(reqCtx, userEmail)
		if err != nil {
			return totpVerifyResponse{}, fmt.Errorf("internal error")
		}
		if enabled {
			return totpVerifyResponse{}, fuego.BadRequestError{Detail: "TOTP is already enabled"}
		}
		if encryptedSecret == "" {
			return totpVerifyResponse{}, fuego.BadRequestError{Detail: "call /me/totp/setup first"}
		}

		secret, err := db.Decrypt(encryptedSecret)
		if err != nil {
			return totpVerifyResponse{}, fmt.Errorf("internal error")
		}

		if !totp.Validate(body.Code, secret) {
			return totpVerifyResponse{}, fuego.BadRequestError{Detail: "invalid TOTP code"}
		}

		// Enable TOTP.
		if err := db.Users.EnableTOTP(reqCtx, userEmail); err != nil {
			return totpVerifyResponse{}, fmt.Errorf("internal error")
		}

		// Generate recovery codes.
		plaintextCodes := make([]string, 10)
		hashedCodes := make([]string, 10)
		for i := range plaintextCodes {
			b := make([]byte, 5)
			if _, err := rand.Read(b); err != nil {
				return totpVerifyResponse{}, fmt.Errorf("generating recovery codes: %w", err)
			}
			plaintextCodes[i] = hex.EncodeToString(b)
			hash, err := bcrypt.GenerateFromPassword([]byte(plaintextCodes[i]), bcrypt.DefaultCost)
			if err != nil {
				return totpVerifyResponse{}, fmt.Errorf("hashing recovery code: %w", err)
			}
			hashedCodes[i] = string(hash)
		}
		if err := db.Users.SetTOTPRecoveryCodes(reqCtx, userEmail, hashedCodes); err != nil {
			return totpVerifyResponse{}, fmt.Errorf("internal error")
		}

		return totpVerifyResponse{
			Enabled:       true,
			RecoveryCodes: plaintextCodes,
		}, nil
	},
		fuego.OptionTags("User"),
		fuego.OptionSummary("Verify TOTP code and enable 2FA"),
	)

	type totpDisableRequest struct {
		Password string `json:"password" validate:"required"`
	}
	fuegogin.Delete(engine, meGroup, "/totp", func(c fuego.ContextWithBody[totpDisableRequest]) (messageResponse, error) {
		gc := c.Context().(*gin.Context)
		provider, _ := gc.Get(middleware.CtxUserProvider)
		if provider.(string) != "internal" {
			return messageResponse{}, fuego.BadRequestError{Detail: "TOTP is only available for internal users"}
		}
		body, err := c.Body()
		if err != nil || body.Password == "" {
			return messageResponse{}, fuego.BadRequestError{Detail: "password is required"}
		}
		email, _ := gc.Get(middleware.CtxUserEmail)
		userEmail := email.(string)
		reqCtx := gc.Request.Context()

		storedHash, err := db.Users.GetPasswordHash(reqCtx, userEmail)
		if err != nil {
			return messageResponse{}, fmt.Errorf("internal error")
		}
		if err := bcrypt.CompareHashAndPassword([]byte(storedHash), []byte(body.Password)); err != nil {
			return messageResponse{}, fuego.ForbiddenError{Detail: "incorrect password"}
		}

		if err := db.Users.DisableTOTP(reqCtx, userEmail); err != nil {
			return messageResponse{}, fmt.Errorf("internal error")
		}

		return messageResponse{Message: "TOTP disabled"}, nil
	},
		fuego.OptionTags("User"),
		fuego.OptionSummary("Disable TOTP (requires password)"),
	)

	type totpRegenerateRequest struct {
		Password string `json:"password" validate:"required"`
	}
	type totpRegenerateResponse struct {
		RecoveryCodes []string `json:"recovery_codes"`
	}
	fuegogin.Post(engine, meGroup, "/totp/recovery-codes", func(c fuego.ContextWithBody[totpRegenerateRequest]) (totpRegenerateResponse, error) {
		gc := c.Context().(*gin.Context)
		provider, _ := gc.Get(middleware.CtxUserProvider)
		if provider.(string) != "internal" {
			return totpRegenerateResponse{}, fuego.BadRequestError{Detail: "TOTP is only available for internal users"}
		}
		body, err := c.Body()
		if err != nil || body.Password == "" {
			return totpRegenerateResponse{}, fuego.BadRequestError{Detail: "password is required"}
		}
		email, _ := gc.Get(middleware.CtxUserEmail)
		userEmail := email.(string)
		reqCtx := gc.Request.Context()

		enabled, err := db.Users.IsTOTPEnabled(reqCtx, userEmail)
		if err != nil {
			return totpRegenerateResponse{}, fmt.Errorf("internal error")
		}
		if !enabled {
			return totpRegenerateResponse{}, fuego.BadRequestError{Detail: "TOTP is not enabled"}
		}

		storedHash, err := db.Users.GetPasswordHash(reqCtx, userEmail)
		if err != nil {
			return totpRegenerateResponse{}, fmt.Errorf("internal error")
		}
		if err := bcrypt.CompareHashAndPassword([]byte(storedHash), []byte(body.Password)); err != nil {
			return totpRegenerateResponse{}, fuego.ForbiddenError{Detail: "incorrect password"}
		}

		plaintextCodes := make([]string, 10)
		hashedCodes := make([]string, 10)
		for i := range plaintextCodes {
			b := make([]byte, 5)
			if _, err := rand.Read(b); err != nil {
				return totpRegenerateResponse{}, fmt.Errorf("generating recovery codes: %w", err)
			}
			plaintextCodes[i] = hex.EncodeToString(b)
			hash, err := bcrypt.GenerateFromPassword([]byte(plaintextCodes[i]), bcrypt.DefaultCost)
			if err != nil {
				return totpRegenerateResponse{}, fmt.Errorf("hashing recovery code: %w", err)
			}
			hashedCodes[i] = string(hash)
		}
		if err := db.Users.SetTOTPRecoveryCodes(reqCtx, userEmail, hashedCodes); err != nil {
			return totpRegenerateResponse{}, fmt.Errorf("internal error")
		}

		return totpRegenerateResponse{RecoveryCodes: plaintextCodes}, nil
	},
		fuego.OptionTags("User"),
		fuego.OptionSummary("Regenerate TOTP recovery codes (requires password)"),
	)

	fuegogin.Get(engine, meGroup, "/mcp-clients", func(c fuego.ContextNoBody) ([]mcpClientResponse, error) {
		if mcpServer == nil {
			return []mcpClientResponse{}, nil
		}
		gc := c.Context().(*gin.Context)
		identity, _ := gc.Get(middleware.CtxUserIdentity)
		userIdentity, _ := identity.(string)
		sessions := mcpServer.ConnectedClients(userIdentity)
		result := make([]mcpClientResponse, len(sessions))
		for i, sess := range sessions {
			result[i] = mcpClientResponse{
				ID:          sess.ID,
				Name:        sess.Name,
				ConnectedAt: sess.ConnectedAt.UTC().Format("2006-01-02T15:04:05Z"),
			}
		}
		return result, nil
	},
		fuego.OptionTags("User"),
		fuego.OptionSummary("List connected MCP clients for the current user"),
	)

	llmGroup := r.Group("/llm")
	llmGroup.Use(middleware.JWTAuthOptional(jwtService))
	fuegogin.Get(engine, llmGroup, "/models", llmHandler.GetModels,
		fuego.OptionTags("LLM"),
		fuego.OptionSummary("List available LLM models"),
	)
	llmActionGroup := llmGroup.Group("", middleware.TokenQuota(usageTracker, db))
	fuegogin.Post(engine, llmActionGroup, "", llmHandler.HandleLLM,
		fuego.OptionTags("LLM"),
		fuego.OptionSummary("LLM-powered document actions"),
	)

	convGroup := r.Group("/conversations")
	convGroup.Use(middleware.JWTAuth(jwtService))
	fuegogin.Get(engine, convGroup, "", convHandler.List,
		fuego.OptionTags("Conversations"),
		fuego.OptionSummary("List AI conversations"),
	)
	fuegogin.Post(engine, convGroup, "", convHandler.Create,
		fuego.OptionTags("Conversations"),
		fuego.OptionSummary("Create a new AI conversation"),
	)
	fuegogin.Get(engine, convGroup, "/:id", convHandler.Get,
		fuego.OptionTags("Conversations"),
		fuego.OptionSummary("Get conversation with messages"),
	)
	fuegogin.Patch(engine, convGroup, "/:id", convHandler.Update,
		fuego.OptionTags("Conversations"),
		fuego.OptionSummary("Update conversation title"),
	)
	fuegogin.Delete(engine, convGroup, "/:id", convHandler.Delete,
		fuego.OptionTags("Conversations"),
		fuego.OptionSummary("Delete a conversation"),
	)

	checkerGroup := r.Group("/checker")
	checkerGroup.Use(middleware.JWTAuthOptional(jwtService))
	fuegogin.Post(engine, checkerGroup, "", checkerHandler.Check,
		fuego.OptionTags("Checker"),
		fuego.OptionSummary("Check text for grammar and spelling errors"),
	)

	repos := r.Group("/repos")
	repos.Use(middleware.JWTAuthOptional(jwtService))

	fuegogin.Get(engine, repos, "", repoHandler.GetRepos,
		fuego.OptionTags("Repos"),
		fuego.OptionSummary("List repositories"),
	)

	repo := repos.Group("/:repo")
	repo.Use(repoHandler.RepoAccessCheck())

	fuegogin.Get(engine, repo, "", repoHandler.ListFiles,
		fuego.OptionTags("Files"),
		fuego.OptionSummary("List all files and folders"),
	)
	agentGroup := repo.Group("", middleware.TokenQuota(usageTracker, db))
	fuegogin.Post(engine, agentGroup, "/agent", agentHandler.Agent,
		fuego.OptionTags("RAG"),
		fuego.OptionSummary("Agentic AI assistant with repo-scoped file tools"),
	)
	repo.GET("/file/*path", repoHandler.GetFile())
	fuegogin.Get(engine, repo, "/fileinfo/*path", repoHandler.GetFileInfo,
		fuego.OptionTags("Files"),
		fuego.OptionSummary("Get file info from Git"),
	)
	fuegogin.Get(engine, repo, "/filehistory/*path", repoHandler.GetFileHistory,
		fuego.OptionTags("Files"),
		fuego.OptionSummary("Get file git history"),
	)
	fuegogin.Get(engine, repo, "/filediff/*path", repoHandler.GetFileDiff,
		fuego.OptionTags("Files"),
		fuego.OptionSummary("Get diff between a commit and HEAD"),
		fuego.OptionQuery("commit", "Commit hash to diff against HEAD", fuego.ParamRequired()),
	)
	fuegogin.Get(engine, repo, "/fileatcommit/*path", repoHandler.GetFileAtCommit,
		fuego.OptionTags("Files"),
		fuego.OptionSummary("Get file content at a specific commit"),
		fuego.OptionQuery("commit", "Commit hash to retrieve file content from", fuego.ParamRequired()),
	)

	repoWrite := repo.Group("", repoHandler.RepoWriteCheck())

	fuegogin.Post(engine, repoWrite, "/file/*path", repoHandler.CreateFile,
		fuego.OptionTags("Files"),
		fuego.OptionSummary("Create a file"),
		fuego.OptionDefaultStatusCode(201),
	)
	fuegogin.Put(engine, repoWrite, "/file/*path", repoHandler.UpdateFile,
		fuego.OptionTags("Files"),
		fuego.OptionSummary("Update a file"),
	)
	fuegogin.Delete(engine, repoWrite, "/file/*path", repoHandler.DeleteFile,
		fuego.OptionTags("Files"),
		fuego.OptionSummary("Delete a file"),
		fuego.OptionDefaultStatusCode(204),
	)
	fuegogin.Patch(engine, repoWrite, "/file/*path", repoHandler.MoveFile,
		fuego.OptionTags("Files"),
		fuego.OptionSummary("Move a file"),
	)
	fuegogin.Put(engine, repoWrite, "/rename/*path", repoHandler.RenameFile,
		fuego.OptionTags("Files"),
		fuego.OptionSummary("Rename a file"),
	)

	repoWrite.POST("/upload/*path", repoHandler.UploadFile())
	repoWrite.POST("/import/confluence", repoHandler.ImportConfluence())
	repoWrite.POST("/delete", repoHandler.BulkDelete())

	// Draft routes
	fuegogin.Put(engine, repoWrite, "/draft/*path", repoHandler.SaveDraft,
		fuego.OptionTags("Drafts"),
		fuego.OptionSummary("Save or update a draft"),
	)
	fuegogin.Post(engine, repoWrite, "/draft/publish/*path", repoHandler.PublishDraft,
		fuego.OptionTags("Drafts"),
		fuego.OptionSummary("Publish a draft"),
	)
	fuegogin.Delete(engine, repoWrite, "/draft/*path", repoHandler.DiscardDraft,
		fuego.OptionTags("Drafts"),
		fuego.OptionSummary("Discard a draft"),
		fuego.OptionDefaultStatusCode(204),
	)

	fuegogin.Post(engine, repoWrite, "/folder/*path", repoHandler.CreateFolder,
		fuego.OptionTags("Folders"),
		fuego.OptionSummary("Create a folder"),
		fuego.OptionDefaultStatusCode(201),
	)
	fuegogin.Put(engine, repoWrite, "/folder/*path", repoHandler.UpdateFolder,
		fuego.OptionTags("Folders"),
		fuego.OptionSummary("Rename a folder"),
	)
	fuegogin.Delete(engine, repoWrite, "/folder/*path", repoHandler.DeleteFolder,
		fuego.OptionTags("Folders"),
		fuego.OptionSummary("Delete a folder"),
		fuego.OptionDefaultStatusCode(204),
	)
	fuegogin.Patch(engine, repoWrite, "/folder/*path", repoHandler.MoveFolder,
		fuego.OptionTags("Folders"),
		fuego.OptionSummary("Move a folder"),
	)

	// Order routes
	fuegogin.Put(engine, repoWrite, "/order/*path", repoHandler.SetOrder,
		fuego.OptionTags("Order"),
		fuego.OptionSummary("Set directory ordering"),
	)

	// Permission management routes (require authentication)
	fuegogin.Post(engine, repoWrite, "/permissions/init", repoHandler.InitPermissions,
		fuego.OptionTags("Permissions"),
		fuego.OptionSummary("Initialize or reset permissions for a repository"),
	)
	fuegogin.Get(engine, repo, "/permissions/*path", repoHandler.GetPermissions,
		fuego.OptionTags("Permissions"),
		fuego.OptionSummary("Get permissions for a path"),
	)
	fuegogin.Put(engine, repoWrite, "/permissions/*path", repoHandler.SetPermissions,
		fuego.OptionTags("Permissions"),
		fuego.OptionSummary("Set permissions for a path"),
	)
	fuegogin.Delete(engine, repoWrite, "/permissions/*path", repoHandler.RemovePermissions,
		fuego.OptionTags("Permissions"),
		fuego.OptionSummary("Remove permissions for a path"),
		fuego.OptionDefaultStatusCode(204),
	)
	fuegogin.Get(engine, repo, "/groups", repoHandler.GetPermissionGroups,
		fuego.OptionTags("Permissions"),
		fuego.OptionSummary("List permission groups"),
	)
	fuegogin.Post(engine, repoWrite, "/groups/:name", repoHandler.AddPermissionGroup,
		fuego.OptionTags("Permissions"),
		fuego.OptionSummary("Create a permission group"),
	)
	fuegogin.Put(engine, repoWrite, "/groups/:name", repoHandler.UpdatePermissionGroup,
		fuego.OptionTags("Permissions"),
		fuego.OptionSummary("Update a permission group"),
	)
	fuegogin.Delete(engine, repoWrite, "/groups/:name", repoHandler.DeletePermissionGroup,
		fuego.OptionTags("Permissions"),
		fuego.OptionSummary("Delete a permission group"),
		fuego.OptionDefaultStatusCode(204),
	)

	// ===== /admin routes =====
	adminBase := r.Group("/admin")
	adminBase.Use(middleware.JWTAuth(jwtService))

	// User management routes — user_admin role
	adminUsersGroup := adminBase.Group("/users", middleware.AdminCheckFunc(adminHandler.IsUserAdmin))
	fuegogin.Get(engine, adminUsersGroup, "", adminHandler.GetAllUsers,
		fuego.OptionTags("Admin"),
		fuego.OptionSummary("List all users"),
	)

	// Internal user CRUD
	adminInternalUsers := adminUsersGroup.Group("/internal")
	fuegogin.Get(engine, adminInternalUsers, "", adminHandler.GetInternalUsers,
		fuego.OptionTags("Admin"),
		fuego.OptionSummary("List internal users"),
	)
	fuegogin.Post(engine, adminInternalUsers, "", adminHandler.AddInternalUser,
		fuego.OptionTags("Admin"),
		fuego.OptionSummary("Create an internal user"),
	)
	fuegogin.Put(engine, adminInternalUsers, "/:email", adminHandler.UpdateInternalUser,
		fuego.OptionTags("Admin"),
		fuego.OptionSummary("Update an internal user"),
	)
	fuegogin.Delete(engine, adminInternalUsers, "/:email", adminHandler.DeleteInternalUser,
		fuego.OptionTags("Admin"),
		fuego.OptionSummary("Remove an internal user"),
	)
	fuegogin.Delete(engine, adminInternalUsers, "/:email/totp", adminHandler.ResetInternalUserTOTP,
		fuego.OptionTags("Admin"),
		fuego.OptionSummary("Reset TOTP for an internal user"),
	)

	// External user CRUD
	adminExternalUsers := adminUsersGroup.Group("/external")
	fuegogin.Get(engine, adminExternalUsers, "", adminHandler.GetExternalUsers,
		fuego.OptionTags("Admin"),
		fuego.OptionSummary("List external users"),
	)
	fuegogin.Post(engine, adminExternalUsers, "", adminHandler.AddExternalUser,
		fuego.OptionTags("Admin"),
		fuego.OptionSummary("Create an external user"),
	)
	fuegogin.Put(engine, adminExternalUsers, "/:provider/:email", adminHandler.UpdateExternalUser,
		fuego.OptionTags("Admin"),
		fuego.OptionSummary("Update an external user"),
	)
	fuegogin.Delete(engine, adminExternalUsers, "/:provider/:email", adminHandler.DeleteExternalUser,
		fuego.OptionTags("Admin"),
		fuego.OptionSummary("Remove an external user (also denies a pending request)"),
	)
	fuegogin.Post(engine, adminExternalUsers, "/:provider/:email/approve", adminHandler.ApproveExternalUser,
		fuego.OptionTags("Admin"),
		fuego.OptionSummary("Approve a pending external user"),
	)

	// Group management routes — user_admin only
	adminGroupsGroup := adminBase.Group("/groups", middleware.AdminCheckFunc(adminHandler.IsUserAdmin))
	fuegogin.Get(engine, adminGroupsGroup, "", adminHandler.GetGroups,
		fuego.OptionTags("Admin"),
		fuego.OptionSummary("List all user groups"),
	)
	fuegogin.Post(engine, adminGroupsGroup, "", adminHandler.AddGroup,
		fuego.OptionTags("Admin"),
		fuego.OptionSummary("Add a user group"),
	)
	fuegogin.Put(engine, adminGroupsGroup, "/:name", adminHandler.UpdateGroup,
		fuego.OptionTags("Admin"),
		fuego.OptionSummary("Update a user group"),
	)
	fuegogin.Delete(engine, adminGroupsGroup, "/:name", adminHandler.RemoveGroup,
		fuego.OptionTags("Admin"),
		fuego.OptionSummary("Remove a user group"),
	)

	// Auth provider management routes — system_admin only
	adminAuthGroup := adminBase.Group("/auth", middleware.AdminCheckFunc(adminHandler.IsSystemAdmin))
	fuegogin.Get(engine, adminAuthGroup, "/supported", adminHandler.GetSupportedAuthProviders,
		fuego.OptionTags("Admin"),
		fuego.OptionSummary("List supported auth provider types"),
	)
	fuegogin.Get(engine, adminAuthGroup, "", adminHandler.GetAuthProviders,
		fuego.OptionTags("Admin"),
		fuego.OptionSummary("List all auth providers"),
	)
	fuegogin.Post(engine, adminAuthGroup, "", adminHandler.AddAuthProvider,
		fuego.OptionTags("Admin"),
		fuego.OptionSummary("Add an auth provider"),
	)
	fuegogin.Put(engine, adminAuthGroup, "/:name", adminHandler.UpdateAuthProvider,
		fuego.OptionTags("Admin"),
		fuego.OptionSummary("Update an auth provider"),
	)
	fuegogin.Delete(engine, adminAuthGroup, "/:name", adminHandler.RemoveAuthProvider,
		fuego.OptionTags("Admin"),
		fuego.OptionSummary("Remove an auth provider"),
	)

	// LLM provider management routes — system_admin only
	adminLLMGroup := adminBase.Group("/llm", middleware.AdminCheckFunc(adminHandler.IsSystemAdmin))
	fuegogin.Get(engine, adminLLMGroup, "/supported", adminHandler.GetSupportedLLMProviders,
		fuego.OptionTags("Admin"),
		fuego.OptionSummary("List supported LLM provider types"),
	)
	fuegogin.Get(engine, adminLLMGroup, "", adminHandler.GetLLMProviders,
		fuego.OptionTags("Admin"),
		fuego.OptionSummary("List all LLM providers"),
	)
	fuegogin.Post(engine, adminLLMGroup, "", adminHandler.AddLLMProvider,
		fuego.OptionTags("Admin"),
		fuego.OptionSummary("Add an LLM provider"),
	)
	fuegogin.Get(engine, adminLLMGroup, "/token-limits", adminHandler.GetLLMTokenLimits,
		fuego.OptionTags("Admin"),
		fuego.OptionSummary("Get per-action LLM token limits"),
	)
	fuegogin.Put(engine, adminLLMGroup, "/token-limits", adminHandler.SetLLMTokenLimits,
		fuego.OptionTags("Admin"),
		fuego.OptionSummary("Set per-action LLM token limits"),
	)
	fuegogin.Get(engine, adminLLMGroup, "/usage-limits", adminHandler.GetDailyTokenLimits,
		fuego.OptionTags("Admin"),
		fuego.OptionSummary("Get the global default per-user daily token limits"),
	)
	fuegogin.Put(engine, adminLLMGroup, "/usage-limits", adminHandler.SetDailyTokenLimits,
		fuego.OptionTags("Admin"),
		fuego.OptionSummary("Set the global default per-user daily token limits"),
	)
	fuegogin.Get(engine, adminLLMGroup, "/usage", adminHandler.GetLLMUsage,
		fuego.OptionTags("Admin"),
		fuego.OptionSummary("Get current per-user daily token usage"),
	)
	fuegogin.Post(engine, adminLLMGroup, "/usage/reset", adminHandler.ResetLLMUsage,
		fuego.OptionTags("Admin"),
		fuego.OptionSummary("Reset current daily token usage for one user"),
	)
	fuegogin.Post(engine, adminLLMGroup, "/usage/reset-all", adminHandler.ResetAllLLMUsage,
		fuego.OptionTags("Admin"),
		fuego.OptionSummary("Reset current daily token usage for all users"),
	)
	fuegogin.Get(engine, adminLLMGroup, "/:name/models", adminHandler.GetLLMProviderModels,
		fuego.OptionTags("Admin"),
		fuego.OptionSummary("List available models of an LLM provider"),
	)
	fuegogin.Put(engine, adminLLMGroup, "/:name", adminHandler.UpdateLLMProvider,
		fuego.OptionTags("Admin"),
		fuego.OptionSummary("Update an LLM provider"),
	)
	fuegogin.Delete(engine, adminLLMGroup, "/:name", adminHandler.RemoveLLMProvider,
		fuego.OptionTags("Admin"),
		fuego.OptionSummary("Remove an LLM provider"),
	)

	// Repo management routes — repo_admin role
	adminReposGroup := adminBase.Group("/repos", middleware.AdminCheckFunc(adminHandler.CanManageRepos))
	fuegogin.Get(engine, adminReposGroup, "", adminHandler.GetRepos,
		fuego.OptionTags("Admin"),
		fuego.OptionSummary("List all repository configs"),
	)
	fuegogin.Post(engine, adminReposGroup, "", adminHandler.AddRepo,
		fuego.OptionTags("Admin"),
		fuego.OptionSummary("Add a repository"),
	)
	fuegogin.Put(engine, adminReposGroup, "/:slug/settings", adminHandler.UpdateRepoSettings,
		fuego.OptionTags("Admin"),
		fuego.OptionSummary("Update repository settings"),
	)
	fuegogin.Delete(engine, adminReposGroup, "/:slug", adminHandler.RemoveRepo,
		fuego.OptionTags("Admin"),
		fuego.OptionSummary("Remove a repository"),
	)
	fuegogin.Post(engine, adminReposGroup, "/:slug/reclone", adminHandler.RecloneRepo,
		fuego.OptionTags("Admin"),
		fuego.OptionSummary("Re-clone a repository"),
	)
	fuegogin.Post(engine, adminReposGroup, "/:slug/reindex", adminHandler.ReindexRepo,
		fuego.OptionTags("Admin"),
		fuego.OptionSummary("Rebuild search index for a repository"),
	)

	// Search provider management routes — system_admin only
	adminSearchGroup := adminBase.Group("/search", middleware.AdminCheckFunc(adminHandler.IsSystemAdmin))
	fuegogin.Get(engine, adminSearchGroup, "/supported", adminHandler.GetSupportedSearchProviders,
		fuego.OptionTags("Admin"),
		fuego.OptionSummary("List supported search provider types"),
	)
	fuegogin.Get(engine, adminSearchGroup, "/providers", adminHandler.GetSearchProviders,
		fuego.OptionTags("Admin"),
		fuego.OptionSummary("List all search providers"),
	)
	fuegogin.Post(engine, adminSearchGroup, "/providers", adminHandler.AddSearchProvider,
		fuego.OptionTags("Admin"),
		fuego.OptionSummary("Add a search provider"),
	)
	fuegogin.Put(engine, adminSearchGroup, "/providers/:name", adminHandler.UpdateSearchProvider,
		fuego.OptionTags("Admin"),
		fuego.OptionSummary("Update a search provider"),
	)
	fuegogin.Delete(engine, adminSearchGroup, "/providers/:name", adminHandler.RemoveSearchProvider,
		fuego.OptionTags("Admin"),
		fuego.OptionSummary("Remove a search provider"),
	)
	// Search routes (always registered; returns 503 when no backend is active)
	searchRateLimiter := middleware.NewRateLimiter(60)
	searchGroup := repos.Group("", middleware.RateLimit(searchRateLimiter))
	fuegogin.Post(engine, searchGroup, "/search", searchHandler.Search,
		fuego.OptionTags("Search"),
		fuego.OptionSummary("Search documents"),
	)
	fuegogin.Get(engine, repos, "/search/status", searchHandler.Status,
		fuego.OptionTags("Search"),
		fuego.OptionSummary("Search index status"),
	)
	// RAG is an LLM call, so it also enforces the per-user daily token quota.
	ragGroup := repos.Group("", middleware.RateLimit(searchRateLimiter), middleware.TokenQuota(usageTracker, db))
	fuegogin.Post(engine, ragGroup, "/rag", ragHandler.RAG,
		fuego.OptionTags("RAG"),
		fuego.OptionSummary("Retrieval-augmented generation Q&A"),
	)

	fuegogin.Get(engine, r, "/swagger/openapi.json", engine.SpecHandler(), fuego.OptionHide())
	uiHandler := gin.WrapH(fuego.DefaultOpenAPIHandler("/swagger/openapi.json"))
	r.GET("/swagger", uiHandler)
	r.GET("/swagger/index.html", uiHandler)

	// Log registered routes sorted by path
	type routeEntry struct {
		method, path string
	}
	var routes []routeEntry
	maxMethod := 0
	for _, route := range r.Routes() {
		if len(route.Method) > maxMethod {
			maxMethod = len(route.Method)
		}
		routes = append(routes, routeEntry{route.Method, route.Path})
	}
	sort.Slice(routes, func(i, j int) bool {
		if routes[i].path != routes[j].path {
			return routes[i].path < routes[j].path
		}
		return routes[i].method < routes[j].method
	})
	var lines []string
	for _, r := range routes {
		lines = append(lines, fmt.Sprintf("  %-*s %s", maxMethod, r.method, r.path))
	}
	log.Infof("registered routes:\n%s", strings.Join(lines, "\n"))

	r.Run(cfg.Addr())
}

// initSearchBackend creates a SearchBackend from provider type and config.
// The caller is responsible for closing the returned backend.
func initSearchBackend(providerType string, providerConfig map[string]string, fallbackDSN string, searchCfg config.SearchConfig) (backend.SearchBackend, error) {
	switch providerType {
	case "pgvector":
		searchDSN := buildPgvectorDSN(providerConfig, fallbackDSN)
		emb, err := embedder.New(searchCfg.OnnxRuntimePath, searchCfg.ModelPath, searchCfg.VocabPath)
		if err != nil {
			return nil, fmt.Errorf("initializing embedder: %w", err)
		}
		pgBackend, err := pgvectorbackend.New(context.Background(), searchDSN, emb)
		if err != nil {
			emb.Close()
			return nil, fmt.Errorf("creating pgvector backend: %w", err)
		}
		return pgBackend, nil
	default:
		return nil, fmt.Errorf("unsupported search provider type: %s", providerType)
	}
}

// llmTokenLimitsKey is the settings key holding the per-action output-token
// limits as JSON, e.g. {"ask":4096,"generate":32768,"summarize":1024,"edit":8192}.
const llmTokenLimitsKey = "llm_token_limits"

// buildLLMClient creates an LLM client from all providers stored in the database.
// The tracker, when non-nil, receives per-call token usage attributed to the
// request's user identity (set by the TokenQuota middleware).
func buildLLMClient(db *store.Store, registry *llm.ModelRegistry, tracker *usage.Tracker) *llm.Client {
	providers, err := db.LLM.ListLLMProviders(context.Background())
	if err != nil {
		log.WithError(err).Error("failed to load LLM providers from database")
		client := llm.NewClient(registry)
		setUsageHook(client, tracker)
		return client
	}

	client := llm.NewClient(registry)
	setUsageHook(client, tracker)
	for _, cfg := range providers {
		if cfg.APIKey == "" && cfg.ProviderType != "ollama" && cfg.ProviderType != "bedrock" {
			continue
		}
		if err := registerLLMProvider(client, registry, cfg); err != nil {
			log.WithError(err).WithFields(log.Fields{"name": cfg.Name, "type": cfg.ProviderType}).Error("failed to load LLM provider")
			continue
		}
		log.WithFields(log.Fields{"name": cfg.Name, "type": cfg.ProviderType}).Info("LLM provider loaded")
	}

	if raw, err := db.GetSetting(context.Background(), llmTokenLimitsKey); err == nil && raw != "" {
		var limits map[string]int64
		if err := json.Unmarshal([]byte(raw), &limits); err == nil {
			client.SetActionLimits(limits)
		} else {
			log.WithError(err).Warn("invalid llm_token_limits setting, ignoring")
		}
	}
	return client
}

// setUsageHook attaches the per-user token usage recorder to an LLM client.
func setUsageHook(client *llm.Client, tracker *usage.Tracker) {
	if tracker == nil {
		return
	}
	client.SetUsageHook(func(ctx context.Context, provider string, input, output int64) {
		if key := usage.IdentityFrom(ctx); key != "" {
			tracker.Add(key, provider, input, output)
		}
	})
}

// registerLLMProvider creates and registers a provider + fetcher for the given config.
func registerLLMProvider(client *llm.Client, registry *llm.ModelRegistry, cfg llm_provider.LLMConfig) error {
	maxTokens := cfg.MaxTokens
	if maxTokens == 0 {
		// Reasoning models (o-series, gpt-5.x) consume the completion budget
		// with reasoning tokens before emitting content — 4096 routinely
		// produces empty responses for them.
		maxTokens = 16384
	}
	var provider llm.Provider
	provType := cfg.ProviderType
	switch provType {
	case "openai":
		provider = llm.NewOpenAIProvider(cfg.APIKey, maxTokens)
		registry.RegisterFetcher(provType, llm.NewOpenAICompatFetcher(provType, cfg.APIKey, ""))
	case "deepseek":
		provider = llm.NewDeepSeekProvider(cfg.APIKey, maxTokens)
		registry.RegisterFetcher(provType, llm.NewOpenAICompatFetcher(provType, cfg.APIKey, "https://api.deepseek.com"))
	case "google":
		provider = llm.NewGoogleProvider(cfg.APIKey, maxTokens)
		registry.RegisterFetcher(provType, llm.NewGoogleFetcher(cfg.APIKey))
	case "meta":
		provider = llm.NewMetaProvider(cfg.APIKey, maxTokens)
		registry.RegisterFetcher(provType, llm.NewOpenAICompatFetcher(provType, cfg.APIKey, "https://api.llama.com/compat/v1"))
	case "mistral":
		provider = llm.NewMistralProvider(cfg.APIKey, maxTokens)
		registry.RegisterFetcher(provType, llm.NewOpenAICompatFetcher(provType, cfg.APIKey, "https://api.mistral.ai/v1"))
	case "perplexity":
		provider = llm.NewPerplexityProvider(cfg.APIKey, maxTokens)
		registry.RegisterFetcher(provType, llm.NewPerplexityFetcher())
	case "xai":
		provider = llm.NewXAIProvider(cfg.APIKey, maxTokens)
		registry.RegisterFetcher(provType, llm.NewOpenAICompatFetcher(provType, cfg.APIKey, "https://api.x.ai/v1"))
	case "ollama":
		provider = llm.NewOllamaProvider(cfg.BaseURL, maxTokens)
		registry.RegisterFetcher(provType, llm.NewOpenAICompatFetcher(provType, "ollama", cfg.BaseURL))
	case "anthropic":
		provider = llm.NewAnthropicProvider(cfg.APIKey, maxTokens)
		registry.RegisterFetcher(provType, llm.NewAnthropicFetcher(cfg.APIKey))
	case "bedrock":
		bedrockProvider, err := llm.NewBedrockProvider(context.Background(), cfg.Region, cfg.DefaultModel, maxTokens)
		if err != nil {
			return err
		}
		provider = bedrockProvider
		registry.RegisterDefault(provType, bedrockProvider.DefaultModel())
		registry.RegisterFetcher(provType, bedrockProvider)
	default:
		return fmt.Errorf("unsupported LLM provider type %q", provType)
	}
	registry.MarkConfigured(provType)
	registry.SetFavourites(provType, cfg.FavouriteModels)
	client.AddProvider(provType, provider)
	return nil
}

// buildPgvectorDSN returns a PostgreSQL DSN for the search backend.
// If external_db is true, builds from config fields; otherwise uses the service DSN.
func buildPgvectorDSN(cfg map[string]string, fallbackDSN string) string {
	if cfg["external_db"] != "true" {
		return fallbackDSN
	}
	port := cfg["port"]
	if port == "" {
		port = "5432"
	}
	sslmode := "disable"
	if cfg["ssl_enabled"] == "true" {
		sslmode = "require"
	}
	return fmt.Sprintf("postgres://%s:%s@%s:%s/%s?sslmode=%s", cfg["username"], cfg["password"], cfg["host"], port, cfg["database"], sslmode)
}
