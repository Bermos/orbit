// Package web embeds the built web UI (web/dist) into the Orbit binary.
// Build it with `npm run build` in this directory before `go build`.
package web

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var dist embed.FS

// Dist returns the built UI, or nil when it has not been built.
func Dist() fs.FS {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		return nil
	}
	if _, err := fs.Stat(sub, "index.html"); err != nil {
		return nil
	}
	return sub
}
