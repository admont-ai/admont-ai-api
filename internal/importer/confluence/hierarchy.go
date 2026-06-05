package confluence

import (
	"bytes"
	"path/filepath"
	"strings"

	"golang.org/x/net/html"
)

// PageNode represents a page in the Confluence hierarchy.
type PageNode struct {
	PageID   string
	Title    string
	Href     string // original filename in zip (e.g. "My-Page_12345.html")
	Children []*PageNode
}

// ParseHierarchy parses the nested <ul>/<li>/<a> structure from Confluence's index.html
// and returns the root node whose children are the top-level pages.
func ParseHierarchy(indexHTML []byte) (*PageNode, error) {
	doc, err := html.Parse(bytes.NewReader(indexHTML))
	if err != nil {
		return nil, err
	}

	root := &PageNode{Title: "root"}

	// Find the first <ul> inside <body> — that's the page tree.
	var firstUL *html.Node
	var findUL func(*html.Node)
	findUL = func(n *html.Node) {
		if firstUL != nil {
			return
		}
		if n.Type == html.ElementNode && n.Data == "ul" {
			firstUL = n
			return
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			findUL(c)
		}
	}
	findUL(doc)

	if firstUL == nil {
		return root, nil
	}

	root.Children = parseUL(firstUL)
	return root, nil
}

// parseUL extracts PageNode children from a <ul> element.
func parseUL(ul *html.Node) []*PageNode {
	var nodes []*PageNode
	for li := ul.FirstChild; li != nil; li = li.NextSibling {
		if li.Type != html.ElementNode || li.Data != "li" {
			continue
		}
		node := parseLI(li)
		if node != nil {
			nodes = append(nodes, node)
		}
	}
	return nodes
}

// parseLI extracts a PageNode from a <li> element, including nested children.
// Confluence may wrap <a> tags in <span>, <p>, or other elements, so we search
// the full subtree for the first <a> rather than only checking direct children.
func parseLI(li *html.Node) *PageNode {
	// Find the first <a> anywhere inside this <li> (but not inside nested <ul>).
	a := findFirstAnchor(li)
	if a == nil {
		return nil
	}

	href := attrVal(a, "href")
	linkText := textContent(a)
	base := filepath.Base(href)
	_, pageID := ParseConfluenceFilename(base)
	node := &PageNode{
		PageID: pageID,
		Title:  strings.TrimSpace(linkText),
		Href:   base,
	}

	// Find nested <ul> for child pages (direct children of <li> only, not deeper).
	// Confluence wraps each sibling page in its own <ul>, so we must collect all of them.
	for c := li.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == html.ElementNode && c.Data == "ul" {
			node.Children = append(node.Children, parseUL(c)...)
		}
	}
	return node
}

// findFirstAnchor finds the first <a> element in a subtree, stopping at nested <ul> boundaries.
func findFirstAnchor(n *html.Node) *html.Node {
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == html.ElementNode {
			if c.Data == "ul" {
				continue // don't descend into child page lists
			}
			if c.Data == "a" {
				return c
			}
			if found := findFirstAnchor(c); found != nil {
				return found
			}
		}
	}
	return nil
}

func attrVal(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

func textContent(n *html.Node) string {
	if n.Type == html.TextNode {
		return n.Data
	}
	var sb strings.Builder
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		sb.WriteString(textContent(c))
	}
	return sb.String()
}
