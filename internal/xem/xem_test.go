package xem

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

func testClient(base string, hc *http.Client) *Client {
	return &Client{http: hc, base: base, log: slog.New(slog.NewTextHandler(io.Discard, nil))}
}

func TestFetchParsesSceneAndTVDB(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// scene single-season; tvdb split into S1/S2 — both should be keyed.
		w.Write([]byte(`{"result":"success","data":[
			{"scene":{"season":1,"episode":1,"absolute":1},"tvdb":{"season":1,"episode":1,"absolute":1}},
			{"scene":{"season":1,"episode":15,"absolute":15},"tvdb":{"season":2,"episode":1,"absolute":15}}
		]}`))
	}))
	defer srv.Close()
	c := testClient(srv.URL, srv.Client())
	m, err := c.Fetch(context.Background(), 999)
	if err != nil {
		t.Fatal(err)
	}
	if m[Key(2, 1)] != 15 { // tvdb S2E1 -> absolute 15 (the DBS case)
		t.Fatalf("tvdb key: map = %v", m)
	}
	if m[Key(1, 15)] != 15 || m[Key(1, 1)] != 1 { // scene keys too
		t.Fatalf("scene keys: map = %v", m)
	}

	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"result":"failure","message":"no show"}`))
	}))
	defer srv2.Close()
	if m, err := testClient(srv2.URL, srv2.Client()).Fetch(context.Background(), 1); err != nil || len(m) != 0 {
		t.Fatalf("failure result: m=%v err=%v", m, err)
	}
}

func TestExtractJSONFromFlareResponse(t *testing.T) {
	page := `<html><head></head><body><pre style="word-wrap: break-word;">{&quot;result&quot;:&quot;success&quot;,&quot;data&quot;:[{&quot;tvdb&quot;:{&quot;season&quot;:2,&quot;episode&quot;:1,&quot;absolute&quot;:15}}]}</pre></body></html>`
	c := testClient("", nil)
	m, err := c.parse(1, []byte(extractJSON(page)))
	if err != nil || m[Key(2, 1)] != 15 {
		t.Fatalf("extract+parse = %v, err=%v", m, err)
	}
	if got := extractJSON(`{"result":"success","data":[]}`); got != `{"result":"success","data":[]}` {
		t.Errorf("bare json passthrough = %q", got)
	}
}
