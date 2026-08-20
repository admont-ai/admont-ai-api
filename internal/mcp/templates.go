package mcp

import (
	"embed"
	"html/template"
)

//go:embed templates/*.html
var templateFS embed.FS

var mcpTemplates = template.Must(template.ParseFS(templateFS, "templates/*.html"))
