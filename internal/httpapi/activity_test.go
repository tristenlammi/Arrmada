package httpapi

import (
	"testing"

	"github.com/tristenlammi/arrmada/internal/download"
	"github.com/tristenlammi/arrmada/internal/parser"
)

// ACQ-17: each queued torrent is labelled by its download category. A music torrent is
// Music, takes its artist's profile, and is never matched against movies — "Inception"
// the soundtrack must not count as the film.
func TestLabelQueueItemByCategory(t *testing.T) {
	movieCalls := 0
	m := queueMatchers{
		movie: func(string, int) (string, bool) {
			movieCalls++
			return "custom:1", true
		},
		series: func(string) (string, bool) { return "custom:2", true },
		album:  func(string) (string, bool) { return "custom:3", true },
	}
	cases := []struct {
		category, name string
		wantType       string
		wantRef        string
		wantMovieCall  bool
	}{
		{download.CategoryMusic, "Hans Zimmer - Inception (2010) [FLAC]", "music", "custom:3", false},
		{download.CategorySeries, "Show.S01E01.1080p.WEB-DL.x264-GRP", "series", "custom:2", false},
		{download.CategoryBooks, "Author - Book (2020) EPUB", "book", "", false},
		{download.CategoryMovies, "Inception.2010.1080p.BluRay.x264-GRP", "movie", "custom:1", true},
		{"movies-custom", "Inception.2010.1080p.BluRay.x264-GRP", "movie", "custom:1", true},
	}
	for _, tc := range cases {
		movieCalls = 0
		it := download.Item{Name: tc.name, Category: tc.category}
		typ, ref := labelQueueItem(it, parser.Parse(tc.name), m)
		if typ != tc.wantType || ref != tc.wantRef {
			t.Errorf("%s %q: got (%s, %q), want (%s, %q)", tc.category, tc.name, typ, ref, tc.wantType, tc.wantRef)
		}
		if (movieCalls > 0) != tc.wantMovieCall {
			t.Errorf("%s: movie matcher called %d times", tc.category, movieCalls)
		}
	}
	// No album matches (or music is off): Music, with no profile.
	none := queueMatchers{album: func(string) (string, bool) { return "", false }}
	if typ, ref := labelQueueItem(download.Item{Name: "x", Category: download.CategoryMusic}, parser.Release{}, none); typ != "music" || ref != "" {
		t.Errorf("unmatched music = (%s, %q)", typ, ref)
	}
	if typ, _ := labelQueueItem(download.Item{Name: "x", Category: download.CategoryMusic}, parser.Release{}, queueMatchers{}); typ != "music" {
		t.Errorf("music with no matcher = %s", typ)
	}
}
