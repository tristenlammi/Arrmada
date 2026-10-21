package store_test

import (
	"context"
	"log/slog"
	"testing"

	"github.com/tristenlammi/arrmada/internal/movies"
	"github.com/tristenlammi/arrmada/internal/settings"
	"github.com/tristenlammi/arrmada/internal/store"
	"github.com/tristenlammi/arrmada/internal/subtitles"
)

func TestSQLiteSubtitleJobIDsSurvivePruningAndRestart(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	checkSubtitleJobIDs(t, context.Background(), st.DB())
}

func TestPostgresSubtitleJobIDsSurvivePruningAndRestart(t *testing.T) {
	ctx, _, opt, _ := store.PostgresFixture(t)
	db, err := store.OpenPostgresPool(ctx, opt)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := store.MigratePostgres(ctx, db, store.PostgresMigrations(), nil); err != nil {
		t.Fatal(err)
	}
	checkSubtitleJobIDs(t, ctx, db)
}

func checkSubtitleJobIDs(t *testing.T, ctx context.Context, db store.Pool) {
	t.Helper()
	if _, err := db.ExecContext(ctx, `INSERT INTO movies (id, tmdb_id, title, has_file, movie_file_path) VALUES (1, 1, 'Movie', 1, '/movie.mkv')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO subtitle_jobs (id, media_key, kind, title, priority, state, queued_at)
		VALUES (900, 'movie:1', 'movie', 'Old job', 1, 'done', 1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM subtitle_jobs`); err != nil {
		t.Fatal(err)
	}
	for _, want := range []int64{901, 902} {
		// Reconstruct the service as a process restart would; don't run workers
		// or touch media files. Persisted ids must outlive the newest pruned row.
		mv := movies.NewService(db, nil, nil, "", "", nil, slog.Default())
		svc := subtitles.NewService(db, mv, nil, settings.NewService(db), nil, "", "", "", slog.Default())
		job, err := svc.QueueMovie(ctx, 1, false, 1)
		if err != nil || job == nil || job.ID != want {
			t.Fatalf("restarted queue id = %+v, want %d, err %v", job, want, err)
		}
		var persisted int64
		if err := db.QueryRowContext(ctx, `SELECT id FROM subtitle_jobs`).Scan(&persisted); err != nil || persisted != want {
			t.Fatalf("queue was not persisted: %d, %v", persisted, err)
		}
		if _, err := db.ExecContext(ctx, `DELETE FROM subtitle_jobs`); err != nil {
			t.Fatal(err)
		}
	}
}
