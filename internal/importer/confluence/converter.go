package confluence

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"strings"

	htmltomarkdown "github.com/JohannesKaufmann/html-to-markdown/v2"
	"golang.org/x/net/html"
)

// ImportedPage represents a converted page ready to be written to the repo.
type ImportedPage struct {
	Path    string // relative path in repo, e.g. "docs/getting-started.md"
	Content []byte
}

// ImportedAttachment represents an attachment file extracted from the zip.
type ImportedAttachment struct {
	Path    string // relative path in repo, e.g. "docs/getting-started/assets/image.png"
	Content []byte
}

// ImportError records a non-fatal error during import.
type ImportError struct {
	File    string `json:"file"`
	Message string `json:"message"`
}

// OrderFile represents a .order file to write for a directory.
type OrderFile struct {
	Dir     string   // directory path (e.g. "docs/getting-started")
	Entries []string // ordered entry names (files and folders)
}

// ImportResult holds the outcome of a Confluence import.
type ImportResult struct {
	Pages       []ImportedPage
	Attachments []ImportedAttachment
	OrderFiles  []OrderFile
	Errors      []ImportError
}

// pageInfo is intermediate metadata for a page being imported.
type pageInfo struct {
	zipFile  *zip.File
	title    string
	pageID   string
	slug     string   // sanitized filename without extension
	dirPath  string   // parent directory path in output (e.g. "docs/getting-started")
	mdPath   string   // full markdown path (e.g. "docs/getting-started/installation-guide.md")
	children []string // page IDs of children
}

// Import processes a Confluence HTML space export zip and returns converted pages and attachments.
func Import(zipReader *zip.Reader, targetPath string) (*ImportResult, error) {
	result := &ImportResult{}

	// Detect and strip a common root directory prefix.
	// Many zip tools wrap the export in a single top-level directory (e.g. "SD/index.html").
	prefix := detectZipPrefix(zipReader)

	// Build a name→file index with the prefix stripped.
	zipIndex := make(map[string]*zip.File, len(zipReader.File))
	for _, f := range zipReader.File {
		name := f.Name
		if prefix != "" {
			name = strings.TrimPrefix(name, prefix)
			if name == "" {
				continue // the prefix directory entry itself
			}
		}
		zipIndex[name] = f
	}

	// Step 1: Parse hierarchy from index.html.
	var hierarchy *PageNode
	if indexFile, ok := zipIndex["index.html"]; ok {
		data, err := readZipFile(indexFile)
		if err != nil {
			result.Errors = append(result.Errors, ImportError{File: "index.html", Message: err.Error()})
		} else {
			hierarchy, err = ParseHierarchy(data)
			if err != nil {
				result.Errors = append(result.Errors, ImportError{File: "index.html", Message: err.Error()})
			}
		}
	}

	// Step 2: Build page info map from zip entries.
	pages := make(map[string]*pageInfo) // pageID -> pageInfo
	hrefToPageID := make(map[string]string)

	for stripped, f := range zipIndex {
		if f.FileInfo().IsDir() {
			continue
		}
		name := filepath.Base(stripped)
		if !strings.HasSuffix(name, ".html") {
			continue
		}
		if name == "index.html" {
			continue
		}
		// Skip Confluence style/image directories
		dir := filepath.Dir(stripped)
		if dir == "styles" || dir == "images" {
			continue
		}

		title, pageID := ParseConfluenceFilename(name)
		if pageID == "" {
			// Not a standard Confluence page filename — try to use as-is.
			title = strings.TrimSuffix(name, ".html")
			pageID = name // use filename as key
		}

		slug := SanitizeFilename(title)
		pages[pageID] = &pageInfo{
			zipFile: f,
			title:   title,
			pageID:  pageID,
			slug:    slug,
		}
		hrefToPageID[name] = pageID
	}

	// Step 3: Assign directory paths from hierarchy and collect ordering.
	if hierarchy != nil {
		result.OrderFiles = assignPaths(hierarchy.Children, targetPath, pages, hrefToPageID)
	}

	// Assign flat paths to any pages not found in the hierarchy.
	for _, p := range pages {
		if p.mdPath == "" {
			p.dirPath = targetPath
			p.mdPath = joinPath(targetPath, p.slug+".md")
		}
	}

	// Step 4: Build filename mapping for link rewriting (old href → new md path).
	linkMap := make(map[string]string)
	for _, p := range pages {
		if href := filepath.Base(p.zipFile.Name); href != "" {
			linkMap[href] = p.mdPath
		}
	}

	// Step 5: Convert each page.
	for _, p := range pages {
		data, err := readZipFile(p.zipFile)
		if err != nil {
			result.Errors = append(result.Errors, ImportError{File: p.zipFile.Name, Message: err.Error()})
			continue
		}

		bodyHTML, err := extractBody(data)
		if err != nil {
			result.Errors = append(result.Errors, ImportError{File: p.zipFile.Name, Message: err.Error()})
			continue
		}

		md, err := htmltomarkdown.ConvertString(bodyHTML)
		if err != nil {
			result.Errors = append(result.Errors, ImportError{File: p.zipFile.Name, Message: fmt.Sprintf("markdown conversion: %v", err)})
			continue
		}

		// Rewrite inter-page links.
		md = rewriteLinks(md, p.mdPath, linkMap)

		// Rewrite attachment references.
		md = rewriteAttachmentRefs(md, p, targetPath)

		// Add title as H1 if not already present.
		if !strings.HasPrefix(strings.TrimSpace(md), "# ") {
			md = "# " + p.title + "\n\n" + md
		}

		result.Pages = append(result.Pages, ImportedPage{
			Path:    p.mdPath,
			Content: []byte(md),
		})
	}

	// Step 6: Collect attachments.
	// Confluence exports use several attachment directory layouts:
	//   - attachments/<pageId>/<filename>
	//   - download/attachments/<pageId>/<filename>
	//   - images/<filename>
	for stripped, f := range zipIndex {
		if f.FileInfo().IsDir() {
			continue
		}

		var pageID, attachName string

		parts := strings.Split(stripped, "/")
		switch {
		case len(parts) >= 3 && parts[0] == "attachments":
			// attachments/<pageId>/rest/of/path
			pageID = parts[1]
			attachName = strings.Join(parts[2:], "/")
		case len(parts) >= 4 && parts[0] == "download" && parts[1] == "attachments":
			// download/attachments/<pageId>/rest/of/path
			pageID = parts[2]
			attachName = strings.Join(parts[3:], "/")
		case len(parts) >= 2 && parts[0] == "images":
			// images/<filename> — shared images, attach to all pages via a top-level assets dir
			attachName = strings.Join(parts[1:], "/")
		default:
			continue
		}

		data, err := readZipFile(f)
		if err != nil {
			result.Errors = append(result.Errors, ImportError{File: f.Name, Message: err.Error()})
			continue
		}

		if pageID != "" {
			// Page-specific attachment.
			page, ok := pages[pageID]
			if !ok {
				// Try to find by iterating pages (some exports use different ID formats).
				for _, p := range pages {
					if p.pageID == pageID {
						page = p
						ok = true
						break
					}
				}
			}
			if !ok {
				result.Errors = append(result.Errors, ImportError{File: f.Name, Message: "no matching page for attachment"})
				continue
			}
			assetPath := joinPath(page.dirPath, "assets", attachName)
			result.Attachments = append(result.Attachments, ImportedAttachment{
				Path:    assetPath,
				Content: data,
			})
		} else {
			// Shared image (from images/ directory) — place in target root assets.
			assetPath := joinPath(targetPath, "assets", attachName)
			result.Attachments = append(result.Attachments, ImportedAttachment{
				Path:    assetPath,
				Content: data,
			})
		}
	}

	return result, nil
}

