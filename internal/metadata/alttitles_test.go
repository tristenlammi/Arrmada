package metadata

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// GetSeries asks for alternative titles and origin countries in the same round trip, and
// reads TV's {"results": [...]} shape (movies use "titles").
func TestGetSeriesReadsAltTitlesAndOrigin(t *testing.T) {
	var appended string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/tv/209867" {
			appended = r.URL.Query().Get("append_to_response")
			_, _ = w.Write([]byte(`{"id":209867,"name":"Frieren: Beyond Journey's End","original_name":"葬送のフリーレン",
				"origin_country":["JP"],"first_air_date":"2023-09-29","seasons":[],
				"alternative_titles":{"results":[
					{"iso_3166_1":"JP","title":"Sousou no Frieren","type":"Romaji"},
					{"iso_3166_1":"US","title":"Frieren","type":""},
					{"iso_3166_1":"US","title":"  ","type":""}]}}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()
	tm := NewTMDB("k")
	tm.base = srv.URL

	d, err := tm.GetSeries(context.Background(), 209867)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(appended, "alternative_titles") {
		t.Errorf("append_to_response = %q, want alternative_titles", appended)
	}
	if len(d.OriginCountry) != 1 || d.OriginCountry[0] != "JP" {
		t.Errorf("OriginCountry = %v", d.OriginCountry)
	}
	if len(d.AltTitles) != 2 || d.AltTitles[0] != (AltTitle{Title: "Sousou no Frieren", Country: "JP", Type: "Romaji"}) {
		t.Errorf("AltTitles = %+v", d.AltTitles)
	}
}
