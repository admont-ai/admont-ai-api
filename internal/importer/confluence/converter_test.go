package confluence

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func TestImportSDExport(t *testing.T) {
	sdDir := "/Users/christianfischer/Downloads/SD"
	if _, err := os.Stat(sdDir); err != nil {
		t.Skip("SD export not available at", sdDir)
	}

	// Create zip from directory.
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	err := filepath.Walk(sdDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(sdDir, path)
		w, err := zw.Create(rel)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		_, err = w.Write(data)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	zw.Close()

	data := buf.Bytes()
	r, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}

	result, err := Import(r, "")
	if err != nil {
		t.Fatal(err)
	}

	// Log all errors.
	for _, e := range result.Errors {
		t.Logf("import error: %s: %s", e.File, e.Message)
	}

	// Collect page paths.
	var pagePaths []string
	for _, p := range result.Pages {
		pagePaths = append(pagePaths, p.Path)
	}
	sort.Strings(pagePaths)

	t.Log("=== Pages ===")
	for _, p := range pagePaths {
		t.Log(" ", p)
	}

	// Collect attachment paths.
	var attachPaths []string
	for _, a := range result.Attachments {
		attachPaths = append(attachPaths, a.Path)
	}
	sort.Strings(attachPaths)

	t.Log("=== Attachments ===")
	for _, p := range attachPaths {
		t.Log(" ", p)
	}

	// Verify structure.
	if len(result.Pages) == 0 {
		t.Fatal("no pages imported")
	}
	if len(result.Attachments) == 0 {
		t.Fatal("no attachments imported")
	}

	// Root page should be in its own folder.
	found := false
	for _, p := range pagePaths {
		if p == "software-development/software-development.md" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected software-development/software-development.md")
	}

	// CICD should be nested.
	found = false
	for _, p := range pagePaths {
		if p == "software-development/cicd/cicd.md" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected software-development/cicd/cicd.md")
	}

	// Children of CICD should be in the cicd folder.
	found = false
	for _, p := range pagePaths {
		if p == "software-development/cicd/cicd-pipelines.md" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected software-development/cicd/cicd-pipelines.md")
	}

	// Attachments for page 33329 should be in software-development/assets/.
	found = false
	for _, p := range attachPaths {
		if strings.HasPrefix(p, "software-development/assets/") && !strings.Contains(p, "icons") {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected attachments in software-development/assets/")
	}

	// Attachments for secure-by-design (page 88932353) should be in coding-standard/assets/.
	found = false
	for _, p := range attachPaths {
		if p == "software-development/coding-standard/assets/88997920.png" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected software-development/coding-standard/assets/88997920.png")
	}

	// No page should be at root level (all under software-development/).
	for _, p := range pagePaths {
		if !strings.HasPrefix(p, "software-development/") {
			t.Errorf("page at unexpected root level: %s", p)
		}
	}

	// Verify .order files are generated.
	if len(result.OrderFiles) == 0 {
		t.Fatal("expected .order files to be generated")
	}

	t.Log("=== Order Files ===")
	for _, of := range result.OrderFiles {
		t.Logf("  %s: %v", of.Dir, of.Entries)
	}

	// Find the root order (for "" dir which is the top-level).
	orderMap := make(map[string][]string)
	for _, of := range result.OrderFiles {
		orderMap[of.Dir] = of.Entries
	}

	// Root should just have the software-development folder.
	rootOrder, ok := orderMap[""]
	if !ok {
		t.Error("expected .order file for root directory")
	} else if len(rootOrder) != 1 || rootOrder[0] != "software-development" {
		t.Errorf("root .order: got %v, want [software-development]", rootOrder)
	}

	// software-development/ should list children in index.html order.
	sdOrder, ok := orderMap["software-development"]
	if !ok {
		t.Error("expected .order file for software-development/")
	} else {
		// First entry should be the parent's own md file.
		if sdOrder[0] != "software-development.md" {
			t.Errorf("software-development .order[0]: got %s, want software-development.md", sdOrder[0])
		}
		t.Logf("  software-development order: %v", sdOrder)
	}

	// cicd/ should have cicd.md first, then children.
	cicdOrder, ok := orderMap["software-development/cicd"]
	if !ok {
		t.Error("expected .order file for software-development/cicd/")
	} else {
		if cicdOrder[0] != "cicd.md" {
			t.Errorf("cicd .order[0]: got %s, want cicd.md", cicdOrder[0])
		}
		if len(cicdOrder) != 4 { // cicd.md + 3 children
			t.Errorf("cicd .order length: got %d, want 4", len(cicdOrder))
		}
	}
}

// TestImportSDExportWithPrefix tests that a zip with a directory prefix (e.g. SD/) works.
func TestImportSDExportWithPrefix(t *testing.T) {
	sdDir := "/Users/christianfischer/Downloads/SD"
	if _, err := os.Stat(sdDir); err != nil {
		t.Skip("SD export not available at", sdDir)
	}

	// Create zip with "SD/" prefix on all entries (simulating zip created from parent dir).
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	err := filepath.Walk(sdDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(sdDir, path)
		w, err := zw.Create("SD/" + rel) // add prefix
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		_, err = w.Write(data)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	zw.Close()

	data := buf.Bytes()
	r, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}

	result, err := Import(r, "")
	if err != nil {
		t.Fatal(err)
	}

	if len(result.Pages) != 17 {
		t.Errorf("expected 17 pages, got %d", len(result.Pages))
	}
	if len(result.Attachments) == 0 {
		t.Error("expected attachments, got 0")
	}

	// Verify nested structure still works.
	var pagePaths []string
	for _, p := range result.Pages {
		pagePaths = append(pagePaths, p.Path)
	}
	sort.Strings(pagePaths)

	t.Log("=== Pages (prefixed zip) ===")
	for _, p := range pagePaths {
		t.Log(" ", p)
	}

	found := false
	for _, p := range pagePaths {
		if p == "software-development/cicd/cicd.md" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected software-development/cicd/cicd.md")
	}

	// No page should be flat at root.
	for _, p := range pagePaths {
		if !strings.HasPrefix(p, "software-development/") {
			t.Errorf("page at unexpected root level: %s", p)
		}
	}
}
