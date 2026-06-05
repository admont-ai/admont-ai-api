package chunker

import (
	"bytes"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/text"
)

const maxChunkChars = 1000

type Chunk struct {
	HeadingPath string
	Content     string
	Index       int
}

// ChunkMarkdown splits markdown content into chunks based on headings.
func ChunkMarkdown(source []byte) []Chunk {
	md := goldmark.New()
	reader := text.NewReader(source)
	doc := md.Parser().Parse(reader)

	var chunks []Chunk
	var headingStack []headingEntry // stack tracking current heading hierarchy
	var accum strings.Builder       // accumulated text for current section
	chunkIdx := 0

	// Walk through the AST
	ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}

		if heading, ok := n.(*ast.Heading); ok {
			// Flush accumulated text as chunk(s)
			if accum.Len() > 0 {
				flushChunks(&chunks, &chunkIdx, buildHeadingPath(headingStack), accum.String())
				accum.Reset()
			}

			// Update heading stack
			level := heading.Level
			// Pop headings at same or deeper level
			for len(headingStack) > 0 && headingStack[len(headingStack)-1].level >= level {
				headingStack = headingStack[:len(headingStack)-1]
			}
			headingStack = append(headingStack, headingEntry{
				level: level,
				text:  extractText(heading, source),
			})

			return ast.WalkSkipChildren, nil
		}

		// Accumulate text from leaf/block nodes
		if n.Type() == ast.TypeBlock && n.Kind() != ast.KindDocument {
			nodeText := extractBlockText(n, source)
			if nodeText != "" {
				if accum.Len() > 0 {
					accum.WriteString("\n\n")
				}
				accum.WriteString(nodeText)
			}
			return ast.WalkSkipChildren, nil
		}

		return ast.WalkContinue, nil
	})

	// Flush remaining text
	if accum.Len() > 0 {
		flushChunks(&chunks, &chunkIdx, buildHeadingPath(headingStack), accum.String())
	}

	return chunks
}

type headingEntry struct {
	level int
	text  string
}

func buildHeadingPath(stack []headingEntry) string {
	if len(stack) == 0 {
		return ""
	}
	parts := make([]string, len(stack))
	for i, h := range stack {
		parts[i] = h.text
	}
	return strings.Join(parts, " > ")
}

func extractText(n ast.Node, source []byte) string {
	var buf bytes.Buffer
	for child := n.FirstChild(); child != nil; child = child.NextSibling() {
		if t, ok := child.(*ast.Text); ok {
			buf.Write(t.Segment.Value(source))
		}
	}
	return buf.String()
}

func extractBlockText(n ast.Node, source []byte) string {
	var buf bytes.Buffer
	ast.Walk(n, func(child ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch c := child.(type) {
		case *ast.Text:
			buf.Write(c.Segment.Value(source))
			if c.SoftLineBreak() || c.HardLineBreak() {
				buf.WriteByte('\n')
			}
		case *ast.CodeSpan:
			// Extract inline code
			for i := 0; i < c.ChildCount(); i++ {
				seg := c.FirstChild()
				if seg != nil {
					if t, ok := seg.(*ast.Text); ok {
						buf.Write(t.Segment.Value(source))
					}
				}
			}
			return ast.WalkSkipChildren, nil
		case *ast.FencedCodeBlock:
			lines := c.Lines()
			for i := 0; i < lines.Len(); i++ {
				seg := lines.At(i)
				buf.Write(seg.Value(source))
			}
			return ast.WalkSkipChildren, nil
		case *ast.CodeBlock:
			lines := c.Lines()
			for i := 0; i < lines.Len(); i++ {
				seg := lines.At(i)
				buf.Write(seg.Value(source))
			}
			return ast.WalkSkipChildren, nil
		}
		return ast.WalkContinue, nil
	})
	return strings.TrimSpace(buf.String())
}

// flushChunks creates one or more chunks from the given text.
// If the text exceeds maxChunkChars, it splits at paragraph boundaries.
func flushChunks(chunks *[]Chunk, idx *int, headingPath, text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}

	if len(text) <= maxChunkChars {
		*chunks = append(*chunks, Chunk{
			HeadingPath: headingPath,
			Content:     text,
			Index:       *idx,
		})
		*idx++
		return
	}

	// Split at paragraph boundaries (double newline)
	paragraphs := strings.Split(text, "\n\n")
	var current strings.Builder

	for _, p := range paragraphs {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}

		if current.Len() > 0 && current.Len()+len(p)+2 > maxChunkChars {
			// Flush current accumulation
			*chunks = append(*chunks, Chunk{
				HeadingPath: headingPath,
				Content:     strings.TrimSpace(current.String()),
				Index:       *idx,
			})
			*idx++
			current.Reset()
		}

		if current.Len() > 0 {
			current.WriteString("\n\n")
		}
		current.WriteString(p)
	}

	// Flush remaining
	if current.Len() > 0 {
		*chunks = append(*chunks, Chunk{
			HeadingPath: headingPath,
			Content:     strings.TrimSpace(current.String()),
			Index:       *idx,
		})
		*idx++
	}
}
