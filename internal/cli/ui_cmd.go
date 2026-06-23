package cli

import (
	"context"
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
	port    := ln.Addr().(*net.TCPAddr).Port
	openURL := fmt.Sprintf("http://127.0.0.1:%d/?t=%s", port, uiNonce)

	mux := http.NewServeMux()
	mux.Handle("/v1/", daemonProxy(addr, daemonToken))
	mux.Handle("/", spaHandler(assets))
	srv := &http.Server{Handler: uiAuth(uiNonce, mux)}

	fmt.Fprintf(stdout, "toktop ui: %s\n", openURL)
	if *noBrowser {
		fmt.Fprintln(stdout, "(--no-browser: open the URL above manually)")
	} else if e := openBrowser(openURL); e != nil {
		fmt.Fprintf(stderr, "toktop ui: could not open a browser (%v); open the URL above manually\n", e)
	}

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

// uiAuth gates browser<->proxy traffic with an ephemeral per-launch nonce.
// A valid ?t= sets a same-site HttpOnly cookie so subsequent same-origin
// fetch/EventSource requests carry it automatically. Unauthenticated → 401.
func uiAuth(nonce string, next http.Handler) http.Handler {
	const cookieName = "toktop_ui"
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if q := r.URL.Query().Get("t"); q == nonce {
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
		if c, err := r.Cookie(cookieName); err == nil && c.Value == nonce {
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
