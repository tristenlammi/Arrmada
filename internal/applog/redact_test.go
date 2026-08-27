package applog

import (
	"log/slog"
	"strings"
	"testing"
)

// The Logs page is shared and exportable, so a key that reaches an attribute is a
// leak. Prove the ring never stores one.
func TestHandlerRedactsSecretAttrs(t *testing.T) {
	ring := NewRing(100)
	log := slog.New(NewHandler(slog.NewTextHandler(discard{}, nil), ring))
	log.Info("indexer configured", "name", "MAM", "api_key", "s3cr3t-key", "url", "https://x")
	log.With("token", "plex-tok").Info("plex connected")

	for _, e := range ring.Snapshot(Filter{}) {
		if strings.Contains(e.Attrs, "s3cr3t-key") || strings.Contains(e.Attrs, "plex-tok") {
			t.Fatalf("a secret survived into the log: %q", e.Attrs)
		}
		if !strings.Contains(e.Attrs, redacted) {
			t.Errorf("nothing was marked redacted in %q", e.Attrs)
		}
	}
	// Non-secret attrs must be untouched, or the log stops being useful.
	if got := ring.Snapshot(Filter{})[0].Attrs; !strings.Contains(got, "name=MAM") {
		t.Errorf("ordinary attrs were lost: %q", got)
	}
}

// Torznab and MAM carry the key in the query string, so a traced request URL leaks it
// even when no attribute is named "api_key".
func TestScrubQueryHidesKeysInURLs(t *testing.T) {
	cases := map[string]string{
		"searching https://idx/api?t=search&apikey=abc123&q=dune": "abc123",
		"GET https://mam/tor/js/loadSearch.php?token=zzz":         "zzz",
	}
	for in, secret := range cases {
		got := scrubQuery(in)
		if strings.Contains(got, secret) {
			t.Errorf("scrubQuery(%q) = %q, still contains the key", in, got)
		}
		if !strings.Contains(got, "redacted") {
			t.Errorf("scrubQuery(%q) = %q, want the redaction marker", in, got)
		}
	}
	// A URL with nothing sensitive comes back byte-identical.
	plain := "fetched https://api.themoviedb.org/3/movie/603?language=en"
	if got := scrubQuery(plain); got != plain {
		t.Errorf("scrubQuery mangled a harmless URL:\n got %q\nwant %q", got, plain)
	}
}
