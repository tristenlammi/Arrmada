package audioserver

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// traceHarness is a harness logging to a buffer, with a clock the test moves.
func traceHarness(t *testing.T) (*harness, *lockedBuffer, *atomic.Int64) {
	t.Helper()
	var buf lockedBuffer
	h := newHarnessLogging(t, slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	var now atomic.Int64
	now.Store(time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC).UnixMilli())
	h.srv.now = func() time.Time { return time.UnixMilli(now.Load()) }
	// A cover uploaded in Arrmada, so the cover request answers 200 (failures are logged
	// anyway; tracing is about the ones that succeed).
	if err := os.MkdirAll(h.srv.coverDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(h.srv.coverDir, fmt.Sprintf("book-%d.jpg", h.book.ID)), []byte("jpg"), 0o644); err != nil {
		t.Fatal(err)
	}
	h.signIn()
	return h, &buf, &now
}

// playTraffic is the steady traffic of listening: a play, a sync, a cover and a file.
func playTraffic(h *harness) {
	h.t.Helper()
	key := itemKeyFor(h.book.ID, 0)
	play := h.json("POST", "/api/items/"+key+"/play", map[string]any{
		"deviceInfo": map[string]string{"clientName": "Lissen", "deviceId": "dev-1"}})
	files := list1(h.t, obj1(h.t, play["libraryItem"])["media"].(map[string]any)["audioFiles"])
	ino := obj1(h.t, files[0])["ino"].(string)
	h.do("POST", "/api/session/"+play["id"].(string)+"/sync", map[string]float64{"timeListened": 5, "currentTime": 30}, nil)
	h.do("GET", "/api/items/"+key+"/cover?width=400", nil, nil)
	h.do("GET", "/api/items/"+key+"/file/"+ino+"?token="+h.token, nil, nil)
	h.do("PATCH", "/api/me/progress/"+key, map[string]any{"currentTime": 40, "duration": 36000}, nil)
}

func traced(log string) []string {
	var out []string
	for _, l := range strings.Split(log, "\n") {
		if strings.Contains(l, "trace=true") {
			out = append(out, l)
		}
	}
	return out
}

// Tracing logs the playing traffic while it's on, and stops by itself when the time is up.
func TestTraceSwitchExpires(t *testing.T) {
	h, buf, now := traceHarness(t)
	ctx := context.Background()

	playTraffic(h)
	if got := traced(buf.String()); len(got) != 0 {
		t.Fatalf("traced with tracing off: %v", got)
	}

	until, err := h.srv.SetTrace(ctx, 48*time.Hour) // capped at 24 h
	if err != nil {
		t.Fatal(err)
	}
	if want := now.Load() + MaxTrace.Milliseconds(); until != want || h.srv.TraceUntil() != want {
		t.Fatalf("trace until %d (%d), want %d", until, h.srv.TraceUntil(), want)
	}
	// A restart picks the saved end time back up.
	if s2 := New(Options{DB: h.srv.db, Books: h.srv.books, Listen: h.srv.listen, Users: h.srv.users, Settings: h.srv.settings,
		Log: slog.Default(), DataDir: t.TempDir()}); s2.traceUntil.Load() != until {
		t.Fatalf("restart lost the trace: %d", s2.traceUntil.Load())
	}
	playTraffic(h)
	log := buf.String()
	for _, route := range []string{"POST /api/session/{sid}/sync", "GET /api/items/{id}/cover", "GET /api/items/{id}/file/{ino}", "PATCH /api/me/progress/{id}"} {
		found := false
		for _, l := range traced(log) {
			if strings.Contains(l, route) {
				found = true
			}
		}
		if !found {
			t.Errorf("tracing on: no traced line for %s:\n%s", route, log)
		}
	}

	// A day later it's off, without anyone switching it off.
	now.Add(MaxTrace.Milliseconds())
	if h.srv.TraceUntil() != 0 {
		t.Fatal("tracing outlived its time")
	}
	before := len(traced(buf.String()))
	playTraffic(h)
	if after := len(traced(buf.String())); after != before {
		t.Fatalf("still tracing after the time was up: %d new lines", after-before)
	}

	// Stop ends it at once.
	if _, err := h.srv.SetTrace(ctx, time.Hour); err != nil {
		t.Fatal(err)
	}
	if _, err := h.srv.SetTrace(ctx, 0); err != nil || h.srv.TraceUntil() != 0 {
		t.Fatalf("stop: %v %d", err, h.srv.TraceUntil())
	}
}

// Traced lines are route patterns: no item id, no query value, no token, no username.
func TestTraceLogsNoIdsOrQueries(t *testing.T) {
	h, buf, _ := traceHarness(t)
	if _, err := h.srv.SetTrace(context.Background(), time.Hour); err != nil {
		t.Fatal(err)
	}
	playTraffic(h)
	h.http.Close()
	lines := traced(buf.String())
	if len(lines) == 0 {
		t.Fatal("nothing traced")
	}
	key := itemKeyFor(h.book.ID, 0)
	itemRe := regexp.MustCompile(`(^|[^a-z0-9])` + key + `([^0-9v]|$)`)
	for _, l := range lines {
		if itemRe.MatchString(l) || strings.Contains(l, "width=400") ||
			strings.Contains(l, h.token) || strings.Contains(l, "reader") || strings.Contains(l, "Dungeon") {
			t.Errorf("traced line names something: %s", l)
		}
		if !strings.Contains(l, "route=") {
			t.Errorf("traced line without a route: %s", l)
		}
	}
	for _, l := range lines {
		if strings.Contains(l, "/cover") && !strings.Contains(l, "query_keys=width ") {
			t.Errorf("cover line should carry the query key only: %s", l)
		}
		if strings.Contains(l, "/file/") && strings.Contains(l, "query_keys=token") {
			t.Errorf("the token query key is dropped: %s", l)
		}
	}
}
