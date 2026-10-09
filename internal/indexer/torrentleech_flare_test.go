package indexer

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tristenlammi/arrmada/internal/flaresolverr"
)

// With FlareSolverr set but its container down, the TorrentLeech error names the
// container (and no request reaches TorrentLeech); with none set, it says where to set
// one — not "configure FlareSolverr", which pointed at nothing.
func TestTorrentLeechFlareSolverrErrors(t *testing.T) {
	dead := httptest.NewServer(http.NotFoundHandler())
	deadURL := dead.URL
	dead.Close()
	tl := NewTorrentLeechSearcher(flaresolverr.New(deadURL))
	_, err := tl.newSession(context.Background(), Indexer{ID: 1, Username: "u", Password: "p"})
	if err == nil || !strings.Contains(err.Error(), "isn't answering") || !strings.Contains(err.Error(), "Arrmada-flaresolverr container") {
		t.Fatalf("unreachable FlareSolverr: %v", err)
	}

	msg := errNoFlareSolverr.Error()
	if !strings.Contains(msg, "FlareSolverr isn't set up") || !strings.Contains(msg, "Settings → System → API keys") {
		t.Fatalf("not-configured text = %q", msg)
	}
	// A client with no URL is "not configured", just like none at all.
	if NewTorrentLeechSearcher(flaresolverr.New("")).fs.Configured() || NewTorrentLeechSearcher(nil).fs.Configured() {
		t.Fatal("an empty FlareSolverr URL must read as not configured")
	}
}
