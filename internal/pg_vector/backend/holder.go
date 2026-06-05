package backend

import "sync"

// Holder provides thread-safe access to the active SearchBackend.
// All consumers (indexer, search handler, MCP server) use Holder.Get() per operation.
type Holder struct {
	mu      sync.RWMutex
	current SearchBackend
}

// NewHolder creates a Holder with an initial backend (may be nil).
func NewHolder(initial SearchBackend) *Holder {
	return &Holder{current: initial}
}

// Get returns the current SearchBackend. May return nil if no backend is active.
func (h *Holder) Get() SearchBackend {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.current
}

// Swap atomically replaces the current backend and returns the old one.
// The caller is responsible for closing the old backend.
func (h *Holder) Swap(newBackend SearchBackend) (old SearchBackend) {
	h.mu.Lock()
	defer h.mu.Unlock()
	old = h.current
	h.current = newBackend
	return old
}
