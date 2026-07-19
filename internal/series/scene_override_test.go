package series

import (
	"context"
	"log/slog"
	"testing"

	"github.com/tristenlammi/arrmada/internal/parser"
	"github.com/tristenlammi/arrmada/internal/store"
)

// TestSceneOverride is the Dandadan case: TMDB carries one 24-episode season while the
// release ships as S01 + S02, so "S02E01" is really S01E13. A manual mapping must pin
// that, and must beat the air-date-gap guess underneath it.
func TestSceneOverride(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	db := st.DB()
	ctx := context.Background()

	db.ExecContext(ctx, `INSERT INTO series (id,tmdb_id,title,series_type,monitored) VALUES (1,7,'Dan Da Dan','anime',1)`)
	db.ExecContext(ctx, `INSERT INTO seasons (series_id,season_number) VALUES (1,1)`)
	for n := 1; n <= 24; n++ {
		db.ExecContext(ctx, `INSERT INTO episodes (series_id,season_number,episode_number,air_date) VALUES (1,1,?,'2024-10-04')`, n)
	}
	svc := &Service{repo: NewRepo(db), log: slog.Default()}
	if err := svc.repo.BackfillAbsolute(ctx, 1); err != nil {
		t.Fatal(err)
	}

	if err := svc.SetSceneOverride(ctx, 1, SceneOverride{SceneSeason: 2, TMDBSeason: 1, TMDBEpisode: 13}); err != nil {
		t.Fatal(err)
	}

	// Scene S02E01 → S01E13, and the rest of the cour follows by offset.
	for _, c := range []struct{ sceneEp, wantEp int }{{1, 13}, {2, 14}, {12, 24}} {
		refs := svc.ResolveEpisodes(ctx, 1, parser.Parse("DAN.DA.DAN.S02E"+pad(c.sceneEp)+".1080p.BluRay.x265-GRP"))
		if len(refs) != 1 || refs[0].Season != 1 || refs[0].Episode != c.wantEp {
			t.Errorf("scene S02E%02d resolved to %+v, want S01E%02d", c.sceneEp, refs, c.wantEp)
		}
	}

	// A whole-cour pack maps to the full range, so "S02" pack matching works.
	if got := svc.SceneSeasonEpisodes(ctx, 1, 2); len(got) != 12 || got[0].Episode != 13 || got[11].Episode != 24 {
		t.Errorf("SceneSeasonEpisodes(2) = %d refs starting %+v, want 12 from E13", len(got), got)
	}

	// Removing it falls back to the automatic ladder.
	if err := svc.DeleteSceneOverride(ctx, 1, 2); err != nil {
		t.Fatal(err)
	}
	if _, ok := svc.repo.SceneOverrideFor(ctx, 1, 2); ok {
		t.Error("override should be gone after delete")
	}
}

// TestSceneOverrideAnimeOnly checks the scoping the feature was asked for.
func TestSceneOverrideAnimeOnly(t *testing.T) {
	st, _ := store.Open(t.TempDir())
	defer st.Close()
	db := st.DB()
	ctx := context.Background()
	db.ExecContext(ctx, `INSERT INTO series (id,tmdb_id,title,series_type) VALUES (1,7,'Normal Show','standard')`)
	db.ExecContext(ctx, `INSERT INTO episodes (series_id,season_number,episode_number) VALUES (1,1,1)`)
	svc := &Service{repo: NewRepo(db), log: slog.Default()}
	if err := svc.SetSceneOverride(ctx, 1, SceneOverride{SceneSeason: 2, TMDBSeason: 1, TMDBEpisode: 1}); err == nil {
		t.Error("expected a standard (non-anime) series to be refused")
	}
}

func pad(n int) string {
	if n < 10 {
		return "0" + string(rune('0'+n))
	}
	return string(rune('0'+n/10)) + string(rune('0'+n%10))
}
