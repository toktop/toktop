//go:build ui

package web

import (
	"embed"
	"io/fs"
)

// dist is the built SPA produced by `make ui` (internal/web/dist via Vite outDir). Building with -tags ui
// without first running `make ui` fails here with "no matching files found" —
// that is intentional: the embed and the asset build are bound together.
//
//go:embed all:dist
var dist embed.FS

// Enabled reports that this binary carries the web UI.
const Enabled = true

// Assets returns the SPA filesystem rooted at dist/, so index.html is at "/".
func Assets() (fs.FS, error) {
	return fs.Sub(dist, "dist")
}
