package store_test

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/tristenlammi/arrmada/internal/insights"
	"github.com/tristenlammi/arrmada/internal/settings"
	"github.com/tristenlammi/arrmada/internal/store"
)

func TestSQLiteInsightsRepositories(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	checkInsightsRepositories(t, context.Background(), st.DB())
}

func TestPostgresInsightsRepositories(t *testing.T) {
	ctx, _, opt, _ := store.PostgresFixture(t)
	db, err := store.OpenPostgresPool(ctx, opt)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := store.MigratePostgres(ctx, db, store.PostgresMigrations(), nil); err != nil {
		t.Fatal(err)
	}
	checkInsightsRepositories(t, ctx, db)
}

func checkInsightsRepositories(t *testing.T, ctx context.Context, db store.Pool) {
	// The application timezone need not have a PostgreSQL/IANA name and need
	// not match the database server's UTC timezone.
	previousZone := time.Local
	if store.DialectOf(db) == store.Postgres {
		time.Local = time.FixedZone("application-fixed", 5*3600+1800)
	}
	t.Cleanup(func() { time.Local = previousZone })
	set := settings.NewService(db)
	svc := insights.NewService(db, set, nil, nil, slog.Default())
	start := time.Now().Add(-2 * time.Hour).Truncate(time.Hour).Add(10 * time.Minute).Unix()
	if _, err := db.ExecContext(ctx, `INSERT INTO stream_sessions
		(session_key, user_id, user_name, title, year, media_type, rating_key, started_at, stopped_at, watched_ms, platform)
		VALUES ('live', '42', 'Old name', 'Heat', 1995, 'movie', 'film', ?, ?, 120000, 'Test')`, start, start+120); err != nil {
		t.Fatal(err)
	}
	imported := insights.ImportedSession{UserID: 42, UserName: "Current name", Title: "Heat", Year: 1995, MediaType: "movie", RatingKey: "film",
		StartedAt: start - 600, StoppedAt: start - 420, WatchedMS: 60000, Platform: "Test"}
	counts := svc.ImportHistory(ctx, []insights.ImportedSession{
		imported,
		{UserID: 42, Title: "Heat", Year: 1995, MediaType: "movie", RatingKey: "film", StartedAt: start + 10, StoppedAt: start + 110},
	}, insights.ImportOptions{})
	if counts.Imported != 1 || counts.Overlap != 1 || counts.Failed != 0 {
		t.Fatalf("import/overlap = %+v", counts)
	}
	if counts := svc.ImportHistory(ctx, []insights.ImportedSession{imported}, insights.ImportOptions{}); counts.Duplicate != 1 || counts.Failed != 0 {
		t.Fatalf("duplicate import = %+v", counts)
	}
	stats, err := svc.Stats(ctx, 30, true, "")
	if err != nil || len(stats.Movies) != 1 || stats.Movies[0].Plays != 2 || stats.Movies[0].Secs != 180 || len(stats.Users) != 1 || stats.Users[0].Secs != 180 {
		t.Fatalf("watch totals = %+v, %v", stats, err)
	}
	for _, filter := range []insights.HistoryFilter{{Group: true}, {Group: true, Query: "heat"}, {Query: "HEAT"}} {
		history, err := svc.History(ctx, filter)
		if err != nil || history.Total == 0 || len(history.Rows) == 0 {
			t.Fatalf("grouped history (%+v) = %+v, %v", filter, history, err)
		}
		var seconds int64
		for _, row := range history.Rows {
			seconds += row.WatchedSecs
		}
		if seconds != 180 {
			t.Fatalf("grouped history watch time = %d", seconds)
		}
	}
	person, err := svc.Person(ctx, "42", 30)
	if err != nil || person.TotalSecs != 180 {
		t.Fatalf("person totals = %+v, %v", person, err)
	}
	if watched, err := svc.RecentlyWatchedByUser(ctx, 10, "42"); err != nil || len(watched) != 1 || watched[0].Title != "Heat" {
		t.Fatalf("recent titles = %+v, %v", watched, err)
	}
	// Configure only the local read path; no poller or index builder is run.
	for key, value := range map[string]string{"insights_plex_url": "http://not-contacted.invalid", "insights_plex_token": "test"} {
		if err := set.Set(ctx, key, value); err != nil {
			t.Fatal(err)
		}
	}
	watch, err := svc.WatchStats(ctx, "movie", insights.ExternalIDs{}, "heat", 1995)
	if err != nil || watch.Plays != 2 || len(watch.Users) != 1 || watch.Users[0].Name != "Current name" {
		t.Fatalf("case-insensitive title stats = %+v, %v", watch, err)
	}
	for _, sample := range []struct {
		server    string
		at, total int64
	}{
		{"plex", start, 100}, {"plex", start + 1, 150}, {"jellyfin", start, 200},
	} {
		if _, err := db.ExecContext(ctx, `INSERT INTO bandwidth_samples (at, server, total_kbps, lan_kbps, wan_kbps) VALUES (?, ?, ?, 0, 0)`, sample.at, sample.server, sample.total); err != nil {
			t.Fatal(err)
		}
	}
	graphs, err := svc.Graphs(ctx, 30, insights.MetricDuration, "")
	if err != nil {
		t.Fatal(err)
	}
	var hours float64
	for _, n := range graphs.DailyMovies {
		hours += n
	}
	if hours != 0.05 || len(graphs.Bandwidth) != 1 || graphs.Bandwidth[0].Total != 350 {
		t.Fatalf("local graphs = %+v", graphs)
	}
	if graphs.ByHour[time.Unix(start, 0).In(time.Local).Hour()] <= 0 {
		t.Fatalf("hour graph ignored application timezone: %v", graphs.ByHour)
	}
	// Watch time is bounded by wall time and zero, with integer division.
	for i, row := range []struct{ span, watched, paused int64 }{
		{120, 180999, 0}, {120, 0, 60000}, {-10, 60000, 0}, {120, 0, 180000},
	} {
		if _, err := db.ExecContext(ctx, `INSERT INTO stream_sessions
			(session_key, user_id, user_name, title, year, media_type, started_at, stopped_at, watched_ms, paused_ms)
			VALUES (?, '42', 'Current name', 'Heat', 1995, 'movie', ?, ?, ?, ?)`,
			"clamp-"+time.Duration(i).String(), start, start+row.span, row.watched, row.paused); err != nil {
			t.Fatal(err)
		}
	}
	stats, err = svc.Stats(ctx, 30, true, "")
	if err != nil || len(stats.Movies) != 1 || stats.Movies[0].Secs != 360 {
		t.Fatalf("watch time clamps = %+v, %v", stats, err)
	}
	if store.DialectOf(db) == store.Postgres {
		checkPostgresDSTGraphs(t, ctx, db, svc)
	}
	duplicateID, err := store.InsertID(ctx, db, `INSERT INTO stream_sessions
		(user_id, user_name, title, year, media_type, rating_key, started_at, stopped_at)
		VALUES ('42', 'Duplicate import', 'Heat', 1995, 'movie', 'film', ?, ?)`, start+20, start+100)
	if err != nil {
		t.Fatal(err)
	}
	overlaps, _, err := svc.ImportOverlaps(ctx)
	if err != nil || overlaps != 1 {
		t.Fatalf("overlap repair preview = %d, %v", overlaps, err)
	}
	if _, err := svc.RemoveImportOverlaps(ctx, 2); !errors.Is(err, insights.ErrOverlapsChanged) {
		t.Fatalf("changed preview was not refused: %v", err)
	}
	if removed, err := svc.RemoveImportOverlaps(ctx, 1); err != nil || removed != 1 {
		t.Fatalf("overlap repair = %d, %v", removed, err)
	}
	var remaining int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM stream_sessions WHERE id = ?`, duplicateID).Scan(&remaining); err != nil || remaining != 0 {
		t.Fatalf("duplicate remains after repair: %d, %v", remaining, err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE bandwidth_samples SET total_kbps = ? WHERE server = 'plex'`, int64(1<<63-1)); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Graphs(ctx, 30, insights.MetricPlays, ""); err == nil {
		t.Fatal("bandwidth aggregation silently overflowed")
	}
}

