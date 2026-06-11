package chunker

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"strings"

	"github.com/xuri/excelize/v2"
)

// ChunkExcel extracts the sheets of an xlsx workbook into searchable chunks.
// Each chunk holds a group of rows rendered as self-describing
// "Header: value | …" lines so it stays meaningful in isolation.
func ChunkExcel(source []byte) ([]Chunk, error) {
	f, err := excelize.OpenReader(bytes.NewReader(source))
	if err != nil {
		return nil, fmt.Errorf("opening xlsx: %w", err)
	}
	defer f.Close()

	var chunks []Chunk
	for _, sheet := range f.GetSheetList() {
		rows, err := f.GetRows(sheet)
		if err != nil {
			return nil, fmt.Errorf("reading sheet %q: %w", sheet, err)
		}
		chunks = append(chunks, chunkRows(sheet, rows, len(chunks))...)
	}
	return chunks, nil
}

// ChunkCSV extracts a CSV file into searchable chunks (see ChunkExcel).
func ChunkCSV(source []byte) ([]Chunk, error) {
	r := csv.NewReader(bytes.NewReader(source))
	r.FieldsPerRecord = -1 // tolerate ragged rows
	rows, err := r.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("parsing csv: %w", err)
	}
	return chunkRows("", rows, 0), nil
}

// chunkRows groups data rows into chunks of at most maxChunkChars. The first
// non-empty row is treated as the header row and each data row is rendered as
// "Header: value | …" pairs; rows are never split across chunks.
func chunkRows(sheetName string, rows [][]string, startIndex int) []Chunk {
	// Find the header row (first row with any non-empty cell).
	headerIdx := -1
	for i, row := range rows {
		if !rowEmpty(row) {
			headerIdx = i
			break
		}
	}
	if headerIdx == -1 {
		return nil
	}
	header := rows[headerIdx]

	var chunks []Chunk
	var accum strings.Builder
	firstRow := 0 // 1-based spreadsheet row number of the first data row in accum
	lastRow := 0

	flush := func() {
		if accum.Len() == 0 {
			return
		}
		heading := fmt.Sprintf("rows %d–%d", firstRow, lastRow)
		if sheetName != "" {
			heading = sheetName + " · " + heading
		}
		chunks = append(chunks, Chunk{
			HeadingPath: heading,
			Content:     strings.TrimRight(accum.String(), "\n"),
			Index:       startIndex + len(chunks),
		})
		accum.Reset()
	}

	for i := headerIdx + 1; i < len(rows); i++ {
		row := rows[i]
		if rowEmpty(row) {
			continue
		}
		line := renderRow(header, row)
		if accum.Len() > 0 && accum.Len()+len(line) > maxChunkChars {
			flush()
		}
		if accum.Len() == 0 {
			firstRow = i + 1
		}
		lastRow = i + 1
		accum.WriteString(line)
		accum.WriteString("\n")
	}
	flush()
	return chunks
}

// renderRow formats a data row as "Header: value | …", falling back to plain
// "v1 | v2" for cells without a header.
func renderRow(header, row []string) string {
	parts := make([]string, 0, len(row))
	for i, cell := range row {
		cell = strings.TrimSpace(cell)
		if cell == "" {
			continue
		}
		if i < len(header) && strings.TrimSpace(header[i]) != "" {
			parts = append(parts, strings.TrimSpace(header[i])+": "+cell)
		} else {
			parts = append(parts, cell)
		}
	}
	return strings.Join(parts, " | ")
}

func rowEmpty(row []string) bool {
	for _, c := range row {
		if strings.TrimSpace(c) != "" {
			return false
		}
	}
	return true
}
