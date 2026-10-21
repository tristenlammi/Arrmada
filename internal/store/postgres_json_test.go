package store_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"

	"github.com/tristenlammi/arrmada/internal/displaytitle"
	"github.com/tristenlammi/arrmada/internal/libfolders"
	"github.com/tristenlammi/arrmada/internal/movies"
	"github.com/tristenlammi/arrmada/internal/music"
	"github.com/tristenlammi/arrmada/internal/requests"
	"github.com/tristenlammi/arrmada/internal/series"
	"github.com/tristenlammi/arrmada/internal/store"
)

func TestSQLiteJSONRepositories(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	checkJSONRepositories(t, context.Background(), st.DB())
}

func TestPostgresJSONRepositories(t *testing.T) {
	ctx, _, opt, _ := store.PostgresFixture(t)
	db, err := store.OpenPostgresPool(ctx, opt)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := store.MigratePostgres(ctx, db, store.PostgresMigrations(), nil); err != nil {
		t.Fatal(err)
	}
	checkJSONRepositories(t, ctx, db)
}

func checkJSONRepositories(t *testing.T, ctx context.Context, db store.Pool) {
	t.Run("metadata", func(t *testing.T) { checkJSONMetadata(t, ctx, db) })
	t.Run("season-notices", func(t *testing.T) { checkJSONSeasonNotices(t, ctx, db) })
	t.Run("folder-paths", func(t *testing.T) { checkJSONFolderPaths(t, ctx, db) })
	t.Run("ascii-matching", func(t *testing.T) { checkASCIIMatching(t, ctx, db) })
}

