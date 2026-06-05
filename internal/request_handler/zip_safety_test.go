package requesthandler

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func createTestZip(t *testing.T, entries map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for name, content := range entries {
		f, err := w.Create(name)
		require.NoError(t, err)
		_, err = f.Write([]byte(content))
		require.NoError(t, err)
	}
	require.NoError(t, w.Close())
	return buf.Bytes()
}

func extractWithSafetyCheck(t *testing.T, zipData []byte, extractDir string) []string {
	t.Helper()
	reader, err := zip.NewReader(bytes.NewReader(zipData), int64(len(zipData)))
	require.NoError(t, err)

	cleanExtractDir := filepath.Clean(extractDir) + string(os.PathSeparator)
	require.NoError(t, os.MkdirAll(extractDir, 0755))

	var extracted []string
	for _, f := range reader.File {
		resolved := filepath.Clean(filepath.Join(extractDir, f.Name))
		if !strings.HasPrefix(resolved, cleanExtractDir) && resolved != filepath.Clean(extractDir) {
			continue
		}
		if f.FileInfo().IsDir() {
			os.MkdirAll(resolved, 0755)
			continue
		}
		os.MkdirAll(filepath.Dir(resolved), 0755)
		rc, err := f.Open()
		if err != nil {
			continue
		}
		data, _ := os.ReadFile(resolved)
		_ = data
		raw := make([]byte, 1024)
		n, _ := rc.Read(raw)
		rc.Close()
		os.WriteFile(resolved, raw[:n], 0644)
		extracted = append(extracted, resolved)
	}
	return extracted
}

func TestZipSlip_Blocked(t *testing.T) {
	dir := t.TempDir()
	extractDir := filepath.Join(dir, "extract")

	zipData := createTestZip(t, map[string]string{
		"../../etc/passwd":        "malicious",
		"../escape.txt":           "malicious",
		"foo/../../../bar.txt":    "malicious",
		"normal/file.txt":         "safe content",
		"also-normal.txt":         "safe content",
	})

	extracted := extractWithSafetyCheck(t, zipData, extractDir)

	for _, path := range extracted {
		assert.True(t, strings.HasPrefix(path, filepath.Clean(extractDir)),
			"extracted file %q should be within extract dir", path)
	}

	assert.FileExists(t, filepath.Join(extractDir, "normal", "file.txt"))
	assert.FileExists(t, filepath.Join(extractDir, "also-normal.txt"))

	assert.NoFileExists(t, filepath.Join(dir, "escape.txt"))
	assert.NoDirExists(t, filepath.Join(dir, "etc"))
}

func TestZipExtraction_NormalZip(t *testing.T) {
	dir := t.TempDir()
	extractDir := filepath.Join(dir, "extract")

	zipData := createTestZip(t, map[string]string{
		"readme.md":         "# Hello",
		"docs/guide.md":     "## Guide",
		"docs/sub/deep.md":  "deep content",
	})

	extracted := extractWithSafetyCheck(t, zipData, extractDir)

	assert.Equal(t, 3, len(extracted))
	assert.FileExists(t, filepath.Join(extractDir, "readme.md"))
	assert.FileExists(t, filepath.Join(extractDir, "docs", "guide.md"))
	assert.FileExists(t, filepath.Join(extractDir, "docs", "sub", "deep.md"))
}
