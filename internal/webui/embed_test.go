package webui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

// testFS mimics a build: one bundle with both precompressed siblings, one with
// none, plus the uncompressed shell files.
func testFS() fstest.MapFS {
	return fstest.MapFS{
		"index.html":             {Data: []byte("<!doctype html><title>Arrmada</title>")},
		"index.html.br":          {Data: []byte("BR-INDEX")}, // must never be used
		"sw.js":                  {Data: []byte("const CACHE = 'arrmada-abc';")},
		"sw.js.gz":               {Data: []byte("GZ-SW")}, // must never be used
		"manifest.webmanifest":   {Data: []byte(`{"name":"Arrmada"}`)},
		"assets/index-abc.js":    {Data: []byte("console.log('identity')")},
		"assets/index-abc.js.br": {Data: []byte("BR-BYTES")},
		"assets/index-abc.js.gz": {Data: []byte("GZ-BYTES")},
		"assets/small-abc.css":   {Data: []byte("body{}")},
	}
}

func get(t *testing.T, h http.Handler, target, acceptEncoding string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, target, nil)
	if acceptEncoding != "" {
		req.Header.Set("Accept-Encoding", acceptEncoding)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestServesBrotliWhenAccepted(t *testing.T) {
	rec := get(t, newHandler(testFS(), true), "/assets/index-abc.js", "gzip, deflate, br, zstd")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if got := rec.Header().Get("Content-Encoding"); got != "br" {
		t.Errorf("Content-Encoding = %q, want br", got)
	}
	if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/javascript") {
		t.Errorf("Content-Type = %q, want JavaScript", got)
	}
	if got := rec.Header().Get("Vary"); got != "Accept-Encoding" {
		t.Errorf("Vary = %q", got)
	}
	if got := rec.Header().Get("Cache-Control"); !strings.Contains(got, "immutable") {
		t.Errorf("Cache-Control = %q, want immutable", got)
	}
	if rec.Body.String() != "BR-BYTES" {
		t.Errorf("body = %q, want the .br file", rec.Body.String())
	}
	if got := rec.Header().Get("Content-Length"); got != "8" {
		t.Errorf("Content-Length = %q, want the compressed size", got)
	}
}

func TestEncodedIgnoresRange(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/assets/index-abc.js", nil)
	req.Header.Set("Accept-Encoding", "br")
	req.Header.Set("Range", "bytes=0-1")
	rec := httptest.NewRecorder()
	newHandler(testFS(), true).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || rec.Body.String() != "BR-BYTES" {
		t.Errorf("status = %d, body = %q; want the whole .br file", rec.Code, rec.Body.String())
	}
}

func TestFallsBackToGzip(t *testing.T) {
	for _, ae := range []string{"gzip", "gzip, deflate", "br;q=0, gzip;q=0.8", "GZIP"} {
		rec := get(t, newHandler(testFS(), true), "/assets/index-abc.js", ae)
		if got := rec.Header().Get("Content-Encoding"); got != "gzip" {
			t.Errorf("%q: Content-Encoding = %q, want gzip", ae, got)
		}
		if rec.Body.String() != "GZ-BYTES" {
			t.Errorf("%q: body = %q", ae, rec.Body.String())
		}
		if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/javascript") {
			t.Errorf("%q: Content-Type = %q", ae, got)
		}
	}
}

func TestIdentityWithoutAcceptEncoding(t *testing.T) {
	for _, ae := range []string{"", "identity", "br;q=0, gzip;q=0", "deflate"} {
		rec := get(t, newHandler(testFS(), true), "/assets/index-abc.js", ae)
		if rec.Code != http.StatusOK {
			t.Fatalf("%q: status = %d", ae, rec.Code)
		}
		if got := rec.Header().Get("Content-Encoding"); got != "" {
			t.Errorf("%q: Content-Encoding = %q, want none", ae, got)
		}
		if got := rec.Header().Get("Vary"); got != "Accept-Encoding" {
			t.Errorf("%q: Vary = %q, want Accept-Encoding (a compressed copy exists)", ae, got)
		}
		if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/javascript") {
			t.Errorf("%q: Content-Type = %q", ae, got)
		}
		if rec.Body.String() != "console.log('identity')" {
			t.Errorf("%q: body = %q", ae, rec.Body.String())
		}
	}
}

