package confluence

import (
	"regexp"
	"strings"
)

// filenamePattern matches Confluence HTML export filenames like "My-Page-Title_12345.html"
// or "My-Page-Title_12345_1.html" (versioned).
var filenamePattern = regexp.MustCompile(`^(.+)_(\d+)(?:_\d+)?\.html$`)

// ParseConfluenceFilename extracts the title and page ID from a Confluence export filename.
// Returns empty strings if the filename doesn't match the expected pattern.
func ParseConfluenceFilename(name string) (title string, pageID string) {
	m := filenamePattern.FindStringSubmatch(name)
	if m == nil {
		return "", ""
	}
	// The title part uses hyphens where spaces were; restore spaces.
	title = strings.ReplaceAll(m[1], "-", " ")
	pageID = m[2]
	return title, pageID
}

var sanitizeRe = regexp.MustCompile(`[^a-z0-9]+`)
var trailingDash = regexp.MustCompile(`-+$`)
var leadingDash = regexp.MustCompile(`^-+`)

// SanitizeFilename converts a page title to a clean filename slug.
// Example: "Getting Started!" → "getting-started"
func SanitizeFilename(title string) string {
	s := strings.ToLower(strings.TrimSpace(title))
	s = sanitizeRe.ReplaceAllString(s, "-")
	s = leadingDash.ReplaceAllString(s, "")
	s = trailingDash.ReplaceAllString(s, "")
	if s == "" {
		s = "untitled"
	}
	return s
}
