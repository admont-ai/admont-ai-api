package backend

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
)

type stubBackend struct {
	name string
}

func (s *stubBackend) Name() string { return s.name }
func (s *stubBackend) Initialize(ctx context.Context) error {
	return nil
}
func (s *stubBackend) Close() error { return nil }
func (s *stubBackend) UpsertChunks(ctx context.Context, chunks []Chunk) error {
	return nil
}
func (s *stubBackend) DeleteFileChunks(ctx context.Context, repoSlug, filePath string) error {
	return nil
}
func (s *stubBackend) DeleteRepoChunks(ctx context.Context, repoSlug string) error {
	return nil
}
func (s *stubBackend) FulltextSearch(ctx context.Context, repos []string, query, pathPrefix string, topK int, threshold float64) ([]SearchResult, error) {
	return nil, nil
}
func (s *stubBackend) SemanticSearch(ctx context.Context, repos []string, query, pathPrefix string, topK int, threshold float64) ([]SearchResult, error) {
	return nil, nil
}
func (s *stubBackend) HybridSearch(ctx context.Context, repos []string, query, pathPrefix string, topK int, threshold float64) ([]SearchResult, error) {
	return nil, nil
}

func TestHolder_NilInitial(t *testing.T) {
	h := NewHolder(nil)
	assert.Nil(t, h.Get())
}

func TestHolder_WithInitial(t *testing.T) {
	sb := &stubBackend{name: "initial"}
	h := NewHolder(sb)
	assert.Equal(t, sb, h.Get())
}

func TestHolder_Swap(t *testing.T) {
	sb1 := &stubBackend{name: "first"}
	sb2 := &stubBackend{name: "second"}

	h := NewHolder(sb1)
	assert.Equal(t, sb1, h.Get())

	old := h.Swap(sb2)
	assert.Equal(t, sb1, old)
	assert.Equal(t, sb2, h.Get())
}

func TestHolder_SwapToNil(t *testing.T) {
	sb := &stubBackend{name: "backend"}
	h := NewHolder(sb)

	old := h.Swap(nil)
	assert.Equal(t, sb, old)
	assert.Nil(t, h.Get())
}

func TestHolder_SwapFromNil(t *testing.T) {
	h := NewHolder(nil)

	sb := &stubBackend{name: "new"}
	old := h.Swap(sb)
	assert.Nil(t, old)
	assert.Equal(t, sb, h.Get())
}

func TestHolder_MultipleSwaps(t *testing.T) {
	h := NewHolder(nil)

	for i := 0; i < 5; i++ {
		sb := &stubBackend{name: "backend"}
		h.Swap(sb)
	}

	assert.NotNil(t, h.Get())
}
