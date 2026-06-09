package httpapi

import (
	"crypto/sha256"
	"crypto/subtle"
	"net"
	"net/http"
	"runtime/debug"
	"strings"
)

func (s *Server) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rw := &recordingWriter{ResponseWriter: w}
		defer func() {
			if rec := recover(); rec != nil {
				s.logger.Error("panic recovered in handler",
					"method", r.Method,
					"path", r.URL.Path,
					"panic", rec,
					"stack", string(debug.Stack()),
				)
				// If nothing has been written yet, emit a structured 500.
				// For already-started responses (e.g. an SSE stream) the
				// header is sent, so we can only log and unwind.
				if !rw.wroteHeader {
					writeError(rw, http.StatusInternalServerError, "internal_panic", "internal server error")
				}
			}
		}()
		if s.token != "" {
			if !validBearerToken(r.Header.Get("Authorization"), s.token) {
				writeError(rw, http.StatusUnauthorized, "unauthorized", "bearer token required")
				return
			}
		}
		next.ServeHTTP(rw, r)
	})
}

// recordingWriter tracks whether the response header has been written so the
// recovery middleware knows whether it can still emit a JSON error body.
type recordingWriter struct {
	http.ResponseWriter
	wroteHeader bool
}

func (w *recordingWriter) WriteHeader(status int) {
	w.wroteHeader = true
	w.ResponseWriter.WriteHeader(status)
}

func (w *recordingWriter) Write(b []byte) (int, error) {
	w.wroteHeader = true
	return w.ResponseWriter.Write(b)
}

// Flush forwards to the underlying writer when it supports flushing, so SSE
// streaming continues to work through the recording wrapper.
func (w *recordingWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Unwrap exposes the wrapped writer so http.NewResponseController can reach the
// underlying connection (e.g. SetWriteDeadline used by the SSE handler).
func (w *recordingWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

func validBearerToken(header, token string) bool {
	const prefix = "Bearer "
	if !strings.HasPrefix(header, prefix) {
		return false
	}
	providedHash := sha256.Sum256([]byte(strings.TrimPrefix(header, prefix)))
	wantHash := sha256.Sum256([]byte(token))
	return subtle.ConstantTimeCompare(providedHash[:], wantHash[:]) == 1
}

func isLoopbackAddr(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		// No port (e.g. "127.0.0.1" or "localhost"): treat the whole value
		// as the host rather than classifying it as non-loopback.
		host = addr
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
