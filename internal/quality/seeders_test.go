package quality

import "testing"

// QUAL-07: between near-equal releases the healthier torrent wins, and a dead one is
// picked only when nothing seeded is eligible.
func TestNearEqualReleasesTieOnSeeders(t *testing.T) {
	e := NewDefaultEngine()
	few := NewCandidate("Movie.2020.1080p.WEB-DL.x264-FEW", 10.0, 2)
	many := NewCandidate("Movie.2020.1080p.WEB-DL.x264-MANY", 9.5, 150)
	d := e.Decide(Profile{}, []Candidate{few, many})
	if d.Winner.Candidate.Name != many.Name {
		t.Errorf("winner = %s, want the 150-seeder release within 10%% of the size", d.Winner.Candidate.Name)
	}
	if d.ChosenOver != "Chosen over the 1080p WEB-DL — fewer seeders" {
		t.Errorf("ChosenOver = %q", d.ChosenOver)
	}

	// Outside the band, size decides in both directions.
	big := NewCandidate("Movie.2020.1080p.WEB-DL.x264-BIG", 12.0, 2)
	if d := e.Decide(Profile{}, []Candidate{many, big}); d.Winner.Candidate.Name != big.Name {
		t.Errorf("SmallBias 0: winner = %s, want the >10%% larger file", d.Winner.Candidate.Name)
	}
	small := NewCandidate("Movie.2020.1080p.WEB-DL.x264-SMALL", 8.0, 2)
	if d := e.Decide(Profile{SmallBias: 0.01}, []Candidate{many, small}); d.Winner.Candidate.Name != small.Name {
		t.Errorf("SmallBias > 0: winner = %s, want the >10%% smaller file", d.Winner.Candidate.Name)
	}
}

func TestDeadTorrentRanksLast(t *testing.T) {
	e := NewDefaultEngine()
	dead := NewCandidate("Movie.2020.2160p.BluRay.x265-DEAD", 30, 0) // scores far higher
	alive := NewCandidate("Movie.2020.1080p.WEB-DL.x264-ALIVE", 8, 3)
	d := e.Decide(Profile{}, []Candidate{dead, alive})
	if d.Winner.Candidate.Name != alive.Name {
		t.Fatalf("winner = %s, want the seeded release over a dead one", d.Winner.Candidate.Name)
	}
	if d.ChosenOver != "Chosen over the 2160p BluRay — it has no seeders" {
		t.Errorf("ChosenOver = %q", d.ChosenOver)
	}
	// Alone, the dead torrent is still the pick: it ranks, it doesn't reject.
	if d := e.Decide(Profile{}, []Candidate{dead}); d.Winner == nil || d.Winner.Candidate.Name != dead.Name {
		t.Error("a lone 0-seeder release should still be picked")
	}
	// The avoided tier still comes first: a seeded avoided release ranks under a dead clean one.
	p := Profile{FormatScores: map[string]int{"HEVC": -50}}
	avoidedSeeded := NewCandidate("Movie.2020.1080p.WEB-DL.x265-AVOID", 8, 50)
	clean := NewCandidate("Movie.2020.1080p.WEB-DL.x264-CLEAN", 8, 0)
	if d := e.Decide(p, []Candidate{avoidedSeeded, clean}); d.Winner.Candidate.Name != clean.Name {
		t.Errorf("winner = %s, want the non-avoided release (tiers come before health)", d.Winner.Candidate.Name)
	}
}

// Series candidates with runtimes use bitrate for the band: a pack and an episode at
// nearly the same Mb/s are near-equal, whatever their sizes.
func TestSeederBandUsesBitrateForSeries(t *testing.T) {
	e := NewDefaultEngine()
	pack := NewCandidate("Show.S01.1080p.WEB-DL.x264-PACK", 30, 3).WithRuntime(450) // ~9.5 Mb/s
	ep := NewCandidate("Show.S01E01.1080p.WEB-DL.x264-EP", 2.9, 90).WithRuntime(45) // ~9.2 Mb/s
	if d := e.Decide(Profile{}, []Candidate{pack, ep}); d.Winner.Candidate.Name != ep.Name {
		t.Errorf("winner = %s, want the better-seeded release at a near-equal bitrate", d.Winner.Candidate.Name)
	}
}
