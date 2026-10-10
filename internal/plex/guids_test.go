package plex

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
)

func TestParseSectionGuids(t *testing.T) {
	cases := []struct {
		name    string
		primary string
		guids   []string
		tmdb    int
		tvdb    int
		imdb    string
	}{
		{"new agent movie", "plex://movie/5d7768", []string{"imdb://tt0133093", "tmdb://603", "tvdb://169"}, 603, 169, "tt0133093"},
		{"legacy themoviedb with lang", "com.plexapp.agents.themoviedb://603?lang=en", nil, 603, 0, ""},
		{"legacy imdb movie", "com.plexapp.agents.imdb://tt0133093?lang=en", nil, 0, 0, "tt0133093"},
		{"tvdb-only show", "com.plexapp.agents.thetvdb://81189?lang=en", nil, 0, 81189, ""},
		{"legacy tvdb episode-style guid", "com.plexapp.agents.thetvdb://81189/1/2?lang=en", nil, 0, 81189, ""},
		{"plex primary ignored", "plex://show/5d9c08", nil, 0, 0, ""},
		{"local media", "local://12345", nil, 0, 0, ""},
		{"list beats the primary", "com.plexapp.agents.themoviedb://1?lang=en", []string{"tmdb://603"}, 603, 0, ""},
		{"junk ignored", "", []string{"tmdb://abc", "imdb://nm123", "weird"}, 0, 0, ""},
	}
	for _, c := range cases {
		tm, tv, im := parseGuids(c.primary, c.guids)
		if tm != c.tmdb || tv != c.tvdb || im != c.imdb {
			t.Errorf("%s: got tmdb %d tvdb %d imdb %q, want %d %d %q", c.name, tm, tv, im, c.tmdb, c.tvdb, c.imdb)
		}
	}
}

// SectionItems pages through a section and reads both guid spellings from the same item:
// the lower-case primary string and the capital-G list.
func TestSectionItemsPagesAndDecodes(t *testing.T) {
	const total = 1203
	var pages int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/library/sections/1/all" || r.URL.Query().Get("type") != "1" || r.URL.Query().Get("includeGuids") != "1" {
			http.NotFound(w, r)
			return
		}
		pages++
		start, _ := strconv.Atoi(r.URL.Query().Get("X-Plex-Container-Start"))
		size, _ := strconv.Atoi(r.URL.Query().Get("X-Plex-Container-Size"))
		fmt.Fprintf(w, `{"MediaContainer":{"totalSize":%d,"Metadata":[`, total)
		for i := start; i < start+size && i < total; i++ {
			if i > start {
				fmt.Fprint(w, ",")
			}
			if i == 0 {
				fmt.Fprint(w, `{"ratingKey":"1000","type":"movie","title":"The Matrix","year":1999,"addedAt":"1600000000",
					"guid":"plex://movie/5d7768","Guid":[{"id":"imdb://tt0133093"},{"id":"tmdb://603"}]}`)
				continue
			}
			fmt.Fprintf(w, `{"ratingKey":"%d","type":"movie","title":"Film %d","year":2000,"guid":"com.plexapp.agents.imdb://tt%07d?lang=en"}`, 1000+i, i, i)
		}
		fmt.Fprint(w, `]}}`)
	}))
	defer srv.Close()

	items, err := New(srv.URL, "tok").SectionItems(context.Background(), "1", TypeMovie)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != total || pages != 3 {
		t.Fatalf("%d items over %d pages, want %d over 3", len(items), pages, total)
	}
	m := items[0]
	if m.RatingKey != "1000" || m.TMDB != 603 || m.IMDB != "tt0133093" || m.Year != 1999 || m.AddedAt != 1600000000 || m.SectionKey != "1" {
		t.Fatalf("first item = %+v", m)
	}
	if last := items[total-1]; last.IMDB != fmt.Sprintf("tt%07d", total-1) || last.TMDB != 0 {
		t.Fatalf("last item = %+v", last)
	}
}
