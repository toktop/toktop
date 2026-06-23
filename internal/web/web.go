// Package web exposes the embedded web UI assets. The assets are compiled in
// only when the binary is built with -tags ui (make ui); a default build ships
// without them and Assets reports ErrDisabled.
package web

import "errors"

// ErrDisabled is returned by Assets when the binary was built without -tags ui.
var ErrDisabled = errors.New("web UI not compiled into this binary; rebuild with -tags ui (make ui)")
