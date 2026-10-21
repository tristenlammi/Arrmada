package store_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/tristenlammi/arrmada/internal/automation"
	"github.com/tristenlammi/arrmada/internal/books"
	"github.com/tristenlammi/arrmada/internal/movies"
	"github.com/tristenlammi/arrmada/internal/outbox"
	"github.com/tristenlammi/arrmada/internal/series"
	"github.com/tristenlammi/arrmada/internal/store"
)

func TestPostgresAcquisitionExpiryUsesNativeTimestamps(t *testing.T) {
	ctx, _, opt, _ := store.PostgresFixture(t)
	db, err := store.OpenPostgresPool(ctx, opt)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := store.MigratePostgres(ctx, db, store.PostgresMigrations(), nil); err != nil {
		t.Fatal(err)
	}
	for _, row := range []struct{ title, hash, status, offset string }{
		{"expired hashless", "", "grabbed", "-2 days"},
		{"recent hashless", "", "grabbed", "-6 hours"},
		{"tracked old", "hash", "grabbed", "-2 days"},
		{"held review", "", "held", "-2 days"},
	} {
		if _, err := db.ExecContext(ctx, `INSERT INTO grabs (movie_id, title, info_hash, status, grabbed_at)
			VALUES (1, ?, ?, ?, `+store.UTCNowTimestamp(db, "?")+`)`, row.title, row.hash, row.status, row.offset); err != nil {
			t.Fatal(err)
		}
	}
	c := automation.New(nil, nil, nil, nil, db, nil, nil, "")
	active, err := c.Active(ctx, "movie", 1)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, row := range active {
		got[row.Title] = true
	}
	if len(got) != 3 || got["expired hashless"] || !got["recent hashless"] || !got["tracked old"] || !got["held review"] {
		t.Fatalf("unexpected active acquisitions: %v", got)
	}
}

