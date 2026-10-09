package series

import (
	"testing"

	"github.com/tristenlammi/arrmada/internal/metadata"
)

// The show row used to be written once, on Add: a show that ended never read as ended
// (so complete packs stayed off and the scheduled refresh kept visiting it), and a
// revived one never came back. Refresh now brings the show itself up to date.
func TestRefreshUpdatesStatusTitlePosterNetwork(t *testing.T) {
	d := standardDetails()
	d.Seasons, d.Status, d.Network, d.PosterURL, d.Overview, d.Year = listing(2), "Returning Series", "HBO", "/old.jpg", "old", 2019
	d.Genres = []string{"Drama"}
	svc, fm, ctx := refreshTestService(t, d)
	sr, err := svc.Add(ctx, d.TMDBID, "", true)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := svc.Get(ctx, sr.ID); got.LastRefreshedAt == "" {
		t.Error("Add pulls metadata, so it should stamp last_refreshed_at")
	}

	fm.d.Status, fm.d.Network, fm.d.PosterURL, fm.d.Overview, fm.d.Year = "Ended", "Max", "/new.jpg", "new", 2020
	fm.d.Genres, fm.d.BackdropURL = []string{"Drama", "Crime"}, "/bd.jpg"
	got, _, err := svc.Refresh(ctx, sr.ID, RefreshOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "Ended" || got.Network != "Max" || got.PosterURL != "/new.jpg" || got.Overview != "new" || got.Year != 2020 {
		t.Errorf("show row = %+v, want TMDB's fresh status, network, poster, overview and year", got)
	}
	if got.Extra == nil || len(got.Extra.Genres) != 2 || got.Extra.BackdropURL != "/bd.jpg" {
		t.Errorf("extra = %+v, want the fresh genres and backdrop", got.Extra)
	}
	if got.LastRefreshedAt == "" {
		t.Error("a successful refresh must stamp last_refreshed_at")
	}
	if !hasEvent(t, svc, sr.ID, "status", "Status: Returning Series → Ended") {
		t.Error("History should say the status changed")
	}
}

// A renamed show keeps matching releases under its old name: the old title becomes a
// title-only alias, and History says what changed.
func TestRefreshKeepsOldTitleAsAlias(t *testing.T) {
	d := standardDetails()
	d.Seasons = listing(1)
	svc, fm, ctx := refreshTestService(t, d)
	sr, err := svc.Add(ctx, d.TMDBID, "", true)
	if err != nil {
		t.Fatal(err)
	}
	fm.d.Title = "Standard Show Reborn"
	got, _, err := svc.Refresh(ctx, sr.ID, RefreshOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != "Standard Show Reborn" {
		t.Fatalf("title = %q, want TMDB's new title", got.Title)
	}
	if len(got.Aliases) != 1 || got.Aliases[0].Title != "Standard Show" || got.Aliases[0].TMDBSeason != 0 {
		t.Fatalf("aliases = %+v, want the old title kept as a title-only alias", got.Aliases)
	}
	if a, ok := svc.AliasFor(ctx, sr.ID, "Standard.Show.S01E01.1080p.WEB-DL-GRP"); !ok || a.Title != "Standard Show" {
		t.Error("a release under the old title should still match the show")
	}
	if !hasEvent(t, svc, sr.ID, "title", "Title changed: Standard Show → Standard Show Reborn") {
		t.Error("History should say the title changed")
	}

	// An unchanged refresh adds nothing more.
	if _, _, err := svc.Refresh(ctx, sr.ID, RefreshOptions{}); err != nil {
		t.Fatal(err)
	}
	if got, _ := svc.Get(ctx, sr.ID); len(got.Aliases) != 1 {
		t.Errorf("aliases = %+v after an unchanged refresh", got.Aliases)
	}

	// An alias the owner pinned to a season isn't reset when the show is renamed away
	// from that very name.
	if _, err := svc.AddAlias(ctx, sr.ID, "Standard Show Final", 1); err != nil {
		t.Fatal(err)
	}
	fm.d.Title = "Standard Show Final"
	if _, _, err := svc.Refresh(ctx, sr.ID, RefreshOptions{}); err != nil {
		t.Fatal(err)
	}
	fm.d.Title = "Standard Show Ultimate"
	if _, _, err := svc.Refresh(ctx, sr.ID, RefreshOptions{}); err != nil {
		t.Fatal(err)
	}
	got, _ = svc.Get(ctx, sr.ID)
	for _, a := range got.Aliases {
		if a.Title == "Standard Show Final" && a.TMDBSeason != 1 {
			t.Errorf("the owner's alias lost its season: %+v", a)
		}
	}
}

// A provider answering with half a record must not blank out what's stored.
func TestRefreshDoesNotBlankOnEmptyFields(t *testing.T) {
	d := standardDetails()
	d.Seasons, d.Status, d.Network, d.PosterURL, d.Overview, d.Year = listing(1), "Ended", "HBO", "/p.jpg", "ov", 2001
	d.Genres, d.BackdropURL, d.OriginalLang = []string{"Drama"}, "/bd.jpg", "en"
	d.Cast = []metadata.CastMember{{Name: "A"}}
	svc, fm, ctx := refreshTestService(t, d)
	sr, err := svc.Add(ctx, d.TMDBID, "", true)
	if err != nil {
		t.Fatal(err)
	}
	fm.d.Title, fm.d.Status, fm.d.Network, fm.d.PosterURL, fm.d.Overview, fm.d.Year = "", "", "", "", "", 0
	fm.d.Genres, fm.d.BackdropURL, fm.d.Cast, fm.d.OriginalLang = nil, "", nil, ""
	got, _, err := svc.Refresh(ctx, sr.ID, RefreshOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != "Standard Show" || got.Status != "Ended" || got.Network != "HBO" || got.PosterURL != "/p.jpg" || got.Overview != "ov" || got.Year != 2001 {
		t.Errorf("show row blanked by an empty answer: %+v", got)
	}
	if got.Extra == nil || len(got.Extra.Genres) != 1 || got.Extra.BackdropURL != "/bd.jpg" || len(got.Extra.Cast) != 1 || got.Extra.OriginalLanguage != "en" {
		t.Errorf("extra blanked by an empty answer: %+v", got.Extra)
	}
	if len(got.Aliases) != 0 {
		t.Errorf("an empty title is not a rename: aliases = %+v", got.Aliases)
	}
}

func hasEvent(t *testing.T, svc *Service, id int64, event, detail string) bool {
	t.Helper()
	evs, err := svc.Events(t.Context(), id, 50)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range evs {
		if e.Event == event && e.Detail == detail {
			return true
		}
	}
	t.Logf("events: %+v", evs)
	return false
}
