package series

import (
	"context"
	"fmt"
	"log/slog"
	"testing"

	"github.com/tristenlammi/arrmada/internal/parser"
	"github.com/tristenlammi/arrmada/internal/store"
)

type fakeScene map[string]int

func (f fakeScene) Fetch(ctx context.Context, tvdbID int) (map[string]int, error) { return f, nil }

// TestXEMSceneMapping replicates Dragon Ball Super: TMDB models it as one continuous
// 131-episode season, but the scene splits it S1..S5 by arc with NO broadcast gap.
// Air-date-gap inference can't help; the TheXEM map must translate a split release.
func TestXEMSceneMapping(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	db := st.DB()
	ctx := context.Background()
	db.ExecContext(ctx, `INSERT INTO series (id,tmdb_id,title,series_type,tvdb_id) VALUES (1,1,'Dragon Ball Super','anime',999)`)
	for n := 1; n <= 131; n++ {
		db.ExecContext(ctx, `INSERT INTO episodes (series_id,season_number,episode_number) VALUES (1,1,?)`, n)
	}
	svc := &Service{repo: NewRepo(db), log: slog.Default()}
	svc.repo.BackfillAbsolute(ctx, 1)

	scene := map[string]int{}
	for e := 1; e <= 14; e++ { // scene S1 = absolute 1..14
		scene[fmt.Sprintf("1-%d", e)] = e
	}
	for e := 1; e <= 20; e++ { // scene S2 = absolute 15..34
		scene[fmt.Sprintf("2-%d", e)] = 14 + e
	}
	svc.SetSceneMapper(fakeScene(scene))
	svc.refreshSceneMap(ctx, 1, 999)

	check := func(name string, wantEp int) {
		t.Helper()
		r := svc.ResolveEpisodes(ctx, 1, parser.Parse(name))
		if len(r) != 1 || r[0].Season != 1 || r[0].Episode != wantEp {
			t.Errorf("%q -> %+v; want {1 %d}", name, r, wantEp)
		}
	}
	check("Dragon Ball Super S02E01 1080p WEB", 15) // scene S2E1 -> absolute 15 -> TMDB E15
	check("Dragon Ball Super S02E20 1080p WEB", 34)
	check("Dragon Ball Super S01E05 1080p WEB", 5) // season 1 passes straight through

	if pack := svc.SceneSeasonEpisodes(ctx, 1, 2); len(pack) != 20 || pack[0].Episode != 15 || pack[19].Episode != 34 {
		t.Errorf("scene-S2 pack = %d episodes (%+v); want 20 (E15..E34)", len(pack), pack)
	}
}
