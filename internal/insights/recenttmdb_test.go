package insights

import (
	"reflect"
	"testing"

	"github.com/tristenlammi/arrmada/internal/plex"
)

// Plex's recently added rows become TMDB titles: an episode and a season count as their
// show (once, however many arrived), and anything the index can't name is dropped.
func TestRecentlyAddedMapsEpisodesToShows(t *testing.T) {
	index := map[string]struct {
		media string
		tmdb  int
	}{
		"show-1":  {"series", 1399},
		"movie-1": {"movie", 603},
		"show-2":  {"series", 66732},
	}
	lookup := func(key string) (string, int, bool) {
		it, ok := index[key]
		return it.media, it.tmdb, ok
	}
	items := []plex.RecentItem{
		{RatingKey: "ep-1", Type: "episode", GrandparentRatingKey: "show-1", ParentRatingKey: "season-1", AddedAt: 900},
		{RatingKey: "ep-2", Type: "episode", GrandparentRatingKey: "show-1", ParentRatingKey: "season-1", AddedAt: 890},
		{RatingKey: "movie-1", Type: "movie", AddedAt: 880},
		{RatingKey: "season-9", Type: "season", ParentRatingKey: "show-2", AddedAt: 870},
		{RatingKey: "home-video", Type: "movie", AddedAt: 860},                       // not in the index
		{RatingKey: "ep-3", Type: "episode", GrandparentRatingKey: "", AddedAt: 850}, // no show key
		{RatingKey: "track-1", Type: "track", AddedAt: 840},
	}
	got := mapRecentToTMDB(items, lookup)
	want := []RecentTMDB{
		{Media: "series", TMDB: 1399, AddedAt: 900},
		{Media: "movie", TMDB: 603, AddedAt: 880},
		{Media: "series", TMDB: 66732, AddedAt: 870},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("mapped = %+v, want %+v", got, want)
	}
}
