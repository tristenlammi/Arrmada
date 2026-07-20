package series

import (
	"log/slog"
	"testing"

	"github.com/tristenlammi/arrmada/internal/parser"
)

// A release that puts the SERIES absolute number in the SxxExx slot — EiNSTEiNSiR ships
// "My Hero Academia - S07E139" for what is really S07E01 (absolute 139) — must resolve to
// the real episode, never a phantom S07E139.
func TestAbsoluteInEpisodeSlot(t *testing.T) {
	repo, ctx := testRepo(t)
	sr, err := repo.Create(ctx, Series{TMDBID: 1, Title: "My Hero Academia", Monitored: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.SetSeriesType(ctx, sr.ID, SeriesTypeAnime); err != nil {
		t.Fatal(err)
	}
	// Season 1 (abs 1-2) and season 7 (abs 139-141), reset per-season numbering.
	if err := repo.InsertSeasons(ctx, sr.ID, []Season{
		{SeasonNumber: 1, Episodes: []Episode{
			{SeasonNumber: 1, EpisodeNumber: 1, AbsoluteNumber: 1},
			{SeasonNumber: 1, EpisodeNumber: 2, AbsoluteNumber: 2},
		}},
		{SeasonNumber: 7, Episodes: []Episode{
			{SeasonNumber: 7, EpisodeNumber: 1, AbsoluteNumber: 139, Title: "In the Nick of Time"},
			{SeasonNumber: 7, EpisodeNumber: 2, AbsoluteNumber: 140},
			{SeasonNumber: 7, EpisodeNumber: 3, AbsoluteNumber: 141},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	svc := &Service{repo: repo, log: slog.Default()}

	resolve := func(name string) []EpisodeRef {
		return svc.ResolveEpisodes(ctx, sr.ID, parser.Parse(name))
	}

	// S07E139 → the episode whose absolute is 139 → S07E01.
	got := resolve("My Hero Academia - S07E139 1080p WEB-DL")
	if len(got) != 1 || got[0].Season != 7 || got[0].Episode != 1 {
		t.Errorf("S07E139 should resolve to S07E01, got %+v", got)
	}
	if got := resolve("My Hero Academia - S07E141 1080p WEB-DL"); len(got) != 1 || got[0].Episode != 3 {
		t.Errorf("S07E141 should resolve to S07E03, got %+v", got)
	}

	// A real per-season number still resolves directly (never treated as absolute).
	if got := resolve("My Hero Academia - S07E02 1080p WEB-DL"); len(got) != 1 || got[0].Episode != 2 {
		t.Errorf("S07E02 should resolve to S07E02, got %+v", got)
	}

	// An out-of-range number with NO matching absolute must be HELD, not placed as a
	// phantom episode.
	if got := resolve("My Hero Academia - S07E900 1080p WEB-DL"); len(got) != 0 {
		t.Errorf("S07E900 has no matching absolute and must be held (empty), got %+v", got)
	}

	// The same-season guard: a file claiming S01 whose number maps (as an absolute) into a
	// DIFFERENT season must NOT be pulled across. S01E139 → abs 139 → S07E01 (season 7 ≠ 1),
	// so it is rejected and held rather than silently moved to season 7.
	if got := resolve("My Hero Academia - S01E139 1080p WEB-DL"); len(got) != 0 {
		t.Errorf("S01E139 maps to a different season and must be held, got %+v", got)
	}
}
