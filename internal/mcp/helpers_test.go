package mcp

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestIsDotPath(t *testing.T) {
	tests := []struct {
		name string
		path string
		want bool
	}{
		{"normal file", "docs/readme.md", false},
		{"root file", "readme.md", false},
		{"git dir", ".git/config", true},
		{"drafts dir", ".drafts/file.md", true},
		{"nested dot dir", "docs/.hidden/file.md", true},
		{"dot file at root", ".gitignore", true},
		{"deeply nested dot", "a/b/c/.secret/d.md", true},
		{"empty", "", false},
		{"single dot", ".", true},
		{"double dot", "..", true},
		{"file starting with dot", ".env", true},
		{"dir with dot in name", "my.folder/readme.md", false},
		{"file with dots", "my.file.name.md", false},
		{"trailing slash dot", "docs/.hidden/", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, isDotPath(tt.path))
		})
	}
}

func TestSplitPath(t *testing.T) {
	tests := []struct {
		name       string
		path       string
		wantFolder string
		wantFile   string
	}{
		{"simple file", "readme.md", "", "readme.md"},
		{"nested file", "docs/readme.md", "docs", "readme.md"},
		{"deeply nested", "a/b/c/file.md", "a/b/c", "file.md"},
		{"single component", "file.txt", "", "file.txt"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			folder, file := splitPath(tt.path)
			assert.Equal(t, tt.wantFolder, folder)
			assert.Equal(t, tt.wantFile, file)
		})
	}
}