func TestPostgresBookFreshnessAndDuplicateKeys(t *testing.T) {
	ctx, _, opt, _ := store.PostgresFixture(t)
	db, err := store.OpenPostgresPool(ctx, opt)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := store.MigratePostgres(ctx, db, store.PostgresMigrations(), nil); err != nil {
		t.Fatal(err)
	}
	repo := books.NewRepo(db)
	book, err := repo.Create(ctx, books.Book{OLKey: "OL-test", Title: "Book", Author: "Author"})
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := repo.AddKey(ctx, "former-key", book.ID, "openlibrary"); err != nil {
			t.Fatal(err)
		}
	}
	if id, ok := repo.BookIDForKey(ctx, "former-key"); !ok || id != book.ID {
		t.Fatalf("alias owner = %d, %v", id, ok)
	}
	if keys, err := repo.KeysFor(ctx, book.ID); err != nil || len(keys) != 2 {
		t.Fatalf("current/former keys = %+v, %v", keys, err)
	}
	if due, err := repo.DueForRefresh(ctx, 7, 10); err != nil || len(due) != 1 || due[0] != book.ID {
		t.Fatalf("freshness before stamp = %v, %v", due, err)
	}
	if err := repo.StampMetaChecked(ctx, book.ID); err != nil {
		t.Fatal(err)
	}
	if due, err := repo.DueForRefresh(ctx, 7, 10); err != nil || len(due) != 0 {
		t.Fatalf("freshness after stamp = %v, %v", due, err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE books SET meta_checked_at = `+store.UTCNowText(db, "?")+` WHERE id = ?`, "-8 days", book.ID); err != nil {
		t.Fatal(err)
	}
	if due, err := repo.DueForRefresh(ctx, 7, 10); err != nil || len(due) != 1 || due[0] != book.ID {
		t.Fatalf("stale book omitted = %v, %v", due, err)
	}
}

func TestPostgresSeriesDuplicateRefreshPreservesLibraryState(t *testing.T) {
	ctx, _, opt, _ := store.PostgresFixture(t)
	db, err := store.OpenPostgresPool(ctx, opt)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := store.MigratePostgres(ctx, db, store.PostgresMigrations(), nil); err != nil {
		t.Fatal(err)
	}
	repo := series.NewRepo(db)
	show, err := repo.Create(ctx, series.Series{TMDBID: 123, Title: "A series", Monitored: true})
	if err != nil {
		t.Fatal(err)
	}
	listing := []series.Season{{SeasonNumber: 1, Monitored: true, Episodes: []series.Episode{
		{SeasonNumber: 1, EpisodeNumber: 1, Title: "Original", Monitored: true, AbsoluteNumber: 1},
	}}}
	if err := repo.InsertSeasons(ctx, show.ID, listing); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE episodes SET monitored = 0, has_file = 1, file_path = '/library/episode.mkv', size_bytes = 42 WHERE series_id = ?`, show.ID); err != nil {
		t.Fatal(err)
	}
	listing[0].Episodes[0].Title = "Corrected"
	listing[0].Episodes[0].AirDate = "2020-01-01"
	if err := repo.InsertSeasons(ctx, show.ID, listing); err != nil {
		t.Fatal(err)
	}
	var title, path, date string
	var monitored, size, count int
	if err := db.QueryRowContext(ctx, `SELECT title, file_path, air_date, monitored, size_bytes FROM episodes WHERE series_id = ?`, show.ID).Scan(&title, &path, &date, &monitored, &size); err != nil {
		t.Fatal(err)
	}
	if title != "Corrected" || date != "2020-01-01" || path != "/library/episode.mkv" || monitored != 0 || size != 42 {
		t.Fatalf("refresh lost metadata/library state: %q %q %q %d %d", title, date, path, monitored, size)
	}
	listing[0].Episodes[0].Title = "Untrusted stand-in"
	listing[0].Episodes = append(listing[0].Episodes, series.Episode{SeasonNumber: 1, EpisodeNumber: 2, Title: "New"})
	if added, err := repo.InsertNewEpisodes(ctx, show.ID, listing); err != nil || added != 1 {
		t.Fatalf("additive refresh = %d, %v", added, err)
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM episodes WHERE series_id = ?`, show.ID).Scan(&count); err != nil || count != 2 {
		t.Fatalf("episode count = %d, %v", count, err)
	}
	if title := repo.EpisodeTitle(ctx, show.ID, 1, 1); title != "Corrected" {
		t.Fatalf("stand-in overwrote title: %q", title)
	}
	repo.RecordSearchMiss(ctx, show.ID)
	stamp, misses := repo.SearchState(ctx, show.ID)
	if _, err := time.Parse("2006-01-02 15:04:05", stamp); err != nil || misses != 1 {
		t.Fatalf("search state = %q, %d, %v", stamp, misses, err)
	}
	repo.ResetSearchMisses(ctx, show.ID)
	if _, misses := repo.SearchState(ctx, show.ID); misses != 0 {
		t.Fatalf("search misses not reset: %d", misses)
	}
	if err := repo.MarkRefreshed(ctx, show.ID); err != nil {
		t.Fatal(err)
	}
	importedAt := time.Now().UTC().Truncate(time.Second).Add(-time.Hour)
	for _, stamp := range []time.Time{importedAt.Add(-time.Hour), importedAt} {
		if _, err := db.ExecContext(ctx, `INSERT INTO series_events (series_id, event, created_at) VALUES (?, 'imported', ?)`, show.ID, stamp.Format("2006-01-02 15:04:05")+".900000"); err != nil {
			t.Fatal(err)
		}
	}
	recent, err := repo.RecentlyImported(ctx, 7, 10)
	if err != nil || len(recent) != 1 || recent[0].At != importedAt.Unix() || recent[0].TMDBID != 123 {
		t.Fatalf("recent show imports = %+v, %v", recent, err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO movies (id, tmdb_id, title, has_file) VALUES (1, 456, 'Movie', 1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO movie_events (movie_id, event, created_at) VALUES (1, 'imported', ?)`, importedAt.Format("2006-01-02 15:04:05")+".900000"); err != nil {
		t.Fatal(err)
	}
	recentMovies, err := movies.NewRepo(db).RecentlyImported(ctx, 7, 10)
	if err != nil || len(recentMovies) != 1 || recentMovies[0].At != importedAt.Unix() || recentMovies[0].TMDBID != 456 {
		t.Fatalf("recent movie imports = %+v, %v", recentMovies, err)
	}
}

