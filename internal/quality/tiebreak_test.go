package quality

import (
	"strconv"
	"strings"
	"testing"
)

// QUAL-06: once series candidates cover different lengths, an equal-score tie compares
// bitrate. A pack is no longer better than an episode just because it holds more of them.
func TestDecideTieBreaksOnBitrateWhenRuntimesDiffer(t *testing.T) {
	e := NewDefaultEngine()
	pack := NewCandidate("Show.S01.1080p.WEB-DL.x264-GRP", 30, 10).WithRuntime(450)        // ~9.5 Mb/s
	episode := NewCandidate("Show.S01E01.1080p.WEB-DL.x264-GRP", 4, 10).WithRuntime(45)    // ~12.7 Mb/s
	thinner := NewCandidate("Show.S01E01.1080p.WEB-DL.x264-THIN", 2.5, 10).WithRuntime(45) // ~7.95 Mb/s

	d := e.Decide(Profile{}, []Candidate{pack, episode})
	if d.Winner == nil || d.Winner.Candidate.Name != episode.Name {
		t.Errorf("winner = %v, want the higher-bitrate episode over the larger pack", d.Winner.Candidate.Name)
	}
	d = e.Decide(Profile{}, []Candidate{thinner, pack})
	if d.Winner == nil || d.Winner.Candidate.Name != pack.Name {
		t.Errorf("winner = %v, want the pack: it is the higher bitrate here", d.Winner.Candidate.Name)
	}
	// A small-size lean picks the lower bitrate of equals, not the smaller file.
	d = e.Decide(Profile{SmallBias: 0.01}, []Candidate{pack, thinner})
	if d.Winner == nil || d.Winner.Candidate.Name != thinner.Name {
		t.Errorf("winner = %v, want the leaner episode under a small-size lean", d.Winner.Candidate.Name)
	}
	// One runtime unknown: fall back to size, as before.
	d = e.Decide(Profile{}, []Candidate{episode, NewCandidate("Show.S01.1080p.WEB-DL.x264-GRP", 30, 10)})
	if d.Winner == nil || d.Winner.Candidate.Name != "Show.S01.1080p.WEB-DL.x264-GRP" {
		t.Errorf("winner = %v, want the larger file when a runtime is unknown", d.Winner.Candidate.Name)
	}
}

// Movies share one runtime, so bitrate order is size order: the larger file wins a tie,
// or the smaller under any small-size lean.
func TestDecideMovieTieBreakUnchanged(t *testing.T) {
	e := NewDefaultEngine()
	big := NewCandidate("Movie.2020.1080p.WEB-DL.x264-BIG", 10, 10).WithRuntime(120)
	small := NewCandidate("Movie.2020.1080p.WEB-DL.x264-SMALL", 8, 10).WithRuntime(120)
	if d := e.Decide(Profile{}, []Candidate{small, big}); d.Winner.Candidate.Name != big.Name {
		t.Errorf("winner = %s, want the larger file", d.Winner.Candidate.Name)
	}
	if d := e.Decide(Profile{SmallBias: 0.01}, []Candidate{big, small}); d.Winner.Candidate.Name != small.Name {
		t.Errorf("winner = %s, want the smaller file under a small-size lean", d.Winner.Candidate.Name)
	}
	// No runtime at all: the same answer from the sizes.
	big.RuntimeMin, small.RuntimeMin = 0, 0
	if d := e.Decide(Profile{}, []Candidate{small, big}); d.Winner.Candidate.Name != big.Name {
		t.Errorf("winner = %s, want the larger file", d.Winner.Candidate.Name)
	}
}

func TestExceedsCeiling(t *testing.T) {
	s, ctx := testService(t)
	sp, err := s.Create(ctx, StoredProfile{
		MediaType: MediaSeries, Name: "TV",
		Ideal: &IdealFile{Bitrate: map[string]BitrateWindow{"1080p": {Min: 3, Max: 15}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	ref := "custom:" + strconv.FormatInt(sp.ID, 10)
	over, why := s.ExceedsCeiling(ctx, ref, "Show.S01E01.1080p.BluRay.x264-GRP", 8, 45)
	if !over || !strings.HasPrefix(why, "Over your 15 Mbps ceiling (25.") {
		t.Errorf("8 GB over 45 minutes: over=%v %q", over, why)
	}
	if over, _ := s.ExceedsCeiling(ctx, ref, "Show.S01E01.1080p.BluRay.x264-GRP", 2, 45); over {
		t.Error("2 GB over 45 minutes is inside the window")
	}
	// Unknown runtime: can't be judged, so never over.
	if over, _ := s.ExceedsCeiling(ctx, ref, "Show.S01E01.1080p.BluRay.x264-GRP", 80, 0); over {
		t.Error("with no runtime the ceiling can't apply")
	}
	// No window for 720p and no profile-wide cap: nothing to exceed.
	if over, _ := s.ExceedsCeiling(ctx, ref, "Show.S01E01.720p.BluRay.x264-GRP", 80, 45); over {
		t.Error("720p has no ceiling in this profile")
	}
}
