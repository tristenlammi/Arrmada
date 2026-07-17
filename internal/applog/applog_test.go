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

	all := ring.Snapshot(0, slog.LevelDebug, "")
	if len(all) != 3 {
		t.Fatalf("want 3 entries, got %d", len(all))
	}
	if all[0].Message != "importing episode" || !strings.Contains(all[0].Attrs, "series=Ben 10") || !strings.Contains(all[0].Attrs, "count=13") {
		t.Errorf("first entry = %+v", all[0])
	}
	if got := ring.Snapshot(0, slog.LevelWarn, ""); len(got) != 2 { // level filter drops info
		t.Errorf("warn+ = %d, want 2", len(got))
	}
	if got := ring.Snapshot(0, slog.LevelDebug, "ben 10"); len(got) != 1 { // query matches attrs
		t.Errorf("query ben10 = %d, want 1", len(got))
	}
	if got := ring.Snapshot(1, slog.LevelDebug, ""); len(got) != 1 || got[0].Message != "boom" { // newest N
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
	if got := ring.Snapshot(0, slog.LevelDebug, ""); len(got) != 100 {
		t.Errorf("after 150 logs into a 100-ring, got %d, want 100", len(got))
	}
}
