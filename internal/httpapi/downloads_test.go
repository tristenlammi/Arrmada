package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/tristenlammi/arrmada/internal/download"
	"github.com/tristenlammi/arrmada/internal/settings"
	"github.com/tristenlammi/arrmada/internal/store"
)

// fakeQbit is just enough of qBittorrent's Web API for the resume paths: login, the
// torrent list, and start/stop, which flip a torrent's state and are recorded.
type fakeQbit struct {
	mu      sync.Mutex
	states  map[string]string // hash -> raw qBittorrent state
	started []string
}

func (f *fakeQbit) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch r.URL.Path {
	case "/api/v2/auth/login":
		fmt.Fprint(w, "Ok.")
	case "/api/v2/torrents/info":
		var list []map[string]any
		for h, st := range f.states {
			list = append(list, map[string]any{"hash": h, "name": "Release " + h, "state": st, "progress": 0.4})
		}
		_ = json.NewEncoder(w).Encode(list)
	case "/api/v2/torrents/start", "/api/v2/torrents/stop":
		_ = r.ParseForm()
		h := r.Form.Get("hashes")
		if r.URL.Path == "/api/v2/torrents/start" {
			f.started = append(f.started, h)
			f.states[h] = "downloading"
		} else {
			f.states[h] = "pausedDL"
		}
	default:
		http.NotFound(w, r)
	}
}

// guardAPI wires the real download service and disk guard over a fake qBittorrent.
// The guard's thresholds are set to 1%/0% so any real volume reads as over the line —
// the guard measures the disk it runs on, and this keeps the test off the owner's.
func guardAPI(t *testing.T, states map[string]string) (*api, *fakeQbit, context.Context) {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	fq := &fakeQbit{states: states}
	srv := httptest.NewServer(fq)
	t.Cleanup(srv.Close)

	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	dl := download.NewService(st.DB(), log)
	if _, err := dl.Create(ctx, download.Client{Name: "qb", Kind: download.KindQbittorrent, URL: srv.URL, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	set := settings.NewService(st.DB())
	_ = set.Set(ctx, download.KeyDiskGuardPause, "1")
	_ = set.Set(ctx, download.KeyDiskGuardResum, "0")
	guard := download.NewDiskGuard(dl, set, log, t.TempDir())
	if !guard.Status(ctx).Measurable {
		t.Skip("this platform can't measure disk usage")
	}
	return &api{deps: Deps{Store: st, Log: log, Downloads: dl, DiskGuard: guard, Settings: set}}, fq, ctx
}

func postResume(a *api, hash string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/queue/"+hash+"/resume", nil)
	req.SetPathValue("hash", hash)
	rec := httptest.NewRecorder()
	a.handleResumeDownload(rec, req)
	return rec
}

// During a hold, "Resume all" resumes only what the user paused, and says how many it
// left for the guard. It used to be forwarded to qBittorrent as hash "all".
func TestResumeAllSkipsTorrentsTheGuardHolds(t *testing.T) {
	a, fq, ctx := guardAPI(t, map[string]string{"aaa": "downloading", "bbb": "pausedDL"})
	if err := a.deps.DiskGuard.Check(ctx); err != nil { // pauses aaa and holds it
		t.Fatal(err)
	}
	if !a.deps.DiskGuard.Held(ctx)["aaa"] {
		t.Fatal("setup: the guard should be holding aaa")
	}

	rec := postResume(a, "all")
	if rec.Code != http.StatusOK {
		t.Fatalf("resume all: %d %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Resumed int `json:"resumed"`
		Held    int `json:"held_by_guard"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if body.Resumed != 1 || body.Held != 1 {
		t.Errorf("resumed=%d held_by_guard=%d, want 1 and 1", body.Resumed, body.Held)
	}
	if len(fq.started) != 1 || fq.started[0] != "bbb" {
		t.Errorf("started %v, want only the manually paused torrent", fq.started)
	}
}

// Resuming a held torrent from its card is refused with a reason and a way out.
func TestResumeHeldTorrentIsRefused(t *testing.T) {
	a, fq, ctx := guardAPI(t, map[string]string{"aaa": "downloading"})
	_ = a.deps.DiskGuard.Check(ctx)

	rec := postResume(a, "AAA")
	if rec.Code != http.StatusConflict {
		t.Fatalf("held resume: %d %s, want 409", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Held by the disk guard") || !strings.Contains(rec.Body.String(), "turn the guard off") {
		t.Errorf("message should explain and say how to override: %s", rec.Body.String())
	}
	if len(fq.started) != 0 {
		t.Errorf("a refused resume still reached the client: %v", fq.started)
	}

	// With the guard off it isn't holding anything, so the same resume goes through.
	_ = a.deps.Settings.SetBool(ctx, download.KeyDiskGuard, false)
	if rec := postResume(a, "aaa"); rec.Code != http.StatusOK {
		t.Errorf("resume with the guard off: %d %s", rec.Code, rec.Body.String())
	}
}
