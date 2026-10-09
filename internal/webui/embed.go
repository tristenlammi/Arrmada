// Package webui embeds the compiled React single-page app and serves it from the
// Go binary, so Arrmada ships as one process on one port. When no web build has
// been embedded yet (fresh checkout / `go run` before `npm run build`), it serves
// a branded placeholder so the backend still shows signs of life.
package webui

import (
	"bytes"
	"embed"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"
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
	return newHandler(sub, built)
}

// newHandler is Handler over any file system, so tests can use an fstest.MapFS
// instead of the gitignored build output.
func newHandler(sub fs.FS, built bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !built {
			serveHTML(w, placeholderHTML)
			return
		}
		name := path.Clean(strings.TrimPrefix(r.URL.Path, "/"))
		if name == "." || name == "/" {
			name = "index.html"
		}
		if !exists(sub, name) {
			// A missing hashed bundle is a real 404. Answering with index.html would
			// hand the browser HTML where it expects JavaScript (and the service
			// worker could cache it), which turns a stale tab after a deploy into a
			// confusing parse error instead of a clean chunk-load failure.
			if strings.HasPrefix(name, "assets/") {
				w.Header().Set("Cache-Control", "no-store")
				http.Error(w, "404 page not found", http.StatusNotFound)
				return
			}
			// Anything else is a client-side route (e.g. /movies/12).
			name = "index.html"
		}
		// Cache policy is the fix for "the browser keeps running old JS after a
		// deploy": index.html must ALWAYS be revalidated so it points at the current
		// content-hashed bundle; those hashed assets never change under a name, so
		// they can be cached forever.
		if strings.HasPrefix(name, "assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "no-cache")
		}
		serveFile(w, r, sub, name)
	})
}

// serveFile sends name with the right Content-Type, using the build's
// precompressed .br or .gz sibling when the client accepts it.
func serveFile(w http.ResponseWriter, r *http.Request, sub fs.FS, name string) {
	h := w.Header()
	h.Set("Content-Type", contentType(name))

	served := name
	// index.html and sw.js stay identity-encoded (the build never compresses them).
	if name != "index.html" && name != "sw.js" {
		hasBr, hasGz := exists(sub, name+".br"), exists(sub, name+".gz")
		if hasBr || hasGz {
			// The response depends on Accept-Encoding whichever copy we pick, so
			// shared caches (a reverse proxy) must key on it.
			h.Add("Vary", "Accept-Encoding")
			switch ae := r.Header.Get("Accept-Encoding"); {
			case hasBr && accepts(ae, "br"):
				served = name + ".br"
				h.Set("Content-Encoding", "br")
			case hasGz && accepts(ae, "gzip"):
				served = name + ".gz"
				h.Set("Content-Encoding", "gzip")
			}
		}
	}

	f, err := sub.Open(served)
	if err != nil {
		unavailable(w)
		return
	}
	defer f.Close()
	rs, ok := f.(io.ReadSeeker)
	if !ok {
		// embed and fstest files both seek; this is a safety net for other FSes.
		b, err := io.ReadAll(f)
		if err != nil {
			unavailable(w)
			return
		}
		rs = bytes.NewReader(b)
	}
	if h.Get("Content-Encoding") != "" {
		// ServeContent leaves out Content-Length for an encoded body; send it so
		// proxies and progress bars know the size. Byte ranges of a compressed copy
		// are no use to a browser, so answer those with the whole file.
		if size, err := rs.Seek(0, io.SeekEnd); err == nil {
			if _, err := rs.Seek(0, io.SeekStart); err != nil {
				unavailable(w)
				return
			}
			h.Set("Content-Length", strconv.FormatInt(size, 10))
		}
		r = r.Clone(r.Context())
		r.Header.Del("Range")
	}
	// ServeContent (unlike ServeFileFS) leaves our Content-Type alone, so a .br
	// file goes out as JavaScript rather than as an unknown binary.
	http.ServeContent(w, r, name, time.Time{}, rs)
}

// unavailable answers for a file that exists but can't be read, without the
// encoding or long-lived caching headers meant for the real file.
func unavailable(w http.ResponseWriter) {
	h := w.Header()
	h.Del("Content-Encoding")
	h.Del("Vary")
	h.Set("Cache-Control", "no-store")
	http.Error(w, "file unavailable", http.StatusInternalServerError)
}

// accepts reports whether an Accept-Encoding header allows coding, honouring an
// explicit q=0 refusal.
func accepts(header, coding string) bool {
	for _, part := range strings.Split(header, ",") {
		token, params, _ := strings.Cut(strings.TrimSpace(part), ";")
		if !strings.EqualFold(strings.TrimSpace(token), coding) {
			continue
		}
		for _, p := range strings.Split(params, ";") {
			k, v, _ := strings.Cut(strings.TrimSpace(p), "=")
			if strings.EqualFold(strings.TrimSpace(k), "q") {
				if q, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil && q <= 0 {
					return false
				}
			}
		}
		return true
	}
	return false
}

// contentType pins the types the UI depends on rather than trusting the OS MIME
// table (on Windows the registry can map .js to text/plain, which nosniff then
// refuses to run). Anything else falls back to the standard table.
func contentType(name string) string {
	switch ext := strings.ToLower(path.Ext(name)); ext {
	case ".html":
		return "text/html; charset=utf-8"
	case ".js", ".mjs":
		return "text/javascript; charset=utf-8"
	case ".css":
		return "text/css; charset=utf-8"
	case ".svg":
		return "image/svg+xml"
	case ".webmanifest":
		return "application/manifest+json"
	case ".json":
		return "application/json"
	case ".woff2":
		return "font/woff2"
	default:
		if t := mime.TypeByExtension(ext); t != "" {
			return t
		}
		return "application/octet-stream"
	}
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
