package plex

import (
	"net/url"
	"strings"
	"testing"
)

// The forward URL rides inside the fragment's query, so it must be escaped there, and
// left out entirely when there is none (popup mode).
func TestAuthURLForwardURL(t *testing.T) {
	fwd := "https://arrmada.example.com/?plexpin=42&x=a b"
	got := AuthURL("cid", "CODE", "Arrmada", fwd)
	if !strings.HasPrefix(got, plexAuthUI) {
		t.Fatalf("AuthURL = %q, want the app.plex.tv auth page", got)
	}
	q, err := url.ParseQuery(strings.TrimPrefix(got, plexAuthUI))
	if err != nil {
		t.Fatal(err)
	}
	if q.Get("forwardUrl") != fwd || q.Get("code") != "CODE" || q.Get("clientID") != "cid" {
		t.Errorf("params = %v", q)
	}
	if strings.Contains(got, "plexpin=42&x") {
		t.Errorf("forwardUrl isn't escaped: %q", got)
	}

	if bare := AuthURL("cid", "CODE", "Arrmada", ""); strings.Contains(bare, "forwardUrl") {
		t.Errorf("no forward URL should leave the param out: %q", bare)
	}
}
