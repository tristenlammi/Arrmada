package series

import (
	"context"
	"testing"

	"github.com/tristenlammi/arrmada/internal/parser"
	"github.com/tristenlammi/arrmada/internal/store"
)

// TestResolveEpisodes exercises the full anime resolver through the service: a
// per-cour file and an absolute-numbered release both land on the right episode.
func TestResolveEpisodes(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	db := st.DB()
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `INSERT INTO series (id,tmdb_id,title,series_type) VALUES (1,7,'HxH','anime')`); err != nil {
		t.Fatal(err)
	}
	for n := 1; n <= 136; n++ {
		db.ExecContext(ctx, `INSERT INTO episodes (series_id,season_number,episode_number) VALUES (1,1,?)`, n)
	}
	for n := 137; n <= 148; n++ {
		db.ExecContext(ctx, `INSERT INTO episodes (series_id,season_number,episode_number) VALUES (1,3,?)`, n)
	}
	svc := &Service{repo: NewRepo(db)}
	if err := svc.repo.BackfillAbsolute(ctx, 1); err != nil {
		t.Fatal(err)
	}

	// Per-cour file "S03E02" → positional → season 3, episode 138.
	refs := svc.ResolveEpisodes(ctx, 1, parser.Parse("HxH - S03E02"))
	if len(refs) != 1 || refs[0].Season != 3 || refs[0].Episode != 138 {
		t.Fatalf("per-cour resolve = %+v; want [{3 138}]", refs)
	}
	// Absolute release "[Group] HxH - 140" → season 3, episode 140.
	refs = svc.ResolveEpisodes(ctx, 1, parser.Parse("[SubsPlease] HxH - 140 [1080p]"))
	if len(refs) != 1 || refs[0].Season != 3 || refs[0].Episode != 140 {
		t.Fatalf("absolute resolve = %+v; want [{3 140}]", refs)
	}
}

// TestAnimeResolution replicates the Hunter x Hunter case: metadata numbers season 3
// absolutely (episodes 137-148) while files are per-cour (S03E01..). The repo helpers
// that back the anime resolver must map both a per-cour file and an absolute release to
// the right episode.
func TestAnimeResolution(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	db := st.DB()
	ctx := context.Background()

	if _, err := db.ExecContext(ctx, `INSERT INTO series (id,tmdb_id,title,series_type) VALUES (1,999,'Hunter x Hunter','anime')`); err != nil {
		t.Fatal(err)
	}
	// Season 1: episodes 1..136. Season 3 (skip a "season 2" for brevity): 137..148.
	for n := 1; n <= 136; n++ {
		if _, err := db.ExecContext(ctx, `INSERT INTO episodes (series_id,season_number,episode_number) VALUES (1,1,?)`, n); err != nil {
			t.Fatal(err)
		}
	}
	for n := 137; n <= 148; n++ {
		if _, err := db.ExecContext(ctx, `INSERT INTO episodes (series_id,season_number,episode_number) VALUES (1,3,?)`, n); err != nil {
			t.Fatal(err)
		}
	}
	r := NewRepo(db)
	if err := r.BackfillAbsolute(ctx, 1); err != nil {
		t.Fatal(err)
	}

	// Positional: file "S03E01" → the 1st episode of season 3 = episode 137.
	if ep, ok := r.NthEpisodeOfSeason(ctx, 1, 3, 1); !ok || ep != 137 {
		t.Fatalf("NthEpisodeOfSeason(3,1) = %d,%v; want 137,true", ep, ok)
	}
	if ep, ok := r.NthEpisodeOfSeason(ctx, 1, 3, 12); !ok || ep != 148 {
		t.Fatalf("NthEpisodeOfSeason(3,12) = %d,%v; want 148,true", ep, ok)
	}
	// Absolute: "[Group] HxH - 137" → absolute 137 → season 3, episode 137.
	if se, ep, ok := r.EpisodeByAbsolute(ctx, 1, 137); !ok || se != 3 || ep != 137 {
		t.Fatalf("EpisodeByAbsolute(137) = %d,%d,%v; want 3,137,true", se, ep, ok)
	}
	// EpisodeExists guards the positional fallback (exact (3,1) must not exist).
	if r.EpisodeExists(ctx, 1, 3, 1) {
		t.Fatal("EpisodeExists(3,1) should be false for absolute-numbered metadata")
	}
	if !r.EpisodeExists(ctx, 1, 3, 137) {
		t.Fatal("EpisodeExists(3,137) should be true")
	}
}
