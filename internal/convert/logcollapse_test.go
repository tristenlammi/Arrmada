package convert

import (
	"context"
	"fmt"
	"testing"
)

// Identical consecutive lines fold into one "msg (×N)" line, in memory and in the database,
// so a retry loop can't push the real history out of the log. A different line in between
// starts a new line, and the fold survives a restart (the ring is reloaded from the DB).
func TestEventCollapsesRepeats(t *testing.T) {
	s := newTestService(t)
	for i := 0; i < 3; i++ {
		s.event("warn", "scratch is full")
	}
	s.event("info", "scratch is full") // same text, other level: its own line
	s.event("info", "a")
	s.event("info", "b")
	s.event("info", "a")

	want := []LogLine{
		{Level: "warn", Msg: "scratch is full (×3)"},
		{Level: "info", Msg: "scratch is full"},
		{Level: "info", Msg: "a"},
		{Level: "info", Msg: "b"},
		{Level: "info", Msg: "a"},
	}
	check := func(where string, got []LogLine) {
		t.Helper()
		if len(got) != len(want) {
			t.Fatalf("%s: %d lines, want %d: %+v", where, len(got), len(want), got)
		}
		for i := range want {
			if got[i].Level != want[i].Level || got[i].Msg != want[i].Msg {
				t.Errorf("%s line %d = %s %q, want %s %q", where, i, got[i].Level, got[i].Msg, want[i].Level, want[i].Msg)
			}
		}
	}
	check("memory", s.Logs())
	check("database", s.logs.recent(context.Background(), maxLogLines))
}

// A long run keeps counting past nine without nesting the suffix.
func TestEventCollapseCountsOn(t *testing.T) {
	s := newTestService(t)
	for i := 0; i < 12; i++ {
		s.event("info", "waiting for your encode hours")
	}
	got := s.Logs()
	if len(got) != 1 || got[0].Msg != "waiting for your encode hours (×12)" {
		t.Fatalf("got %+v", got)
	}
}

// updateLast rewrites the newest row and nothing else.
func TestLogStoreUpdateLast(t *testing.T) {
	s := newTestService(t)
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		s.logs.append(ctx, LogLine{At: int64(i), Level: "info", Msg: fmt.Sprintf("line %d", i)})
	}
	s.logs.updateLast(ctx, LogLine{At: 99, Level: "info", Msg: "line 2 (×2)"})
	got := s.logs.recent(ctx, 10)
	if len(got) != 3 || got[0].Msg != "line 0" || got[1].Msg != "line 1" {
		t.Fatalf("older rows changed: %+v", got)
	}
	if got[2].Msg != "line 2 (×2)" || got[2].At != 99 {
		t.Fatalf("newest row = %+v, want the rewrite", got[2])
	}
}

func TestSplitRepeat(t *testing.T) {
	for _, c := range []struct {
		in   string
		base string
		n    int
	}{
		{"plain", "plain", 1},
		{"x (×3)", "x", 3},
		{"x (×12)", "x", 12},
		{"x (×0)", "x (×0)", 1},
		{"x (×3) tail", "x (×3) tail", 1},
	} {
		if b, n := splitRepeat(c.in); b != c.base || n != c.n {
			t.Errorf("splitRepeat(%q) = %q, %d; want %q, %d", c.in, b, n, c.base, c.n)
		}
	}
}
