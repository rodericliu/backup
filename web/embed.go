// Package web embeds the built web UI (Vue 3 + Element Plus + ECharts). Run
// `make web` to build it into dist before building backup-server; without it
// the server serves a page explaining how to build the UI.
package web

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var dist embed.FS

// FS returns the built UI rooted at dist.
func FS() fs.FS {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		panic(err)
	}
	return sub
}
