package metadata

import (
	"encoding/json"
	"reflect"
	"testing"
)

// The adult filter reads Tags; every catalogue mapping must fill them.

func TestDecodeWorkListFillsTags(t *testing.T) {
	body := []byte(`{"works":[
		{"key":"/works/OL1W","title":"Velvet Nights","cover_id":7,"authors":[{"name":"B. Writer"}],"subject":["Fiction","Erotica"," "]},
		{"key":"/works/OL2W","title":"Dune","authors":[{"name":"Frank Herbert"}]}
	]}`)
	got, err := decodeWorkList(body)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || !reflect.DeepEqual(got[0].Tags, []string{"Fiction", "Erotica"}) || got[1].Tags != nil {
		t.Errorf("tags = %q / %q", got[0].Tags, got[1].Tags)
	}
}

func TestHardcoverMappingsFillTags(t *testing.T) {
	// A book row: every Genre and Tag label, untrimmed; moods and content warnings left out.
	b := hcBook{ID: 5, Title: "Locked Door", CachedTags: json.RawMessage(`{
		"Genre":[{"tag":"Romance"},{"tag":"Contemporary"},{"tag":"Drama"},{"tag":"Erotica"}],
		"Tag":[{"tag":"BDSM"}],
		"Mood":[{"tag":"dark"}],
		"Content Warning":[{"tag":"Sexual content"}]}`)}
	r := b.result()
	if len(r.Genres) != 3 {
		t.Errorf("card genres = %q, want the first three", r.Genres)
	}
	if want := []string{"Romance", "Contemporary", "Drama", "Erotica", "BDSM"}; !reflect.DeepEqual(r.Tags, want) {
		t.Errorf("tags = %q, want %q", r.Tags, want)
	}

	// A search hit: all genres plus tags, and an odd tags shape doesn't break it.
	d := hcSearchDoc{ID: json.RawMessage(`9`), Title: "Velvet", Genres: []string{"Romance", "Drama", "Fiction", "Erotica"}, Tags: json.RawMessage(`["Steamy"]`)}
	sr, ok := d.bookResult()
	if !ok || !reflect.DeepEqual(sr.Tags, []string{"Romance", "Drama", "Fiction", "Erotica", "Steamy"}) {
		t.Errorf("search tags = %q", sr.Tags)
	}
	d.Tags = json.RawMessage(`[{"tag":"x"}]`)
	if sr, ok := d.bookResult(); !ok || len(sr.Tags) != 4 {
		t.Errorf("odd tags shape: ok=%v tags=%q", ok, sr.Tags)
	}
	hits, err := decodeHits(json.RawMessage(`{"hits":[{"document":{"id":"9","title":"Velvet","tags":[{"tag":"x"}]}}]}`))
	if err != nil || len(hits) != 1 {
		t.Errorf("a hit with object tags failed the search: %v", err)
	}
}

// Tags survive the disk cache, which stores results as JSON.
func TestBookTagsSurviveJSON(t *testing.T) {
	in := []BookResult{{Key: "hc:1", Title: "X", Tags: []string{"Erotica"}}}
	b, _ := json.Marshal(in)
	var out []BookResult
	if err := json.Unmarshal(b, &out); err != nil || !reflect.DeepEqual(out[0].Tags, in[0].Tags) {
		t.Errorf("round trip lost tags: %s", b)
	}
}
