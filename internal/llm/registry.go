package llm

import (
	"context"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
)

// ModelFetcher can dynamically retrieve models from a provider API.
type ModelFetcher interface {
	FetchModels(ctx context.Context) ([]Model, error)
}

// ModelRegistry stores fallback (hardcoded) and dynamic (API-fetched) models per provider.
type ModelRegistry struct {
	mu             sync.RWMutex
	fallbackModels map[string][]Model
	dynamicModels  map[string][]Model
	defaultModels  map[string]Model
	fetchers       map[string]ModelFetcher
	configured     map[string]bool // providers that are actually configured (have API key / endpoint)

	cancel context.CancelFunc
	done   chan struct{}
}

// NewModelRegistry creates an empty registry.
func NewModelRegistry() *ModelRegistry {
	return &ModelRegistry{
		fallbackModels: make(map[string][]Model),
		dynamicModels:  make(map[string][]Model),
		defaultModels:  make(map[string]Model),
		fetchers:       make(map[string]ModelFetcher),
		configured:     make(map[string]bool),
	}
}

// RegisterFallback stores the hardcoded models and default for a provider.
func (r *ModelRegistry) RegisterFallback(name string, models []Model, defaultModel Model) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.fallbackModels[name] = models
	r.defaultModels[name] = defaultModel
}

// MarkConfigured marks a provider as actively configured (has API key / endpoint).
// Only configured providers are included in AllModels().
func (r *ModelRegistry) MarkConfigured(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.configured[name] = true
}

// UnmarkConfigured removes a provider from the configured set.
func (r *ModelRegistry) UnmarkConfigured(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.configured, name)
}

// RegisterFetcher registers a dynamic model fetcher for a provider.
func (r *ModelRegistry) RegisterFetcher(name string, fetcher ModelFetcher) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.fetchers[name] = fetcher
}

// Models returns dynamic models if available, otherwise fallback.
func (r *ModelRegistry) Models(provider string) []Model {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if dyn, ok := r.dynamicModels[provider]; ok && len(dyn) > 0 {
		return dyn
	}
	return r.fallbackModels[provider]
}

// DefaultModel returns the default model for a provider.
func (r *ModelRegistry) DefaultModel(provider string) Model {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.defaultModels[provider]
}

// AllModels returns models from all configured providers.
func (r *ModelRegistry) AllModels() []Model {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []Model
	for name := range r.configured {
		var models []Model
		if dyn, ok := r.dynamicModels[name]; ok && len(dyn) > 0 {
			models = dyn
		} else if fb, ok := r.fallbackModels[name]; ok {
			models = fb
		}
		for _, m := range models {
			m.Provider = name
			out = append(out, m)
		}
	}
	return out
}

// ValidModel returns true if the given model ID is known across any provider.
func (r *ModelRegistry) ValidModel(id string) bool {
	for _, m := range r.AllModels() {
		if m.ID == id {
			return true
		}
	}
	return false
}

// ProviderForModel returns the provider type that owns the given model ID, or "" if unknown.
func (r *ModelRegistry) ProviderForModel(id string) string {
	for _, m := range r.AllModels() {
		if m.ID == id {
			return m.Provider
		}
	}
	return ""
}

// FetchAll queries all registered fetchers and updates dynamic models.
func (r *ModelRegistry) FetchAll(ctx context.Context) {
	r.mu.RLock()
	fetchers := make(map[string]ModelFetcher, len(r.fetchers))
	for k, v := range r.fetchers {
		fetchers[k] = v
	}
	r.mu.RUnlock()

	for name, fetcher := range fetchers {
		models, err := fetcher.FetchModels(ctx)
		if err != nil {
			log.WithError(err).WithField("provider", name).Warn("failed to fetch models dynamically, using fallback")
			continue
		}
		r.mu.Lock()
		r.dynamicModels[name] = models
		r.mu.Unlock()
		log.WithFields(log.Fields{"provider": name, "count": len(models)}).Info("fetched dynamic models")
	}
}

// StartRefresh runs FetchAll on a periodic interval in the background.
func (r *ModelRegistry) StartRefresh(interval time.Duration) {
	ctx, cancel := context.WithCancel(context.Background())
	r.cancel = cancel
	r.done = make(chan struct{})

	go func() {
		defer close(r.done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				r.FetchAll(ctx)
			}
		}
	}()
}

// Stop cancels the background refresh goroutine.
func (r *ModelRegistry) Stop() {
	if r.cancel != nil {
		r.cancel()
		<-r.done
	}
}
