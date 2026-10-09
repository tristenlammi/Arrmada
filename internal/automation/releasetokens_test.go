package automation

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

// Tokens expire after their TTL, the store evicts oldest-first at its cap, and a token
// only resolves for the kind, title and user it was issued to.
func TestReleaseTokensTTLEvictionScope(t *testing.T) {
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	tk := newReleaseTokens()
	tk.now = func() time.Time { return now }

	ref := ReleaseRef{Indexer: "ix", DownloadURL: "http://ix/dl/1?apikey=k", Title: "Alpha", MediaKind: ReleaseKindMovie, MediaID: 1, UserID: 7}
	tok := tk.Issue(ref)
	if len(tok) < 20 || strings.ContainsAny(tok, "+/=") {
		t.Fatalf("token %q isn't a 16-byte base64url value", tok)
	}
	if tok2 := tk.Issue(ref); tok2 == tok {
		t.Fatal("two issues gave the same token")
	}

	got, err := tk.Resolve(tok, ReleaseKindMovie, 1, 7)
	if err != nil || got.DownloadURL != ref.DownloadURL || got.Title != "Alpha" || got.Indexer != "ix" {
		t.Fatalf("resolve = %+v, %v", got, err)
	}

	for _, tc := range []struct {
		name          string
		kind          string
		media, userID int64
	}{
		{"another movie", ReleaseKindMovie, 2, 7},
		{"another user", ReleaseKindMovie, 1, 8},
		{"another kind", ReleaseKindSeries, 1, 7},
	} {
		if _, err := tk.Resolve(tok, tc.kind, tc.media, tc.userID); !errors.Is(err, ErrReleaseScope) {
			t.Errorf("%s: err = %v, want ErrReleaseScope", tc.name, err)
		}
	}
	if _, err := tk.Resolve("never-issued", ReleaseKindMovie, 1, 7); !errors.Is(err, ErrReleaseExpired) {
		t.Errorf("unknown token: err = %v, want ErrReleaseExpired", err)
	}

	// Just inside the TTL it still works; at the TTL it's gone.
	now = now.Add(releaseTokenTTL - time.Second)
	if _, err := tk.Resolve(tok, ReleaseKindMovie, 1, 7); err != nil {
		t.Fatalf("inside the TTL: %v", err)
	}
	now = now.Add(time.Second)
	if _, err := tk.Resolve(tok, ReleaseKindMovie, 1, 7); !errors.Is(err, ErrReleaseExpired) {
		t.Fatalf("at the TTL: err = %v, want ErrReleaseExpired", err)
	}
	// Issuing sweeps out the expired ones.
	tk.Issue(ref)
	if n := tk.len(); n != 1 {
		t.Fatalf("after expiry, %d tokens held, want 1", n)
	}

	// The cap evicts the oldest first.
	small := newReleaseTokens()
	small.now = func() time.Time { return now }
	small.cap = 3
	var toks []string
	for i := range 5 {
		toks = append(toks, small.Issue(ReleaseRef{MediaKind: ReleaseKindMovie, MediaID: int64(i)}))
	}
	if n := small.len(); n != 3 {
		t.Fatalf("held %d tokens, want the cap of 3", n)
	}
	for i, tok := range toks {
		_, err := small.Resolve(tok, ReleaseKindMovie, int64(i), 0)
		if evicted := i < 2; evicted != errors.Is(err, ErrReleaseExpired) {
			t.Errorf("token %d: err = %v (evicted should be %v)", i, err, evicted)
		}
	}
}

// The download link and info hash never serialise; only the token does.
func TestRankedReleaseJSONHasNoDownloadURL(t *testing.T) {
	b, err := json.Marshal(RankedRelease{
		Title: "Alpha", DownloadURL: "http://prowlarr/1/download?apikey=1",
		InfoHash: "abc", Token: "tok",
	})
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, bad := range []string{"download_url", "apikey", "prowlarr", "info_hash"} {
		if strings.Contains(s, bad) {
			t.Errorf("JSON carries %q: %s", bad, s)
		}
	}
	if !strings.Contains(s, `"token":"tok"`) {
		t.Errorf("JSON is missing the token: %s", s)
	}
}

// A details link keeps its page but loses any credential, and a details link that is
// really the download link is dropped.
func TestSafeInfoURL(t *testing.T) {
	dl := "http://jackett:9117/dl/tl/?jackett_apikey=SECRET&path=abc&file=X"
	cases := []struct{ info, download, want string }{
		{"https://tracker.example/details/1", dl, "https://tracker.example/details/1"},
		{"https://tracker.example/details.php?id=1&passkey=SECRET", dl, "https://tracker.example/details.php?id=1"},
		{"http://prowlarr/1/details?apikey=SECRET&id=9", dl, "http://prowlarr/1/details?id=9"},
		{dl, dl, ""},
		{"http://jackett:9117/dl/tl/?path=abc&file=X&jackett_apikey=OTHER", dl, ""},
		{"https://user:pw@tracker.example/t/1", "", "https://tracker.example/t/1"},
		{"javascript:alert(1)", "", ""},
		{"", dl, ""},
	}
	for _, tc := range cases {
		if got := safeInfoURL(tc.info, tc.download); got != tc.want {
			t.Errorf("safeInfoURL(%q) = %q, want %q", tc.info, got, tc.want)
		}
		if strings.Contains(safeInfoURL(tc.info, tc.download), "SECRET") {
			t.Errorf("safeInfoURL(%q) kept a secret", tc.info)
		}
	}
}
