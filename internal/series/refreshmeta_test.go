package series

import (
	"context"
	"testing"

	"github.com/tristenlammi/arrmada/internal/store"
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

// testRepo is a repo on the real schema (every migration applied) in a temp dir, so new
// columns — numbering_source, and whatever comes after it — never break these tests.
func testRepo(t *testing.T) (*Repo, context.Context) {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return NewRepo(st.DB()), context.Background()
}

// A refresh that can only ADD leaves a show stuck with whatever a previous metadata
// source invented. TVmaze numbers some long-running shows by broadcast year, so Naruto
// gained seasons 2002-2007 beside its real ones — and no refresh could remove them.
func TestRefreshPrunesStaleSeasons(t *testing.T) {
	repo, ctx := testRepo(t)
	sr, err := repo.Create(ctx, Series{TMDBID: 1, Title: "Naruto", Monitored: true})
	if err != nil {
		t.Fatal(err)
	}

	// The real seasons, plus the year-numbered ones a bad listing left behind.
	if err := repo.InsertSeasons(ctx, sr.ID, []Season{
		{SeasonNumber: 1, Episodes: []Episode{{SeasonNumber: 1, EpisodeNumber: 1, Title: "Enter: Naruto Uzumaki!"}}},
		{SeasonNumber: 2002, Episodes: []Episode{{SeasonNumber: 2002, EpisodeNumber: 1, Title: "Enter: Naruto Uzumaki!"}}},
		{SeasonNumber: 2003, Episodes: []Episode{{SeasonNumber: 2003, EpisodeNumber: 1}}},
	}); err != nil {
		t.Fatal(err)
	}

	// A later refresh sees only the real season.
	real := []Season{{SeasonNumber: 1, Episodes: []Episode{{SeasonNumber: 1, EpisodeNumber: 1, Title: "Enter: Naruto Uzumaki!"}}}}
	if err := repo.InsertSeasons(ctx, sr.ID, real); err != nil {
		t.Fatal(err)
	}
	removed, err := repo.PruneSeasonsNotIn(ctx, sr.ID, []int{1})
	if err != nil {
		t.Fatal(err)
	}
	if removed != 2 {
		t.Errorf("removed %d seasons, want the 2 year-numbered ones", removed)
	}

	seasons, err := repo.SeasonsFor(ctx, sr.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(seasons) != 1 || seasons[0].SeasonNumber != 1 {
		t.Errorf("seasons = %+v, want just season 1", seasons)
	}
}

// A season holding a FILE must survive whatever the metadata says. Losing track of
// something the user actually has is far worse than an extra row in the season list.
func TestPruneKeepsSeasonsWithFiles(t *testing.T) {
	repo, ctx := testRepo(t)
	sr, _ := repo.Create(ctx, Series{TMDBID: 2, Title: "Show", Monitored: true})
	if err := repo.InsertSeasons(ctx, sr.ID, []Season{
		{SeasonNumber: 5, Episodes: []Episode{
			{SeasonNumber: 5, EpisodeNumber: 1},
			{SeasonNumber: 5, EpisodeNumber: 2},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	// One episode has a file; the metadata no longer lists season 5 at all.
	if _, err := repo.db.ExecContext(ctx,
		`UPDATE episodes SET has_file = 1, file_path = '/keep.mkv' WHERE series_id = ? AND episode_number = 1`, sr.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.PruneSeasonsNotIn(ctx, sr.ID, []int{1}); err != nil {
		t.Fatal(err)
	}

	seasons, _ := repo.SeasonsFor(ctx, sr.ID)
	if len(seasons) != 1 || seasons[0].SeasonNumber != 5 {
		t.Fatalf("the season holding a file must survive, got %+v", seasons)
	}
	if len(seasons[0].Episodes) != 1 || seasons[0].Episodes[0].FilePath != "/keep.mkv" {
		t.Errorf("the episode with a file must survive; the empty one may go: %+v", seasons[0].Episodes)
	}
}

// An empty keep list means we have no listing to trust, so nothing is pruned.
func TestPruneDoesNothingWithoutAListing(t *testing.T) {
	repo, ctx := testRepo(t)
	sr, _ := repo.Create(ctx, Series{TMDBID: 3, Title: "Show", Monitored: true})
	_ = repo.InsertSeasons(ctx, sr.ID, []Season{{SeasonNumber: 1, Episodes: []Episode{{SeasonNumber: 1, EpisodeNumber: 1}}}})

	if n, err := repo.PruneSeasonsNotIn(ctx, sr.ID, nil); err != nil || n != 0 {
		t.Errorf("prune with no listing = %d (err %v), want 0 — a failed fetch must not empty a library", n, err)
	}
	if seasons, _ := repo.SeasonsFor(ctx, sr.ID); len(seasons) != 1 {
		t.Error("seasons were removed with nothing to justify it")
	}
}
