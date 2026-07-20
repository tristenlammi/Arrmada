package indexer

import (
	"net/url"
	"testing"
)

// Torznab's generic "search" ignores season and episode, so a TV query has to use
// tvsearch — that's what lets an indexer be asked for a specific season instead of
// whatever single page a bare title match happens to return.
func TestSearchTypePerMediaType(t *testing.T) {
	cases := []struct{ media, want string }{
		{MediaSeries, "tvsearch"},
		{MediaMovie, "movie"},
		{MediaBook, "search"},
		{"", "search"},
	}
	for _, c := range cases {
		if got := searchType("search", SearchQuery{MediaType: c.media}); got != c.want {
			t.Errorf("searchType(search, %q) = %q, want %q", c.media, got, c.want)
		}
	}
	// An explicit endpoint (caps) is never rewritten.
	if got := searchType("caps", SearchQuery{MediaType: MediaSeries}); got != "caps" {
		t.Errorf("caps must not be rewritten, got %q", got)
	}
}

func TestBuildURLCarriesSeasonAndEpisode(t *testing.T) {
	idx := Indexer{URL: "http://prowlarr:9696/16/api", APIKey: "k"}

	raw, err := buildURL(idx, "search", SearchQuery{Text: "Teen Titans Go", MediaType: MediaSeries, Season: 7, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	q, _ := url.Parse(raw)
	got := q.Query()
	if got.Get("t") != "tvsearch" {
		t.Errorf("t = %q, want tvsearch", got.Get("t"))
	}
	if got.Get("season") != "7" {
		t.Errorf("season = %q, want 7", got.Get("season"))
	}
	if got.Get("ep") != "" {
		t.Errorf("ep should be unset when no episode is given, got %q", got.Get("ep"))
	}

	// Episode only applies alongside a season.
	raw2, _ := buildURL(idx, "search", SearchQuery{Text: "x", MediaType: MediaSeries, Season: 2, Episode: 5})
	u2, _ := url.Parse(raw2)
	if u2.Query().Get("ep") != "5" {
		t.Errorf("ep = %q, want 5", u2.Query().Get("ep"))
	}
}
