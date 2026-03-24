package web

import (
	"io/fs"
	"strings"
	"testing"
)

func TestReadIndexHTMLPrefersBuiltDist(t *testing.T) {
	contents, source, err := ReadIndexHTML()
	if err != nil {
		t.Fatalf("ReadIndexHTML() error: %v", err)
	}

	if got, want := source, distIndexPath; got != want {
		t.Fatalf("ReadIndexHTML() source = %q, want %q", got, want)
	}

	html := string(contents)
	if !strings.Contains(html, "/assets/") {
		t.Fatalf("ReadIndexHTML() did not return built asset references: %s", html)
	}
	if strings.Contains(html, "Deckhand is loading") {
		t.Fatalf("ReadIndexHTML() returned placeholder HTML instead of built dist: %s", html)
	}
}

func TestDistFSIncludesBuiltAssets(t *testing.T) {
	distFS := DistFS()

	indexHTML, err := fs.ReadFile(distFS, "index.html")
	if err != nil {
		t.Fatalf("ReadFile(index.html) error: %v", err)
	}
	if !strings.Contains(string(indexHTML), "/assets/") {
		t.Fatalf("embedded dist index is missing built assets: %s", string(indexHTML))
	}

	entries, err := fs.ReadDir(distFS, "assets")
	if err != nil {
		t.Fatalf("ReadDir(assets) error: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("embedded dist assets directory is empty")
	}
}
