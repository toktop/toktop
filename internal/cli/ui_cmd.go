package cli

import (
	"context"
	"crypto/subtle"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"toktop.unceas.dev/internal/httpapi"
	"toktop.unceas.dev/internal/web"
)

func runUI(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if !web.Enabled {
		cliErrf(stderr, "this build has no web UI; rebuild with -tags ui (or run `make ui`)")
		return 2
	}

	fs0 := flag.NewFlagSet("ui", flag.ContinueOnError)
	fs0.SetOutput(stderr)
	noBrowser := fs0.Bool("no-browser", false, "print the URL instead of opening a browser")
	bindAddr  := fs0.String("addr", "127.0.0.1:0", "loopback address to serve the UI on (must be 127.0.0.1/localhost)")
	setFlagUsage(fs0, "usage: toktop ui [flags]",
		"Serve the local web UI and open it in your browser.",
		"Starts a loopback proxy that serves the embedded SPA and forwards /v1 to the daemon.")
	if code := parseFlagsNoPositionals(fs0, args, stdout, stderr); code >= 0 {
		return code
	}

	host, _, err := net.SplitHostPort(*bindAddr)
	if err != nil || !isLoopbackHost(host) {
		cliErrf(stderr, "--addr must be a loopback address (127.0.0.1 or localhost), got %q", *bindAddr)
		return 2
	}

	assets, err := web.Assets()
	if err != nil {
		cliErr(stderr, err)
		return 2
	}

	home, ok := resolveHome(stderr)
	if !ok {
		return 1
	}
	loader, err := configFor(ctx, home)
	if err != nil {
		cliErr(stderr, err)
		return 2
	}
	snap  := loader.Current()
	addr  := clientAddr(snap)
	if e := ensureDaemon(ctx, home, addr, snap.Autostart, stderr); e != nil {
		cliErr(stderr, e)
	}
	daemonToken := clientToken("", false)

	uiNonce, err := httpapi.GenerateToken()
	if err != nil {
		cliErr(stderr, err)
		return 2
	}

	ln, err := net.Listen("tcp", *bindAddr)
	if err != nil {
		cliErrf(stderr, "bind UI address %s: %v", *bindAddr, err)
		return 2
	}
	openURL := fmt.Sprintf("http://%s/?t=%s", ln.Addr().(*net.TCPAddr).String(), uiNonce)

	mux := http.NewServeMux()
	mux.Handle("/v1/", observeOnly(daemonProxy(addr, daemonToken)))
	mux.Handle("/", spaHandler(assets))
	srv := &http.Server{
		Handler:           uiAuth(uiNonce, mux),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	fmt.Fprintf(stdout, "toktop ui: %s\n", openURL)
	if *noBrowser {
		fmt.Fprintln(stdout, "(--no-browser: open the URL above manually)")
	} else if e := openBrowser(openURL); e != nil {
		fmt.Fprintf(stderr, "toktop ui: could not open a browser (%v); open the URL above manually\n", e)
	}

	go keepaliveDaemonSSE(ctx, addr, daemonToken)
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
	}()
	if e := srv.Serve(ln); e != nil && !errors.Is(e, http.ErrServerClosed) {
		cliErr(stderr, e)
		return 1
	}
	return 0
}

// keepaliveDaemonSSE holds one GET /v1/stream connection to the daemon for as
// long as ctx is live. The daemon's idle monitor counts SSE subscribers; without
// this the daemon would idle-stop after ~60 s when no dashboard page is open,
// causing 502 on every /v1 request until the user restarts toktop ui.
// On EOF or any error it waits 2 s and reconnects; on ctx cancellation it exits.
func keepaliveDaemonSSE(ctx context.Context, addr, token string) {
	client, base := httpClientFor(addr, 0)
	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/v1/stream", nil)
		if err == nil {
			if token != "" {
				req.Header.Set("Authorization", "Bearer "+token)
			}
			if resp, err2 := client.Do(req); err2 == nil {
				_, _ = io.Copy(io.Discard, resp.Body)
				_ = resp.Body.Close()
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(2 * time.Second):
		}
	}
}

// daemonProxy reverse-proxies /v1 to the daemon at addr (unix socket or tcp),
// injecting the daemon bearer token when set. FlushInterval -1 streams SSE immediately.
func daemonProxy(addr, token string) http.Handler {
	network, address := httpapi.SplitListenAddr(addr)
	transport := &http.Transport{}
	host := "daemon"
	if network == "unix" {
		transport.DialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", address)
		}
	} else {
		host = tcpClientHost(address)
	}
	return &httputil.ReverseProxy{
		Transport:     transport,
		FlushInterval: -1,
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.Out.URL.Scheme = "http"
			pr.Out.URL.Host   = host
			if token != "" {
				pr.Out.Header.Set("Authorization", "Bearer "+token)
			}
		},
	}
}

// spaHandler serves the embedded SPA, falling back to index.html for unknown
// paths so client-side routing deep-links survive a refresh.
func spaHandler(assets fs.FS) http.Handler {
	fileServer := http.FileServer(http.FS(assets))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(r.URL.Path, "/")
		if p == "" {
			p = "index.html"
		}
		if f, err := assets.Open(p); err == nil {
			_ = f.Close()
			fileServer.ServeHTTP(w, r)
			return
		}
		r2 := r.Clone(r.Context())
		r2.URL.Path = "/"
		fileServer.ServeHTTP(w, r2)
	})
}

// observeOnly enforces the web UI's read-only-plus-config-set contract: it
// forwards GET routes (all of which are reads, incl. /v1/stream) and the one
// allowed mutation, POST /v1/config:set, and 403s everything else — so the UI
// can never reach the daemon's destructive control routes (prune, daemon
// trigger/pause/resume, export, emit, …). Mirrors the config RemoteSettable
// default-deny stance, applied to the whole surface.
func observeOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet || (r.Method == http.MethodPost && r.URL.Path == "/v1/config:set") {
			next.ServeHTTP(w, r)
			return
		}
		http.Error(w, "this route is not available over the web UI; use the toktop CLI", http.StatusForbidden)
	})
}

// uiAuth gates browser<->proxy traffic with an ephemeral per-launch nonce.
// A valid ?t= sets a same-site HttpOnly cookie so subsequent same-origin
// fetch/EventSource requests carry it automatically. Unauthenticated → 401.
func uiAuth(nonce string, next http.Handler) http.Handler {
	const cookieName = "toktop_ui"
	nb := []byte(nonce)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if q := r.URL.Query().Get("t"); subtle.ConstantTimeCompare([]byte(q), nb) == 1 {
			http.SetCookie(w, &http.Cookie{
				Name:     cookieName,
				Value:    nonce,
				Path:     "/",
				HttpOnly: true,
				SameSite: http.SameSiteStrictMode,
			})
			next.ServeHTTP(w, r)
			return
		}
		if c, err := r.Cookie(cookieName); err == nil && subtle.ConstantTimeCompare([]byte(c.Value), nb) == 1 {
			next.ServeHTTP(w, r)
			return
		}
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	})
}

func isLoopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// openBrowser opens target in the default browser, per OS.
func openBrowser(target string) error {
	if _, err := url.Parse(target); err != nil {
		return err
	}
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", target).Start()
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", target).Start()
	default:
		return exec.Command("xdg-open", target).Start()
	}
}
