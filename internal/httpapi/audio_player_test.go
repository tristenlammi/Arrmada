package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/tristenlammi/arrmada/internal/audioserver"
	"github.com/tristenlammi/arrmada/internal/auth"
	"github.com/tristenlammi/arrmada/internal/books"
	"github.com/tristenlammi/arrmada/internal/listening"
	"github.com/tristenlammi/arrmada/internal/metadata"
)

// audioHarness is the real router with the audiobook server behind it: one book with a
// three-file audiobook (fake mp3s in a temp dir), the server switched on, and every log
// line captured so a test can check nothing names what anyone listens to.
type audioHarness struct {
	t      *testing.T
	rs     *routeServer
	srv    *audioserver.Server
	key    string
	bookID int64
	title  string
	logs   *syncBuffer
}

// syncBuffer is a log sink handlers write to while the test reads it.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func newAudioHarness(t *testing.T) *audioHarness {
	t.Helper()
	h := &audioHarness{t: t, logs: &syncBuffer{}, title: "Dungeon Crawler Carl"}
	log := slog.New(slog.NewTextHandler(h.logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	h.rs = newRouteServer(t, func(d *Deps) {
		d.Log = log
		ctx := context.Background()
		db := d.Store.DB()
		bs := books.NewService(db, nil, log)
		added, _ := bs.AddWorks(ctx, []metadata.BookResult{{Key: "hc:1", Title: h.title, Author: "Matt Dinniman"}}, "", true)
		if len(added) != 1 {
			t.Fatal("book not added")
		}
		dir := filepath.Join(t.TempDir(), "Matt Dinniman", h.title)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		for _, n := range []string{"Part 1.mp3", "Part 2.mp3"} {
			if err := os.WriteFile(filepath.Join(dir, n), bytes.Repeat([]byte(n[:6]), 4000), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		if err := bs.MarkImported(ctx, added[0].ID, books.KindAudiobook, dir, "MP3", 48000, 2); err != nil {
			t.Fatal(err)
		}
		h.bookID = added[0].ID
		h.key = audioserver.ItemKey(added[0].ID, 0)
		h.srv = audioserver.New(audioserver.Options{DB: db, Books: bs, Listen: listening.NewStore(db), Users: d.Auth,
			Settings: d.Settings, Log: log, FFprobe: "ffprobe-not-installed", DataDir: t.TempDir()})
		d.AudioServer = h.srv
		if err := d.Settings.Set(ctx, audioserver.KeyEnabled, "true"); err != nil {
			t.Fatal(err)
		}
	})
	return h
}

// req sends a request as the cookie's account. external makes it come from the internet
// (through the tunnel) rather than the LAN.
func (h *audioHarness) req(method, path string, body any, c *http.Cookie, external bool, hdr ...string) *httptest.ResponseRecorder {
	h.t.Helper()
	var rd io.Reader
	if body != nil {
		if raw, ok := body.(string); ok {
			rd = bytes.NewReader([]byte(raw))
		} else {
			b, _ := json.Marshal(body)
			rd = bytes.NewReader(b)
		}
	}
	r := httptest.NewRequest(method, "http://arrmada.local"+path, rd)
	r.RemoteAddr = "192.168.1.20:5000"
	if external {
		r.RemoteAddr = "8.8.8.8:5000"
	}
	if body != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		r.Header.Set(hdr[i], hdr[i+1])
	}
	if c != nil {
		r.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	h.rs.h.ServeHTTP(rec, r)
	return rec
}

// ok sends a request that must answer 200 and decodes the JSON reply.
func (h *audioHarness) ok(method, path string, body any, c *http.Cookie) map[string]any {
	h.t.Helper()
	rec := h.req(method, path, body, c, false)
	if rec.Code != http.StatusOK {
		h.t.Fatalf("%s %s: HTTP %d %s", method, path, rec.Code, rec.Body)
	}
	var m map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		h.t.Fatalf("%s %s: not a JSON object: %s", method, path, rec.Body)
	}
	return m
}

// The You page gets a "later spot" offer for an upload that wasn't used and a Recently
// removed list, and the undiscard and dismiss actions only ever touch the caller's own.
func TestMyAudioOffersAndRemoved(t *testing.T) {
	h := newAudioHarness(t)
	kid, kidC := h.rs.user(t, "kid", auth.RoleRequester)
	_, momC := h.rs.user(t, "mom", auth.RoleRequester)
	ctx := context.Background()
	store := h.srv.Listen()

	// The kid's place, then a stale upload of a later spot (an offline phone).
	if _, err := store.SetProgress(ctx, kid.ID, h.key, 3600, 36000, nil, "web"); err != nil {
		t.Fatal(err)
	}
	stale := time.Now().Add(-time.Hour).UnixMilli()
	if _, err := store.ReportPosition(ctx, kid.ID, h.key, 14531, 36000, false, stale, "Pixel"); err != nil {
		t.Fatal(err)
	}
	me := h.ok("GET", "/api/v1/me/audio", nil, kidC)
	places := me["places"].([]any)
	if len(places) != 1 {
		t.Fatalf("places = %v", places)
	}
	offer, _ := places[0].(map[string]any)["offer"].(map[string]any)
	if offer == nil || offer["position"] != 14531.0 || offer["device"] != "Pixel" {
		t.Fatalf("offer = %v, want the 14531 from the Pixel", places[0])
	}
	hid := int64(offer["history_id"].(float64))

	// Someone else can't dismiss (or see) it.
	if rec := h.req("POST", "/api/v1/me/audio/dismiss", map[string]any{"item": h.key, "history_id": hid}, momC, false); rec.Code != http.StatusNotFound {
		t.Fatalf("another account dismissing it: HTTP %d, want 404", rec.Code)
	}
	if mom := h.ok("GET", "/api/v1/me/audio", nil, momC); len(mom["places"].([]any)) != 0 || len(mom["removed"].([]any)) != 0 {
		t.Fatalf("another account sees the kid's places: %v", mom)
	}
	if rec := h.req("POST", "/api/v1/me/audio/dismiss", map[string]any{"item": h.key, "history_id": hid}, kidC, false); rec.Code != http.StatusNoContent {
		t.Fatalf("dismiss: HTTP %d %s", rec.Code, rec.Body)
	}
	if o := h.ok("GET", "/api/v1/me/audio", nil, kidC)["places"].([]any)[0].(map[string]any)["offer"]; o != nil {
		t.Fatalf("a dismissed offer is still offered: %v", o)
	}

	// An app removes the place: it moves to removed, and comes back exactly.
	if err := store.DeleteProgress(ctx, kid.ID, h.key, "Lissen"); err != nil {
		t.Fatal(err)
	}
	me = h.ok("GET", "/api/v1/me/audio", nil, kidC)
	removed := me["removed"].([]any)
	if len(me["places"].([]any)) != 0 || len(removed) != 1 {
		t.Fatalf("after removal: places %v removed %v", me["places"], removed)
	}
	rm := removed[0].(map[string]any)
	if rm["position"] != 3600.0 || rm["device"] != "Lissen" || rm["title"] != h.title || rm["discarded_at"].(float64) <= 0 {
		t.Fatalf("removed row = %v", rm)
	}
	if rec := h.req("POST", "/api/v1/me/audio/undiscard", map[string]any{"item": h.key}, momC, false); rec.Code != http.StatusBadRequest {
		t.Fatalf("another account undiscarding it: HTTP %d, want 400", rec.Code)
	}
	if r := h.ok("POST", "/api/v1/me/audio/undiscard", map[string]any{"item": h.key}, kidC); r["position"] != 3600.0 {
		t.Fatalf("undiscard = %v", r)
	}
	me = h.ok("GET", "/api/v1/me/audio", nil, kidC)
	if len(me["places"].([]any)) != 1 || len(me["removed"].([]any)) != 0 {
		t.Fatalf("after undiscard: %v", me)
	}
	// The undiscard and dismiss routes work through the tunnel for a requester.
	if rec := h.req("POST", "/api/v1/me/audio/undiscard", map[string]any{"item": h.key}, kidC, true); rec.Code != http.StatusBadRequest {
		t.Fatalf("undiscard from outside: HTTP %d %s, want it to reach the handler (400: nothing to restore)", rec.Code, rec.Body)
	}
}