// assignPaths recursively assigns directory and markdown paths based on hierarchy.
// Returns OrderFile entries for each directory that has children, preserving index.html order.
func assignPaths(nodes []*PageNode, parentDir string, pages map[string]*pageInfo, hrefToPageID map[string]string) []OrderFile {
	var orderFiles []OrderFile
	var dirOrder []string // ordered entry names for parentDir

	for _, node := range nodes {
		pageID := node.PageID
		// If pageID is empty, try to look up by href.
		if pageID == "" && node.Href != "" {
			pageID = hrefToPageID[node.Href]
		}

		p, ok := pages[pageID]
		if !ok {
			// Page in hierarchy but not in zip — skip.
			continue
		}

		if len(node.Children) > 0 {
			// Parent page with children: create a folder and place the page inside it.
			dir := joinPath(parentDir, p.slug)
			p.mdPath = joinPath(dir, p.slug+".md")
			p.dirPath = dir
			dirOrder = append(dirOrder, p.slug)
			childOrderFiles := assignPaths(node.Children, dir, pages, hrefToPageID)
			// Prepend the parent's own md file to the child order for this folder.
			for i, of := range childOrderFiles {
				if of.Dir == dir {
					childOrderFiles[i].Entries = append([]string{p.slug + ".md"}, of.Entries...)
					break
				}
			}
			orderFiles = append(orderFiles, childOrderFiles...)
		} else {
			p.mdPath = joinPath(parentDir, p.slug+".md")
			p.dirPath = parentDir
			dirOrder = append(dirOrder, p.slug+".md")
		}
	}

	if len(dirOrder) > 0 {
		orderFiles = append(orderFiles, OrderFile{Dir: parentDir, Entries: dirOrder})
	}

	return orderFiles
}

