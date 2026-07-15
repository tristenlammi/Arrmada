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
