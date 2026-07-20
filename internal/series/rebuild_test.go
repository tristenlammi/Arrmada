package series

import "testing"

// The Naruto Shippuden case in miniature: the show was stored with TMDB's continuous
// numbering (season 2 starting at episode 33), a file sitting on S02E33. A TVDB refresh
// re-models it with per-season numbering (season 2 restarts at episode 1) — the SAME
// absolute episode, 33, now living at S02E01. The rebuild must move the file there, report
// the remap, and preserve what's the user's (monitoring) and the library's (source release).
func TestRebuildEpisodesCarriesFilesByAbsolute(t *testing.T) {
	repo, ctx := testRepo(t)
	sr, err := repo.Create(ctx, Series{TMDBID: 1, Title: "Naruto Shippuden", Monitored: true})
	if err != nil {
		t.Fatal(err)
	}

	// Old (TMDB) model: continuous episode numbers, absolute == episode number here.
	if err := repo.InsertSeasons(ctx, sr.ID, []Season{
		{SeasonNumber: 1, Episodes: []Episode{
			{SeasonNumber: 1, EpisodeNumber: 1, AbsoluteNumber: 1, Monitored: true},
			{SeasonNumber: 1, EpisodeNumber: 2, AbsoluteNumber: 2, Monitored: true},
		}},
		{SeasonNumber: 2, Episodes: []Episode{
			{SeasonNumber: 2, EpisodeNumber: 33, AbsoluteNumber: 33, Title: "The New Target", Monitored: false},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	const oldPath = "/tv/Naruto Shippuden/Season 2/Naruto Shippuden - S02E33 - The New Target.mkv"
	if err := repo.SetEpisodeFile(ctx, sr.ID, 2, 33, oldPath, 100); err != nil {
		t.Fatal(err)
	}
	if err := repo.SetEpisodeSourceRelease(ctx, sr.ID, 2, 33, "Naruto.Shippuden.480p-Group"); err != nil {
		t.Fatal(err)
	}

	// New (TVDB) model: same absolute 33, but re-numbered to S02E01.
	remaps, err := repo.RebuildEpisodes(ctx, sr.ID, []Season{
		{SeasonNumber: 1, Episodes: []Episode{
			{SeasonNumber: 1, EpisodeNumber: 1, AbsoluteNumber: 1},
			{SeasonNumber: 1, EpisodeNumber: 2, AbsoluteNumber: 2},
		}},
		{SeasonNumber: 2, Episodes: []Episode{
			{SeasonNumber: 2, EpisodeNumber: 1, AbsoluteNumber: 33, Title: "The New Target"},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}

	// The remap tells the caller exactly what to rename on disk.
	if len(remaps) != 1 {
		t.Fatalf("want 1 remap, got %d: %+v", len(remaps), remaps)
	}
	r := remaps[0]
	if r.Absolute != 33 || r.OldSeason != 2 || r.OldEpisode != 33 || r.NewSeason != 2 || r.NewEpisode != 1 {
		t.Errorf("remap = %+v, want abs 33 from S02E33 to S02E01", r)
	}
	if r.FilePath != oldPath {
		t.Errorf("remap path = %q, want the old on-disk path so it can be renamed", r.FilePath)
	}

	// The file now lives on S02E01, with size and source release intact.
	moved := repo.CurrentEpisodeFile(ctx, sr.ID, 2, 1)
	if moved.Path != oldPath || moved.SizeBytes != 100 {
		t.Errorf("file didn't follow its absolute to S02E01: %+v", moved)
	}
	if moved.SourceRelease != "Naruto.Shippuden.480p-Group" {
		t.Errorf("source release lost across the rebuild: %q", moved.SourceRelease)
	}

	// The old slot is gone entirely — no phantom S02E33 left behind.
	if repo.EpisodeExists(ctx, sr.ID, 2, 33) {
		t.Error("S02E33 should no longer exist after the rebuild")
	}

	// The user's monitoring choice survives, matched by absolute number.
	seasons, err := repo.SeasonsFor(ctx, sr.ID)
	if err != nil {
		t.Fatal(err)
	}
	var s2e1 *Episode
	for i := range seasons {
		for j := range seasons[i].Episodes {
			if seasons[i].Episodes[j].SeasonNumber == 2 && seasons[i].Episodes[j].EpisodeNumber == 1 {
				s2e1 = &seasons[i].Episodes[j]
			}
		}
	}
	if s2e1 == nil {
		t.Fatal("S02E01 missing after rebuild")
	}
	if s2e1.Monitored {
		t.Error("the episode was unmonitored before the rebuild; that choice should carry over")
	}
	if !s2e1.HasFile {
		t.Error("S02E01 should report its carried file")
	}
}

// A file whose absolute number the new model doesn't carry must never be orphaned: it stays
// at its old (season, episode) if that slot still exists, for a later rescan to reconcile.
func TestRebuildKeepsUnmappedFileInPlace(t *testing.T) {
	repo, ctx := testRepo(t)
	sr, _ := repo.Create(ctx, Series{TMDBID: 2, Title: "Show", Monitored: true})
	if err := repo.InsertSeasons(ctx, sr.ID, []Season{
		{SeasonNumber: 1, Episodes: []Episode{
			{SeasonNumber: 1, EpisodeNumber: 1, AbsoluteNumber: 1},
			{SeasonNumber: 1, EpisodeNumber: 2, AbsoluteNumber: 999}, // an absolute the new model drops
		}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := repo.SetEpisodeFile(ctx, sr.ID, 1, 2, "/tv/Show/S01E02.mkv", 50); err != nil {
		t.Fatal(err)
	}

	// New model keeps S01E01/E02 but no longer has absolute 999.
	_, err := repo.RebuildEpisodes(ctx, sr.ID, []Season{
		{SeasonNumber: 1, Episodes: []Episode{
			{SeasonNumber: 1, EpisodeNumber: 1, AbsoluteNumber: 1},
			{SeasonNumber: 1, EpisodeNumber: 2, AbsoluteNumber: 2},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if f := repo.CurrentEpisodeFile(ctx, sr.ID, 1, 2); f.Path != "/tv/Show/S01E02.mkv" {
		t.Errorf("an unmapped file must stay in its old slot, got %+v", f)
	}
}

// Detection: only a genuine season-MODEL change (a shared absolute at a different
// season/episode) triggers a rebuild. Matching numbering, or a first-time listing, does not.
func TestNumberingModelChanged(t *testing.T) {
	stored := map[int][2]int{33: {2, 33}, 1: {1, 1}}

	sameModel := []Season{{SeasonNumber: 2, Episodes: []Episode{{SeasonNumber: 2, EpisodeNumber: 33, AbsoluteNumber: 33}}}}
	if numberingModelChanged(sameModel, stored) {
		t.Error("identical numbering should not trigger a rebuild")
	}

	newModel := []Season{{SeasonNumber: 2, Episodes: []Episode{{SeasonNumber: 2, EpisodeNumber: 1, AbsoluteNumber: 33}}}}
	if !numberingModelChanged(newModel, stored) {
		t.Error("absolute 33 moving from S02E33 to S02E01 is a model change and must rebuild")
	}

	if numberingModelChanged(newModel, map[int][2]int{}) {
		t.Error("with nothing stored there's nothing to reconcile — the additive path is correct")
	}
}
