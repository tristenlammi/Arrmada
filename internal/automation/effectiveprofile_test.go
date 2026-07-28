package automation

import (
	"context"
	"strconv"
	"testing"

	"github.com/tristenlammi/arrmada/internal/quality"
	"github.com/tristenlammi/arrmada/internal/store"
)

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
