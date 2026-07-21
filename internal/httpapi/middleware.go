package httpapi

import (
	"net/http"
	"time"
)

// statusRecorder captures the response status code for request logging.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

// Unwrap lets http.ResponseController reach the underlying ResponseWriter (for
// the websocket hijack) even though we've wrapped it for status capture.
func (r *statusRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

// logRequests emits one structured line per request at debug level (info for
// slow or error responses).
func (a *api) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)

		attrs := []any{
			"method", r.Method,
			"path", r.URL.Path,
			"status", rec.status,
			"dur_ms", time.Since(start).Milliseconds(),
		}
		if rec.status >= 500 {
			a.deps.Log.Error("request", attrs...)
		} else {
			a.deps.Log.Debug("request", attrs...)
		}
	})
}

// securityHeaders sets conservative, always-safe response headers on every
// request. Deliberately NOT a strict Content-Security-Policy: the UI relies on
// inline styles throughout, which a strict style-src would break — that needs a
// nonce/hash pass before it can ship. These headers cost nothing and don't.
func (a *api) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff") // no MIME sniffing
		h.Set("X-Frame-Options", "SAMEORIGIN")     // clickjacking: no foreign framing
		h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		// HSTS only when the client actually reached us over HTTPS, so a plain-HTTP
		// LAN setup isn't pinned to a scheme it doesn't serve.
		if requestIsHTTPS(r) {
			h.Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		}
		next.ServeHTTP(w, r)
	})
}

// recoverPanics turns a panic in any handler into a 500 instead of crashing the
// whole server.
func (a *api) recoverPanics(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				a.deps.Log.Error("panic recovered", "panic", v, "path", r.URL.Path)
				http.Error(w, `{"status":"error","message":"internal server error"}`, http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}
