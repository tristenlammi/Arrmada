package metadata

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// A person's credits go through toItem: adult rows, adult titles and posterless rows are
// dropped, a title they both acted in and directed is one card, directing counts and
// producing doesn't, and the most popular comes first.
func TestPersonMapsCreditsThroughToItem(t *testing.T) {
	tm, _ := fakeTMDB(t, `{"id":5,"name":"Ada Mariner","profile_path":"/ada.jpg","known_for_department":"Acting",
		"combined_credits":{
			"cast":[
				{"id":1,"media_type":"movie","title":"Harbour Lights","poster_path":"/a.jpg","vote_count":500,"popularity":20},
				{"id":2,"media_type":"movie","title":"Quiet Evening","poster_path":"/b.jpg","vote_count":500,"adult":true},
				{"id":3,"media_type":"movie","title":"Brazzers Night Shift","poster_path":"/c.jpg","vote_count":500},
				{"id":4,"media_type":"movie","title":"No Poster","vote_count":500},
				{"id":6,"media_type":"tv","name":"Saltwind","poster_path":"/s.jpg","vote_count":90,"popularity":55},
				{"id":7,"media_type":"tv","name":"Late Talk","poster_path":"/t.jpg","vote_count":900,"genre_ids":[10767]},
				{"id":8,"media_type":"movie","title":"Barely Seen","poster_path":"/x.jpg","vote_count":2}
			],
			"crew":[
				{"id":1,"media_type":"movie","title":"Harbour Lights","poster_path":"/a.jpg","vote_count":500,"popularity":20,"job":"Director"},
				{"id":9,"media_type":"movie","title":"Her Own Film","poster_path":"/h.jpg","vote_count":40,"popularity":5,"job":"Director"},
				{"id":10,"media_type":"movie","title":"Money Picture","poster_path":"/m.jpg","vote_count":400,"job":"Producer"}
			]}}`)
	p, err := tm.Person(context.Background(), 5)
	if err != nil {
		t.Fatal(err)
	}
	var got []int
	for _, c := range p.Credits {
		got = append(got, c.TMDBID)
	}
	want := []int{6, 1, 9}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Fatalf("credits = %v, want %v", got, want)
	}
	if p.Name != "Ada Mariner" || p.ProfileURL == "" || p.KnownForDepartment != "Acting" {
		t.Errorf("person = %+v", p)
	}
}

// A person TMDB flags adult is not found, and so is one TMDB doesn't know.
func TestPersonAdultIsNotFound(t *testing.T) {
	tm, _ := fakeTMDB(t, `{"id":5,"name":"Someone","adult":true,"profile_path":"/p.jpg","combined_credits":{"cast":[]}}`)
	if _, err := tm.Person(context.Background(), 5); !errors.Is(err, ErrNotFound) {
		t.Errorf("adult person: err = %v, want ErrNotFound", err)
	}
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()
	tm2 := NewTMDB("k")
	tm2.base = srv.URL
	if _, err := tm2.Person(context.Background(), 6); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown person: err = %v, want ErrNotFound", err)
	}
}

// Search's people: adult performers, people without a photo and near-unknowns are left
// out, and an adult title is never named among what someone is known for.
func TestSearchReturnsPeopleFiltered(t *testing.T) {
	tm, _ := fakeTMDB(t, `{"page":1,"total_pages":1,"results":[
		{"id":1,"media_type":"person","name":"Florence Pugh","profile_path":"/f.jpg","popularity":40,"known_for_department":"Acting",
			"known_for":[{"id":11,"media_type":"movie","title":"Midsommar","poster_path":"/m.jpg"},{"id":12,"media_type":"movie","title":"Vixen Nights","poster_path":"/v.jpg"},{"id":13,"media_type":"movie","title":"Quiet","poster_path":"/q.jpg","adult":true}]},
		{"id":2,"media_type":"person","name":"Adult Performer","profile_path":"/a.jpg","popularity":40,"adult":true},
		{"id":3,"media_type":"person","name":"No Photo","popularity":40},
		{"id":4,"media_type":"person","name":"Nobody Much","profile_path":"/n.jpg","popularity":0.2},
		{"id":20,"media_type":"movie","title":"Little Women","poster_path":"/l.jpg"}]}`)
	sr, err := tm.SearchPage(context.Background(), "florence", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(sr.People) != 1 || sr.People[0].ID != 1 || sr.People[0].ProfileURL == "" {
		t.Fatalf("people = %+v, want only Florence Pugh", sr.People)
	}
	if kf := sr.People[0].KnownFor; len(kf) != 1 || kf[0] != "Midsommar" {
		t.Errorf("known for = %v, want only Midsommar", kf)
	}
	if len(sr.Items) != 1 || sr.Items[0].TMDBID != 20 {
		t.Errorf("items = %+v", sr.Items)
	}
}

// Cast and crew carry their person ids, so a tile can open their page.
func TestCastIDsParsed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/movie/603":
			_, _ = w.Write([]byte(`{"id":603,"title":"The Matrix","release_date":"1999-03-31",
				"credits":{"cast":[{"id":6384,"name":"Keanu Reeves","character":"Neo"}],
				"crew":[{"id":9339,"name":"Lana Wachowski","job":"Director"}]}}`))
		case "/tv/1399":
			_, _ = w.Write([]byte(`{"id":1399,"name":"Saltwind","created_by":[{"id":9813,"name":"D. B. Weiss"}],
				"credits":{"cast":[{"id":22970,"name":"Peter Dinklage"}]}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	tm := NewTMDB("k")
	tm.base = srv.URL
	m, err := tm.MediaDetails(context.Background(), "movie", 603)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Cast) != 1 || m.Cast[0].ID != 6384 || len(m.Crew) != 1 || m.Crew[0].ID != 9339 {
		t.Errorf("movie cast %+v crew %+v", m.Cast, m.Crew)
	}
	s, err := tm.MediaDetails(context.Background(), "series", 1399)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Cast) != 1 || s.Cast[0].ID != 22970 || len(s.Crew) != 1 || s.Crew[0].ID != 9813 || s.Crew[0].Job != "Creator" {
		t.Errorf("series cast %+v crew %+v", s.Cast, s.Crew)
	}
	// Stored cast JSON from before ids were kept still reads.
	d, err := parseTMDBMovie([]byte(`{"id":1,"title":"Old","credits":{"cast":[{"name":"No Id","character":"Someone"}]}}`))
	if err != nil || len(d.Cast) != 1 || d.Cast[0].ID != 0 || d.Cast[0].Name != "No Id" {
		t.Errorf("old cast = %+v, %v", d, err)
	}
}
