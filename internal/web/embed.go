// Package web exposes the optional embedded web UI assets.
//
// The web build and the Go build are independent. The SPA is built into the
// dist/app subdir (Vite outDir); the embed root dist/ also holds a committed
// .gitkeep so `//go:embed all:dist` resolves for a plain `go build` even when the
// SPA was never built — no build tag, no Node needed for the CLI build. Whether
// the UI is present is a runtime fact (Assets' ok return) that `toktop ui`
// reports. Build the SPA with `make ui` (or `make web-dist`) to include it.
package web

import (
	"embed"
	"io/fs"
)

// all: so the committed dist/.gitkeep placeholder (a dotfile) satisfies the embed
// even when dist/app has not been built — without it `go build` would fail with
// "no matching files found" on a fresh checkout.
//
//go:embed all:dist
var dist embed.FS

// Assets returns the embedded SPA rooted at dist/app (index.html at "/"). ok is
// false when this binary was built without the SPA (only the .gitkeep placeholder
// is embedded, no dist/app); callers report that the UI was not packaged in.
func Assets() (assets fs.FS, ok bool, err error) {
	sub, err := fs.Sub(dist, "dist/app")
	if err != nil {
		return nil, false, err
	}
	if _, err := fs.Stat(sub, "index.html"); err != nil {
		return nil, false, nil
	}
	return sub, true, nil
}
