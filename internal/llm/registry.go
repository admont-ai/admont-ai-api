package llm

import (
	"context"
	"sort"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
)

// ModelFetcher can dynamically retrieve models from a provider API.
type ModelFetcher interface {
	FetchModels(ctx context.Context) ([]Model, error)
}

// ModelRegistry stores dynamically fetched models, per-provider favourites,
// and default models. Model lists come exclusively from the provider APIs
// (already filtered by the fetchers); there are no hardcoded lists.
type ModelRegistry struct {
	mu            sync.RWMutex
	dynamicModels map[string][]Model
	defaultModels map[string]Model
	fetchers      map[string]ModelFetcher
	favourites    map[string][]string // admin-selected model IDs per provider
	configured    map[string]bool     // providers that are actually configured (have API key / endpoint)

	cancel context.CancelFunc
	done   chan struct{}
}

// NewModelRegistry creates an empty registry.
func NewModelRegistry() *ModelRegistry {
	return &ModelRegistry{
		dynamicModels: make(map[string][]Model),
		defaultModels: make(map[string]Model),
		fetchers:      make(map[string]ModelFetcher),
		favourites:    make(map[string][]string),
		configured:    make(map[string]bool),
	}
}

// RegisterDefault stores the default model for a provider (used when a
// request does not specify a model).
func (r *ModelRegistry) RegisterDefault(name string, defaultModel Model) {
	r.mu.Lock()
	defer r.mu.Unlock()
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
	delete(r.favourites, name)
}

// SetFavourites stores the admin-selected model IDs for a provider. An empty
// list clears the selection (all fetched models are shown).
func (r *ModelRegistry) SetFavourites(name string, ids []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(ids) == 0 {
		delete(r.favourites, name)
		return
	}
	r.favourites[name] = ids
}

// RegisterFetcher registers a dynamic model fetcher for a provider.
func (r *ModelRegistry) RegisterFetcher(name string, fetcher ModelFetcher) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.fetchers[name] = fetcher
}

// Models returns the dynamically fetched models for a provider.
func (r *ModelRegistry) Models(provider string) []Model {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.dynamicModels[provider]
}

// DefaultModel returns the default model for a provider.
func (r *ModelRegistry) DefaultModel(provider string) Model {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.defaultModels[provider]
}

// AllModels returns the user-visible models from all configured providers:
// the provider's favourites if the admin selected any (in selection order),
// otherwise all fetched models.
func (r *ModelRegistry) AllModels() []Model {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []Model
	for name := range r.configured {
		models := r.dynamicModels[name]
		if favs := r.favourites[name]; len(favs) > 0 {
			byID := make(map[string]Model, len(models))
			for _, m := range models {
				byID[m.ID] = m
			}
			selected := make([]Model, 0, len(favs))
			for _, id := range favs {
				if m, ok := byID[id]; ok {
					selected = append(selected, m)
				} else {
					// Favourite no longer in the fetched list — keep it
					// selectable rather than silently dropping it.
					selected = append(selected, Model{ID: id, Name: id})
				}
			}
			models = selected
		}
		for _, m := range models {
			m.Provider = name
			out = append(out, m)
		}
	}
	return out
}

// fullModels returns all fetched models of configured providers, ignoring
// favourites. Used for validation and routing so that a configured default
// model outside the favourites still resolves.
func (r *ModelRegistry) fullModels() []Model {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []Model
	for name := range r.configured {
		for _, m := range r.dynamicModels[name] {
			m.Provider = name
			out = append(out, m)
		}
	}
	return out
}

// ValidModel returns true if the given model ID is known across any provider.
func (r *ModelRegistry) ValidModel(id string) bool {
	for _, m := range r.fullModels() {
		if m.ID == id {
			return true
		}
	}
	return false
}

// ProviderForModel returns the provider type that owns the given model ID, or "" if unknown.
func (r *ModelRegistry) ProviderForModel(id string) string {
	for _, m := range r.fullModels() {
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
		r.fetchProvider(ctx, name, fetcher)
	}
}

// FetchProvider fetches models for a single provider if a fetcher is
// registered. Returns the fetched models (or the cached ones on error).
func (r *ModelRegistry) FetchProvider(ctx context.Context, name string) []Model {
	r.mu.RLock()
	fetcher := r.fetchers[name]
	r.mu.RUnlock()
	if fetcher != nil {
		r.fetchProvider(ctx, name, fetcher)
	}
	return r.Models(name)
}

func (r *ModelRegistry) fetchProvider(ctx context.Context, name string, fetcher ModelFetcher) {
	models, err := fetcher.FetchModels(ctx)
	if err != nil {
		log.WithError(err).WithField("provider", name).Warn("failed to fetch models dynamically")
		return
	}
	sort.Slice(models, func(i, j int) bool { return models[i].Name < models[j].Name })
	r.mu.Lock()
	r.dynamicModels[name] = models
	r.mu.Unlock()
	log.WithFields(log.Fields{"provider": name, "count": len(models)}).Info("fetched dynamic models")
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