func checkJSONMetadata(t *testing.T, ctx context.Context, db store.Pool) {
	const raw = `{"vote_average":7.5,"original_language":"ja","opaque":{"large":123456789012345678901234567890}}`
	for _, table := range []string{"movies", "series"} {
		if _, err := db.ExecContext(ctx, `INSERT INTO `+table+` (id, tmdb_id, title, extra_json) VALUES (10, 10, 'English', ?)`, raw); err != nil {
			t.Fatal(err)
		}
	}
	mv := movies.NewRepo(db)
	sr := series.NewRepo(db)
	// Independent metadata backfills must not overwrite each other's fields.
	errs := make(chan error, 5)
	var wg sync.WaitGroup
	for _, write := range []func() error{
		func() error { return mv.SetTitles(ctx, 10, "日本語", []string{"Alternative"}) },
		func() error { return mv.SetDisplayNames(ctx, 10, &displaytitle.Names{}) },
		func() error { return mv.SetLocalTitles(ctx, 10, nil, []string{"ja"}) },
		func() error { return sr.SetDisplayNames(ctx, 10, &displaytitle.Names{}) },
		func() error { return sr.SetLocalTitles(ctx, 10, nil, []string{"ja"}) },
	} {
		wg.Add(1)
		go func() { defer wg.Done(); errs <- write() }()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, table := range []string{"movies", "series"} {
		var updated string
		if err := db.QueryRowContext(ctx, `SELECT extra_json FROM `+table+` WHERE id = 10`).Scan(&updated); err != nil {
			t.Fatal(err)
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal([]byte(updated), &fields); err != nil {
			t.Fatal(err)
		}
		if string(fields["opaque"]) != `{"large":123456789012345678901234567890}` || string(fields["vote_average"]) != "7.5" || fields["display_names"] == nil || fields["local_titles"] == nil {
			t.Fatalf("%s lost unrelated/raw metadata: %s", table, updated)
		}
		if table == "movies" && (string(fields["original_title"]) != `"日本語"` || string(fields["titles_read"]) != "true") {
			t.Fatalf("movie title update lost: %s", updated)
		}
	}
	summaries, err := mv.ListSummaries(ctx)
	if err != nil || len(summaries) != 1 || summaries[0].VoteAverage != 7.5 {
		t.Fatalf("movie summaries = %+v, %v", summaries, err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO episodes (series_id, season_number, episode_number, has_file, file_path) VALUES (10, 1, 1, 1, '/episode')`); err != nil {
		t.Fatal(err)
	}
	files, err := sr.LibraryEpisodeFiles(ctx)
	if err != nil || len(files) != 1 || files[0].OrigLanguage != "ja" {
		t.Fatalf("episode language query = %+v, %v", files, err)
	}
}

func checkJSONSeasonNotices(t *testing.T, ctx context.Context, db store.Pool) {
	r := requests.NewRepo(db)
	req, err := r.Create(ctx, requests.Request{MediaType: "series", TMDBID: 888, Title: "Show", Status: requests.StatusApproved, Seasons: []int{1, 2}})
	if err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{"sHOW", "S_ow"} {
		found, total, err := r.List(ctx, requests.ListFilter{Query: query, Limit: 10})
		want := 1
		if query == "S_ow" {
			want = 0 // the user's underscore is literal, not a wildcard
		}
		if err != nil || len(found) != want || total != want {
			t.Fatalf("request title filter %q = %+v/%d, %v", query, found, total, err)
		}
	}
	for _, step := range []struct {
		season int
		at     int64
		want   map[int]int64
	}{
		{1, 100, map[int]int64{1: 100}},
		{1, 200, map[int]int64{1: 100}},
		{2, 300, map[int]int64{1: 100, 2: 300}},
	} {
		got, err := r.MarkSeasonOnDisk(ctx, req.ID, step.season, step.at)
		if err != nil || !reflect.DeepEqual(got, step.want) {
			t.Fatalf("season stamps = %v, want %v, err %v", got, step.want, err)
		}
	}
	if waiting, err := r.ListPlexWaiting(ctx); err != nil || len(waiting) != 1 {
		t.Fatalf("waiting season notice = %+v, %v", waiting, err)
	}
	if err := r.MarkSeasonTold(ctx, req.ID, 1); err != nil {
		t.Fatal(err)
	}
	for _, season := range []int{1, 2} {
		if err := r.ClearSeasonOnDisk(ctx, req.ID, season); err != nil {
			t.Fatal(err)
		}
	}
	got, err := r.MarkSeasonOnDisk(ctx, req.ID, 1, 999)
	if err != nil || !reflect.DeepEqual(got, map[int]int64{1: -1}) {
		t.Fatalf("announced season was revived: %v, %v", got, err)
	}
	if waiting, err := r.ListPlexWaiting(ctx); err != nil || len(waiting) != 0 {
		t.Fatalf("announced/cleared seasons still waiting = %+v, %v", waiting, err)
	}
	// A malformed historical document is treated as no prior stamps.
	if _, err := db.ExecContext(ctx, `UPDATE requests SET season_disk_at = 'broken' WHERE id = ?`, req.ID); err != nil {
		t.Fatal(err)
	}
	if got, err := r.MarkSeasonOnDisk(ctx, req.ID, 2, 400); err != nil || !reflect.DeepEqual(got, map[int]int64{2: 400}) {
		t.Fatalf("malformed stamp recovery = %v, %v", got, err)
	}
	errs := make(chan error, 4)
	var wg sync.WaitGroup
	for season := 3; season <= 6; season++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := r.MarkSeasonOnDisk(ctx, req.ID, season, int64(season*100))
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if got, err := r.MarkSeasonOnDisk(ctx, req.ID, 2, 999); err != nil || !reflect.DeepEqual(got, map[int]int64{2: 400, 3: 300, 4: 400, 5: 500, 6: 600}) {
		t.Fatalf("concurrent season stamps lost: %v, %v", got, err)
	}
}

func checkJSONFolderPaths(t *testing.T, ctx context.Context, db store.Pool) {
	const oldDir, newDir = "/old/Heat_100%", "/new/Heat_100%"
	const raw = `{"path":"/old/Heat_100%/film.mkv","codec":"hevc","unknown":123456789012345678901234567890}`
	if _, err := db.ExecContext(ctx, `INSERT INTO movies (id, tmdb_id, title, movie_file_path, media_json) VALUES (20, 20, 'Heat', ?, ?)`, oldDir+"/film.mkv", raw); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO convert_probe_cache (path, size_bytes, mtime_unix, info_json)
		VALUES (?, 1, 1, '{}'), (?, 2, 2, '{"stale":true}')`, oldDir+"/film.mkv", newDir+"/film.mkv"); err != nil {
		t.Fatal(err)
	}
	if err := store.WithTx(ctx, db, func(tx *store.Transaction) error {
		_, err := libfolders.RewritePrefix(ctx, tx, oldDir, newDir)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	var path, rawAfter, cache string
	if err := db.QueryRowContext(ctx, `SELECT movie_file_path, media_json FROM movies WHERE id = 20`).Scan(&path, &rawAfter); err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(rawAfter), &fields); err != nil {
		t.Fatal(err)
	}
	if path != newDir+"/film.mkv" || string(fields["path"]) != fmt.Sprintf("%q", path) || string(fields["unknown"]) != "123456789012345678901234567890" {
		t.Fatalf("path rewrite lost metadata: %q %s", path, rawAfter)
	}
	if err := db.QueryRowContext(ctx, `SELECT info_json FROM convert_probe_cache WHERE path = ?`, path).Scan(&cache); err != nil || cache != "{}" {
		t.Fatalf("stale cache not replaced: %q, %v", cache, err)
	}
	boom := errors.New("file move failed")
	err := store.WithTx(ctx, db, func(tx *store.Transaction) error {
		if _, err := libfolders.RewritePrefix(ctx, tx, newDir, oldDir); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatal(err)
	}
	var afterRollback string
	if err := db.QueryRowContext(ctx, `SELECT media_json FROM movies WHERE id = 20`).Scan(&afterRollback); err != nil || afterRollback != rawAfter {
		t.Fatalf("failed move changed paths: %q, %v", afterRollback, err)
	}
}

func checkASCIIMatching(t *testing.T, ctx context.Context, db store.Pool) {
	if _, err := db.ExecContext(ctx, `INSERT INTO artists (id, mbid, name) VALUES (40, 'MB-ARTIST', 'ÉArtist')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO albums (id, artist_id, mbid, title, year) VALUES (40, 40, 'MB-ALBUM', 'ALBUM_100%', 2000)`); err != nil {
		t.Fatal(err)
	}
	repo := music.NewRepo(db)
	for _, tt := range []struct {
		query string
		want  int
	}{
		{"album_100%", 1}, {"ÉARTIST", 1}, {"éartist", 0}, {"albumX100Z", 0},
	} {
		found, err := repo.SearchAlbums(ctx, tt.query, 10)
		if err != nil || len(found) != tt.want {
			t.Errorf("album search %q = %+v, %v", tt.query, found, err)
		}
	}
	artist, err := music.NewService(db, nil, nil).ArtistByMBID(ctx, "mb-artist")
	if err != nil || artist.ID != 40 {
		t.Fatalf("ASCII MBID lookup = %+v, %v", artist, err)
	}
}
