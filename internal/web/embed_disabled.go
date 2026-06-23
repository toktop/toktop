//go:build !ui

package web

import "io/fs"

// Enabled reports that this binary was built without the web UI.
const Enabled = false

// Assets reports ErrDisabled: this binary lacks the embedded UI.
func Assets() (fs.FS, error) {
	return nil, ErrDisabled
}