func TestPostgresOutboxRetryClosesSupersededWork(t *testing.T) {
	ctx, _, opt, _ := store.PostgresFixture(t)
	db, err := store.OpenPostgresPool(ctx, opt)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := store.MigratePostgres(ctx, db, store.PostgresMigrations(), nil); err != nil {
		t.Fatal(err)
	}
	o := outbox.New(db, nil)
	o.Register("test", "worker", func(context.Context, json.RawMessage) error { return nil })
	for i := range 2 {
		if err := o.Enqueue(ctx, db, "test", i, "upsert-key"); err != nil {
			t.Fatal(err)
		}
	}
	var gen int
	if err := db.QueryRowContext(ctx, `SELECT gen FROM outbox WHERE dedupe_key = 'test|upsert-key'`).Scan(&gen); err != nil || gen != 1 {
		t.Fatalf("upsert generation = %d, %v", gen, err)
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM outbox`); err != nil {
		t.Fatal(err)
	}
	// Two failed rows may share a key; only one may be revived. The unique
	// index must still reject duplicates after retrying, not be bypassed.
	ids := make([]int64, 2)
	for i := range ids {
		ids[i], err = store.InsertID(ctx, db, `INSERT INTO outbox (topic, consumer, payload, dedupe_key, created_at, failed_at)
			VALUES ('test', 'worker', '{}', 'same-key', 1, 1)`)
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := o.Retry(ctx, ids[0]); err != nil {
		t.Fatal(err)
	}
	if err := o.Retry(ctx, ids[1]); err != nil {
		t.Fatal(err)
	}
	var pending, closed int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM outbox WHERE done_at = 0 AND failed_at = 0`).Scan(&pending); err != nil || pending != 1 {
		t.Fatalf("pending retries = %d, %v", pending, err)
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM outbox WHERE done_at != 0 AND last_error = 'superseded by a newer queued row'`).Scan(&closed); err != nil || closed != 1 {
		t.Fatalf("superseded retries = %d, %v", closed, err)
	}
	if err := o.Retry(ctx, ids[1]); !errors.Is(err, outbox.ErrNotFound) {
		t.Fatalf("closed retry = %v", err)
	}
}

func TestSQLiteBookRenamePreservesAudioIdentity(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	checkBookRenameIdentity(t, context.Background(), st.DB())
}

func TestPostgresBookRenamePreservesAudioIdentity(t *testing.T) {
	ctx, _, opt, _ := store.PostgresFixture(t)
	db, err := store.OpenPostgresPool(ctx, opt)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := store.MigratePostgres(ctx, db, store.PostgresMigrations(), nil); err != nil {
		t.Fatal(err)
	}
	checkBookRenameIdentity(t, ctx, db)
}

func checkBookRenameIdentity(t *testing.T, ctx context.Context, db store.Pool) {
	t.Helper()
	if _, err := db.ExecContext(ctx, `INSERT INTO books (id, ol_key, title) VALUES (1, 'book', 'Book')`); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"old", "new", "unrelated"} {
		if _, err := db.ExecContext(ctx, `INSERT INTO audio_file_meta (path, size, mtime, ino) VALUES (?, 10, 1, ?)`, path, "identity-"+path); err != nil {
			t.Fatal(err)
		}
	}
	svc := books.NewService(db, nil, nil)
	if err := svc.RepointEdition(ctx, 1, 0, books.KindAudiobook, "new", []books.PathMove{
		{From: "old", To: "new"},
		{From: "missing", To: "unrelated"},
		{From: "new", To: "new"},
	}, "renamed"); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]string{"new": "identity-old", "unrelated": "identity-unrelated"} {
		var ino string
		if err := db.QueryRowContext(ctx, `SELECT ino FROM audio_file_meta WHERE path = ?`, path).Scan(&ino); err != nil || ino != want {
			t.Fatalf("metadata at %s = %q, want %q, err %v", path, ino, want, err)
		}
	}
	var count int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM audio_file_meta`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("metadata row count = %d, %v", count, err)
	}
}
