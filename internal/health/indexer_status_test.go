package health

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// A paused indexer among working ones is a warning naming it, its error and its next
// try; every enabled one paused is an error; nothing paused, or a read error, says nothing.
func TestIndexerStatusCheck(t *testing.T) {
	now := time.Date(2026, 10, 9, 14, 0, 0, 0, time.Local)
	until := now.Add(time.Hour)
	tl := PausedIndexer{ID: 3, Name: "TorrentLeech", Failures: 4, Until: until, LastError: "login failed"}
	check := func(enabled int, paused []PausedIndexer, err error) []Finding {
		return indexerStatusCheck(func(context.Context) (int, []PausedIndexer, error) { return enabled, paused, err }, func() time.Time { return now }).Run(context.Background())
	}

	got := check(2, []PausedIndexer{tl}, nil)
	if len(got) != 1 || got[0].Key != "indexers.paused.3" || got[0].Level != LevelWarning || got[0].Fix != FixIndexerStatus {
		t.Fatalf("one of two paused: %+v", got)
	}
	for _, want := range []string{"TorrentLeech", "4 times", "15:00", "login failed"} {
		if !strings.Contains(got[0].Message, want) {
			t.Errorf("message %q is missing %q", got[0].Message, want)
		}
	}

	if got := check(1, []PausedIndexer{tl}, nil); len(got) != 1 || got[0].Level != LevelError {
		t.Fatalf("every indexer paused should be an error: %+v", got)
	}
	if got := check(2, nil, nil); len(got) != 0 {
		t.Fatalf("nothing paused: %+v", got)
	}
	if got := check(0, nil, errors.New("db")); len(got) != 0 {
		t.Fatalf("read error: %+v", got)
	}
}
