// Package assets provides the UI and third-party notices embedded in regview.
package assets

import (
	"embed"
	"io/fs"
)

// embeddedAssets contains the UI and generated third-party notices.
// Run just generate before compiling a distributable binary.
//
//go:embed dist
var embeddedAssets embed.FS

// FS returns the embedded files with paths relative to the generated dist directory.
func FS() fs.FS {
	files, err := fs.Sub(embeddedAssets, "dist")
	if err != nil {
		panic(err)
	}
	return files
}