func checkPostgresDSTGraphs(t *testing.T, ctx context.Context, db store.Pool, svc *insights.Service) {
	zone, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	previous := time.Local
	time.Local = zone
	defer func() { time.Local = previous }()
	// First Sunday of November in the preceding year: 01:30 occurs twice.
	first := time.Date(time.Now().Year()-1, time.November, 1, 5, 30, 0, 0, time.UTC)
	first = first.AddDate(0, 0, (7-int(first.Weekday()))%7)
	for i, at := range []time.Time{first, first.Add(time.Hour)} {
		if _, err := db.ExecContext(ctx, `INSERT INTO stream_sessions
			(session_key, user_id, user_name, title, media_type, started_at, stopped_at, watched_ms, server)
			VALUES (?, 'dst', 'DST', 'Track', 'track', ?, ?, 60000, 'jellyfin')`,
			"dst-"+time.Duration(i).String(), at.Unix(), at.Unix()+60); err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO bandwidth_samples (at, server, total_kbps, lan_kbps, wan_kbps) VALUES (?, 'jellyfin', ?, 0, 0)`, at.Unix(), (i+1)*100); err != nil {
			t.Fatal(err)
		}
	}
	graphs, err := svc.Graphs(ctx, 800, insights.MetricPlays, "jellyfin")
	if err != nil || graphs.ByHour[1] != 2 {
		t.Fatalf("DST repeated-hour count = %v, %v", graphs.ByHour, err)
	}
	date := first.In(zone).Format("2006-01-02")
	foundDay, foundPeak := false, false
	for i, d := range graphs.Days {
		if d == date {
			foundDay = graphs.DailyMusic[i] == 2
		}
	}
	for _, point := range graphs.Bandwidth {
		if point.T == date+" 01:00" {
			foundPeak = point.Total == 200 // peak, not sum, over the repeated hour
		}
	}
	if !foundDay || !foundPeak {
		t.Fatalf("DST local-day/peak buckets missing: day=%v peak=%v", foundDay, foundPeak)
	}
}
