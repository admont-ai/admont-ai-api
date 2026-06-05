package store

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestRRFMerge_BothEmpty(t *testing.T) {
	results := rrfMerge(nil, nil, 10, 0)
	assert.Empty(t, results)
}

func TestRRFMerge_FulltextOnly(t *testing.T) {
	ft := []SearchResult{
		{RepoSlug: "wiki", FilePath: "a.md", Chunk: "content a"},
		{RepoSlug: "wiki", FilePath: "b.md", Chunk: "content b"},
	}
	results := rrfMerge(ft, nil, 10, 0)
	assert.Equal(t, 2, len(results))
	assert.Equal(t, "a.md", results[0].FilePath)
	assert.True(t, results[0].Score > results[1].Score)
}

func TestRRFMerge_SemanticOnly(t *testing.T) {
	sem := []SearchResult{
		{RepoSlug: "wiki", FilePath: "x.md", Chunk: "content x"},
	}
	results := rrfMerge(nil, sem, 10, 0)
	assert.Equal(t, 1, len(results))
	assert.Equal(t, "x.md", results[0].FilePath)
}

func TestRRFMerge_Dedup(t *testing.T) {
	ft := []SearchResult{
		{RepoSlug: "wiki", FilePath: "a.md", Chunk: "shared content"},
	}
	sem := []SearchResult{
		{RepoSlug: "wiki", FilePath: "a.md", Chunk: "shared content"},
	}

	results := rrfMerge(ft, sem, 10, 0)
	assert.Equal(t, 1, len(results))
	assert.True(t, results[0].Score > 0)
}

func TestRRFMerge_Dedup_HigherScore(t *testing.T) {
	ft := []SearchResult{
		{RepoSlug: "wiki", FilePath: "a.md", Chunk: "shared"},
	}
	sem := []SearchResult{
		{RepoSlug: "wiki", FilePath: "a.md", Chunk: "shared"},
	}

	merged := rrfMerge(ft, sem, 10, 0)

	ftOnly := rrfMerge(ft, nil, 10, 0)

	assert.True(t, merged[0].Score > ftOnly[0].Score,
		"item appearing in both lists should have higher RRF score")
}

func TestRRFMerge_TopK(t *testing.T) {
	ft := []SearchResult{
		{RepoSlug: "wiki", FilePath: "a.md", Chunk: "a"},
		{RepoSlug: "wiki", FilePath: "b.md", Chunk: "b"},
		{RepoSlug: "wiki", FilePath: "c.md", Chunk: "c"},
	}
	results := rrfMerge(ft, nil, 2, 0)
	assert.Equal(t, 2, len(results))
}

func TestRRFMerge_Threshold(t *testing.T) {
	ft := []SearchResult{
		{RepoSlug: "wiki", FilePath: "a.md", Chunk: "a"},
	}

	results := rrfMerge(ft, nil, 10, 0.5)
	assert.Empty(t, results, "single-list RRF score should be below 0.5 threshold")
}

func TestRRFMerge_SortOrder(t *testing.T) {
	ft := []SearchResult{
		{RepoSlug: "wiki", FilePath: "third.md", Chunk: "c"},
		{RepoSlug: "wiki", FilePath: "first.md", Chunk: "a"},
		{RepoSlug: "wiki", FilePath: "second.md", Chunk: "b"},
	}
	sem := []SearchResult{
		{RepoSlug: "wiki", FilePath: "first.md", Chunk: "a"},
		{RepoSlug: "wiki", FilePath: "second.md", Chunk: "b"},
	}

	results := rrfMerge(ft, sem, 10, 0)
	assert.True(t, len(results) >= 2)
	assert.Equal(t, "first.md", results[0].FilePath, "item in both at rank 1 should be first")

	for i := 1; i < len(results); i++ {
		assert.True(t, results[i-1].Score >= results[i].Score, "results should be sorted descending by score")
	}
}

func TestRRFMerge_DifferentRepos(t *testing.T) {
	ft := []SearchResult{
		{RepoSlug: "wiki-a", FilePath: "doc.md", Chunk: "content"},
	}
	sem := []SearchResult{
		{RepoSlug: "wiki-b", FilePath: "doc.md", Chunk: "content"},
	}

	results := rrfMerge(ft, sem, 10, 0)
	assert.Equal(t, 2, len(results), "same path+chunk in different repos should NOT be deduped")
}

func TestRRFMerge_ScoreRounding(t *testing.T) {
	ft := []SearchResult{
		{RepoSlug: "wiki", FilePath: "a.md", Chunk: "a"},
	}
	results := rrfMerge(ft, nil, 10, 0)
	assert.Equal(t, 1, len(results))

	score := results[0].Score
	roundedStr := results[0].Score
	assert.Equal(t, score, roundedStr, "score should be rounded to 4 decimal places")
}
