package store_test

import (
	"context"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/tristenlammi/arrmada/internal/automation"
	"github.com/tristenlammi/arrmada/internal/music"
	"github.com/tristenlammi/arrmada/internal/quality"
	"github.com/tristenlammi/arrmada/internal/series"
	"github.com/tristenlammi/arrmada/internal/store"
)

func TestSQLiteCalendarRepositories(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	checkCalendarRepositories(t, context.Background(), st.DB())
}

func TestPostgresCalendarRepositories(t *testing.T) {
	ctx, _, opt, _ := store.PostgresFixture(t)
	db, err := store.OpenPostgresPool(ctx, opt)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := store.MigratePostgres(ctx, db, store.PostgresMigrations(), nil); err != nil {
		t.Fatal(err)
	}
	checkCalendarRepositories(t, ctx, db)
}

func checkCalendarRepositories(t *testing.T, ctx context.Context, db store.Pool) {
	now := time.Now().UTC()
	past, future := now.AddDate(0, 0, -7).Format("2006-01-02"), now.AddDate(0, 0, 7).Format("2006-01-02")
	from, to := now.AddDate(0, 0, -8).Format("2006-01-02"), now.AddDate(0, 0, 8).Format("2006-01-02")
	repo := series.NewRepo(db)
	show, err := repo.Create(ctx, series.Series{TMDBID: 123, Title: "Show", Monitored: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.InsertSeasons(ctx, show.ID, []series.Season{
		{SeasonNumber: 0, Monitored: true, Episodes: []series.Episode{{SeasonNumber: 0, EpisodeNumber: 1, AirDate: past, Monitored: true}}},
		{SeasonNumber: 1, Monitored: true, Episodes: []series.Episode{
			{SeasonNumber: 1, EpisodeNumber: 1, AirDate: past, Monitored: true},
			{SeasonNumber: 1, EpisodeNumber: 2, AirDate: future, Monitored: true},
			{SeasonNumber: 1, EpisodeNumber: 3, Monitored: true},
			{SeasonNumber: 1, EpisodeNumber: 4, AirDate: "not-a-date", Monitored: true},
			{SeasonNumber: 1, EpisodeNumber: 5, AirDate: past},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := repo.SetEpisodeFile(ctx, show.ID, 1, 5, "/show/5.mkv", 10); err != nil {
		t.Fatal(err)
	}
	stats, err := repo.StatsFor(ctx, show.ID)
	if err != nil || stats.Episodes != 2 || stats.HaveFiles != 1 || stats.Missing != 1 || stats.NextAirDate != future {
		t.Fatalf("series stats = %+v, %v", stats, err)
	}
	progress, err := repo.SeasonProgress(ctx, show.ID)
	if err != nil || progress[1].Episodes != 5 || progress[1].Have != 1 || progress[1].Aired != 2 || progress[1].Wanted != 1 {
		t.Fatalf("season progress = %+v, %v", progress, err)
	}
	service := series.NewService(db, nil, "", nil)
	upcoming, err := service.UpcomingEpisodes(ctx, from, to)
	if err != nil || len(upcoming) != 3 {
		t.Fatalf("calendar = %+v, %v", upcoming, err)
	}
	if !repo.HasWantedEpisodes(ctx, show.ID) || !repo.SeasonHasMissing(ctx, show.ID, 1) {
		t.Fatal("aired missing episode not searchable")
	}
	if count, err := service.CountSearchStuck(ctx, 0); err != nil || count != 1 {
		t.Fatalf("search-stuck count = %d, %v", count, err)
	}
	if err := repo.ApplyMonitorPreset(ctx, show.ID, series.PresetFuture, nil, nil); err != nil {
		t.Fatal(err)
	}
	rows, err := db.QueryContext(ctx, `SELECT episode_number, monitored FROM episodes WHERE series_id = ? AND season_number = 1 ORDER BY episode_number`, show.ID)
	if err != nil {
		t.Fatal(err)
	}
	flags := map[int]int{}
	for rows.Next() {
		var episode, monitored int
		if err := rows.Scan(&episode, &monitored); err != nil {
			_ = rows.Close()
			t.Fatal(err)
		}
		flags[episode] = monitored
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		t.Fatal(err)
	}
	_ = rows.Close()
	if !reflect.DeepEqual(flags, map[int]int{1: 0, 2: 1, 3: 1, 4: 0, 5: 0}) {
		t.Fatalf("future monitoring preset = %v", flags)
	}
	for _, preset := range []string{series.PresetNone, series.PresetAll, series.PresetMissing, series.PresetExisting, series.PresetFirstSeason, series.PresetLatestSeason, series.PresetAll} {
		if err := repo.ApplyMonitorPreset(ctx, show.ID, preset, nil, nil); err != nil {
			t.Fatalf("preset %s: %v", preset, err)
		}
	}
	summary, err := repo.AcquisitionSummary(ctx)
	if err != nil || len(summary) != 1 || summary[0].SearchingCount != 1 || !reflect.DeepEqual(summary[0].WantedSeasons, []int{1}) || summary[0].NextAir != future {
		t.Fatalf("wanted/acquisition summary = %+v, %v", summary, err)
	}
	listed, err := repo.List(ctx)
	if err != nil || len(listed) != 1 || listed[0].Stats.Missing != 1 {
		t.Fatalf("series list = %+v, %v", listed, err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO artists (id, mbid, name) VALUES (10, 'calendar-artist', 'Artist')`); err != nil {
		t.Fatal(err)
	}
	for i, date := range []string{past, now.Format("2006"), "2026-99-99", ""} {
		if _, err := db.ExecContext(ctx, `INSERT INTO albums (artist_id, mbid, title, release_date) VALUES (10, ?, 'Album', ?)`, fmt.Sprintf("calendar-%d", i), date); err != nil {
			t.Fatal(err)
		}
	}
	albums, err := music.NewRepo(db).CalendarAlbums(ctx, from, to)
	if err != nil || len(albums) != 1 || albums[0].ReleaseDate != past {
		t.Fatalf("album calendar = %+v, %v", albums, err)
	}
	artists, err := music.NewRepo(db).ListArtists(ctx)
	if err != nil || len(artists) != 1 || artists[0].Stats == nil {
		t.Fatalf("artist list flags/stats = %+v, %v", artists, err)
	}
	coordinator := automation.New(nil, nil, nil, nil, db, nil, nil, "")
	if any, err := coordinator.AnyImported(ctx); err != nil || any {
		t.Fatalf("initial import flag = %v, %v", any, err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO grabs (movie_id, title, media_type, status) VALUES (?, 'Show', 'series', 'imported')`, show.ID); err != nil {
		t.Fatal(err)
	}
	if any, err := coordinator.AnyImported(ctx); err != nil || !any {
		t.Fatalf("completed import flag = %v, %v", any, err)
	}
	for i, ref := range []string{"custom:not-a-number", "custom:1", "n/a"} {
		if _, err := db.ExecContext(ctx, `INSERT INTO movies (id, tmdb_id, title, quality_profile) VALUES (?, ?, 'Movie', ?)`, 100+i, 100+i, ref); err != nil {
			t.Fatal(err)
		}
	}
	q := quality.NewService(db)
	repaired, err := q.RepairDanglingRefs(ctx)
	if err != nil || repaired["movies"] != 1 {
		t.Fatalf("profile repair = %v, %v", repaired, err)
	}
	var untouched string
	if err := db.QueryRowContext(ctx, `SELECT quality_profile FROM movies WHERE id = 102`).Scan(&untouched); err != nil || untouched != "n/a" {
		t.Fatalf("deliberate profile marker changed: %q, %v", untouched, err)
	}
}
