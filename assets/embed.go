// Package assets embeds the index bundle written by the seeder.
package assets

import (
	"embed"
	"io/fs"
)

//go:embed index
var files embed.FS

// Index returns the embedded bundle directory.
func Index() (fs.FS, error) { return fs.Sub(files, "index") }
