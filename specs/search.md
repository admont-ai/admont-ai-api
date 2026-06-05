# Markdown Search Service — Specification

## 1. Overview

This feature implements a full-text and semantic search across markdown documents stored in Git repositories. The service clones configured repositories, indexes their markdown content, and exposes search functionality via an HTTP REST API.

The search function is optional and will be enabled in the service config. If enabled, a configuration for the vector database is also required.

## 2. Document Model

Each document is a single markdown file (`.md` extension) that may contain headings, regular text, links to other documents, external links, and references to images and videos. Documents are stored in raw markdown source format across multiple directories with no limit on subdirectory depth.

Non-markdown files (images, diagram definitions, etc.) are ignored.

## 3. Git Repository Configuration

Multiple Git repositories are configured in the service configuration. Each repository requires the following parameters:

| Parameter    | Type   | Required | Description                                              |
|--------------|--------|----------|----------------------------------------------------------|
| GitRepoUrl   | string | yes      | URL of the Git repository                                |
| GitBranch    | string | no       | Branch to index. Defaults to the repository's default branch |
| GitUsername   | string | no       | Username for authentication                              |
| GitAuthToken | string | no       | Token for authentication                                 |

All configured repositories are fully cloned at service startup.

## 4. Search API

### 4.1 Search Request

| Parameter   | Type     | Required | Default  | Description                                                        |
|-------------|----------|----------|----------|--------------------------------------------------------------------|
| repos       | list     | yes      | —        | List of repository names, each with an optional offset search path |
| query       | string   | yes      | —        | The search query text                                              |
| mode        | string   | no       | hybrid   | Search mode: `fulltext`, `semantic`, or `hybrid`                   |
| top_k       | integer  | no       | 10       | Number of results to return. Maximum: 100                          |
| threshold   | float    | no       | none     | Minimum similarity score to include a result                       |

### 4.2 Search Response

Results are ordered by score descending. Each match contains:

| Field      | Type   | Description                                           |
|------------|--------|-------------------------------------------------------|
| repo       | string | Repository name                                       |
| file_path  | string | Path to the source markdown file                      |
| chunk      | string | The matched chunk of text                             |
| score      | float  | Similarity score (cosine for semantic, RRF for hybrid)|
| error      | string | Error message, if applicable                          |

### 4.3 Hybrid Search

Hybrid search combines full-text and semantic results using Reciprocal Rank Fusion (RRF) with k=60. Both search modes return up to 2× top_k candidates internally, which are merged and the top top_k results returned to the client. The returned score is the RRF score.

### 4.4 Error Handling

| Condition                        | HTTP Status | Description                    |
|----------------------------------|-------------|--------------------------------|
| Unknown repository name          | 404         | Repository not found           |
| Invalid request parameters       | 400         | Bad request with error details |
| Git or indexing failure           | 500         | Internal server error          |

## 5. Chunking

Documents are chunked by heading and paragraph at every heading level.

- If a section under a heading is under 1000 characters, it is kept as a single chunk.
- If a section exceeds 1000 characters, it is split at paragraph boundaries, keeping each chunk under 1000 characters.
- Paragraphs are never split mid-sentence. If a single paragraph exceeds 1000 characters, it may exceed the limit rather than break awkwardly.
- Each chunk is prefixed with its heading hierarchy (e.g., `Setup > Prerequisites > `) for embedding context. This prefix is not counted toward the 1000-character content limit.
- Content before the first heading is treated as its own chunk.

## 6. Indexing

- Re-indexing is triggered when a new document is uploaded via the existing API, or a document has been changed.
- Only the new or changed document will be re-indexed.
- The service stores the last indexed commit SHA per repository and uses `git diff` to identify changed files. On first run, the full repository is indexed.
- Only changed, added, moved, or deleted files are re-indexed.
- Links within documents are ignored during indexing.
- Only files with the `.md` extension are indexed.
- The index is stored in PostgreSQL.

### 6.1 Status Endpoint

The service provides a indexing status endpoint that returns, for each configured repository:

- Last indexed commit SHA
- Total chunk count
- Last index timestamp

## 7. Non-Functional Requirements

| Requirement         | Value                                                        |
|---------------------|--------------------------------------------------------------|
| Document scale      | Typically ~100 documents per repo, up to 1000                |
| Search latency      | p95 < 500ms                                                  |
| Licensing           | Open-source libraries only, self-hosted                      |
| Hardware            | Standard CPU and memory (no GPU required)                    |
| Language            | English for now. Other languages may be added but no mixed-language documents |

## 8. Technology Stack

| Component        | Technology                                                         |
|------------------|--------------------------------------------------------------------|
| Language         | Go                                                                 |
| Embedding model  | `all-MiniLM-L6-v2` (384 dimensions, CPU-compatible) via ONNX Runtime |
| Storage engine   | PostgreSQL with `pgvector` (vector search) + `tsvector` (full-text search) |
| Vector index     | HNSW via pgvector                                                  |
| Full-text index  | GIN index on tsvector column                                       |

### 8.1 Programming Language: Go

Go is recommended for this service. It produces a single static binary with no runtime dependencies, which simplifies deployment. It has excellent concurrency support for handling parallel search queries, strong standard library support for HTTP servers, and mature libraries for Git and PostgreSQL. Memory usage is low and predictable, which aligns with the standard hardware requirement.

### 8.2 Core Libraries

| Library                          | Purpose                              | Notes                                              |
|----------------------------------|--------------------------------------|----------------------------------------------------|
| `github.com/go-git/go-git/v5`   | Git operations                       | Pure Go Git implementation. Clone, pull, diff.     |
| `github.com/jackc/pgx/v5`       | PostgreSQL driver                    | High-performance driver with pgvector support.     |
| `github.com/pgvector/pgvector-go`| pgvector type support                | Go types for vector columns, integrates with pgx.  |
| `github.com/yalue/onnxruntime_go`| ONNX Runtime bindings                | Run the MiniLM embedding model on CPU.             |
| `github.com/gin-gonic/gin`      | HTTP framework                       | Lightweight, fast REST API framework.              |
| `github.com/yuin/goldmark`      | Markdown parsing                     | Parse markdown into AST for heading-based chunking.|

### 8.3 Embedding Model Integration

The `all-MiniLM-L6-v2` model is exported to ONNX format and loaded via ONNX Runtime. This avoids a Python dependency entirely and runs efficiently on CPU. The ONNX model file (~80 MB) is bundled with the service or loaded from a configured path at startup. Tokenization is handled using a Go-native WordPiece tokenizer compatible with the model's vocabulary.