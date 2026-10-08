package automation

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"testing"

	"github.com/tristenlammi/arrmada/internal/series"
	"github.com/tristenlammi/arrmada/internal/store"
)

func manualTestCoordinator(t *testing.T) (*Coordinator, context.Context) {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return &Coordinator{db: st.DB(), log: slog.New(slog.NewTextHandler(io.Discard, nil))}, context.Background()
}

// The import gate compares a finished download against what's on disk and skips anything
// that scores worse. Right for the automation; wrong for a release the user picked out of
// the interactive search — they saw the options and chose that one, so a lower-scoring
// pick is the answer, not a mistake to be corrected.
func TestGrabManualFlag(t *testing.T) {
	c, ctx := manualTestCoordinator(t)
	const auto, manual = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

	for _, h := range []string{auto, manual} {
		if _, err := c.db.ExecContext(ctx,
			`INSERT INTO grabs (movie_id, title, indexer, info_hash, media_type) VALUES (1, 'X', 'i', ?, 'series')`, h); err != nil {
			t.Fatal(err)
		}
	}

	// Nothing is manual until it's said to be — the automation's grabs stay gated.
	if c.grabForce(ctx, auto).On {
		t.Error("an ordinary grab must not read as manual")
	}
	c.markGrabManual(ctx, manual)
	if !c.grabForce(ctx, manual).On {
		t.Error("a grab marked manual must read back as manual")
	}
	if c.grabForce(ctx, auto).On {
		t.Error("marking one grab must not flag the others")
	}

	// Matching is on hash alone. An unknown hash is not manual — erring the other way
	// would let any untracked download overwrite a better file.
	if c.grabForce(ctx, "cccccccccccccccccccccccccccccccccccccccc").On {
		t.Error("an unrecorded hash must not read as manual")
	}
	if c.grabForce(ctx, "").On {
		t.Error("an empty hash must not read as manual")
	}
	// Case is not identity: qBittorrent and the indexer disagree on it constantly.
	if !c.grabForce(ctx, "BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB").On {
		t.Error("hash matching must be case-insensitive")
	}
}

// Each kind of grab records who chose it and for what. The import reads both back, so a
// row with the wrong scope either overwrites seasons nobody asked for or gates the one
// episode the user explicitly replaced.
func TestMarkGrabRecordsScope(t *testing.T) {
	c, ctx := manualTestCoordinator(t)
	cases := []struct {
		name      string
		manual    bool
		scope     GrabScope
		wantRow   string
		wantForce forceRule
	}{
		{"quick season Grab", false, GrabScope{Season: 3}, "0/S03", forceRule{}},
		{"quick episode Grab", false, GrabScope{Season: 3, Episode: 4}, "0/S03E04", forceRule{}},
		{"Replace", true, GrabScope{Season: 3, Episode: 4}, "1/S03E04", forceRule{On: true, Scope: GrabScope{Season: 3, Episode: 4}}},
		{"season-modal pick", true, GrabScope{Season: 3}, "1/S03", forceRule{On: true, Scope: GrabScope{Season: 3}}},
		{"specials-modal pick", true, GrabScope{Season: 0}, "1/S00", forceRule{On: true, Scope: GrabScope{Season: 0}}},
		{"whole-show modal pick", true, WholeShow, "1/", forceAll},
	}
	for i, tc := range cases {
		hash := fmt.Sprintf("%040x", i+1)
		if _, err := c.db.ExecContext(ctx,
			`INSERT INTO grabs (movie_id, title, indexer, info_hash, media_type) VALUES (1, 'X', 'i', ?, 'series')`, hash); err != nil {
			t.Fatal(err)
		}
		c.markGrab(ctx, hash, tc.manual, tc.scope)
		var manual int
		var scope string
		if err := c.db.QueryRowContext(ctx, `SELECT manual, scope FROM grabs WHERE info_hash = ?`, hash).Scan(&manual, &scope); err != nil {
			t.Fatal(err)
		}
		if got := fmt.Sprintf("%d/%s", manual, scope); got != tc.wantRow {
			t.Errorf("%s: row = %q, want %q", tc.name, got, tc.wantRow)
		}
		if got := c.grabForce(ctx, hash); got != tc.wantForce {
			t.Errorf("%s: grabForce = %+v, want %+v", tc.name, got, tc.wantForce)
		}
	}

	// A sweep grab is never marked: the column defaults are "automatic, whole show".
	const sweep = "ffffffffffffffffffffffffffffffffffffffff"
	if _, err := c.db.ExecContext(ctx,
		`INSERT INTO grabs (movie_id, title, indexer, info_hash, media_type) VALUES (1, 'X', 'i', ?, 'series')`, sweep); err != nil {
		t.Fatal(err)
	}
	var manual int
	var scope string
	if err := c.db.QueryRowContext(ctx, `SELECT manual, scope FROM grabs WHERE info_hash = ?`, sweep).Scan(&manual, &scope); err != nil {
		t.Fatal(err)
	}
	if manual != 0 || scope != "" {
		t.Errorf("sweep row = %d/%q, want 0/''", manual, scope)
	}
}

// Grabs flagged manual before the scope column existed have an empty scope — the whole release —
// which is exactly how they imported before, so nothing needs backfilling.
func TestLegacyManualGrabForcesAll(t *testing.T) {
	c, ctx := manualTestCoordinator(t)
	const legacy = "dddddddddddddddddddddddddddddddddddddddd"
	if _, err := c.db.ExecContext(ctx,
		`INSERT INTO grabs (movie_id, title, indexer, info_hash, media_type, manual) VALUES (1, 'X', 'i', ?, 'series', 1)`, legacy); err != nil {
		t.Fatal(err)
	}
	f := c.grabForce(ctx, legacy)
	if f != forceAll {
		t.Fatalf("legacy manual grab = %+v, want force all", f)
	}
	if !f.forces(series.EpisodeRef{Season: 9, Episode: 9}) {
		t.Error("a legacy manual grab must still force every episode")
	}

	// A scope the app can't read gates everything rather than guessing wide.
	const garbled = "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
	if _, err := c.db.ExecContext(ctx,
		`INSERT INTO grabs (movie_id, title, indexer, info_hash, media_type, manual, scope) VALUES (1, 'X', 'i', ?, 'series', 1, 'season three')`, garbled); err != nil {
		t.Fatal(err)
	}
	if f := c.grabForce(ctx, garbled); f.On {
		t.Errorf("an unreadable scope forced %+v", f)
	}

	// markGrabManual (movies, books, uploaded series torrents) stays whole-release.
	const movie = "9999999999999999999999999999999999999999"
	if _, err := c.db.ExecContext(ctx,
		`INSERT INTO grabs (movie_id, title, indexer, info_hash, media_type) VALUES (1, 'X', 'i', ?, 'movie')`, movie); err != nil {
		t.Fatal(err)
	}
	c.markGrabManual(ctx, movie)
	if f := c.grabForce(ctx, movie); f != forceAll {
		t.Errorf("markGrabManual = %+v, want force all", f)
	}
}
