package web

import (
	"embed"
	"io/fs"
)

const (
	distDir              = "dist"
	distIndexPath        = "dist/index.html"
	placeholderIndexPath = "placeholder/index.html"
)

// embeddedFiles contains the built SPA when available plus a tracked fallback
// document so Go builds still succeed before a fresh Vite build has run.
//
//go:embed all:dist placeholder/index.html
var embeddedFiles embed.FS

// DistFS returns the embedded dist/ subtree for static asset serving.
func DistFS() fs.FS {
	subtree, err := fs.Sub(embeddedFiles, distDir)
	if err != nil {
		panic("deckhand web dist embed misconfigured: " + err.Error())
	}
	return subtree
}

// ReadIndexHTML returns the built index.html when present, otherwise the
// tracked placeholder fallback document.
func ReadIndexHTML() ([]byte, string, error) {
	if contents, err := embeddedFiles.ReadFile(distIndexPath); err == nil {
		return contents, distIndexPath, nil
	}

	contents, err := embeddedFiles.ReadFile(placeholderIndexPath)
	if err != nil {
		return nil, "", err
	}
	return contents, placeholderIndexPath, nil
}
