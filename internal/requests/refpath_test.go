package requests

import (
	"context"
	"sync"
	"testing"
)

// The same vectors as web/src/lib/refLink.test.ts: Web Push (here) and the bell (there)
// must open the same place for the same notice. "" is the TS null.
var refPathVectors = []struct{ ref, want string }{
	{"movie:603", "/discover/movie/603"},
	{"movie:603:approved:1700000000", "/discover/movie/603"},
	{"movie:603:declined:1700000000", "/discover/movie/603"},
	{"series:1399", "/discover/series/1399"},
	{"series:1399:r12", "/discover/series/1399"},
	{"series:1399:r12:s3", "/discover/series/1399"},
	{"series:1399:r12:approved:1700000000", "/discover/series/1399"},
	{"book:OL27448W", "/discover?tab=books&work=OL27448W"},
	{"book:OL27448W:audiobook", "/discover?tab=books&work=OL27448W"},
	{"book:hc:123", "/discover?tab=books&work=hc%3A123"},
	{"book:hc:123:approved:1700000000", "/discover?tab=books&work=hc%3A123"},
	{"book:hc:123:audiobook", "/discover?tab=books&work=hc%3A123"},
	{"book:a b/c", "/discover?tab=books&work=a%20b%2Fc"},
	{"request:40:new:1700000000", "/requests?id=40"},
	{"request:40:new:1700000000:2", "/requests?id=40"},
	{"1007", ""},
	{"", ""},
	{"movie:", ""},
	{"movie:abc", ""},
	{"movie:0", ""},
	{"book:", ""},
	{"episode:5", ""},
}

func TestRefPath(t *testing.T) {
	for _, v := range refPathVectors {
		if got := RefPath(v.ref); got != v.want {
			t.Errorf("RefPath(%q) = %q, want %q", v.ref, got, v.want)
		}
	}
	if got := RefPath("+5"); got != "" {
		t.Errorf("RefPath(%q) = %q", "+5", got)
	}
	if got := RefPath("movie:+5"); got != "" {
		t.Errorf("a signed id makes an address: %q", got)
	}
}

// urlPush records where each Web Push would open.
type urlPush struct {
	mu   sync.Mutex
	urls []string
}

func (p *urlPush) SendToUserAsync(_ int64, _, _, url string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.urls = append(p.urls, url)
}

// A 'ready' push opens that exact title, and a book's decision opens the book — not the
// Discover front page.
func TestReadyPushURL(t *testing.T) {
	s, _, _ := quietFixture(t)
	push := &urlPush{}
	s.push = push
	ctx := context.Background()

	req, _, err := s.Create(ctx, Request{MediaType: "movie", TMDBID: 603, Title: "The Matrix", RequestedBy: 7, RequestedByName: "alice"}, CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.notifyReady(ctx, req); err != nil {
		t.Fatal(err)
	}
	book := Request{ID: 9999, MediaType: "book", OLKey: "hc:123", Title: "Dune", RequestedBy: 7, DecidedAt: 1700000000}
	if _, err := s.notifyPartiesCount(ctx, book, "Request approved", "“Dune” was approved.", decisionRef(book, "approved"), "request-approved", nil); err != nil {
		t.Fatal(err)
	}
	// An unknown reference still opens something sensible.
	if _, err := s.notifyPartiesCount(ctx, Request{ID: 9998, MediaType: "movie", RequestedBy: 7}, "x", "y", "legacy-ref", "test", nil); err != nil {
		t.Fatal(err)
	}

	push.mu.Lock()
	defer push.mu.Unlock()
	want := []string{"/discover/movie/603", "/discover?tab=books&work=hc%3A123", "/discover"}
	if len(push.urls) != len(want) {
		t.Fatalf("pushes = %v, want %v", push.urls, want)
	}
	for i := range want {
		if push.urls[i] != want[i] {
			t.Errorf("push %d opens %q, want %q", i, push.urls[i], want[i])
		}
	}
}
