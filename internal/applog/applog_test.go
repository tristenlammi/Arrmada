package applog

import (
	"log/slog"
	"strings"
	"testing"
)

type discard struct{}

func (discard) Write(p []byte) (int, error) { return len(p), nil }

func TestRingCaptureAndFilter(t *testing.T) {
	ring := NewRing(100)
	log := slog.New(NewHandler(slog.NewTextHandler(discard{}, &slog.HandlerOptions{Level: slog.LevelDebug}), ring))
	log.Info("importing episode", "series", "Ben 10", "count", 13)
	log.Warn("stalled download", "movie", "Hope")
	log.Error("boom")

	all := ring.Snapshot(Filter{Min: slog.LevelDebug})
	if len(all) != 3 {
		t.Fatalf("want 3 entries, got %d", len(all))
	}
	if all[0].Message != "importing episode" || !strings.Contains(all[0].Attrs, "series=Ben 10") || !strings.Contains(all[0].Attrs, "count=13") {
		t.Errorf("first entry = %+v", all[0])
	}
	if got := ring.Snapshot(Filter{Min: slog.LevelWarn}); len(got) != 2 { // level filter drops info
		t.Errorf("warn+ = %d, want 2", len(got))
	}
	if got := ring.Snapshot(Filter{Min: slog.LevelDebug, Query: "ben 10"}); len(got) != 1 { // query matches attrs
		t.Errorf("query ben10 = %d, want 1", len(got))
	}
	if got := ring.Snapshot(Filter{Limit: 1, Min: slog.LevelDebug}); len(got) != 1 || got[0].Message != "boom" { // newest N
		t.Errorf("limit 1 = %+v", got)
	}
}

// TestRingWraps confirms the buffer keeps the most recent entries once it overflows.
func TestRingWraps(t *testing.T) {
	ring := NewRing(100) // min capacity
	log := slog.New(NewHandler(slog.NewTextHandler(discard{}, nil), ring))
	for i := 0; i < 150; i++ {
		log.Info("line")
	}
	if got := ring.Snapshot(Filter{Min: slog.LevelDebug}); len(got) != 100 {
		t.Errorf("after 150 logs into a 100-ring, got %d, want 100", len(got))
	}
}

// The exclude filter is what makes a busy log readable. Routine chatter — per-page
// indexer tracing especially — can outnumber everything else by an order of magnitude,
// and a keep-only filter is no help when you don't yet know what you're looking for.
func TestRingHideExcludesNoise(t *testing.T) {
	ring := NewRing(100)
	log := slog.New(NewHandler(slog.NewTextHandler(discard{}, nil), ring))
	log.Info("torznab page", "indexer", "TorrentLeech", "page", 0)
	log.Info("indexer search", "query", "Peppa Pig")
	log.Info("series: grabbing", "series", "Taskmaster")
	log.Warn("series: grab failed", "series", "Taskmaster")

	got := ring.Snapshot(Filter{Min: slog.LevelDebug, Hide: "torznab,indexer search"})
	if len(got) != 2 {
		t.Fatalf("hiding the indexer chatter should leave 2 entries, got %d: %+v", len(got), got)
	}
	for _, e := range got {
		if strings.Contains(e.Message, "torznab") || strings.Contains(e.Message, "indexer search") {
			t.Errorf("hidden entry survived: %+v", e)
		}
	}

	// Hide matches attrs too, not just the message — that's where the indexer name lives.
	if got := ring.Snapshot(Filter{Min: slog.LevelDebug, Hide: "torrentleech"}); len(got) != 3 {
		t.Errorf("hiding by an attr value = %d entries, want 3", len(got))
	}
}

// A blank or trailing-comma exclude list must not turn into a term that matches
// everything and blanks the whole page.
func TestRingHideIgnoresEmptyTerms(t *testing.T) {
	ring := NewRing(100)
	log := slog.New(NewHandler(slog.NewTextHandler(discard{}, nil), ring))
	log.Info("one")
	log.Info("two")

	for _, hide := range []string{"", "   ", ",", ",,", "torznab,", " , "} {
		if got := ring.Snapshot(Filter{Min: slog.LevelDebug, Hide: hide}); len(got) != 2 {
			t.Errorf("hide=%q dropped entries it shouldn't: got %d, want 2", hide, len(got))
		}
	}
}

// Query narrows first, then Hide subtracts from what's left — so you can search for a
// show and still drop its indexer chatter.
func TestRingQueryAndHideCompose(t *testing.T) {
	ring := NewRing(100)
	log := slog.New(NewHandler(slog.NewTextHandler(discard{}, nil), ring))
	log.Info("torznab page", "q", "Taskmaster")
	log.Info("series: grabbing", "series", "Taskmaster")
	log.Info("series: grabbing", "series", "Peppa Pig")

	got := ring.Snapshot(Filter{Min: slog.LevelDebug, Query: "taskmaster", Hide: "torznab"})
	if len(got) != 1 || got[0].Message != "series: grabbing" {
		t.Errorf("query+hide = %+v, want just the Taskmaster grab", got)
	}
}