// extractBody returns the inner HTML of the <body> element, or the full content
// if no <body> is found.
func extractBody(data []byte) (string, error) {
	doc, err := html.Parse(bytes.NewReader(data))
	if err != nil {
		return "", err
	}

	var body *html.Node
	var findBody func(*html.Node)
	findBody = func(n *html.Node) {
		if body != nil {
			return
		}
		if n.Type == html.ElementNode && n.Data == "body" {
			body = n
			return
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			findBody(c)
		}
	}
	findBody(doc)

	if body == nil {
		return string(data), nil
	}

	// Remove unwanted elements before rendering.
	removeElements(body)

	var buf bytes.Buffer
	for c := body.FirstChild; c != nil; c = c.NextSibling {
		if err := html.Render(&buf, c); err != nil {
			return "", err
		}
	}
	return buf.String(), nil
}

// removeElements removes unwanted HTML elements (footer, etc.) from the tree.
func removeElements(n *html.Node) {
	var toRemove []*html.Node
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == html.ElementNode && c.Data == "div" {
			for _, a := range c.Attr {
				if (a.Key == "id" && a.Val == "footer") ||
					(a.Key == "class" && a.Val == "page-metadata") {
					toRemove = append(toRemove, c)
					break
				}
			}
		}
		removeElements(c)
	}
	for _, c := range toRemove {
		n.RemoveChild(c)
	}
}

// linkPattern matches markdown links: [text](url)
var linkPattern = regexp.MustCompile(`\[([^\]]*)\]\(([^)]+)\)`)

// rewriteLinks replaces inter-page HTML links with relative markdown paths.
func rewriteLinks(md string, currentPath string, linkMap map[string]string) string {
	currentDir := filepath.Dir(currentPath)

	return linkPattern.ReplaceAllStringFunc(md, func(match string) string {
		sub := linkPattern.FindStringSubmatch(match)
		if len(sub) != 3 {
			return match
		}
		text, href := sub[1], sub[2]

		// Strip any fragment or query.
		cleanHref := href
		if i := strings.IndexAny(cleanHref, "#?"); i >= 0 {
			cleanHref = cleanHref[:i]
		}

		base := filepath.Base(cleanHref)
		if newPath, ok := linkMap[base]; ok {
			rel, err := filepath.Rel(currentDir, newPath)
			if err == nil {
				return fmt.Sprintf("[%s](%s)", text, rel)
			}
		}
		return match
	})
}

// rewriteAttachmentRefs rewrites references to Confluence attachment paths.
// targetPath is the root target directory for the import (used for shared images).
func rewriteAttachmentRefs(md string, p *pageInfo, targetPath string) string {
	assetsDir := "assets"
	quotedID := regexp.QuoteMeta(p.pageID)

	// attachments/<pageId>/<filename> → assets/<filename> (relative, same dir)
	md = regexp.MustCompile(`attachments/`+quotedID+`/([^\s)"]+)`).
		ReplaceAllString(md, assetsDir+"/$1")

	// download/attachments/<pageId>/<filename> → assets/<filename> (relative, same dir)
	md = regexp.MustCompile(`download/attachments/`+quotedID+`/([^\s)"]+)`).
		ReplaceAllString(md, assetsDir+"/$1")

	// images/<filename> → relative path to root assets/ directory.
	// Shared images live at <targetPath>/assets/, so compute the relative path
	// from the page's directory.
	rootAssetsDir := joinPath(targetPath, "assets")
	relAssets, err := filepath.Rel(p.dirPath, rootAssetsDir)
	if err != nil || p.dirPath == "" {
		relAssets = "assets"
	}
	md = regexp.MustCompile(`images/([^\s)"]+)`).
		ReplaceAllString(md, relAssets+"/$1")

	return md
}

// detectZipPrefix finds a common single-directory prefix in all zip entries.
// Returns "prefix/" if all entries start with it, or "" if entries are at root.
func detectZipPrefix(r *zip.Reader) string {
	if len(r.File) == 0 {
		return ""
	}

	var commonDir string
	for _, f := range r.File {
		parts := strings.SplitN(f.Name, "/", 2)
		if len(parts) < 2 {
			// A file at the root level — no common prefix.
			return ""
		}
		dir := parts[0]
		if commonDir == "" {
			commonDir = dir
		} else if dir != commonDir {
			return ""
		}
	}
	if commonDir == "" {
		return ""
	}
	return commonDir + "/"
}

func readZipFile(f *zip.File) ([]byte, error) {
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return io.ReadAll(rc)
}

// joinPath joins path segments, filtering out empty strings.
func joinPath(parts ...string) string {
	var nonEmpty []string
	for _, p := range parts {
		if p != "" {
			nonEmpty = append(nonEmpty, p)
		}
	}
	return filepath.Join(nonEmpty...)
}
