CREATE SCHEMA IF NOT EXISTS admont_ai;

SET search_path TO admont_ai;

CREATE TYPE user_role AS ENUM ('system_admin', 'user_admin', 'repo_admin');

-- Account lifecycle state. "pending" is used for external users awaiting
-- admin approval; "active" is the normal state. Blocking is orthogonal
-- (the suspended flag), so it is not represented here.
CREATE TYPE user_status AS ENUM ('active', 'pending');

-- Auto-update updated_at on row modification.
CREATE OR REPLACE FUNCTION update_updated_at()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = NOW();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TABLE settings (
    id         SERIAL PRIMARY KEY,
    key        TEXT NOT NULL UNIQUE,
    value      TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE auth_providers (
    id            SERIAL PRIMARY KEY,
    name          TEXT NOT NULL UNIQUE,
    client_id     TEXT NOT NULL,
    client_secret TEXT NOT NULL,
    tenant_id     TEXT NOT NULL DEFAULT '',
    issuer_url    TEXT NOT NULL DEFAULT '',
    domain        TEXT NOT NULL DEFAULT '',
    scopes        TEXT NOT NULL DEFAULT '',
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE llm_providers (
    id            SERIAL PRIMARY KEY,
    name          TEXT NOT NULL UNIQUE,
    provider_type TEXT NOT NULL,
    api_key       TEXT NOT NULL DEFAULT '',
    base_url      TEXT NOT NULL DEFAULT '',
    max_tokens    BIGINT NOT NULL DEFAULT 0,
    default_model TEXT NOT NULL DEFAULT '',
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE search_providers (
    id            SERIAL PRIMARY KEY,
    name          TEXT NOT NULL UNIQUE,
    provider_type TEXT NOT NULL,
    config        JSONB NOT NULL DEFAULT '{}',
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE git_repos (
    id                 SERIAL PRIMARY KEY,
    slug               TEXT NOT NULL UNIQUE,
    repo_url           TEXT NOT NULL,
    branch             TEXT NOT NULL DEFAULT 'main',
    authenticated      BOOLEAN NOT NULL DEFAULT FALSE,
    username           TEXT NOT NULL DEFAULT '',
    auth_token         TEXT NOT NULL DEFAULT '',
    search_provider_id INTEGER REFERENCES search_providers(id) ON DELETE SET NULL,
    lfs_enabled        BOOLEAN NOT NULL DEFAULT FALSE,
    doc_path           TEXT NOT NULL DEFAULT '',
    name               TEXT NOT NULL DEFAULT '',
    public_access      BOOLEAN NOT NULL DEFAULT FALSE,
    read_only          BOOLEAN NOT NULL DEFAULT FALSE,
    backend_type       TEXT NOT NULL DEFAULT 'remote_git',
    s3_bucket          TEXT NOT NULL DEFAULT '',
    s3_prefix          TEXT NOT NULL DEFAULT '',
    s3_region          TEXT NOT NULL DEFAULT '',
    s3_access_key      TEXT NOT NULL DEFAULT '',
    s3_secret_key      TEXT NOT NULL DEFAULT '',
    s3_endpoint        TEXT NOT NULL DEFAULT '',
    created_at         TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Repo indexing state (lives in main DB so it survives backend switches).
CREATE TABLE search_repo_state (
    repo_slug        TEXT PRIMARY KEY,
    last_indexed_sha TEXT NOT NULL DEFAULT '',
    total_chunks     INT NOT NULL DEFAULT 0,
    last_indexed_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Unified users table for both internal (password) and external (IdP) users.
CREATE TABLE users (
    id          SERIAL PRIMARY KEY,
    provider    TEXT NOT NULL,              -- "internal" or the external IdP name
    email       TEXT NOT NULL,
    first_name  TEXT NOT NULL DEFAULT '',
    last_name   TEXT NOT NULL DEFAULT '',
    super_admin BOOLEAN NOT NULL DEFAULT FALSE,
    roles       user_role[] NOT NULL DEFAULT '{}',
    suspended   BOOLEAN NOT NULL DEFAULT FALSE,
    status      user_status NOT NULL DEFAULT 'active',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (provider, email)
);

-- Credentials are 0..1 per user; only internal users have a row.
CREATE TABLE credentials (
    user_id             INTEGER PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    password_hash       TEXT NOT NULL DEFAULT '',
    password_expired    BOOLEAN NOT NULL DEFAULT FALSE,
    password_changed_at TIMESTAMPTZ,
    totp_secret         TEXT NOT NULL DEFAULT '',
    totp_enabled        BOOLEAN NOT NULL DEFAULT FALSE,
    totp_recovery_codes TEXT[] NOT NULL DEFAULT '{}',
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE user_groups (
    id          SERIAL PRIMARY KEY,
    name        TEXT NOT NULL UNIQUE,
    description TEXT NOT NULL DEFAULT '',
    roles       user_role[] NOT NULL DEFAULT '{}',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE user_group_members (
    group_id   INTEGER NOT NULL REFERENCES user_groups(id) ON DELETE CASCADE,
    user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (group_id, user_id)
);

CREATE TABLE ai_conversations (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_email  TEXT NOT NULL,
    title       TEXT NOT NULL DEFAULT '',
    scope       TEXT NOT NULL DEFAULT 'all',
    repo_slug   TEXT NOT NULL DEFAULT '',
    file_path   TEXT NOT NULL DEFAULT '',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE ai_messages (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    conversation_id UUID NOT NULL REFERENCES ai_conversations(id) ON DELETE CASCADE,
    role            TEXT NOT NULL,
    content         TEXT NOT NULL,
    sources         JSONB,
    token_usage     JSONB,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Indexes.
CREATE INDEX idx_ai_conversations_user ON ai_conversations (user_email, updated_at DESC);
CREATE INDEX idx_ai_messages_conversation ON ai_messages (conversation_id, created_at ASC);

-- updated_at triggers.
CREATE TRIGGER trg_settings_updated_at BEFORE UPDATE ON settings FOR EACH ROW EXECUTE FUNCTION update_updated_at();
CREATE TRIGGER trg_auth_providers_updated_at BEFORE UPDATE ON auth_providers FOR EACH ROW EXECUTE FUNCTION update_updated_at();
CREATE TRIGGER trg_llm_providers_updated_at BEFORE UPDATE ON llm_providers FOR EACH ROW EXECUTE FUNCTION update_updated_at();
CREATE TRIGGER trg_search_providers_updated_at BEFORE UPDATE ON search_providers FOR EACH ROW EXECUTE FUNCTION update_updated_at();
CREATE TRIGGER trg_git_repos_updated_at BEFORE UPDATE ON git_repos FOR EACH ROW EXECUTE FUNCTION update_updated_at();
CREATE TRIGGER trg_users_updated_at BEFORE UPDATE ON users FOR EACH ROW EXECUTE FUNCTION update_updated_at();
CREATE TRIGGER trg_credentials_updated_at BEFORE UPDATE ON credentials FOR EACH ROW EXECUTE FUNCTION update_updated_at();
CREATE TRIGGER trg_user_groups_updated_at BEFORE UPDATE ON user_groups FOR EACH ROW EXECUTE FUNCTION update_updated_at();
CREATE TRIGGER trg_ai_conversations_updated_at BEFORE UPDATE ON ai_conversations FOR EACH ROW EXECUTE FUNCTION update_updated_at();
