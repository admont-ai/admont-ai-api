# md-wiki-server
Backend for Markdown Wiki

## Authentication

The server supports multiple OAuth providers via the [goth](https://github.com/markbates/goth) library. Currently supported: **Google** and **GitHub**.

### Configuration

```yaml
jwt_secret: "your-shared-jwt-secret"
super_admin: "google:christian@example.com"

auth:
  - provider: "google"
    client_id: "..."
    client_secret: "..."
    base_url: http://localhost:8080
  - provider: "github"
    client_id: "..."
    client_secret: "..."
    base_url: http://localhost:8080
```

| Field | Required | Description |
|-------|----------|-------------|
| `jwt_secret` | Yes | Shared secret for signing JWTs (top-level config) |
| `auth[].provider` | Yes | Provider type: `google` or `github` |
| `auth[].client_id` | Yes | OAuth client ID |
| `auth[].client_secret` | Yes | OAuth client secret |
| `auth[].base_url` | Yes | Externally-reachable server URL |
| `auth[].scopes` | No | Override default OAuth scopes |
| `auth[].tenant_id` | No | Microsoft tenant ID (future, default `common`) |
| `auth[].issuer_url` | No | OIDC issuer URL (future) |

### User Identity

User identity uses the format `provider:email` (e.g. `google:user@example.com`, `github:user@example.com`). This format is used in:
- JWT claims (`identity` field)
- `super_admin` config value
- User entries in `users.yaml`
- Permission file owner/user entries

**Backward compatibility:** Existing bare email entries (without a provider prefix) are matched automatically. For example, `super_admin: "user@example.com"` matches a user logging in via any provider with that email.

### Endpoints

| Path | Method | Description |
|------|--------|-------------|
| `/auth/login` | GET | Initiate OAuth flow. Query params: `provider` (optional, defaults to first), `redirect_uri` (required) |
| `/auth/callback` | GET | OAuth callback (handles code exchange and JWT issuance) |
| `/auth/providers` | GET | List available provider names (e.g. `["github", "google"]`) |

### Adding a New Provider

1. Register an OAuth application with the provider
2. Set the callback URL to `{base_url}/auth/callback`
3. Add the provider config block to `config.yaml`
4. For MCP, also add `{base_url}/mcp/callback` as an authorized redirect URI

## API Documentation

The server auto-generates an OpenAPI 3.1 spec from route definitions using [Fuego](https://github.com/go-fuego/fuego).

When the server is running:

- **Spec (JSON):** `http://localhost:8080/swagger/openapi.json`
- **Interactive UI:** `http://localhost:8080/swagger/index.html`

## User Permissions

`GET /me/permissions`

Requires authentication. Returns the permissions for the current user.

```json
{
  "admin": true,
  "ai_user": false
}
```

## Repository Backends

The server supports four backend types for storing wiki content. All are managed through the Admin API (`POST /admin/repos`).

### remote_git (default)

Clones a remote Git repository to local disk, pushes changes back on every save.

```json
{
  "backend_type": "remote_git",
  "repo_url": "https://github.com/org/wiki.git",
  "branch": "main",
  "authenticated": true,
  "username": "bot",
  "auth_token": "ghp_xxxxxxxxxxxx",
  "lfs_enabled": false,
  "name": "Engineering Wiki",
  "public_access": false,
  "read_only": false
}
```

| Field | Required | Description |
|-------|----------|-------------|
| `repo_url` | Yes | Git remote URL |
| `branch` | No | Branch name (default `main`) |
| `authenticated` | No | Whether credentials are needed |
| `username` | If authenticated | Git username |
| `auth_token` | If authenticated | Git access token |
| `lfs_enabled` | No | Enable Git LFS tracking for new files |

### local_git

Git repository on local disk with no remote. Useful for standalone wikis or as a starting point before connecting a remote.

```json
{
  "backend_type": "local_git",
  "slug": "internal-notes",
  "branch": "main",
  "name": "Internal Notes"
}
```

| Field | Required | Description |
|-------|----------|-------------|
| `slug` | Yes | Repository identifier (no URL to derive it from) |
| `branch` | No | Branch name (default `main`) |

A `local_git` repo can be promoted to `remote_git` by setting `repo_url` via `PUT /admin/repos/:slug/settings`.

### s3_git

Local Git repository with S3 as the sync remote. Provides full Git history while keeping S3 as the source of truth.

```json
{
  "backend_type": "s3_git",
  "s3_bucket": "my-wiki-bucket",
  "s3_prefix": "docs/",
  "s3_region": "eu-central-1",
  "s3_access_key": "AKIAIOSFODNN7EXAMPLE",
  "s3_secret_key": "wJalrXUtnFEMI/...",
  "branch": "main",
  "name": "S3 Git Wiki"
}
```

On initialize, files are downloaded from S3 into a local Git repo. On save, changes are committed to Git and uploaded to S3. On sync, new files are pulled from S3.

### s3_store

Pure S3 object storage with no Git. Reads and writes go directly to S3. Version history is available only when S3 bucket versioning is enabled.

```json
{
  "backend_type": "s3_store",
  "s3_bucket": "my-wiki-bucket",
  "s3_prefix": "docs/",
  "s3_region": "eu-central-1",
  "name": "S3 Storage Wiki"
}
```

When bucket versioning is enabled, the server automatically detects it and provides file history, version retrieval, and diffs. Author metadata (name, email, commit message) is stored as S3 object metadata on each write.

### S3 Configuration Fields

These fields apply to both `s3_git` and `s3_store` backends:

| Field | Required | Description |
|-------|----------|-------------|
| `s3_bucket` | Yes | S3 bucket name |
| `s3_prefix` | No | Key prefix within the bucket (e.g. `docs/`) |
| `s3_region` | No | AWS region (or uses default credential chain) |
| `s3_access_key` | No | AWS access key (omit to use IAM role / env vars) |
| `s3_secret_key` | No | AWS secret key (omit to use IAM role / env vars) |
| `s3_endpoint` | No | Custom endpoint for S3-compatible services (MinIO) |
| `slug` | No | Auto-derived from bucket+prefix if not provided |

Credentials are encrypted at rest in the database and never returned in API responses.

**MinIO example** (works for both `s3_git` and `s3_store`):

```json
{
  "backend_type": "s3_store",
  "s3_bucket": "wiki",
  "s3_endpoint": "http://minio:9000",
  "s3_access_key": "minioadmin",
  "s3_secret_key": "minioadmin",
  "slug": "minio-wiki",
  "name": "MinIO Wiki"
}
```

### Shared Fields

All backend types accept these optional fields:

| Field | Default | Description |
|-------|---------|-------------|
| `name` | — | Display name |
| `public_access` | `false` | Allow unauthenticated read access |
| `read_only` | `false` | Prevent all write operations |
| `search_provider` | — | Search provider name for indexing |
| `doc_path` | — | Subdirectory to treat as the document root |

### Backend Comparison

| | `remote_git` | `local_git` | `s3_git` | `s3_store` |
|---|---|---|---|---|
| Storage | Local clone + remote | Local disk | Local clone + S3 | S3 objects |
| History | Git (full) | Git (full) | Git (full) | S3 bucket versioning |
| Drafts | Filesystem | Filesystem | Filesystem | S3 objects |
| Push/pull | Yes (remote) | No | S3 sync | N/A (direct writes) |
| LFS | Yes | No | No | N/A |

### Admin API

| Method | Endpoint | Description |
|--------|----------|-------------|
| `POST` | `/admin/repos` | Create a repo (any backend type) |
| `GET` | `/admin/repos` | List all repos |
| `PUT` | `/admin/repos/:slug/settings` | Update repo settings (including S3 fields) |
| `DELETE` | `/admin/repos/:slug` | Remove a repo |
| `POST` | `/admin/repos/:slug/reclone` | Re-initialize backend |
| `POST` | `/admin/repos/:slug/reindex` | Rebuild search index |

## File & Folder Permissions

Per-path access control stored inside each Git repository via a `.file-permissions.yaml` file at the repo root. Permissions are hierarchical: `delete` > `update` > `read` > `none` (each level implies all lower levels).

### Permission File Format

```yaml
version: 1

# Default permission for authenticated users with no specific entry
defaults: read    # "none" | "read" | "update" | "delete"

paths:
  "docs/":
    owner: google:alice@example.com
    default: update
    users:
      github:bob@example.com: delete

  "private/":
    owner: google:alice@example.com
    default: none
    users:
      google:bob@example.com: read

  "private/secret.md":
    owner: google:alice@example.com
    default: none
```

User identities use the `provider:email` format. Existing bare email entries (without provider prefix) continue to work for backward compatibility.

Folder paths end with `/`. File paths do not.

### Permission Levels

| Level | Access |
|-------|--------|
| `none` | No access |
| `read` | View file/folder contents |
| `update` | Read + edit content, create files in folder |
| `delete` | Update + delete the file/folder |

### Resolution Algorithm

Permissions are resolved in this order:

1. **System admin** (`super_admin`, `admin`, `repo_admin` roles) → full access
2. **Exact file path match** → owner gets full access; then check `users` map; then entry `default`
3. **Nearest ancestor folder** (walk from closest parent to root) → same owner/users/default checks
4. **Top-level `defaults`** value
5. **No `.file-permissions.yaml`** → all authenticated users get full access (backward compatibility)

### Operation Requirements

| Operation | Required Level |
|-----------|---------------|
| Read/list folder | `read` on folder |
| Create file in folder | `update` on folder |
| Update file | `update` on file |
| Delete file | `delete` on file |
| Move file | `delete` on source + `update` on destination folder |
| Rename file | `update` on file |
| Delete folder | `delete` on folder |
| Move folder | `delete` on source + `update` on destination parent |
| Rename folder | `update` on folder |

When a file or folder is created, the creating user is automatically set as owner. When files/folders are moved, renamed, or deleted, the permission entries are updated accordingly.

### Permission Management API

All routes are under `/repos/:repo/permissions/*path`.

**GET /repos/:repo/permissions/\*path** — Get effective permissions for a path

Returns the caller's effective permission level and the source of the resolution.

```json
{
  "owner": "google:alice@example.com",
  "default": "update",
  "users": {"github:bob@example.com": "delete"},
  "effective_level": "delete",
  "source": "file"
}
```

The `source` field indicates where the permission was resolved from: `"file"` (exact match), `"folder:path/"` (inherited from a folder entry), or `"defaults"` (top-level default).

**PUT /repos/:repo/permissions/\*path** — Set permissions (owner or system admin only)

```json
{
  "default": "read",
  "users": {
    "google:bob@example.com": "update",
    "github:charlie@example.com": "read"
  }
}
```

**DELETE /repos/:repo/permissions/\*path** — Remove permission entry (owner or system admin only)

Changes to `.file-permissions.yaml` are committed and pushed to the repository like any other file change.

### Repo-Level Public Flag

The `public` flag in the server's repo configuration controls whether unauthenticated users can access the repository. When `public: "read"`, anyone can read files (subject to path-level restrictions). When not set to `"read"`, authentication is required to access the repository at all.

## MCP Server

The server exposes an [MCP (Model Context Protocol)](https://modelcontextprotocol.io/) endpoint over SSE, allowing AI assistants like Claude Code and Claude Desktop to interact with your wiki repositories directly.

### Available Tools

The MCP server exposes 22 tools covering all wiki operations:

| Tool | Description |
|------|-------------|
| `list_repos` | List accessible repositories |
| `get_repo` | List all files and folders in a repository |
| `read_file` | Read file content (returns draft if one exists) |
| `get_file_info` | Get file metadata (size, git history, draft status) |
| `get_file_history` | Get git commit history for a file |
| `get_file_diff` | Get diff between a commit and HEAD |
| `get_file_at_commit` | Get file content at a specific commit |
| `create_file` | Create a new file |
| `update_file` | Update an existing file |
| `delete_file` | Delete a file |
| `move_file` | Move a file to a different folder |
| `rename_file` | Rename a file |
| `upload_file` | Upload a binary file (base64-encoded) |
| `create_folder` | Create a new folder |
| `rename_folder` | Rename a folder |
| `delete_folder` | Delete a folder and all its contents |
| `move_folder` | Move a folder to a different location |
| `save_draft` | Save or update a draft |
| `publish_draft` | Publish a draft (three-way merge with HEAD) |
| `delete_draft` | Discard a draft |
| `search` | Search documents (fulltext, semantic, or hybrid) |
| `search_status` | Get search index status |

### Authentication

MCP clients authenticate using the same multi-provider OAuth flow as the web client. The server implements the OAuth 2.0 authorization server protocol for MCP, including dynamic client registration (RFC 7591):

1. Client connects to `/mcp/sse` and receives a `401` with `WWW-Authenticate` header
2. Client discovers protected resource metadata at `/.well-known/oauth-protected-resource`
3. Client discovers auth server metadata at `/.well-known/oauth-authorization-server`
4. Client registers dynamically via `POST /mcp/register` to obtain a `client_id`
5. Client redirects user to `/mcp/authorize` with PKCE (optional `provider` query param)
6. Server proxies to the selected OAuth provider (opens browser for login)
7. After provider auth, server issues an authorization code via `/mcp/callback`
8. Client exchanges the code at `/mcp/token` for a JWT
9. Client reconnects to SSE with the JWT in the `Authorization` header

**Prerequisite:** Add `{base_url}/mcp/callback` (e.g. `http://localhost:8080/mcp/callback`) as an authorized redirect URI in each OAuth provider's configuration (e.g. Google Cloud Console, GitHub OAuth App settings).

### Connecting Claude Code

The recommended way to connect is with OAuth — no manual token management required. Claude Code will automatically open your browser for Google login:

```bash
claude mcp add --transport sse md-wiki http://localhost:8080/mcp/sse
```

To scope the server to a specific project instead of adding it globally, use the `-s` flag:

```bash
claude mcp add -s project --transport sse md-wiki http://localhost:8080/mcp/sse
```

When Claude Code connects, the server returns a `401` with a `WWW-Authenticate` header pointing to the OAuth metadata. Claude Code follows this to discover the authorization server, opens your browser for OAuth login (defaulting to the first configured provider), and exchanges the resulting authorization code for a JWT — all automatically.

Alternatively, you can provide a pre-obtained JWT token to skip the OAuth flow:

```bash
claude mcp add --transport sse md-wiki http://localhost:8080/mcp/sse --header "Authorization: Bearer YOUR_JWT_TOKEN"
```

To obtain a JWT token manually, authenticate through the web client's OAuth flow (`GET /auth/login`).

You can also add the server by editing `~/.claude/settings.json` (global) or `.claude/settings.json` (project) directly:

```json
{
  "mcpServers": {
    "md-wiki": {
      "type": "sse",
      "url": "http://localhost:8080/mcp/sse"
    }
  }
}
```

### Endpoints

| Path | Method | Description |
|------|--------|-------------|
| `/.well-known/oauth-authorization-server` | GET | OAuth authorization server metadata |
| `/.well-known/oauth-protected-resource` | GET | Protected resource metadata |
| `/mcp/register` | POST | Dynamic client registration (RFC 7591) |
| `/mcp/authorize` | GET | OAuth authorization (redirects to provider; optional `provider` query param) |
| `/mcp/callback` | GET | OAuth callback from provider |
| `/mcp/token` | POST | Exchange authorization code for JWT |
| `/mcp/sse` | GET | SSE connection for MCP protocol (requires auth) |
| `/mcp/message` | POST | MCP message endpoint (used by SSE clients) |

### Permissions

MCP tools enforce the same permission model as the REST API. The authenticated user's access level determines which repositories and files they can read or modify through MCP tools.

## AI Integration

The server includes an optional AI feature powered by Claude that can generate content, answer questions, and polish markdown.

### Configuration

Add your Anthropic API key and the list of allowed user emails to `config.yaml`:

```yaml
ai_api:
  - provider: "anthropic"
    api_key: "sk-ant-..."
```

When the API key is omitted or empty, the AI endpoint returns `501 Not Implemented`.

### Endpoint

`POST /ai`

Requires authentication and an email in the `ai_users` list. The request body must include an `action` field.

### Actions

**ask** — Answer a question

```json
{
  "action": "ask",
  "prompt": "What is the best way to structure a wiki?"
}
```

**generate** — Generate markdown content

```json
{
  "action": "generate",
  "prompt": "Create a FAQ about markdown formatting"
}
```

**polish** — Improve provided markdown content

```json
{
  "action": "polish",
  "content": "some rough draft text here",
  "instructions": "make it more concise"
}
```

The `instructions` field is optional and provides additional guidance for polishing.

## RAG (Retrieval-Augmented Generation)

Combines search with AI to answer questions grounded in your wiki content. Retrieves relevant document chunks via hybrid search, then sends them as context to the configured LLM. Requires both search and an AI provider to be configured.

### Endpoint

**POST /repos/rag** — Ask a question with grounded answers

```json
{
  "repos": [{"name": "my-wiki", "path": "docs/"}],
  "query": "How do I configure authentication?",
  "model": "claude-sonnet-4-6",
  "top_k": 10,
  "threshold": 0.5
}
```

| Field | Required | Default | Description |
|-------|----------|---------|-------------|
| `repos` | Yes | — | Repositories to search (same format as `/search`) |
| `query` | Yes | — | The question to answer |
| `model` | No | Provider default | LLM model ID to use |
| `top_k` | No | 10 | Max document chunks to retrieve (1–100) |
| `threshold` | No | 0 | Minimum search score filter |
| `repos[].path` | No | — | Path prefix filter within a repo |

**Response:**

```json
{
  "answer": "Authentication is configured by adding an OAuth provider block to your config. See [1] for details on supported providers.",
  "sources": [
    {
      "repo": "my-wiki",
      "file_path": "docs/auth.md",
      "chunk": "The server supports multiple OAuth providers...",
      "score": 0.92
    }
  ],
  "usage": {
    "input_tokens": 1842,
    "output_tokens": 156
  }
}
```

The `answer` field contains markdown with numbered citations (e.g. `[1]`, `[2]`) referring to entries in the `sources` array. If the retrieved documents don't contain enough information to answer the question, the LLM will say so rather than hallucinate.

## Git LFS

Repositories that use [Git LFS](https://git-lfs.com/) for large files (images, PDFs, etc.) are supported. LFS objects are fetched automatically during clone and pull operations.

### Requirements

Git LFS must be installed and configured on the host:

```bash
# macOS
brew install git-lfs
git lfs install

# Debian / Ubuntu
apt-get install git-lfs
git lfs install
```

The Docker image includes Git LFS out of the box — no extra setup is needed.

### How it works

Clone and pull operations use the `git` CLI (rather than an in-process library) so that Git LFS smudge filters run automatically. After each clone or pull, `git lfs pull` is run as an additional safety net. If Git LFS is not installed, a warning is logged and the server continues to work normally for repositories that don't use LFS.

## Grammar & Spelling Checker

Optional grammar and spelling checking powered by [LanguageTool](https://languagetool.org/). Returns inline annotations (offset, length, message, replacements) suitable for editor underline decorations.

### Setup

**Option 1: Self-hosted (recommended)**

Run LanguageTool locally via Docker:

```bash
docker run -d --name languagetool -p 8081:8010 \
  erikvl87/languagetool
```

**Option 2: Public API**

Use the public API at `https://api.languagetool.org` (rate-limited, not recommended for production).

### Configuration

Set `languagetool_url` in `config.yaml` or via the `LANGUAGETOOL_URL` environment variable:

```yaml
languagetool_url: "http://localhost:8081"
```

When omitted or empty, the checker endpoint returns `503 Service Unavailable`.

### Endpoint

`POST /checker`

Requires authentication (optional JWT, same as LLM routes).

**Request:**

```json
{
  "text": "Ths is a tset of the checker.",
  "language": "en-US"
}
```

The `language` field is optional and defaults to `"auto"` (auto-detection).

**Response:**

```json
{
  "annotations": [
    {
      "offset": 0,
      "length": 3,
      "message": "Possible spelling mistake found.",
      "short_message": "Spelling mistake",
      "rule_id": "MORFOLOGIK_RULE_EN_US",
      "category": "Typo",
      "type": "spelling",
      "replacements": ["This", "The", "Ths"]
    }
  ],
  "language": "en-US"
}
```

Each annotation includes:

| Field | Description |
|-------|-------------|
| `offset` | Character offset in the input text |
| `length` | Length of the flagged span |
| `message` | Human-readable explanation |
| `type` | `"spelling"`, `"grammar"`, `"style"`, or `"typographical"` |
| `replacements` | Up to 5 suggested corrections |

## Search

Optional full-text and semantic search across markdown documents. Requires PostgreSQL with the `pgvector` extension and an ONNX Runtime embedding model (`all-MiniLM-L6-v2`).

### Prerequisites

**PostgreSQL with pgvector:**

```bash
# Docker
docker run -d --name wiki-pg -p 5432:5432 \
  -e POSTGRES_USER=wiki -e POSTGRES_PASSWORD=wiki -e POSTGRES_DB=wiki \
  pgvector/pgvector:pg17

# Or use the included docker-compose.yaml
```

**ONNX Runtime (local development on macOS):**

```bash
brew install onnxruntime
```

**Embedding model:** Export `all-MiniLM-L6-v2` to ONNX format and download the vocabulary:

```bash
python3 -m venv .venv
source .venv/bin/activate
pip install "optimum[onnxruntime]" onnx onnxruntime transformers torch
python3 export_model.py
deactivate

# Download the WordPiece vocabulary
curl -fsSL -o ./models/vocab.txt \
  https://huggingface.co/sentence-transformers/all-MiniLM-L6-v2/resolve/main/vocab.txt
```

The `export_model.py` script is included in the repository. After running the above, you should have `./models/model.onnx` and `./models/vocab.txt`.

### Configuration

Add the `search` block to `config.yaml`:

```yaml
search:
  enabled: true
  postgres_dsn: "postgres://wiki:wiki@localhost:5432/wiki?sslmode=disable"
  model_path: "./models/model.onnx"
  vocab_path: "./models/vocab.txt"
  onnx_runtime_path: "/opt/homebrew/lib/libonnxruntime.dylib"
```

| Field | Default | Description |
|-------|---------|-------------|
| `enabled` | `false` | Enable search feature |
| `postgres_dsn` | — | PostgreSQL connection string |
| `model_path` | `/models/model.onnx` | Path to ONNX model file |
| `vocab_path` | `/models/vocab.txt` | Path to WordPiece vocab file |
| `onnx_runtime_path` | `/usr/lib/libonnxruntime.so` | Path to ONNX Runtime shared library |

The Docker image defaults (`/models/...`, `/usr/lib/...`) work out of the box. For local macOS development, point `onnx_runtime_path` to the Homebrew library path (`/opt/homebrew/lib/libonnxruntime.dylib`).

When `search.enabled` is `false` (the default), no PostgreSQL or ONNX Runtime is needed.

### Endpoints

**POST /search** — Search documents

```json
{
  "repos": [{"name": "my-wiki", "path": "docs/"}],
  "query": "how to install",
  "mode": "hybrid",
  "top_k": 10,
  "threshold": 0.5
}
```

- `mode`: `fulltext`, `semantic`, or `hybrid` (default)
- `top_k`: max results, 1–100 (default 10)
- `threshold`: minimum score filter (default 0)
- `repos[].path`: optional path prefix filter

**GET /search/status** — Index status per repository

Returns last indexed commit SHA, chunk count, and timestamp for each repo.

### Indexing

Documents are indexed automatically:
- On startup, an incremental reindex runs for all repositories (only changed files since the last indexed commit)
- File create/update/delete/move operations trigger immediate re-indexing of affected files
- Recloning a repository triggers a full reindex
