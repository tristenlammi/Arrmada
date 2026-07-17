package xem

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFetchParsesSceneMap(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"result":"success","data":[
			{"scene":{"season":1,"episode":1,"absolute":1}},
			{"scene":{"season":2,"episode":1,"absolute":15}},
			{"tvdb":{"season":1,"episode":2}}
		]}`))
	}))
	defer srv.Close()
	c := &Client{http: srv.Client(), base: srv.URL}
	m, err := c.Fetch(context.Background(), 999)
	if err != nil {
		t.Fatal(err)
	}
	if m[Key(2, 1)] != 15 || m[Key(1, 1)] != 1 {
		t.Fatalf("map = %v", m)
	}
	// A failure result yields an empty map, not an error (fall back to heuristics).
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"result":"failure","message":"no show"}`))
	}))
	defer srv2.Close()
	c2 := &Client{http: srv2.Client(), base: srv2.URL}
	if m, err := c2.Fetch(context.Background(), 1); err != nil || len(m) != 0 {
		t.Fatalf("failure result: m=%v err=%v", m, err)
	}
}
