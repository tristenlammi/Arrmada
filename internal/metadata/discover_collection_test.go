package metadata

import (
	"context"
	"testing"
)

// A collection's adult members are left out, and its overview and artwork come through.
func TestCollectionDropsAdultMembers(t *testing.T) {
	tm, _ := fakeTMDB(t, `{"id":8091,"name":"Alien Collection","overview":"In space…","poster_path":"/p.jpg","backdrop_path":"/b.jpg","parts":[
		{"id":348,"title":"Alien","release_date":"1979-05-25","poster_path":"/a.jpg"},
		{"id":9,"title":"Quiet Evening","release_date":"1990-01-01","poster_path":"/q.jpg","adult":true},
		{"id":10,"title":"Vixen Encounters","release_date":"1991-01-01","poster_path":"/v.jpg"},
		{"id":679,"title":"Aliens","release_date":"1986-07-18","poster_path":"/b.jpg"}]}`)
	c, err := tm.GetCollection(context.Background(), 8091)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Members) != 2 || c.Members[0].TMDBID != 348 || c.Members[1].TMDBID != 679 || c.Members[1].ReleaseDate != "1986-07-18" {
		t.Errorf("members = %+v", c.Members)
	}
	if c.Overview != "In space…" || c.PosterURL == "" || c.BackdropURL == "" {
		t.Errorf("collection = %+v", c)
	}
}

// A movie's detail names the collection it belongs to.
func TestMediaDetailCollectionParsed(t *testing.T) {
	tm, _ := fakeTMDB(t, `{"id":348,"title":"Alien","release_date":"1979-05-25","belongs_to_collection":{"id":8091,"name":"Alien Collection"}}`)
	d, err := tm.MediaDetails(context.Background(), "movie", 348)
	if err != nil {
		t.Fatal(err)
	}
	if d.Collection == nil || d.Collection.ID != 8091 || d.Collection.Name != "Alien Collection" {
		t.Errorf("collection = %+v", d.Collection)
	}
	tm2, _ := fakeTMDB(t, `{"id":1,"title":"Standalone","release_date":"2000-01-01"}`)
	if d, err := tm2.MediaDetails(context.Background(), "movie", 1); err != nil || d.Collection != nil {
		t.Errorf("standalone: %+v, %v", d.Collection, err)
	}
}
