// Package web embeds the built SPA so the Go binary serves it directly.
//
// The SPA source lives under /web (Vite project). After `npm run build` the
// artifacts land in /web/dist and are embedded here at compile time.
//
// Until the SPA is built, web/dist contains a placeholder index.html so the
// binary still compiles and serves a fallback page.
package web

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var dist embed.FS

// DistFS is the embedded build output, rooted at /web/dist.
var DistFS fs.FS

func init() {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		panic("web: embed dist: " + err.Error())
	}
	DistFS = sub
}
