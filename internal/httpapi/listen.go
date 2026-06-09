package httpapi

import "strings"

// SplitListenAddr classifies a listen/dial address into a (network, address)
// pair for net.Listen / net.Dial:
//
//	"unix:/p/toktop.sock"     -> ("unix", "/p/toktop.sock")   // explicit unix socket
//	"/p/toktop.sock"          -> ("unix", "/p/toktop.sock")   // absolute path is a socket
//	"tcp://127.0.0.1:8787"  -> ("tcp", "127.0.0.1:8787")
//	"127.0.0.1:8787"        -> ("tcp", "127.0.0.1:8787")  // host:port shorthand
//
// The default IPC transport is a Unix socket (OS file-permission access control,
// no network surface); TCP is an explicit opt-in downgrade that still requires a
// bearer token off loopback.
func SplitListenAddr(addr string) (network, address string) {
	switch {
	case strings.HasPrefix(addr, "unix:"):
		return "unix", strings.TrimPrefix(addr, "unix:")
	case strings.HasPrefix(addr, "tcp://"):
		return "tcp", strings.TrimPrefix(addr, "tcp://")
	case strings.HasPrefix(addr, "/"):
		return "unix", addr
	default:
		return "tcp", addr
	}
}
