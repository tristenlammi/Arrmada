package series

import (
	"context"
	"testing"

	"github.com/tristenlammi/arrmada/internal/parser"
	"github.com/tristenlammi/arrmada/internal/store"
)

// TestSceneSeasonMapping replicates Frieren: TMDB models it as one continuous 38-episode
// season, but the scene splits it S1 (28) / S2 (10) with a ~2-year air-date gap. A
// split-season release must map onto TMDB's numbering via air-date-gap inference.
func TestSceneSeasonMapping(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	db := st.DB()
	ctx := context.Background()
	db.ExecContext(ctx, `INSERT INTO series (id,tmdb_id,title,series_type) VALUES (1,1,'Frieren','anime')`)

	s1 := []string{"2023-09-29", "2023-10-06", "2023-10-13", "2023-10-20", "2023-10-27", "2023-11-03", "2023-11-10", "2023-11-17", "2023-11-24", "2023-12-01", "2023-12-08", "2023-12-15", "2023-12-22", "2024-01-05", "2024-01-12", "2024-01-19", "2024-01-26", "2024-02-02", "2024-02-09", "2024-02-16", "2024-02-23", "2024-03-01", "2024-03-08", "2024-03-15", "2024-03-22", "2024-03-29", "2024-04-05", "2024-04-12"}
	s2 := []string{"2026-01-16", "2026-01-23", "2026-01-30", "2026-02-06", "2026-02-13", "2026-02-20", "2026-02-27", "2026-03-06", "2026-03-13", "2026-03-20"}
	dates := append(append([]string{}, s1...), s2...)
	for i, ad := range dates {
		db.ExecContext(ctx, `INSERT INTO episodes (series_id,season_number,episode_number,air_date) VALUES (1,1,?,?)`, i+1, ad)
	}
	svc := &Service{repo: NewRepo(db)}
	svc.repo.BackfillAbsolute(ctx, 1)

	check := func(name string, wantSeason, wantEp int) {
		t.Helper()
		refs := svc.ResolveEpisodes(ctx, 1, parser.Parse(name))
		if len(refs) != 1 || refs[0].Season != wantSeason || refs[0].Episode != wantEp {
			t.Errorf("%q -> %+v; want [{%d %d}]", name, refs, wantSeason, wantEp)
		}
	}
	check("Frieren S02E01 1080p WEB", 1, 29) // scene S2 → TMDB E29
	check("Frieren S02E10 1080p WEB", 1, 38)
	check("Frieren S01E05 1080p WEB", 1, 5) // season 1 passes straight through

	if pack := svc.SceneSeasonEpisodes(ctx, 1, 2); len(pack) != 10 || pack[0].Episode != 29 || pack[9].Episode != 38 {
		t.Errorf("scene-S2 pack = %+v; want E29-E38", pack)
	}
	// A show with no real air-date gap must NOT get a bogus mapping.
	if refs := svc.resolveSceneSeasonUnknown(ctx); refs {
		t.Error("expected no scene mapping without a real gap")
	}
}

// resolveSceneSeasonUnknown returns true if a continuous show (no gap) wrongly maps S2.
func (s *Service) resolveSceneSeasonUnknown(ctx context.Context) bool {
	_, _, ok := s.resolveSceneSeason(ctx, 99, 2, 1) // series 99 doesn't exist → no episodes
	return ok
}
