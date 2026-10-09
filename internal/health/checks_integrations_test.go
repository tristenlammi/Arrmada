package health

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tristenlammi/arrmada/internal/download"
)

func clientsCheck(n int, st ...download.ClientState) Check {
	return DownloadClientsCheck(func(context.Context) (int, []download.ClientState, error) { return n, st, nil })
}

// One client down of two is a warning naming it; the only client down is an error; no
// client at all, or none switched on, is an error linking to the clients page.
func TestDownloadClientsCheck(t *testing.T) {
	got := run(clientsCheck(2,
		download.ClientState{ID: 1, Name: "qBittorrent (bundled)", OK: true},
		download.ClientState{ID: 2, Name: "qBittorrent (seedbox)", Err: "connection refused"}))
	if len(got) != 1 || got[0].Key != "downloads.client.2" || got[0].Level != LevelWarning ||
		!strings.Contains(got[0].Message, "qBittorrent (seedbox) is unreachable") || got[0].Fix != FixDownloadClients {
		t.Errorf("second client down: %+v", got)
	}

	got = run(clientsCheck(1, download.ClientState{ID: 1, Name: "qBittorrent (bundled)", Err: "connection refused"}))
	if len(got) != 1 || got[0].Level != LevelError || !strings.Contains(got[0].Message, "connection refused") {
		t.Errorf("only client down: %+v", got)
	}

	if got := run(clientsCheck(0)); len(got) != 1 || got[0].Key != "downloads.client.none" || got[0].Level != LevelError {
		t.Errorf("no clients: %+v", got)
	}
	if got := run(clientsCheck(2)); len(got) != 1 || !strings.Contains(got[0].Message, "switched off") {
		t.Errorf("all disabled: %+v", got)
	}
	if got := run(clientsCheck(1, download.ClientState{ID: 1, OK: true})); len(got) != 0 {
		t.Errorf("all up: %+v", got)
	}
}

// A refused token is an error at once. Unreachable is nothing for the first ten
// minutes (a Plex restart), then a warning. Plex not set up is skipped.
func TestPlexCheck(t *testing.T) {
	now := time.Date(2026, 10, 9, 14, 30, 0, 0, time.Local)
	clock := func() time.Time { return now }
	noProbe := func(context.Context) error { t.Error("probed while monitoring"); return nil }
	check := func(st PlexState) []Finding {
		return run(plexCheck(func(context.Context) PlexState { return st }, noProbe, clock))
	}

	if got := check(PlexState{Configured: true, Monitoring: true, Consecutive: 1, Unauthorized: true, FailingSince: now}); len(got) != 1 ||
		got[0].Level != LevelError || !strings.Contains(got[0].Message, "rejected") || got[0].Fix != FixPlexConnection {
		t.Errorf("401: %+v", got)
	}
	lastOK := now.Add(-9*time.Minute - 30*time.Second)
	down := PlexState{Configured: true, Monitoring: true, Consecutive: 100, LastOKAt: lastOK, FailingSince: now.Add(-9 * time.Minute), LastErr: "connection refused"}
	if got := check(down); len(got) != 0 {
		t.Errorf("down 9 min: %+v", got)
	}
	down.FailingSince = now.Add(-11 * time.Minute)
	got := check(down)
	if len(got) != 1 || got[0].Level != LevelWarning || !strings.Contains(got[0].Message, "since "+lastOK.Format("15:04")) ||
		!strings.Contains(got[0].Message, "connection refused") {
		t.Errorf("down 11 min: %+v", got)
	}
	if got := check(PlexState{Configured: false, Consecutive: 5, Unauthorized: true}); len(got) != 0 {
		t.Errorf("not configured: %+v", got)
	}
}

// With monitoring off, the check asks Plex itself — but at most every five minutes.
func TestPlexCheckProbesWhenNotMonitoring(t *testing.T) {
	now := time.Date(2026, 10, 9, 14, 30, 0, 0, time.Local)
	var probes atomic.Int32
	st := PlexState{Configured: true}
	c := plexCheck(func(context.Context) PlexState { return st }, func(context.Context) error {
		probes.Add(1)
		st.Consecutive, st.Unauthorized, st.FailingSince = 1, true, now
		return errors.New("plex rejected the token (401)")
	}, func() time.Time { return now })

	if got := run(c); len(got) != 1 || got[0].Level != LevelError {
		t.Errorf("first run: %+v", got)
	}
	now = now.Add(time.Minute)
	run(c)
	if probes.Load() != 1 {
		t.Errorf("probed %d times inside five minutes", probes.Load())
	}
	now = now.Add(5 * time.Minute)
	run(c)
	if probes.Load() != 2 {
		t.Errorf("not probed again after five minutes (%d)", probes.Load())
	}
}

// TMDB: no key and a refused key are errors, a network failure only a warning; TMDB is
// asked again only when the key changes or the recheck time passes.
func TestTMDBCheck(t *testing.T) {
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	key := ""
	var asks atomic.Int32
	answer := error(nil)
	c := tmdbCheck(func() string { return key }, func(context.Context) error { asks.Add(1); return answer }, func() time.Time { return now })

	if got := run(c); len(got) != 1 || got[0].Level != LevelError || !strings.Contains(got[0].Message, "No TMDB key") || got[0].Fix != FixAPIKeys {
		t.Errorf("no key: %+v", got)
	}
	if asks.Load() != 0 {
		t.Error("asked TMDB with no key")
	}

	key, answer = "bad", ErrKeyRejected
	if got := run(c); len(got) != 1 || got[0].Level != LevelError || !strings.Contains(got[0].Message, "rejected") {
		t.Errorf("bad key: %+v", got)
	}
	now = now.Add(time.Hour)
	run(c)
	if asks.Load() != 1 {
		t.Errorf("re-asked inside six hours: %d", asks.Load())
	}
	c.Run(withForced(context.Background())) // "Check now", or the key was just tested
	if asks.Load() != 2 {
		t.Errorf("a forced run didn't ask TMDB: %d", asks.Load())
	}

	key, answer = "good", nil // saved a new key: asked at once
	if got := run(c); len(got) != 0 || asks.Load() != 3 {
		t.Errorf("good key: %+v (asks %d)", got, asks.Load())
	}

	answer = errors.New("tmdb request: dial tcp: i/o timeout")
	now = now.Add(7 * time.Hour)
	got := run(c)
	if len(got) != 1 || got[0].Level != LevelWarning {
		t.Errorf("network failure: %+v", got)
	}
	for _, f := range got {
		if strings.Contains(f.Message, "good") {
			t.Errorf("the key is in the message: %s", f.Message)
		}
	}
}

// Three failures in a row warn, naming the task and its error; fewer don't.
func TestTasksFailingCheck(t *testing.T) {
	got := run(TasksFailingCheck(func() []TaskState {
		return []TaskState{
			{Name: "rss-sync", Label: "Check indexer feeds for new movies", LastError: "indexer timeout", ConsecutiveFailures: 3},
			{Name: "upgrade-movies", ConsecutiveFailures: 2, LastError: "x"},
			{Name: "db-backup", ConsecutiveFailures: 0},
		}
	}))
	if len(got) != 1 || got[0].Key != "tasks.failing.rss-sync" || got[0].Fix != FixTasks ||
		got[0].Message != "“Check indexer feeds for new movies” has failed 3 times in a row: indexer timeout" {
		t.Errorf("got %+v", got)
	}
}
