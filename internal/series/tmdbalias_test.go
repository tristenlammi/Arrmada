package series

import (
	"encoding/json"
	"testing"

	"github.com/tristenlammi/arrmada/internal/metadata"
	"github.com/tristenlammi/arrmada/internal/parser"
)

// Frieren's alternative titles as TMDB lists them (trimmed): the romaji name the fansub
// groups use, translations in other scripts, and an English variant.
const frierenAltTitles = `[
	{"title":"Frieren at the Funeral","country":"US","type":""},
	{"title":"葬送的芙莉莲","country":"CN","type":""},
	{"title":"Sousou no Frieren","country":"JP","type":"Romaji"},
	{"title":"Frieren: Beyond Journey's End","country":"US","type":""},
	{"title":"Frieren – Nach dem Ende der Reise","country":"DE","type":""},
	{"title":"Frieren der Zauberer","country":"JP","type":""}
]`

func frierenDetails(t *testing.T) metadata.SeriesDetails {
	t.Helper()
	d := animeDetails()
	d.Title = "Frieren: Beyond Journey's End"
	d.OriginalName = "葬送のフリーレン"
	d.Seasons = listing(28)
	if err := json.Unmarshal([]byte(frierenAltTitles), &d.AltTitles); err != nil {
		t.Fatal(err)
	}
	return d
}

func aliasTitles(as []Alias) []string {
	out := make([]string, 0, len(as))
	for _, a := range as {
		out = append(out, a.Title)
	}
	return out
}

func TestSyncTMDBAliasesPicksRomaji(t *testing.T) {
	d := frierenDetails(t)
	svc, _, ctx := refreshTestService(t, d)
	sr, err := svc.Add(ctx, d.TMDBID, "", true)
	if err != nil {
		t.Fatal(err)
	}
	got := svc.Aliases(ctx, sr.ID)
	want := []string{"Sousou no Frieren", "Frieren der Zauberer", "Frieren at the Funeral"}
	if titles := aliasTitles(got); len(titles) != len(want) {
		t.Fatalf("aliases = %v, want %v (romaji first, Latin only, the display title skipped)", titles, want)
	} else {
		for i := range want {
			if titles[i] != want[i] {
				t.Fatalf("aliases = %v, want %v", titles, want)
			}
		}
	}
	for _, a := range got {
		if a.Source != AliasTMDB || a.TMDBSeason != 0 {
			t.Errorf("alias %+v should be a title-only TMDB alias", a)
		}
	}
	// And the fansub release now matches without any setup.
	full, _ := svc.Get(ctx, sr.ID)
	if f := FitRelease(parser.Parse("[SubsPlease] Sousou no Frieren - 13 (1080p) [ABCD1234].mkv"), full); !f.OK {
		t.Errorf("the romaji release should match through the TMDB alias: %+v", f)
	}
}

func TestSyncTMDBAliasesCollisionGuard(t *testing.T) {
	d := frierenDetails(t)
	svc, _, ctx := refreshTestService(t, d)
	// Another show in the library already goes by one of the alternative titles.
	if _, err := svc.repo.db.Exec(`INSERT INTO series (tmdb_id, title, monitored) VALUES (777, 'Frieren der Zauberer', 1)`); err != nil {
		t.Fatal(err)
	}
	sr, err := svc.Add(ctx, d.TMDBID, "", true)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range svc.Aliases(ctx, sr.ID) {
		if a.Title == "Frieren der Zauberer" {
			t.Fatal("an alias that is another library show's title must not be added")
		}
	}
}

