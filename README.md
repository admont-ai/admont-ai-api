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

## Configuration

Configuration is read from environment variables (a local `.env` is supported) or a `config.yaml`. The essentials:

| Variable | Purpose |
|---|---|
| `DATABASE_USERNAME` / `DATABASE_PASSWORD` / `DATABASE_HOSTNAME` / `DATABASE_PORT` / `DATABASE_DB` | PostgreSQL connection |
| `JWT_SECRET` | Signing key for access/refresh tokens |
| `ADMONT_ENCRYPTION_KEY` | 32-byte hex key for encrypting secrets at rest (recommended in production) |
| `ALLOWED_ORIGINS` | Comma-separated frontend origins (CORS + OAuth redirect allow-list) |
| `EXTERNAL_AUTH_SIGNUP_MODE` | `manual` \| `approval` \| `auto` for social-login users |

See the documentation for the complete configuration reference, architecture, and API details.

## Documentation

- 🌐 **Website:** [admont.ai](https://admont.ai)
- 📚 **Docs:** architecture, authentication, authorization, and operations guides live in the Admont AI documentation.

## License

[MIT](LICENSE)
