package backend

import (
	"context"
	"fmt"

	"github.com/christianfischer/md-wiki-server/internal/pg_vector/embedder"
)

// BackendFactory creates SearchBackend instances from provider config.
// embedder and searchDSN are only used by the pgvector backend.
type BackendFactory struct {
	Embedder  *embedder.Embedder
	SearchDSN string
}

// NewBackend creates a SearchBackend for the given provider type and config.
func (f *BackendFactory) NewBackend(ctx context.Context, providerType string, cfg map[string]string) (SearchBackend, error) {
	switch providerType {
	case "pgvector":
		return f.newPgvectorBackend(ctx)
	case "elasticsearch":
		return f.newElasticsearchBackend(ctx, cfg)
	case "typesense":
		return f.newTypesenseBackend(ctx, cfg)
	default:
		return nil, fmt.Errorf("unsupported search provider type: %s", providerType)
	}
}

func (f *BackendFactory) newPgvectorBackend(ctx context.Context) (SearchBackend, error) {
	// Lazy import to avoid circular dependency — use the pgvector sub-package
	// We import it here since this is the factory entry point.
	if f.Embedder == nil {
		return nil, fmt.Errorf("pgvector backend requires an embedder (check ONNX model config)")
	}
	if f.SearchDSN == "" {
		return nil, fmt.Errorf("pgvector backend requires a search DSN")
	}

	// Import the pgvector backend package inline to construct the backend.
	// Since Go doesn't support dynamic imports, we use a registration pattern.
	// The actual construction is done by the caller via the pgvector package directly.
	// This method exists for completeness but the pgvector backend is typically
	// constructed directly in main.go where the embedder is available.
	return nil, fmt.Errorf("pgvector backend must be created via pgvector.New() directly — use BackendFactory only for external backends")
}

func (f *BackendFactory) newElasticsearchBackend(_ context.Context, _ map[string]string) (SearchBackend, error) {
	return nil, fmt.Errorf("elasticsearch backend not yet implemented")
}

func (f *BackendFactory) newTypesenseBackend(_ context.Context, _ map[string]string) (SearchBackend, error) {
	return nil, fmt.Errorf("typesense backend not yet implemented")
}

// SupportedProviders returns metadata about all supported search provider types.
func SupportedProviders() []ProviderInfo {
	return []ProviderInfo{
		{
			Type:        "pgvector",
			Description: "PostgreSQL with pgvector extension (built-in, uses local ONNX embeddings). Set external_db to true to use a separate database.",
			Fields: []ProviderField{
				{Name: "external_db", Required: false, Description: "Use an external database instead of the service DB (default: false)"},
				{Name: "host", Required: false, Description: "Database host (required when external_db is true)"},
				{Name: "port", Required: false, Description: "Database port (default: 5432)"},
				{Name: "database", Required: false, Description: "Database name (required when external_db is true)"},
				{Name: "username", Required: false, Description: "Database username (required when external_db is true)"},
				{Name: "password", Required: false, Description: "Database password (required when external_db is true)"},
				{Name: "ssl_enabled", Required: false, Description: "Enable SSL connection (default: false)"},
			},
		},
		{
			Type:        "elasticsearch",
			Description: "Elasticsearch 8.x with optional ML model for embeddings",
			Fields: []ProviderField{
				{Name: "url", Required: true, Description: "Elasticsearch endpoint URL"},
				{Name: "api_key", Required: false, Description: "API key for authentication"},
				{Name: "index_prefix", Required: false, Description: "Index name prefix (default: admont)"},
				{Name: "embedding_model_id", Required: false, Description: "Deployed ML model ID for embeddings (e.g. .elser_model_2)"},
			},
		},
		{
			Type:        "typesense",
			Description: "Typesense with optional built-in auto-embedding",
			Fields: []ProviderField{
				{Name: "url", Required: true, Description: "Typesense endpoint URL"},
				{Name: "api_key", Required: true, Description: "Admin API key"},
				{Name: "collection_prefix", Required: false, Description: "Collection name prefix (default: admont)"},
				{Name: "embedding_model", Required: false, Description: "Built-in model name for auto-embedding (e.g. ts/e5-small)"},
				{Name: "embedding_api_key", Required: false, Description: "API key for remote embedding models"},
			},
		},
	}
}

// ProviderInfo describes a supported search provider type.
type ProviderInfo struct {
	Type        string          `json:"type"`
	Description string          `json:"description"`
	Fields      []ProviderField `json:"fields"`
}

// ProviderField describes a configuration field for a provider.
type ProviderField struct {
	Name        string `json:"name"`
	Required    bool   `json:"required"`
	Description string `json:"description"`
}
