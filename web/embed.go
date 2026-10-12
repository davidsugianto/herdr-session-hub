// Package web embeds the built PWA (web/dist) so `hub serve` can ship it in
// the single binary.
package web

import (
	"embed"
	"io/fs"
)

//go:embed dist
var dist embed.FS

// FS returns the embedded UI rooted at dist (index.html at the top level).
func FS() fs.FS {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		// Unreachable: "dist" is a valid path and the embed directive guarantees it exists.
		panic(err)
	}
	return sub
}
