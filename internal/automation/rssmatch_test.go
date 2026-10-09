package automation

import (
	"testing"

	"github.com/tristenlammi/arrmada/internal/indexer"
	"github.com/tristenlammi/arrmada/internal/series"
)

// RSS sync matches the feed against the List snapshot and only shows with a match are
// loaded in full: a show with nothing in the feed — or one that isn't monitored — never
// appears in the result, so it never costs a Get().
func TestRSSSyncSkipsGetForNonMatching(t *testing.T) {
	all := []series.Series{
		{ID: 1, Title: "Show", Monitored: true},
		{ID: 2, Title: "Quiet Show", Monitored: true},
		{ID: 3, Title: "Paused Show", Monitored: false},
		{ID: 4, Title: "Frieren: Beyond Journey's End", Monitored: true, SeriesType: series.SeriesTypeAnime,
			Aliases: []series.Alias{{Title: "Sousou no Frieren", Source: series.AliasTMDB}}},
		{ID: 5, Title: "Doctor Who", Year: 1963, Monitored: true},
	}
	feed := []indexer.Release{
		{Title: "Show.S03E04.1080p.WEB-DL-GRP"},
		{Title: "Paused.Show.S01E01.1080p.WEB-DL-GRP"},
		{Title: "[SubsPlease] Sousou no Frieren - 29 (1080p)"},
		{Title: "Doctor.Who.2005.S01E01.1080p.BluRay-GRP"},
		{Title: "Something.Else.S01E01.1080p.WEB-DL-GRP"},
	}
	got := rssMatches(all, feed)
	if len(got[1]) != 1 || got[1][0].Title != feed[0].Title {
		t.Errorf("Show = %+v, want its own episode", got[1])
	}
	if len(got[4]) != 1 {
		t.Errorf("Frieren = %+v, want the romaji-named upload", got[4])
	}
	for _, id := range []int64{2, 3, 5} {
		if _, ok := got[id]; ok {
			t.Errorf("series %d has matches %+v, want none (nothing in the feed, paused, or another show's year)", id, got[id])
		}
	}
}
