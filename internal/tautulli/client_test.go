package tautulli

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// History must ask for one row per session: Tautulli's default grouping merges a user's
// consecutive sittings into one row that can't be matched against live-recorded plays.
func TestHistoryAsksForUngroupedRows(t *testing.T) {
	var queries []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		queries = append(queries, r.URL.RawQuery)
		if r.URL.Query().Get("grouping") != "0" {
			t.Errorf("get_history without grouping=0: %s", r.URL.RawQuery)
		}
		_, _ = w.Write([]byte(`{"response":{"result":"success","data":{"recordsFiltered":1,"data":[
			{"user_id":7,"user":"amy","title":"Dune","media_type":"movie","rating_key":"100","started":1000,"stopped":4000,"duration":2900}]}}}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "k")
	var got []Row
	if err := c.History(context.Background(), func(rows []Row) error { got = append(got, rows...); return nil }); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].RatingKey != "100" || got[0].DurationSec != 2900 {
		t.Fatalf("rows = %+v", got)
	}
	if err := c.Ping(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(queries) != 2 {
		t.Fatalf("made %d requests, want 2", len(queries))
	}
}
