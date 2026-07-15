// Package webui embeds the compiled React single-page app and serves it from the
// Go binary, so Arrmada ships as one process on one port. When no web build has
// been embedded yet (fresh checkout / `go run` before `npm run build`), it serves
// a branded placeholder so the backend still shows signs of life.
package webui

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

// The web build drops its output into dist/. It's .gitignored except for
// .gitkeep, so `all:dist` always has at least one file to embed.
//
//go:embed all:dist
var distFS embed.FS

// assets returns the dist/ subtree and whether a real UI build is present.
func assets() (fs.FS, bool) {
	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		return nil, false
	}
	return sub, exists(sub, "index.html")
}

// Handler serves the embedded SPA. Real asset paths are served directly; any
// other path falls back to index.html (client-side routing). With no build
// embedded, it serves the placeholder page.
func Handler() http.Handler {
	sub, built := assets()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !built {
			serveHTML(w, placeholderHTML)
			return
		}
		name := path.Clean(strings.TrimPrefix(r.URL.Path, "/"))
		if name == "." || name == "/" {
			name = "index.html"
		}
		// Serve the real file if it exists; otherwise fall back to index.html so
		// client-side routes (e.g. /activity) resolve.
		serveName := name
		if !exists(sub, name) {
			serveName = "index.html"
		}
		// Cache policy is the fix for "the browser keeps running old JS after a
		// deploy": index.html must ALWAYS be revalidated so it points at the current
		// content-hashed bundle; those hashed assets never change under a name, so
		// they can be cached forever.
		if serveName == "index.html" {
			w.Header().Set("Cache-Control", "no-cache")
		} else if strings.HasPrefix(serveName, "assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "no-cache")
		}
		http.ServeFileFS(w, r, sub, serveName)
	})
}

func exists(fsys fs.FS, name string) bool {
	st, err := fs.Stat(fsys, name)
	return err == nil && !st.IsDir()
}

func serveHTML(w http.ResponseWriter, html string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(html))
}
