package config

import (
	"fmt"

	"github.com/joho/godotenv"
	log "github.com/sirupsen/logrus"
	"github.com/spf13/viper"
)

type DatabaseConfig struct {
	Hostname string `mapstructure:"hostname"`
	Port     int    `mapstructure:"port"`
	Username string `mapstructure:"username"`
	Password string `mapstructure:"password"`
	DB       string `mapstructure:"db"`
	SSL      bool   `mapstructure:"ssl"`
}

func (d *DatabaseConfig) DSN() string {
	sslMode := "disable"
	if d.SSL {
		sslMode = "require"
	}
	return fmt.Sprintf("postgres://%s:%s@%s:%d/%s?sslmode=%s",
		d.Username, d.Password, d.Hostname, d.Port, d.DB, sslMode)
}

type InternalAuthConfig struct {
	Enabled                bool   `mapstructure:"enabled"`
	AdminURL               string `mapstructure:"admin_url"`
	PublicURL              string `mapstructure:"public_url"`
	MaxFailedLogin         int    `mapstructure:"max_failed_login"`
	FailedLoginIntervalMin int    `mapstructure:"failed_login_interval_mins"`
	// WebAuthnRPID is the passkey Relying Party ID (a registrable domain, no
	// scheme/port, e.g. "example.com" or "localhost"). Defaults to the host of
	// auth_base_url. It must be a suffix of every WebAuthnOrigin host.
	WebAuthnRPID string `mapstructure:"webauthn_rp_id"`
	// WebAuthnOrigins is the list of full origins (scheme+host[:port]) allowed to
	// perform passkey ceremonies. Defaults to allowed_origins.
	WebAuthnOrigins []string `mapstructure:"webauthn_origins"`

	// Password complexity rules applied wherever an internal-user password is set
	// (signup, self-service change, admin create/reset).
	PasswordMinLength     int  `mapstructure:"password_min_length"`
	PasswordRequireUpper  bool `mapstructure:"password_require_uppercase"`
	PasswordRequireLower  bool `mapstructure:"password_require_lowercase"`
	PasswordRequireDigit  bool `mapstructure:"password_require_digit"`
	PasswordRequireSymbol bool `mapstructure:"password_require_symbol"`
}

// ExternalAuthConfig governs how social-login (external IdP) users are handled.
type ExternalAuthConfig struct {
	// SignupMode is one of: "manual" (only admin-preadded identities may log in),
	// "approval" (first login creates a pending request requiring admin approval),
	// or "auto" (any provider account is auto-provisioned on first login).
	SignupMode string `mapstructure:"signup_mode"`
}

type SearchConfig struct {
	OnnxRuntimePath string `mapstructure:"onnx_runtime_path"`
	ModelPath       string `mapstructure:"model_path"`
	VocabPath       string `mapstructure:"vocab_path"`
}

type Config struct {
	Hostname        string             `mapstructure:"hostname"`
	Port            int                `mapstructure:"port"`
	ReleaseMode     bool               `mapstructure:"release_mode"`
	LogLevel        string             `mapstructure:"log_level"`
	AllowedOrigins  []string           `mapstructure:"allowed_origins"`
	TrustedProxies  []string           `mapstructure:"trusted_proxies"`
	EncryptionKey   string             `mapstructure:"encryption_key"`
	JWTSecret       string             `mapstructure:"jwt_secret"`
	AuthBaseURL     string             `mapstructure:"auth_base_url"`
	LanguageToolURL string             `mapstructure:"languagetool_url"`
	Database        DatabaseConfig     `mapstructure:"database"`
	RepoClonePath   string             `mapstructure:"repo_clone_path"`
	LocalRepoPath   string             `mapstructure:"local_repo_path"`
	InternalAuth    InternalAuthConfig `mapstructure:"internal_auth"`
	ExternalAuth    ExternalAuthConfig `mapstructure:"external_auth"`
	Search          SearchConfig       `mapstructure:"search"`
	ImportPath      string             `mapstructure:"import_path"`
}

// Redacted returns a copy of the config with secret values masked, suitable for
// logging the effective configuration (defaults + file + env overrides).
func (c Config) Redacted() Config {
	mask := func(s string) string {
		if s == "" {
			return ""
		}
		return "[REDACTED]"
	}
	c.EncryptionKey = mask(c.EncryptionKey)
	c.JWTSecret = mask(c.JWTSecret)
	c.Database.Password = mask(c.Database.Password)
	return c
}

