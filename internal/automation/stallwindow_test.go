package automation

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/tristenlammi/arrmada/internal/download"
	"github.com/tristenlammi/arrmada/internal/quality"
)

// The grab's own value wins, then its profile's, then the global default — and 0 at
// either level means "keep looking", which is what turns fail-over on for every grab
// recorded back when 0 meant off.
func TestStallWindowResolution(t *testing.T) {
	_, q, ctx := profileStore(t)
	mk := func(name string, minutes int) string {
		return createDefault(t, q, quality.StoredProfile{MediaType: quality.MediaMovie, Name: name, StallMinutes: minutes})
	}
	useDefault, off, custom := mk("Default", 0), mk("Off", quality.StallOff), mk("Custom", 90)

	cases := []struct {
		name       string
		grab       int
		profile    string
		global     int
		want       time.Duration
		wantActive bool
	}{
		{"grab 0, profile 0, default 6h", 0, useDefault, 360, 6 * time.Hour, true},
		{"grab off", -1, useDefault, 360, 0, false},
		{"grab 2h", 120, off, 360, 2 * time.Hour, true},
		{"grab 0, profile off", 0, off, 360, 0, false},
		{"grab 0, profile custom", 0, custom, 360, 90 * time.Minute, true},
		{"global default off", 0, useDefault, 0, 0, false},
		{"unknown profile uses the default", 0, "custom:999", 360, 6 * time.Hour, true},
	}
	for _, tc := range cases {
		c := &Coordinator{quality: q}
		global := tc.global
		c.SetStallDefault(func(context.Context) int { return global })
		got, on := c.stallWindow(ctx, grab{StallMinutes: tc.grab, Profile: tc.profile})
		if got != tc.want || on != tc.wantActive {
			t.Errorf("%s: window %v on=%v, want %v on=%v", tc.name, got, on, tc.want, tc.wantActive)
		}
	}

	// Nothing wired (tests, or a build that forgot) still has fail-over on at six hours.
	if got, on := (&Coordinator{quality: q}).stallWindow(ctx, grab{Profile: useDefault}); !on || got != 6*time.Hour {
		t.Errorf("unwired default: %v on=%v, want 6h on", got, on)
	}
}

func TestParseStallMinutes(t *testing.T) {
	for raw, want := range map[string]int{"": 360, "0": 0, "120": 120, " 45 ": 45, "x": 360, "-5": 360} {
		if got := ParseStallMinutes(raw); got != want {
			t.Errorf("ParseStallMinutes(%q) = %d, want %d", raw, got, want)
		}
	}
}

// A grab the disk guard is holding never runs its clock down, whatever state the client
// reports for it; the same torrent un-held is judged as usual.
func TestGuardHeldTorrentIsNotStalled(t *testing.T) {
	c := stallCoord(t, 3)
	c.log = slog.New(slog.NewTextHandler(io.Discard, nil))
	const hash = "ABCDEF0123456789ABCDEF0123456789ABCDEF01"
	g := grab{ID: 3, StallMinutes: 60, GrabbedAt: "2020-01-01 00:00:00", InfoHash: hash}
	queue := []download.Item{{Hash: hash, RawState: "stalledDL", State: "downloading", Progress: 0.2, RemainingBytes: 1}}
	c.holdStallClock(context.Background(), g.ID, 0.2)
	expire := func() { expireStall(t, c, g.ID, 0.2, 2*time.Hour) }

	acted := false
	target := func(context.Context) (stallTarget, bool) { acted = true; return stallTarget{}, false }
	expire()
	c.judgeStall(context.Background(), g, queue, &stallTick{left: 3, held: map[string]bool{"abcdef0123456789abcdef0123456789abcdef01": true}}, target)
	if acted {
		t.Fatal("a guard-held torrent was judged stalled")
	}
	if s := stallClockAt(t, c, g.ID); time.Since(s) > time.Second {
		t.Error("the stall clock must be held while the guard holds the torrent")
	}

	expire()
	c.judgeStall(context.Background(), g, queue, &stallTick{left: 3}, target)
	if !acted {
		t.Error("control: the same torrent un-held and past its window should be judged stalled")
	}
}

// The panel line on Downloads: how long the clock has run, and how long until another
// release is tried.
func TestStallInfo(t *testing.T) {
	db, q, ctx := profileStore(t)
	c := &Coordinator{db: db, quality: q}
	c.SetStallDefault(func(context.Context) int { return 360 })
	res, err := db.Exec(`INSERT INTO grabs (movie_id, title, stall_minutes, media_type, info_hash, grabbed_at)
		VALUES (1, 'Some.Film.2001.1080p', 0, 'movie', 'ABC123', datetime('now', '-10 hours'))`)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	if _, err := db.Exec(`INSERT INTO grabs (movie_id, title, stall_minutes, media_type, info_hash) VALUES (2, 'Off.Film', -1, 'movie', 'DEF456')`); err != nil {
		t.Fatal(err)
	}
	expireStall(t, c, id, 0.1, 2*time.Hour)

	info := c.StallInfo(ctx)
	st, ok := info["abc123"]
	if !ok {
		t.Fatalf("no entry for the pending grab: %+v", info)
	}
	if st.Off || st.IdleMinutes != 120 || st.FailoverInMinutes != 240 {
		t.Errorf("stall = %+v, want idle 120, fail-over in 240", st)
	}
	if !info["def456"].Off {
		t.Errorf("a grab with fail-over off should say so: %+v", info["def456"])
	}
}
