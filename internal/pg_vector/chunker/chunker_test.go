package chunker

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestChunkMarkdown_Simple(t *testing.T) {
	md := []byte(`# Introduction

This is the introduction paragraph.

# Getting Started

Follow these steps to get started.

## Prerequisites

You need Go 1.25 installed.

## Installation

Run go install to install the tool.
`)

	chunks := ChunkMarkdown(md)

	require.GreaterOrEqual(t, len(chunks), 4)

	// First chunk should be under "Introduction"
	assert.Equal(t, "Introduction", chunks[0].HeadingPath)
	assert.Contains(t, chunks[0].Content, "introduction paragraph")
	assert.Equal(t, 0, chunks[0].Index)

	// "Getting Started"
	assert.Equal(t, "Getting Started", chunks[1].HeadingPath)
	assert.Contains(t, chunks[1].Content, "Follow these steps")

	// "Getting Started > Prerequisites"
	assert.Equal(t, "Getting Started > Prerequisites", chunks[2].HeadingPath)
	assert.Contains(t, chunks[2].Content, "Go 1.25")

	// "Getting Started > Installation"
	assert.Equal(t, "Getting Started > Installation", chunks[3].HeadingPath)
	assert.Contains(t, chunks[3].Content, "go install")
}

func TestChunkMarkdown_ContentBeforeFirstHeading(t *testing.T) {
	md := []byte(`Some preamble text before any heading.

# First Heading

Content under first heading.
`)

	chunks := ChunkMarkdown(md)

	require.GreaterOrEqual(t, len(chunks), 2)

	// First chunk: preamble with empty heading path
	assert.Equal(t, "", chunks[0].HeadingPath)
	assert.Contains(t, chunks[0].Content, "preamble text")

	// Second chunk: under heading
	assert.Equal(t, "First Heading", chunks[1].HeadingPath)
}

func TestChunkMarkdown_LongSection(t *testing.T) {
	// Create a section with >1000 chars by creating multiple paragraphs
	var sb strings.Builder
	sb.WriteString("# Long Section\n\n")
	for i := 0; i < 20; i++ {
		sb.WriteString("This is paragraph number that contains enough text to eventually exceed the character limit. ")
		sb.WriteString("It has multiple sentences to make it realistic.\n\n")
	}

	chunks := ChunkMarkdown([]byte(sb.String()))

	// Should have been split into multiple chunks
	require.Greater(t, len(chunks), 1)

	// All chunks should have the same heading path
	for _, c := range chunks {
		assert.Equal(t, "Long Section", c.HeadingPath)
	}

	// Indices should be sequential
	for i, c := range chunks {
		assert.Equal(t, i, c.Index)
	}
}

func TestChunkMarkdown_EmptyDocument(t *testing.T) {
	chunks := ChunkMarkdown([]byte(""))
	assert.Empty(t, chunks)
}

func TestChunkMarkdown_HeadingHierarchy(t *testing.T) {
	md := []byte(`# H1

Text under H1.

## H2

Text under H2.

### H3

Text under H3.

## Another H2

Text under another H2.
`)

	chunks := ChunkMarkdown(md)

	require.GreaterOrEqual(t, len(chunks), 4)

	assert.Equal(t, "H1", chunks[0].HeadingPath)
	assert.Equal(t, "H1 > H2", chunks[1].HeadingPath)
	assert.Equal(t, "H1 > H2 > H3", chunks[2].HeadingPath)
	assert.Equal(t, "H1 > Another H2", chunks[3].HeadingPath)
}

func TestChunkMarkdown_CodeBlock(t *testing.T) {
	md := []byte("# Code Example\n\nSome text.\n\n```go\nfunc main() {\n\tfmt.Println(\"hello\")\n}\n```\n")

	chunks := ChunkMarkdown(md)

	require.GreaterOrEqual(t, len(chunks), 1)
	assert.Contains(t, chunks[0].Content, "func main()")
}
