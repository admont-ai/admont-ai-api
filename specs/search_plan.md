Here is Claude's plan:
╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌
 Search Feature Implementation Plan

 Context

 The md-wiki-server currently stores and serves markdown documents from Git repositories with no search capability. This plan adds full-text and semantic
 search as specified in specs/search.md. The feature is optional, gated by a config flag, and requires PostgreSQL with pgvector.

 Phase 0: go-git v4 → v5 Migration

 The current gopkg.in/src-d/go-git.v4 is deprecated. We need v5 for the DiffTree API (finding changed files between commits for incremental indexing).

 Files to modify:
 - go.mod — Replace gopkg.in/src-d/go-git.v4 with github.com/go-git/go-git/v5
 - internal/git/client.go — Update imports (v4→v5 API is nearly identical); add DiffChangedFiles(oldHash, newHash) and HeadHash() methods
 - internal/git/helper.go — Update imports; add wrapper methods for DiffChangedFiles and HeadHash
 - internal/git/client_test.go, internal/git/helper_test.go — Update imports

 DiffChangedFiles uses oldTree.Diff(newTree) from go-git/v5 to return lists of added, modified, and deleted file paths. When oldHash is empty (first run),
 it walks the full tree.

 Verification: Run existing tests to confirm no regressions.

 Phase 1: Config Extension

 File: config/config.go

 Add SearchConfig struct to Config:

 type SearchConfig struct {
     Enabled         bool   `mapstructure:"enabled"`
     PostgresDSN     string `mapstructure:"postgres_dsn"`
     ModelPath       string `mapstructure:"model_path"`
     VocabPath       string `mapstructure:"vocab_path"`
     OnnxRuntimePath string `mapstructure:"onnx_runtime_path"`
 }

 Defaults: enabled=false, model/vocab/runtime paths point to /models/ and /usr/lib/ (Docker defaults).

 Phase 2: PostgreSQL Store (internal/search/store/store.go)

 Schema:
 CREATE EXTENSION IF NOT EXISTS vector;

 CREATE TABLE search_chunks (
     id            BIGSERIAL PRIMARY KEY,
     repo_slug     TEXT NOT NULL,
     file_path     TEXT NOT NULL,
     chunk_index   INT NOT NULL,
     heading_path  TEXT NOT NULL DEFAULT '',
     content       TEXT NOT NULL,
     embedding     vector(384) NOT NULL,
     tsv           tsvector GENERATED ALWAYS AS (to_tsvector('english', content)) STORED,
     UNIQUE (repo_slug, file_path, chunk_index)
 );

 CREATE INDEX idx_chunks_repo_file ON search_chunks (repo_slug, file_path);
 CREATE INDEX idx_chunks_tsv ON search_chunks USING GIN (tsv);
 CREATE INDEX idx_chunks_embedding ON search_chunks USING hnsw (embedding vector_cosine_ops);

 CREATE TABLE search_repo_state (
     repo_slug         TEXT PRIMARY KEY,
     last_indexed_sha  TEXT NOT NULL DEFAULT '',
     total_chunks      INT NOT NULL DEFAULT 0,
     last_indexed_at   TIMESTAMPTZ NOT NULL DEFAULT NOW()
 );

 Key methods: Migrate, UpsertChunks, DeleteFileChunks, FulltextSearch, SemanticSearch, GetRepoState, UpdateRepoState

 Path prefix filtering (the "offset search path") is implemented as AND file_path LIKE $prefix || '%' in search queries.

 Dependencies: github.com/jackc/pgx/v5, github.com/pgvector/pgvector-go

 Phase 3: Markdown Chunker (internal/search/chunker/chunker.go)

 Uses github.com/yuin/goldmark to parse markdown AST.

 Algorithm:
 1. Parse markdown into AST, walk nodes
 2. Track heading stack (one entry per level)
 3. On heading: flush accumulated text as chunk, update stack
 4. Accumulate paragraph/code/list text between headings
 5. If accumulated section > 1000 chars, split at paragraph boundaries (never mid-sentence)
 6. Prefix each chunk with heading hierarchy: "H1 > H2 > " (not counted toward limit)
 7. Content before first heading = chunk with empty heading path

 Output type: []Chunk{HeadingPath, Content, Index}

 Test file: internal/search/chunker/chunker_test.go — No external dependencies.

 Phase 4: Embedder + Tokenizer

 4a: WordPiece Tokenizer (internal/search/embedder/tokenizer.go)

 Go-native implementation (~100 lines):
 1. Load vocab.txt (30,522 entries, one per line, index = token ID)
 2. Lowercase input, split on whitespace/punctuation
 3. For each word: greedily match longest subword from vocab, use ## prefix for continuations
 4. Add [CLS]/[SEP], pad/truncate to max length (128)
 5. Return input_ids, attention_mask, token_type_ids

 Golden-file tests comparing output to Python transformers tokenizer.

 4b: ONNX Embedder (internal/search/embedder/embedder.go)

 Uses github.com/yalue/onnxruntime_go:
 1. Load ONNX Runtime shared library, create session from model file
 2. Tokenize input text(s)
 3. Run inference: input tensors → output [batch, seq_len, 384]
 4. Mean pooling over non-padding tokens, L2 normalize → pgvector.Vector
 5. Support batch embedding for indexing efficiency

 Phase 5: Indexer (internal/search/indexer/indexer.go)

 Orchestrates chunking → embedding → storage. All operations run in background goroutines (consistent with existing CommitAndPush pattern).

 Methods:
 - IndexFile(repoSlug, filePath) — Read file, chunk, embed, upsert (async)
 - DeleteFileIndex(repoSlug, filePath) — Remove chunks (async)
 - FullReindex(repoSlug) — Delete all chunks, reindex every .md file (async)
 - IncrementalReindex(repoSlug) — Use DiffChangedFiles to only process changes (async)

 Integration hooks in doc_requesthandler.go:

 Add optional indexer field to DocRequesthandler. When non-nil, call after mutations:

 ┌──────────────┬───────────────────────────────────────────────────────────┐
 │   Handler    │                       Indexer Call                        │
 ├──────────────┼───────────────────────────────────────────────────────────┤
 │ CreateFile   │ IndexFile(repo, path)                                     │
 ├──────────────┼───────────────────────────────────────────────────────────┤
 │ UpdateFile   │ IndexFile(repo, path)                                     │
 ├──────────────┼───────────────────────────────────────────────────────────┤
 │ DeleteFile   │ DeleteFileIndex(repo, path)                               │
 ├──────────────┼───────────────────────────────────────────────────────────┤
 │ MoveFile     │ DeleteFileIndex(repo, oldPath) + IndexFile(repo, newPath) │
 ├──────────────┼───────────────────────────────────────────────────────────┤
 │ PublishDraft │ IndexFile(repo, path)                                     │
 ├──────────────┼───────────────────────────────────────────────────────────┤
 │ RecloneRepo  │ FullReindex(repo)                                         │
 └──────────────┴───────────────────────────────────────────────────────────┘

 Phase 6: Search Handler + Route Wiring

 Handler (internal/search/handler.go)

 POST /search — Request:
 {
   "repos": [{"name": "wiki", "path": "docs/"}],
   "query": "how to install",
   "mode": "hybrid",
   "top_k": 10,
   "threshold": 0.5
 }

 Response: {"results": [{"repo", "file_path", "chunk", "score"}]}

 Hybrid search: Run fulltext + semantic each with 2 * top_k limit, merge with RRF (k=60), return top top_k.

 Auth: Uses JWTAuthOptional middleware. For each requested repo, load settings and check user access (isUserAuthorized or public == "read"). Repos the user
 can't access are silently filtered out.

 GET /search/status — Returns per-repo index state (last SHA, chunk count, timestamp).

 Route registration (main.go)

 if cfg.Search.Enabled {
     // Init store, embedder, indexer
     // Pass indexer to docHandler
     // Register /search routes with JWTAuthOptional
     // Trigger IncrementalReindex for all repos
 }

 Phase 7: Dockerfile

 Switch from Alpine/CGO_ENABLED=0 to Debian/CGO_ENABLED=1 with ONNX Runtime:

 - Build stage: golang:1.25-bookworm + gcc
 - Model stage: Download and export all-MiniLM-L6-v2 to ONNX
 - Runtime stage: debian:bookworm-slim + ONNX Runtime lib + model files

 The binary works without search when search.enabled=false — no PostgreSQL or ONNX needed in that mode.

 New Dependencies

 ┌─────────────────────────────────┬──────────────────────────────┐
 │             Library             │           Purpose            │
 ├─────────────────────────────────┼──────────────────────────────┤
 │ github.com/go-git/go-git/v5     │ Replaces deprecated v4       │
 ├─────────────────────────────────┼──────────────────────────────┤
 │ github.com/jackc/pgx/v5         │ PostgreSQL driver            │
 ├─────────────────────────────────┼──────────────────────────────┤
 │ github.com/pgvector/pgvector-go │ pgvector types for Go        │
 ├─────────────────────────────────┼──────────────────────────────┤
 │ github.com/yalue/onnxruntime_go │ ONNX Runtime bindings        │
 ├─────────────────────────────────┼──────────────────────────────┤
 │ github.com/yuin/goldmark        │ Markdown parsing for chunker │
 └─────────────────────────────────┴──────────────────────────────┘

 New Files

 ┌────────────────────────────────────────────┬─────────────────────────────────────┐
 │                    File                    │               Purpose               │
 ├────────────────────────────────────────────┼─────────────────────────────────────┤
 │ internal/search/store/store.go             │ PostgreSQL store + schema migration │
 ├────────────────────────────────────────────┼─────────────────────────────────────┤
 │ internal/search/chunker/chunker.go         │ Heading-based markdown chunking     │
 ├────────────────────────────────────────────┼─────────────────────────────────────┤
 │ internal/search/chunker/chunker_test.go    │ Chunker unit tests                  │
 ├────────────────────────────────────────────┼─────────────────────────────────────┤
 │ internal/search/embedder/tokenizer.go      │ Go-native WordPiece tokenizer       │
 ├────────────────────────────────────────────┼─────────────────────────────────────┤
 │ internal/search/embedder/tokenizer_test.go │ Tokenizer golden-file tests         │
 ├────────────────────────────────────────────┼─────────────────────────────────────┤
 │ internal/search/embedder/embedder.go       │ ONNX Runtime embedding wrapper      │
 ├────────────────────────────────────────────┼─────────────────────────────────────┤
 │ internal/search/indexer/indexer.go         │ Indexing orchestration              │
 ├────────────────────────────────────────────┼─────────────────────────────────────┤
 │ internal/search/handler.go                 │ Search + status API handlers        │
 └────────────────────────────────────────────┴─────────────────────────────────────┘

 Modified Files

 ┌────────────────────────────────────────────────┬───────────────────────────────────────────────────────────────────────────────┐
 │                      File                      │                                    Changes                                    │
 ├────────────────────────────────────────────────┼───────────────────────────────────────────────────────────────────────────────┤
 │ go.mod                                         │ Replace go-git v4→v5; add pgx, pgvector-go, onnxruntime_go, goldmark          │
 ├────────────────────────────────────────────────┼───────────────────────────────────────────────────────────────────────────────┤
 │ config/config.go                               │ Add SearchConfig struct + defaults                                            │
 ├────────────────────────────────────────────────┼───────────────────────────────────────────────────────────────────────────────┤
 │ internal/git/client.go                         │ v5 imports; add DiffChangedFiles, HeadHash                                    │
 ├────────────────────────────────────────────────┼───────────────────────────────────────────────────────────────────────────────┤
 │ internal/git/helper.go                         │ v5 imports; add wrapper methods                                               │
 ├────────────────────────────────────────────────┼───────────────────────────────────────────────────────────────────────────────┤
 │ internal/git/client_test.go                    │ v5 imports                                                                    │
 ├────────────────────────────────────────────────┼───────────────────────────────────────────────────────────────────────────────┤
 │ internal/git/helper_test.go                    │ v5 imports                                                                    │
 ├────────────────────────────────────────────────┼───────────────────────────────────────────────────────────────────────────────┤
 │ internal/request_handler/doc_requesthandler.go │ Add indexer field; add indexer calls after CRUD mutations                     │
 ├────────────────────────────────────────────────┼───────────────────────────────────────────────────────────────────────────────┤
 │ main.go                                        │ Search init block; pass indexer to DocRequesthandler; register /search routes │
 ├────────────────────────────────────────────────┼───────────────────────────────────────────────────────────────────────────────┤
 │ Dockerfile                                     │ Debian base, CGO_ENABLED=1, bundle ONNX runtime + model                       │
 └────────────────────────────────────────────────┴───────────────────────────────────────────────────────────────────────────────┘

 Build Order

 Phases 0→1 are sequential prerequisites. Phases 2, 3, 4 can be built in parallel. Phase 5 depends on 2+3+4. Phase 6 depends on 5. Phase 7 can be done after
  Phase 4.

 Verification

 1. Phase 0: Run go test ./internal/git/... — existing tests pass with go-git v5
 2. Phase 3: Run go test ./internal/search/chunker/... — pure Go, no external deps
 3. Phase 4a: Run tokenizer tests against golden files from Python tokenizer
 4. End-to-end: Start server + PostgreSQL (Docker Compose), create a markdown file via API, verify it appears in search results via POST /search
 5. Incremental index: Update a file, verify search results update; delete a file, verify chunks removed
 6. Hybrid search: Verify RRF scoring returns relevant results combining both search modes
 7. Auth: Verify unauthenticated users only see results from public repos