func TestDisabledAliasNotReAdded(t *testing.T) {
	d := frierenDetails(t)
	svc, _, ctx := refreshTestService(t, d)
	sr, err := svc.Add(ctx, d.TMDBID, "", true)
	if err != nil {
		t.Fatal(err)
	}
	var romaji Alias
	for _, a := range svc.Aliases(ctx, sr.ID) {
		if a.Title == "Sousou no Frieren" {
			romaji = a
		}
	}
	if err := svc.DeleteAlias(ctx, sr.ID, romaji.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.Refresh(ctx, sr.ID, RefreshOptions{}); err != nil {
		t.Fatal(err)
	}
	for _, a := range svc.Aliases(ctx, sr.ID) {
		if a.Title == "Sousou no Frieren" {
			t.Fatal("a TMDB alias the owner removed came back after a refresh")
		}
	}
	// It is kept, switched off — that's what stops the refresh re-adding it.
	found := false
	for _, a := range svc.repo.AllAliases(ctx, sr.ID) {
		if a.Title == "Sousou no Frieren" && a.Disabled {
			found = true
		}
	}
	if !found {
		t.Error("a removed TMDB alias should be kept as disabled")
	}
	// The owner typing it again makes it theirs.
	if _, err := svc.AddAlias(ctx, sr.ID, "Sousou no Frieren", 0); err != nil {
		t.Fatal(err)
	}
	for _, a := range svc.Aliases(ctx, sr.ID) {
		if a.Title == "Sousou no Frieren" && a.Source != AliasUser {
			t.Errorf("re-added alias = %+v, want the owner's", a)
		}
	}
}

func TestUserAliasUntouchedBySync(t *testing.T) {
	d := frierenDetails(t)
	d.AltTitles = nil
	svc, fm, ctx := refreshTestService(t, d)
	sr, err := svc.Add(ctx, d.TMDBID, "", true)
	if err != nil {
		t.Fatal(err)
	}
	// The owner already has the romaji title, pinned to season 1.
	if _, err := svc.AddAlias(ctx, sr.ID, "Sousou no Frieren", 1); err != nil {
		t.Fatal(err)
	}
	fm.d = frierenDetails(t)
	if _, _, err := svc.Refresh(ctx, sr.ID, RefreshOptions{}); err != nil {
		t.Fatal(err)
	}
	for _, a := range svc.Aliases(ctx, sr.ID) {
		if a.Title == "Sousou no Frieren" && (a.Source != AliasUser || a.TMDBSeason != 1) {
			t.Errorf("the owner's alias was changed by the sync: %+v", a)
		}
	}
	// User aliases are deleted outright.
	for _, a := range svc.Aliases(ctx, sr.ID) {
		if a.Source == AliasUser {
			if err := svc.DeleteAlias(ctx, sr.ID, a.ID); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, a := range svc.repo.AllAliases(ctx, sr.ID) {
		if a.Source == AliasUser {
			t.Errorf("a deleted user alias is still stored: %+v", a)
		}
	}
}

// Automatic aliases match the way the show's own title does — exactly — while the owner's
// get the whole-word prefix match.
func TestTMDBAliasExactMatchOnly(t *testing.T) {
	s := Series{Title: "Frieren: Beyond Journey's End", Year: 2023, SeriesType: SeriesTypeAnime,
		Aliases: []Alias{{Title: "Sousou no Frieren", Source: AliasTMDB}}}
	if f := FitRelease(parser.Parse("[SubsPlease] Sousou no Frieren - 13 (1080p)"), s); !f.OK || !f.Alias {
		t.Errorf("exact TMDB alias should match: %+v", f)
	}
	if f := FitRelease(parser.Parse("Sousou no Frieren Mini Anime S01E01 1080p"), s); f.OK {
		t.Errorf("a TMDB alias must not match as a prefix: %+v", f)
	}
	// The year check applies to it, as to the show's own title.
	if f := FitRelease(parser.Parse("Sousou.no.Frieren.2011.S01E01.1080p"), s); f.OK {
		t.Errorf("a TMDB alias match with another show's year must not count: %+v", f)
	}
	s.Aliases[0].Source = AliasUser
	if f := FitRelease(parser.Parse("Sousou no Frieren Mini Anime S01E01 1080p"), s); !f.OK {
		t.Errorf("the owner's alias keeps the prefix match: %+v", f)
	}
}

func TestOriginalTitleNonLatinIgnored(t *testing.T) {
	s := Series{Title: "Frieren: Beyond Journey's End", SeriesType: SeriesTypeAnime,
		Extra: &SeriesExtra{OriginalTitle: "葬送のフリーレン"}}
	if got := s.ownTitles(); len(got) != 1 {
		t.Errorf("own titles = %v, a kana original title is no release title", got)
	}
	s.Extra.OriginalTitle = "Shingeki no Kyojin"
	if f := FitRelease(parser.Parse("Shingeki no Kyojin S01E01 1080p"), s); !f.OK {
		t.Errorf("a Latin original title still matches anime releases: %+v", f)
	}
}
