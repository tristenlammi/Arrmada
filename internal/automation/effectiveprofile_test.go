package automation

import (
	"bytes"
	"context"
	"database/sql"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"testing"

	"github.com/tristenlammi/arrmada/internal/indexer"
	"github.com/tristenlammi/arrmada/internal/movies"
	"github.com/tristenlammi/arrmada/internal/quality"
	"github.com/tristenlammi/arrmada/internal/store"
)

// profileStore opens a temp DB with its quality service.
func profileStore(t *testing.T) (*sql.DB, *quality.Service, context.Context) {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st.DB(), quality.NewService(st.DB()), context.Background()
}

// createDefault stores a profile and makes it the default of its media type (a fresh
// store already carries the seeded profiles), returning its ref.
func createDefault(t *testing.T, q *quality.Service, sp quality.StoredProfile) string {
	t.Helper()
	ctx := context.Background()
	got, err := q.Create(ctx, sp)
	if err != nil {
		t.Fatal(err)
	}
	ref := "custom:" + strconv.FormatInt(got.ID, 10)
	if err := q.SetDefaultProfile(ctx, sp.MediaType, ref); err != nil {
		t.Fatal(err)
	}
	return ref
}

// A title whose profile was deleted runs under the default of its media type — for the
// decision, the upgrade gate and the stall window alike — and the substitution is logged
// once, not on every sweep.
func TestEffectiveDanglingRefUsesDefault(t *testing.T) {
	_, q, ctx := profileStore(t)
	def := createDefault(t, q, quality.StoredProfile{
		MediaType: quality.MediaMovie, Name: "1080p", UpgradesEnabled: true, StallMinutes: 45,
		FormatScores: map[string]int{"HEVC": 10},
	})
	seriesDef := createDefault(t, q, quality.StoredProfile{MediaType: quality.MediaSeries, Name: "TV"})
	var logs bytes.Buffer
	c := &Coordinator{quality: q, log: slog.New(slog.NewTextHandler(&logs, nil))}

	if got := c.effectiveProfile(ctx, "custom:999", quality.MediaMovie); got != def {
		t.Errorf("dangling movie ref resolved to %q, want the movie default %q", got, def)
	}
	if got := c.effectiveProfile(ctx, "custom:999", quality.MediaSeries); got != seriesDef {
		t.Errorf("dangling series ref resolved to %q, want the series default %q", got, seriesDef)
	}
	if got := c.effectiveProfile(ctx, "", quality.MediaMovie); got != def {
		t.Errorf("an empty ref resolved to %q, want the default", got)
	}
	// Upgrades and stall fail-over keep working: they're read off the default.
	ref := c.effectiveProfile(ctx, "custom:999", quality.MediaMovie)
	if !q.AllowsUpgrades(ctx, ref) || q.StallMinutes(ctx, ref) != 45 {
		t.Errorf("upgrades=%v stall=%d under the resolved profile, want true/45", q.AllowsUpgrades(ctx, ref), q.StallMinutes(ctx, ref))
	}
	if n := strings.Count(logs.String(), "no longer exists"); n != 1 {
		t.Errorf("dangling ref logged %d times, want once:\n%s", n, logs.String())
	}
	if !strings.Contains(logs.String(), "ref=custom:999") || !strings.Contains(logs.String(), "default="+def) {
		t.Errorf("log line should name the ref and the default:\n%s", logs.String())
	}
}

