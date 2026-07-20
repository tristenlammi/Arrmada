package series

import (
	"context"
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"
)

// A refresh must actually refresh. INSERT OR IGNORE alone froze an episode's metadata at
// whatever it was when the show was added, so a title TMDB later corrected — or an air
// date it published for a previously-unannounced episode — could never be picked up. The
// second is the damaging one: dateless episodes count as unaired and aren't searched, so
// they'd stay invisible to the searcher permanently.
func TestRefreshUpdatesEpisodeMetadata(t *testing.T) {
	repo, ctx := testRepo(t)
	sr, err := repo.Create(ctx, Series{TMDBID: 1, Title: "Show", Monitored: true})
	if err != nil {
		t.Fatal(err)
	}

	// As first added: a placeholder with no title and no air date.
	first := []Season{{SeasonNumber: 1, Monitored: true, Episodes: []Episode{
		{SeasonNumber: 1, EpisodeNumber: 1, Title: "TBA", AirDate: "", Runtime: 0},
	}}}
	if err := repo.InsertSeasons(ctx, sr.ID, first); err != nil {
		t.Fatal(err)
	}

	// The user marks it and it gains a file — state that must survive a refresh.
	if _, err := repo.db.ExecContext(ctx,
		`UPDATE episodes SET has_file = 1, file_path = '/x.mkv', size_bytes = 42, monitored = 0 WHERE series_id = ?`, sr.ID); err != nil {
		t.Fatal(err)
	}

	// TMDB later fills in the real title and date.
	second := []Season{{SeasonNumber: 1, Monitored: true, Episodes: []Episode{
		{SeasonNumber: 1, EpisodeNumber: 1, Title: "The Real Title", AirDate: "2024-03-21", Runtime: 42},
	}}}
	if err := repo.InsertSeasons(ctx, sr.ID, second); err != nil {
		t.Fatal(err)
	}

	seasons, err := repo.SeasonsFor(ctx, sr.ID)
	if err != nil || len(seasons) != 1 || len(seasons[0].Episodes) != 1 {
		t.Fatalf("unexpected shape: %+v (err %v)", seasons, err)
	}
	e := seasons[0].Episodes[0]

	if e.Title != "The Real Title" {
		t.Errorf("Title = %q, want the corrected title — a refresh that can't fix a title isn't a refresh", e.Title)
	}
	if e.AirDate != "2024-03-21" {
		t.Errorf("AirDate = %q, want the published date — without it the episode is treated as unaired forever", e.AirDate)
	}
	if e.Runtime != 42 {
		t.Errorf("Runtime = %d, want 42 — it drives the bitrate upgrade comparison", e.Runtime)
	}

	// Library and user state is NOT TMDB's to overwrite.
	if !e.HasFile || e.FilePath != "/x.mkv" || e.SizeBytes != 42 {
		t.Errorf("file state was clobbered: has_file=%v path=%q size=%d", e.HasFile, e.FilePath, e.SizeBytes)
	}
	if e.Monitored {
		t.Error("monitoring is the user's choice and must survive a refresh")
	}
}

func testRepo(t *testing.T) (*Repo, context.Context) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+t.TempDir()+"/t.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	schema := []string{
		`CREATE TABLE series (id INTEGER PRIMARY KEY AUTOINCREMENT, tmdb_id INTEGER UNIQUE, imdb_id TEXT DEFAULT '',
		 title TEXT, year INTEGER DEFAULT 0, overview TEXT DEFAULT '', poster_url TEXT DEFAULT '', status TEXT DEFAULT '',
		 network TEXT DEFAULT '', monitored INTEGER DEFAULT 1, quality_profile TEXT DEFAULT '', extra_json TEXT DEFAULT '',
		 series_type TEXT DEFAULT 'standard', tvdb_id INTEGER DEFAULT 0, added_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP)`,
		`CREATE TABLE seasons (id INTEGER PRIMARY KEY AUTOINCREMENT, series_id INTEGER, season_number INTEGER,
		 name TEXT DEFAULT '', overview TEXT DEFAULT '', poster_url TEXT DEFAULT '', monitored INTEGER DEFAULT 1,
		 UNIQUE(series_id, season_number))`,
		`CREATE TABLE episodes (id INTEGER PRIMARY KEY AUTOINCREMENT, series_id INTEGER, season_number INTEGER,
		 episode_number INTEGER, title TEXT DEFAULT '', overview TEXT DEFAULT '', air_date TEXT DEFAULT '',
		 runtime INTEGER DEFAULT 0, still_url TEXT DEFAULT '', monitored INTEGER DEFAULT 1, has_file INTEGER DEFAULT 0,
		 file_path TEXT DEFAULT '', size_bytes INTEGER DEFAULT 0, absolute_number INTEGER DEFAULT 0,
		 source_release TEXT DEFAULT '', UNIQUE(series_id, season_number, episode_number))`,
	}
	for _, q := range schema {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	return NewRepo(db), context.Background()
}
