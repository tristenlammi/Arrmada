package store_test

import (
	"context"
	"reflect"
	"testing"

	"github.com/tristenlammi/arrmada/internal/series"
	"github.com/tristenlammi/arrmada/internal/store"
)

func TestSQLiteUpstreamFolderQueries(t *testing.T) {
	s, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	checkUpstreamFolderQueries(t, context.Background(), s.DB())
}

func TestPostgresUpstreamFolderQueries(t *testing.T) {
	ctx, _, opt, _ := store.PostgresFixture(t)
	db, err := store.OpenPostgresPool(ctx, opt)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := store.MigratePostgres(ctx, db, store.PostgresMigrations(), nil); err != nil {
		t.Fatal(err)
	}
	checkUpstreamFolderQueries(t, ctx, db)
}

func checkUpstreamFolderQueries(t *testing.T, ctx context.Context, db store.Pool) {
	t.Helper()
	repo := series.NewRepo(db)
	var ids []int64
	for i, path := range []string{
		"/tv/Été_100%/Season 1/one.mkv",
		"/tv/Été_100%/Season 1/two.mkv",
		"/tv/Été_100% Extra/Season 1/three.mkv",
		"/tv/ÉtéX100-other/Season 1/four.mkv",
	} {
		s, err := repo.Create(ctx, series.Series{TMDBID: 99000 + i, Title: path})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, s.ID)
		if err := repo.InsertSeasons(ctx, s.ID, []series.Season{{SeasonNumber: 1, Episodes: []series.Episode{{SeasonNumber: 1, EpisodeNumber: 1}}}}); err != nil {
			t.Fatal(err)
		}
		if err := repo.SetEpisodeFile(ctx, s.ID, 1, 1, path, 1); err != nil {
			t.Fatal(err)
		}
	}
	if got := repo.OtherSeriesUnder(ctx, ids[0], "/tv/Été_100%", 10); !reflect.DeepEqual(got, []int64{ids[1]}) {
		t.Fatalf("whole-path literal prefix guard: got %v, want %v", got, ids[1])
	}
	if got := repo.OtherSeriesUnder(ctx, ids[0], "/tv/Été_100%", 1); !reflect.DeepEqual(got, []int64{ids[1]}) {
		t.Fatalf("limited prefix guard: %v", got)
	}
	if got, err := repo.SeasonFilePaths(ctx, ids[1], 1); err != nil || !reflect.DeepEqual(got, []string{"/tv/Été_100%/Season 1/two.mkv"}) {
		t.Fatalf("season file paths: %v %v", got, err)
	}
	if got, err := repo.AllEpisodeFilePaths(ctx); err != nil || len(got) != 4 {
		t.Fatalf("whole-library file paths: %v %v", got, err)
	}
	if !repo.FileRecorded(ctx, "/tv/Été_100%/Season 1/one.mkv") || repo.FileRecorded(ctx, "/tv/not-recorded.mkv") {
		t.Fatal("recorded-file guard changed")
	}
}
