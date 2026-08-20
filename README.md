# admont-ai-api

> The backend service for **[Admont AI](https://admont.ai)** — a self-hostable, Git-backed Markdown wiki with built-in AI search and authoring.

[![CI](https://github.com/admont-ai/admont-ai-api/actions/workflows/ci.yml/badge.svg)](https://github.com/admont-ai/admont-ai-api/actions/workflows/ci.yml)
[![Docker](https://github.com/admont-ai/admont-ai-api/actions/workflows/docker-publish.yml/badge.svg)](https://github.com/admont-ai/admont-ai-api/actions/workflows/docker-publish.yml)
[![Docker Image Version](https://img.shields.io/docker/v/chfischerx/admont-ai-api?sort=semver&logo=docker&label=docker)](https://hub.docker.com/r/chfischerx/admont-ai-api)
[![Docker Pulls](https://img.shields.io/docker/pulls/chfischerx/admont-ai-api?logo=docker)](https://hub.docker.com/r/chfischerx/admont-ai-api)
[![Go](https://img.shields.io/badge/Go-1.25-00ADD8?logo=go&logoColor=white)](go.mod)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

---

## About

`admont-ai-api` is the Go backend that powers Admont AI. It serves Markdown content from Git (or S3) repositories over a REST API, handles authentication and authorization, and provides AI-assisted search, retrieval-augmented generation, and an MCP server for agent integrations.

It pairs with the Admont AI web frontend and is typically run as a container alongside a PostgreSQL (pgvector) database.

## Features

- **Git- and S3-backed storage** for Markdown wiki content.
- **Authentication** — native email/password with TOTP 2FA, plus optional social login (Google and other OAuth/OIDC providers) with configurable signup modes.
- **Fine-grained authorization** — hierarchical, per-path permissions and role-based admin access.
- **AI search & RAG** — hybrid full-text + semantic search over [pgvector](https://github.com/pgvector/pgvector), with retrieval-augmented Q&A across repositories.
- **MCP server** — exposes wiki tools to AI agents over the Model Context Protocol.
- **Multi-provider LLM** support (OpenAI, Anthropic, Google, and compatible APIs).

## Quick start (Docker)

The published image is on Docker Hub: [`chfischerx/admont-ai-api`](https://hub.docker.com/r/chfischerx/admont-ai-api).

It needs a PostgreSQL database with the `pgvector` extension. The fastest way to run everything locally is the bundled Compose stack:

```bash
cp docker-compose/.env.example docker-compose/.env   # set POSTGRES_USER / POSTGRES_PASSWORD
docker compose -f docker-compose/docker-compose.yaml up -d
```

Or run the API container directly against your own database:

```bash
docker run -p 8080:8080 \
  -e DATABASE_HOSTNAME=... -e DATABASE_USERNAME=... -e DATABASE_PASSWORD=... \
  -e JWT_SECRET=... \
  -e ALLOWED_ORIGINS=https://your-frontend.example.com \
  chfischerx/admont-ai-api:latest
```

On first run, open the app and create the initial administrator account.

## Build from source

Requires Go 1.25+.

```bash
go build ./...      # build
go test ./...       # test
make run            # run locally (see the Makefile / docs for prerequisites)
```

## Releasing (publishing a Docker image)

Images are published to Docker Hub by the [`docker-publish`](.github/workflows/docker-publish.yml) workflow, which runs **only when a `v*` tag is pushed** (commits to `main` do not publish). The version comes entirely from the tag name, so cutting a release is just tagging:

```bash
git checkout main && git pull          # release from up-to-date main
git tag -a v0.1.1 -m "v0.1.1"          # tag must start with "v" and be semver
git push origin v0.1.1                  # pushing the tag triggers the workflow
```

The workflow runs the tests first and builds/pushes the image **only if they pass**. A tag `v0.1.1` produces these Docker Hub tags:

| Tag | Source |
|---|---|
| `0.1.1` | full version |
| `0.1` | major.minor (moves to the newest patch) |
| `sha-<short>` | the tagged commit |
| `latest` | the newest semver tag |

To remove a mistaken tag before relying on it: `git push origin :refs/tags/v0.1.1 && git tag -d v0.1.1` (the image may already exist on Docker Hub). You can also trigger a build manually from the Actions tab via **workflow_dispatch**.

## Configuration

Configuration is read from environment variables (a local `.env` is supported) or a `config.yaml`; environment variables take precedence. The complete set of environment variables is below.

### Server

| Variable | Default | Description |
|---|---|---|
| `HOSTNAME` | `0.0.0.0` | Address the HTTP server binds to |
| `PORT` | `8080` | HTTP listen port |
| `LOG_LEVEL` | `info` | Log verbosity (`debug`, `info`, `warn`, `error`) |
| `GIN_MODE` | _(unset)_ | Set to `release` to run Gin in production mode |

### Database (PostgreSQL + pgvector)

| Variable | Default | Description |
|---|---|---|
| `DATABASE_USERNAME` | _(required)_ | PostgreSQL user — startup fails if unset |
| `DATABASE_PASSWORD` | _(empty)_ | PostgreSQL password |
| `DATABASE_HOSTNAME` | `localhost` | Database host |
| `DATABASE_PORT` | `5433` | Database port |
| `DATABASE_DB` | `admont-ai` | Database name |
| `DATABASE_SSL` | `false` | Use TLS (`sslmode=require`) for the connection |

### Authentication & security

| Variable | Default | Description |
|---|---|---|
| `JWT_SECRET` | _(empty)_ | Signing key for access/refresh tokens — set a strong value in production |
| `ADMONT_ENCRYPTION_KEY` | _(empty)_ | 32-byte hex key for encrypting secrets at rest (recommended in production) |
| `AUTH_BASE_URL` | _(empty)_ | Public base URL of this API, used for OAuth redirects and as the default passkey RP/origin |
| `ALLOWED_ORIGINS` | `http://localhost:5173` | Comma-separated frontend origins (CORS + OAuth redirect allow-list) |
| `TRUSTED_PROXIES` | _(none)_ | Comma-separated proxy CIDRs/IPs trusted for client-IP resolution (rate limiting) |

### Signup & password policy

| Variable | Default | Description |
|---|---|---|
| `EXTERNAL_AUTH_SIGNUP_MODE` | `manual` | Social-login handling: `manual` (admin pre-adds), `approval` (first login awaits approval), or `auto` (auto-provision) |
| `INTERNAL_AUTH_PASSWORD_MIN_LENGTH` | `8` | Minimum password length for internal users |
| `INTERNAL_AUTH_PASSWORD_REQUIRE_UPPERCASE` | `false` | Require an uppercase letter |
| `INTERNAL_AUTH_PASSWORD_REQUIRE_LOWERCASE` | `false` | Require a lowercase letter |
| `INTERNAL_AUTH_PASSWORD_REQUIRE_DIGIT` | `false` | Require a digit |
| `INTERNAL_AUTH_PASSWORD_REQUIRE_SYMBOL` | `false` | Require a symbol |

### Search (semantic embeddings)

| Variable | Default | Description |
|---|---|---|
| `SEARCH_ONNX_RUNTIME_PATH` | `/opt/homebrew/lib/libonnxruntime.dylib` | Path to the ONNX Runtime shared library (the container image sets this to its Linux `.so`) |
| `SEARCH_MODEL_PATH` | `models/model.onnx` | Path to the embedding model |
| `SEARCH_VOCAB_PATH` | `models/vocab.txt` | Path to the model vocabulary |

### Storage paths & integrations

| Variable | Default | Description |
|---|---|---|
| `REPO_CLONE_PATH` | `/tmp/admont-api/repos` | Working directory for cloned Git repositories |
| `LOCAL_REPO_PATH` | `/tmp/admont-api/local-repos` | Working directory for local (non-Git) repositories |
| `IMPORT_PATH` | `/tmp/admont-api/import` | Staging directory for content imports |
| `LANGUAGETOOL_URL` | _(empty)_ | Base URL of a LanguageTool server for grammar/spell checking |
| `MCP_ENABLED` | `true` | Enables the MCP server (`/mcp`) exposing wiki tools to AI agents; set to `false` to disable |

> A few advanced options are only settable via `config.yaml` (not environment variables), including `internal_auth.enabled`, `internal_auth.admin_url`/`public_url`, `internal_auth.max_failed_login`, `internal_auth.failed_login_interval_mins`, and the passkey settings `internal_auth.webauthn_rp_id` / `internal_auth.webauthn_origins`.

See the documentation for the complete configuration reference, architecture, and API details.

## Documentation

- 🌐 **Website:** [admont.ai](https://admont.ai)
- 📚 **Docs:** architecture, authentication, authorization, and operations guides live in the Admont AI documentation.

## License

[MIT](LICENSE)
