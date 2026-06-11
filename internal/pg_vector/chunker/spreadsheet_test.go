package chunker

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/xuri/excelize/v2"
)

func TestChunkCSV_HeaderPairs(t *testing.T) {
	csvData := []byte("Name,Role,Status\nAlice,Developer,Active\nBob,Designer,Pending\n")
	chunks, err := ChunkCSV(csvData)
	require.NoError(t, err)
	require.Len(t, chunks, 1)

	assert.Equal(t, "rows 2–3", chunks[0].HeadingPath)
	assert.Contains(t, chunks[0].Content, "Name: Alice | Role: Developer | Status: Active")
	assert.Contains(t, chunks[0].Content, "Name: Bob | Role: Designer | Status: Pending")
	assert.Equal(t, 0, chunks[0].Index)
}

func TestChunkCSV_RaggedRowsAndEmptyCells(t *testing.T) {
	csvData := []byte("A,B,C\n1,,3\n4,5\n")
	chunks, err := ChunkCSV(csvData)
	require.NoError(t, err)
	require.Len(t, chunks, 1)

	assert.Contains(t, chunks[0].Content, "A: 1 | C: 3")
	assert.Contains(t, chunks[0].Content, "A: 4 | B: 5")
}

func TestChunkCSV_ChunkSizeBoundary(t *testing.T) {
	var sb strings.Builder
	sb.WriteString("ID,Description\n")
	for i := 0; i < 50; i++ {
		sb.WriteString("x,")
		sb.WriteString(strings.Repeat("y", 100))
		sb.WriteString("\n")
	}
	chunks, err := ChunkCSV([]byte(sb.String()))
	require.NoError(t, err)
	assert.Greater(t, len(chunks), 1, "long content must split into multiple chunks")

	for i, c := range chunks {
		assert.LessOrEqual(t, len(c.Content), maxChunkChars+150, "chunk %d too large", i)
		assert.Equal(t, i, c.Index)
		// Every chunk's rows stay self-describing with header names.
		assert.Contains(t, c.Content, "Description: ")
	}
}

func TestChunkCSV_Empty(t *testing.T) {
	chunks, err := ChunkCSV([]byte(""))
	require.NoError(t, err)
	assert.Empty(t, chunks)

	chunks, err = ChunkCSV([]byte("OnlyHeader,Columns\n"))
	require.NoError(t, err)
	assert.Empty(t, chunks)
}

func TestChunkExcel_MultiSheet(t *testing.T) {
	f := excelize.NewFile()
	require.NoError(t, f.SetSheetName("Sheet1", "People"))
	require.NoError(t, f.SetSheetRow("People", "A1", &[]any{"Name", "Role"}))
	require.NoError(t, f.SetSheetRow("People", "A2", &[]any{"Alice", "Developer"}))

	_, err := f.NewSheet("Budget")
	require.NoError(t, err)
	require.NoError(t, f.SetSheetRow("Budget", "A1", &[]any{"Item", "Cost"}))
	require.NoError(t, f.SetSheetRow("Budget", "A2", &[]any{"Laptop", "1200"}))

	var buf bytes.Buffer
	require.NoError(t, f.Write(&buf))

	chunks, err := ChunkExcel(buf.Bytes())
	require.NoError(t, err)
	require.Len(t, chunks, 2)

	assert.Equal(t, "People · rows 2–2", chunks[0].HeadingPath)
	assert.Equal(t, "Name: Alice | Role: Developer", chunks[0].Content)
	assert.Equal(t, "Budget · rows 2–2", chunks[1].HeadingPath)
	assert.Equal(t, "Item: Laptop | Cost: 1200", chunks[1].Content)
	// Indices are sequential across sheets.
	assert.Equal(t, 0, chunks[0].Index)
	assert.Equal(t, 1, chunks[1].Index)
}

func TestChunkExcel_InvalidData(t *testing.T) {
	_, err := ChunkExcel([]byte("not an xlsx file"))
	assert.Error(t, err)
}
