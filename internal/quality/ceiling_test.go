package quality

import (
	"strconv"
	"testing"
)

// sizeForBitrate returns the GiB size that yields the given H.264 Mbps over runtimeMin,
// so the cases below can be written in the units the ceiling actually reasons about.
func sizeForBitrate(mbps float64, runtimeMin int) float64 {
	// Inverse of BitrateMbps.
	return mbps * float64(runtimeMin) * 60 / 8 / 1024
}

// The ceiling is the band between the upgrade step and the profile's bitrate cap: an
// upgrade must be at least pct better AND still under the cap, so once
// current × (1 + pct/100) exceeds the cap there is nothing left to find.
func TestAtCeilingUsesBitrateCapAndStep(t *testing.T) {
	s, ctx := testService(t)
	const runtime = 24 // a 24-minute episode

	p, err := s.Create(ctx, StoredProfile{
		MediaType:          MediaSeries,
		Name:               "1080p ≤30 Mbps",
		AllowedResolutions: []string{"1080p"},
		BitrateCapMbps:     30,
		UpgradeMinPercent:  25,
		UpgradesEnabled:    true,
	})
	if err != nil {
		t.Fatal(err)
	}
	ref := "custom:" + strconv.FormatInt(p.ID, 10)

	cases := []struct {
		name    string
		release string
		mbps    float64
		want    bool
	}{
		// 25 × 1.25 = 31.25, over the 30 ceiling — no permitted release can clear the step.
		{"no headroom left for a 25% step", "Show.S01E01.1080p.WEB-DL.H.264-GRP", 25, true},
		// 10 × 1.25 = 12.5, comfortably under the cap — a better encode can still exist.
		{"plenty of headroom", "Show.S01E01.1080p.WEB-DL.H.264-GRP", 10, false},
		// Already over the ceiling itself: certainly nothing better is permitted.
		{"already above the cap", "Show.S01E01.1080p.WEB-DL.H.264-GRP", 35, true},
		// x265 counts for 1.6× its raw bitrate, so 20 raw is 32 H.264-equivalent — the same
		// units the cap is expressed in. Judging it raw would wrongly call it upgradable.
		{"x265 is judged in H.264-equivalent terms", "Show.S01E01.1080p.WEB-DL.x265-GRP", 20, true},
	}
	for _, tc := range cases {
		got := s.AtCeiling(ctx, ref, tc.release, sizeForBitrate(tc.mbps, runtime), runtime)
		if got != tc.want {
			t.Errorf("%s: AtCeiling(%.0f Mbps) = %v, want %v", tc.name, tc.mbps, got, tc.want)
		}
	}

	// A 720p file can be pinned against the bitrate ceiling and still have a 1080p release
	// waiting for it — UpgradeCandidate takes that on quality alone, so the ceiling must not
	// skip it.
	if s.AtCeiling(ctx, ref, "Show.S01E01.720p.WEB-DL.H.264-GRP", sizeForBitrate(28, runtime), runtime) {
		t.Error("a below-max resolution must never be reported at the ceiling")
	}

	// Nothing to divide by → don't guess.
	if s.AtCeiling(ctx, ref, "Show.S01E01.1080p.WEB-DL.H.264-GRP", sizeForBitrate(28, runtime), 0) {
		t.Error("unknown runtime must not report a ceiling")
	}
	if s.AtCeiling(ctx, ref, "", 1, runtime) {
		t.Error("no recorded release must not report a ceiling")
	}
}

// Without a declared ceiling — or without a percentage step to exhaust — there is no band
// to run out of, so nothing is ever skipped.
func TestAtCeilingNeedsACapAndAStep(t *testing.T) {
	s, ctx := testService(t)
	const runtime = 24

	noCap, err := s.Create(ctx, StoredProfile{
		MediaType: MediaSeries, Name: "uncapped", AllowedResolutions: []string{"1080p"},
		UpgradeMinPercent: 25, UpgradesEnabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if s.AtCeiling(ctx, "custom:"+strconv.FormatInt(noCap.ID, 10),
		"Show.S01E01.1080p.WEB-DL.H.264-GRP", sizeForBitrate(80, runtime), runtime) {
		t.Error("a profile with no bitrate cap has no ceiling to reach")
	}

	noStep, err := s.Create(ctx, StoredProfile{
		MediaType: MediaSeries, Name: "quality-only", AllowedResolutions: []string{"1080p"},
		BitrateCapMbps: 30, UpgradesEnabled: true, // UpgradeMinPercent 0 → quality gains only
	})
	if err != nil {
		t.Fatal(err)
	}
	if s.AtCeiling(ctx, "custom:"+strconv.FormatInt(noStep.ID, 10),
		"Show.S01E01.1080p.WEB-DL.H.264-GRP", sizeForBitrate(29, runtime), runtime) {
		t.Error("with no percentage step there's no bitrate band to exhaust — only quality gains, which this can't rule out")
	}
}

// The searcher takes a release on a quality gain that isn't a resolution gain (a WEB-DL
// with Atmos over a plain WEB rip). The import gate only knew resolution and bitrate, so
// it refused the finished download — a wasted grab that then sat seeding in the client and
// froze the show's sweeps. Both sides must now reach the same verdict.
func TestIsQualityUpgradeMatchesTheSearcher(t *testing.T) {
	s, ctx := testService(t)
	p, err := s.Create(ctx, StoredProfile{
		MediaType: MediaSeries, Name: "web-dl preferred",
		AllowedResolutions: []string{"1080p"},
		FormatScores:       map[string]int{"Atmos": 50, "DDP": 20},
		UpgradesEnabled:    true,
	})
	if err != nil {
		t.Fatal(err)
	}
	ref := "custom:" + strconv.FormatInt(p.ID, 10)

	const (
		ethel = "Lioness 2023 S03E01 1080p WEB h264-ETHEL"
		flux  = "Lioness 2023 S03E01 1080p AMZN WEB-DL DDP5 1 Atmos H 264-FLUX"
	)
	if !s.IsQualityUpgrade(ctx, ref, flux, 2.0, ethel, 2.0) {
		t.Error("the release the searcher chose must not be refused on arrival")
	}
	if s.IsQualityUpgrade(ctx, ref, ethel, 2.0, flux, 2.0) {
		t.Error("the reverse is a downgrade")
	}
	if s.IsQualityUpgrade(ctx, ref, flux, 2.0, flux, 2.0) {
		t.Error("the same release must not score as an upgrade over itself — that's a re-import loop")
	}
	if s.IsQualityUpgrade(ctx, ref, flux, 2.0, "", 2.0) {
		t.Error("no recorded baseline must not count as an upgrade")
	}

	// Upgrades off means the searcher would never have picked it, so the importer must not
	// replace a file on its own initiative either.
	off, err := s.Create(ctx, StoredProfile{
		MediaType: MediaSeries, Name: "no upgrades",
		AllowedResolutions: []string{"1080p"},
		FormatScores:       map[string]int{"Atmos": 50, "DDP": 20},
	})
	if err != nil {
		t.Fatal(err)
	}
	if s.IsQualityUpgrade(ctx, "custom:"+strconv.FormatInt(off.ID, 10), flux, 2.0, ethel, 2.0) {
		t.Error("upgrades disabled must stop the import gate replacing files too")
	}
}