func TestNoSiblingServesIdentity(t *testing.T) {
	rec := get(t, newHandler(testFS(), true), "/assets/small-abc.css", "br, gzip")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if got := rec.Header().Get("Content-Encoding"); got != "" {
		t.Errorf("Content-Encoding = %q, want none", got)
	}
	if got := rec.Header().Get("Vary"); got != "" {
		t.Errorf("Vary = %q, want none (nothing to negotiate)", got)
	}
	if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/css") {
		t.Errorf("Content-Type = %q", got)
	}
	if rec.Body.String() != "body{}" {
		t.Errorf("body = %q", rec.Body.String())
	}
}

func TestMissingAsset404(t *testing.T) {
	rec := get(t, newHandler(testFS(), true), "/assets/does-not-exist.js", "br")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
	if strings.Contains(rec.Body.String(), "<!doctype html>") {
		t.Error("missing asset must not fall back to index.html")
	}
	if got := rec.Header().Get("Content-Encoding"); got != "" {
		t.Errorf("Content-Encoding = %q", got)
	}
}

func TestSPARouteServesIndexNoCache(t *testing.T) {
	for _, p := range []string{"/movies/12", "/", "/discover?tab=tv", "/index.html"} {
		rec := get(t, newHandler(testFS(), true), p, "br, gzip")
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status = %d", p, rec.Code)
		}
		if !strings.HasPrefix(rec.Body.String(), "<!doctype html>") {
			t.Errorf("%s: body = %q, want index.html", p, rec.Body.String())
		}
		if got := rec.Header().Get("Cache-Control"); got != "no-cache" {
			t.Errorf("%s: Cache-Control = %q, want no-cache", p, got)
		}
		if got := rec.Header().Get("Content-Encoding"); got != "" {
			t.Errorf("%s: Content-Encoding = %q, index.html stays identity", p, got)
		}
		if got := rec.Header().Get("Content-Type"); got != "text/html; charset=utf-8" {
			t.Errorf("%s: Content-Type = %q", p, got)
		}
	}
}

func TestServiceWorkerStaysIdentityNoCache(t *testing.T) {
	rec := get(t, newHandler(testFS(), true), "/sw.js", "gzip")
	if rec.Body.String() != "const CACHE = 'arrmada-abc';" {
		t.Errorf("body = %q, want the uncompressed worker", rec.Body.String())
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-cache" {
		t.Errorf("Cache-Control = %q", got)
	}
	if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/javascript") {
		t.Errorf("Content-Type = %q", got)
	}
}

func TestManifestContentType(t *testing.T) {
	rec := get(t, newHandler(testFS(), true), "/manifest.webmanifest", "")
	if got := rec.Header().Get("Content-Type"); got != "application/manifest+json" {
		t.Errorf("Content-Type = %q", got)
	}
}

func TestPlaceholderWithoutBuild(t *testing.T) {
	rec := get(t, newHandler(fstest.MapFS{}, false), "/assets/x.js", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "web UI not built yet") {
		t.Errorf("status = %d, body = %.60q", rec.Code, rec.Body.String())
	}
}

func TestAccepts(t *testing.T) {
	cases := []struct {
		header, coding string
		want           bool
	}{
		{"gzip, deflate, br", "br", true},
		{"gzip, deflate, br", "gzip", true},
		{"gzip", "br", false},
		{"br;q=0", "br", false},
		{"br; q=0.0, gzip", "br", false},
		{"br;q=0.5", "br", true},
		{" BR ", "br", true},
		{"", "gzip", false},
		{"x-gzip", "gzip", false},
	}
	for _, c := range cases {
		if got := accepts(c.header, c.coding); got != c.want {
			t.Errorf("accepts(%q, %q) = %v, want %v", c.header, c.coding, got, c.want)
		}
	}
}