func Load() (*Config, error) {
	// Load .env file if present (does not override existing env vars).
	_ = godotenv.Load()

	viper.SetConfigName("config")
	viper.SetConfigType("yaml")
	viper.AddConfigPath(".")

	viper.SetDefault("hostname", "0.0.0.0")
	viper.SetDefault("port", 8080)
	viper.SetDefault("log_level", "info")
	viper.SetDefault("allowed_origins", "http://localhost:5173")
	viper.SetDefault("database.hostname", "localhost")
	viper.SetDefault("database.port", 5433)
	viper.SetDefault("database.db", "admont-ai")
	viper.SetDefault("internal_auth.enabled", true)
	viper.SetDefault("internal_auth.admin_url", "http://localhost:4445")
	viper.SetDefault("internal_auth.public_url", "http://localhost:4444")
	viper.SetDefault("internal_auth.max_failed_login", 5)
	viper.SetDefault("internal_auth.failed_login_interval_mins", 15)
	viper.SetDefault("internal_auth.password_min_length", 8)
	viper.SetDefault("internal_auth.password_require_uppercase", false)
	viper.SetDefault("internal_auth.password_require_lowercase", false)
	viper.SetDefault("internal_auth.password_require_digit", false)
	viper.SetDefault("internal_auth.password_require_symbol", false)
	viper.SetDefault("external_auth.signup_mode", "manual")
	viper.SetDefault("repo_clone_path", "/tmp/admont-api/repos")
	viper.SetDefault("local_repo_path", "/tmp/admont-api/local-repos")
	viper.SetDefault("search.onnx_runtime_path", "/opt/homebrew/lib/libonnxruntime.dylib")
	viper.SetDefault("search.model_path", "models/model.onnx")
	viper.SetDefault("search.vocab_path", "models/vocab.txt")

	viper.SetDefault("import_path", "/tmp/admont-api/import")

	viper.AutomaticEnv()

	// Explicit bindings for env vars that don't match viper's auto-detection.
	// Nested keys (database.*, search.*) are not picked up by AutomaticEnv
	// without a key replacer, so they must be bound explicitly.
	_ = viper.BindEnv("allowed_origins", "ALLOWED_ORIGINS")
	_ = viper.BindEnv("trusted_proxies", "TRUSTED_PROXIES")
	_ = viper.BindEnv("jwt_secret", "JWT_SECRET")
	_ = viper.BindEnv("auth_base_url", "AUTH_BASE_URL")
	_ = viper.BindEnv("log_level", "LOG_LEVEL")
	_ = viper.BindEnv("languagetool_url", "LANGUAGETOOL_URL")
	_ = viper.BindEnv("import_path", "IMPORT_PATH")
	_ = viper.BindEnv("encryption_key", "ADMONT_ENCRYPTION_KEY")
	_ = viper.BindEnv("external_auth.signup_mode", "EXTERNAL_AUTH_SIGNUP_MODE")
	_ = viper.BindEnv("internal_auth.password_min_length", "INTERNAL_AUTH_PASSWORD_MIN_LENGTH")
	_ = viper.BindEnv("internal_auth.password_require_uppercase", "INTERNAL_AUTH_PASSWORD_REQUIRE_UPPERCASE")
	_ = viper.BindEnv("internal_auth.password_require_lowercase", "INTERNAL_AUTH_PASSWORD_REQUIRE_LOWERCASE")
	_ = viper.BindEnv("internal_auth.password_require_digit", "INTERNAL_AUTH_PASSWORD_REQUIRE_DIGIT")
	_ = viper.BindEnv("internal_auth.password_require_symbol", "INTERNAL_AUTH_PASSWORD_REQUIRE_SYMBOL")
	_ = viper.BindEnv("database.hostname", "DATABASE_HOSTNAME")
	_ = viper.BindEnv("database.port", "DATABASE_PORT")
	_ = viper.BindEnv("database.username", "DATABASE_USERNAME")
	_ = viper.BindEnv("database.password", "DATABASE_PASSWORD")
	_ = viper.BindEnv("database.db", "DATABASE_DB")
	_ = viper.BindEnv("database.ssl", "DATABASE_SSL")
	_ = viper.BindEnv("search.onnx_runtime_path", "SEARCH_ONNX_RUNTIME_PATH")
	_ = viper.BindEnv("search.model_path", "SEARCH_MODEL_PATH")
	_ = viper.BindEnv("search.vocab_path", "SEARCH_VOCAB_PATH")

	if err := viper.ReadInConfig(); err != nil {
		if _, ok := err.(viper.ConfigFileNotFoundError); !ok {
			return nil, fmt.Errorf("reading config: %w", err)
		}
	}

	var cfg Config
	if err := viper.Unmarshal(&cfg); err != nil {
		return nil, fmt.Errorf("unmarshaling config: %w", err)
	}

	if cfg.Database.Username == "" {
		return nil, fmt.Errorf("database.username is not set — provide DATABASE_USERNAME (env/.env) or database.username (config.yaml); default credentials were intentionally removed")
	}

	switch cfg.ExternalAuth.SignupMode {
	case "manual", "approval", "auto":
		// valid
	default:
		log.Warnf("invalid external_auth.signup_mode %q — falling back to \"manual\"", cfg.ExternalAuth.SignupMode)
		cfg.ExternalAuth.SignupMode = "manual"
	}

	return &cfg, nil
}

func (c *Config) Addr() string {
	return fmt.Sprintf("%s:%d", c.Hostname, c.Port)
}
