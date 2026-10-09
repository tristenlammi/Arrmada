package automation

import (
	"reflect"
	"testing"

	"github.com/tristenlammi/arrmada/internal/series"
)

// Absolute queries go out clean and padded the way fansub releases number episodes, and
// again under the romaji name.
func TestAbsoluteQueriesCleanAndPad(t *testing.T) {
	cases := []struct {
		s    series.Series
		abs  int
		want []string
	}{
		{series.Series{Title: "Dr. Stone"}, 13, []string{"Dr Stone 13"}},
		{series.Series{Title: "Dr. Stone"}, 5, []string{"Dr Stone 05"}},
		{series.Series{Title: "Hunter x Hunter"}, 137, []string{"Hunter x Hunter 137"}},
		{series.Series{Title: "Frieren: Beyond Journey's End", Aliases: []series.Alias{
			{Title: "BLEACH arc", TMDBSeason: 3},                   // the owner's: not the romaji name
			{Title: "Sousou no Frieren", Source: series.AliasTMDB}, // the first automatic alias
			{Title: "Frieren der Zauberer", Source: series.AliasTMDB},
		}}, 13, []string{"Frieren Beyond Journeys End 13", "Sousou no Frieren 13"}},
		// No automatic alias: a Latin original title stands in; a kana one doesn't.
		{series.Series{Title: "Attack on Titan", Extra: &series.SeriesExtra{OriginalTitle: "Shingeki no Kyojin"}}, 1,
			[]string{"Attack on Titan 01", "Shingeki no Kyojin 01"}},
		{series.Series{Title: "Frieren", Extra: &series.SeriesExtra{OriginalTitle: "葬送のフリーレン"}}, 1, []string{"Frieren 01"}},
		{series.Series{Title: "Frieren"}, 0, nil},
	}
	for _, c := range cases {
		if got := absoluteQueries(c.s, c.abs); !reflect.DeepEqual(got, c.want) {
			t.Errorf("absoluteQueries(%q, %d) = %q, want %q", c.s.Title, c.abs, got, c.want)
		}
	}
}

func TestSeriesScopeQueriesAnimeEpisode(t *testing.T) {
	anime := series.Series{Title: "Frieren: Beyond Journey's End", SeriesType: series.SeriesTypeAnime,
		Aliases: []series.Alias{{Title: "Sousou no Frieren", Source: series.AliasTMDB}}}
	got := seriesScopeQueries(anime, 1, 13, 13, []string{"Frieren Arc 13"})
	var texts []string
	for _, q := range got {
		texts = append(texts, q.Text)
		if q.Season != 0 || q.Episode != 0 {
			t.Errorf("anime query %q carries season/episode parameters", q.Text)
		}
	}
	want := []string{"Frieren Beyond Journeys End", "Frieren Arc 13", "Frieren Beyond Journeys End 13", "Sousou no Frieren 13", "Sousou no Frieren"}
	if !reflect.DeepEqual(texts, want) {
		t.Errorf("anime episode queries = %q, want %q", texts, want)
	}

	// A standard show is unchanged: one query, season and episode as parameters, no
	// absolute number even when one is passed.
	std := seriesScopeQueries(series.Series{Title: "Teen Titans Go!"}, 7, 3, 120, nil)
	if len(std) != 1 || std[0].Text != "Teen Titans Go" || std[0].Season != 7 || std[0].Episode != 3 {
		t.Errorf("standard show queries = %+v", std)
	}
	// Whole-season anime search: no absolute query.
	if q := seriesScopeQueries(anime, 1, 0, 0, nil); len(q) != 2 {
		t.Errorf("anime season queries = %+v, want the title and the alias", q)
	}
}

// Searches use every alias the owner added but only the first two from TMDB.
func TestSearchAliasesCapsTMDB(t *testing.T) {
	s := series.Series{Aliases: []series.Alias{
		{Title: "a", Source: series.AliasTMDB}, {Title: "b", Source: series.AliasTMDB}, {Title: "c", Source: series.AliasTMDB},
		{Title: "mine"}, {Title: "mine too", Source: series.AliasUser},
	}}
	var got []string
	for _, a := range searchAliases(s) {
		got = append(got, a.Title)
	}
	if want := []string{"a", "b", "mine", "mine too"}; !reflect.DeepEqual(got, want) {
		t.Errorf("searchAliases = %q, want %q", got, want)
	}
}
