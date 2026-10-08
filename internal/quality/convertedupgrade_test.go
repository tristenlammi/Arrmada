package quality

import (
	"strconv"
	"testing"
)

// An AV1-converted movie is not "upgraded" by an equal-resolution HEVC release, nor by
// the very release it was converted from. Before the stamp was swapped in place, the AV1
// file read as H.264, was costed at H.264 efficiency, and the sweep re-grabbed the
// original over it — undoing the conversion every cycle.
func TestUpgradeCandidateLeavesConvertedFileAlone(t *testing.T) {
	s, ctx := testService(t)
	sp, err := s.Create(ctx, StoredProfile{
		MediaType: MediaMovie, Name: "Efficient", UpgradesEnabled: true, UpgradeMinPercent: 25,
		FormatScores: map[string]int{"HEVC": 20, "AV1": 20},
	})
	if err != nil {
		t.Fatal(err)
	}
	ref := "custom:" + strconv.FormatInt(sp.ID, 10)
	baseline := "Film.2021.1080p.BluRay.AV1-GRP"
	cands := []Candidate{
		NewCandidate("Film.2021.1080p.BluRay.x265-OTHER", 6, 200).WithRuntime(120),
		NewCandidate("Film.2021.1080p.BluRay.x264-GRP", 8, 400).WithRuntime(120),
	}
	if pick, ok := s.UpgradeCandidate(ctx, ref, baseline, 4, 120, cands); ok {
		t.Errorf("picked %q as an upgrade of the converted file", pick.Name)
	}
	// The original alone, under a profile that would otherwise reward its bitrate: still
	// never re-grabbed over the file converted from it.
	if pick, ok := s.UpgradeCandidate(ctx, ref, baseline, 1, 120, cands[1:]); ok {
		t.Errorf("re-grabbed the release the file was converted from: %q", pick.Name)
	}
	// The import gate agrees.
	if s.IsQualityUpgrade(ctx, ref, "Film.2021.1080p.BluRay.x264-GRP", 8, baseline, 4) {
		t.Error("the import gate took the original release as an upgrade of its conversion")
	}
}