// With a deleted profile and nothing but a cam on offer, the automatic grab takes
// nothing. Before, the deleted ref resolved to the permissive fallback, the cam won,
// and (with no indexers wired here) the grab would have panicked.
func TestGrabMissingDanglingRefRejectsCam(t *testing.T) {
	cam := "Movie.2024.HDCAM.x264-GRP"
	run := func(t *testing.T, db *sql.DB, q *quality.Service) int {
		c := &Coordinator{quality: q, db: db, log: slog.New(slog.NewTextHandler(io.Discard, nil))}
		m := movies.Movie{ID: 1, Title: "Movie", Year: 2024, Runtime: 120}
		want := []movies.Version{{ID: 0, IsDefault: true, Label: "Default", QualityProfile: "custom:999", Monitored: true}}
		byName := map[string]indexer.Release{cam: {Title: cam, Indexer: "x", DownloadURL: "magnet:?xt=cam"}}
		cands := []quality.Candidate{quality.NewCandidate(cam, 1.4, 500)}
		return c.grabMissing(context.Background(), m, want, byName, cands)
	}
	t.Run("default profile", func(t *testing.T) {
		db, q, _ := profileStore(t)
		createDefault(t, q, quality.StoredProfile{MediaType: quality.MediaMovie, Name: "Any"})
		if n := run(t, db, q); n != 0 {
			t.Errorf("grabbed %d, want 0", n)
		}
	})
	t.Run("no profiles at all", func(t *testing.T) {
		db, q, ctx := profileStore(t)
		if _, err := db.ExecContext(ctx, `DELETE FROM quality_profiles`); err != nil {
			t.Fatal(err)
		}
		if n := run(t, db, q); n != 0 {
			t.Errorf("grabbed %d under the fallback, want 0 — it must still refuse cams", n)
		}
	})
}

// A book whose profile was deleted is scored with the default book profile, not the
// hardcoded EPUB preference.
func TestBookProfileDanglingRefUsesDefault(t *testing.T) {
	_, q, ctx := profileStore(t)
	createDefault(t, q, quality.StoredProfile{MediaType: quality.MediaBook, Name: "Audio", FormatScores: map[string]int{"M4B": 50}})
	c := &Coordinator{quality: q}
	sp := c.bookProfile(ctx, "custom:999")
	if sp.FormatScores["M4B"] != 50 || sp.FormatScores["EPUB"] != 0 {
		t.Errorf("format scores = %v, want the default book profile's", sp.FormatScores)
	}
}

// A library-scanned title carries the profile "n/a", which otherwise scores against a
// generic fallback that PREFERS Dolby Vision — so a manual search recommended a DV release
// even to a user who set DV to Avoid. effectiveProfile must route "n/a" to the user's own
// default profile, where that Avoid is respected.
func TestEffectiveProfileRoutesScannedToDefault(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()
	q := quality.NewService(st.DB())

	sp, err := q.Create(ctx, quality.StoredProfile{
		MediaType:    quality.MediaMovie,
		Name:         "No DV",
		FormatScores: map[string]int{"Dolby Vision": -50, "HDR10": 50},
	})
	if err != nil {
		t.Fatal(err)
	}
	ref := "custom:" + strconv.FormatInt(sp.ID, 10)
	if err := q.SetDefaultProfile(ctx, "movie", ref); err != nil {
		t.Fatal(err)
	}
	c := &Coordinator{quality: q}

	// "n/a" routes to the user's default profile (the only one here), not the fallback.
	if got := c.effectiveProfile(ctx, "n/a", "movie"); got != ref {
		t.Errorf("n/a should route to the default profile %q, got %q", ref, got)
	}
	// A title with a real profile of its own keeps it.
	if got := c.effectiveProfile(ctx, ref, "movie"); got != ref {
		t.Errorf("a real profile must be honoured unchanged, got %q", got)
	}

	// And the routed profile genuinely avoids DV: a DV+HDR10 release must NOT out-rank a
	// clean HDR10 release of the same resolution/source under it.
	dec := q.Decide(ctx, ref, []quality.Candidate{
		quality.NewCandidate("Obsession 2026 2160p BluRay DV HDR x265-a", 14, 300),
		quality.NewCandidate("Obsession 2026 2160p BluRay HDR x265-b", 14, 300),
	})
	if dec.Winner == nil || dec.Winner.Candidate.Name != "Obsession 2026 2160p BluRay HDR x265-b" {
		var w string
		if dec.Winner != nil {
			w = dec.Winner.Candidate.Name
		}
		t.Errorf("under a DV-avoiding profile the clean HDR10 release should win, got %q", w)
	}
}
