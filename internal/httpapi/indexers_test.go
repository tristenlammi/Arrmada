package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/tristenlammi/arrmada/internal/auth"
	"github.com/tristenlammi/arrmada/internal/connstatus"
	"github.com/tristenlammi/arrmada/internal/indexer"
	"github.com/tristenlammi/arrmada/internal/store"
)

func listIndexersAs(t *testing.T, a *api, role auth.Role) (string, []map[string]any) {
	t.Helper()
	w := httptest.NewRecorder()
	r := withUser(httptest.NewRequest(http.MethodGet, "/api/v1/indexers", nil), &auth.User{ID: 3, Username: "someone", Role: role})
	a.handleListIndexers(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("list returned %d: %s", w.Code, w.Body.String())
	}
	var body struct {
		Indexers []map[string]any `json:"indexers"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	return w.Body.String(), body.Indexers
}

// A manager sees each indexer's status — a failing one backing off with its error, an
// unused one unknown, a disabled one disabled — and nothing marshalled carries a secret.
// Anyone below manager gets no status at all.
func TestListIndexersStatus(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc := indexer.NewService(st.DB(), log, nil)
	tr := connstatus.New(st.DB(), log)
	svc.SetStatus(tr)
	ctx := context.Background()
	bad, err := svc.Create(ctx, indexer.Indexer{Name: "Broken", Kind: indexer.KindTorznab, URL: "http://x.test/api", APIKey: "sekrit-apikey", Priority: 10, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Create(ctx, indexer.Indexer{Name: "Fresh", Kind: indexer.KindTorrentLeech, Username: "u", Password: "sekrit-password", Priority: 10, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Create(ctx, indexer.Indexer{Name: "Off", Kind: indexer.KindTorznab, URL: "http://y.test/api", Priority: 10, Enabled: false}); err != nil {
		t.Fatal(err)
	}
	// Three failures: the third lands while already paused, so the streak stays at two.
	for i := 0; i < 3; i++ {
		tr.Record(connstatus.KindIndexer, strconv.FormatInt(bad.ID, 10), connstatus.Outcome{Err: errors.New("login failed"), Backoff: true})
	}
	a := &api{deps: Deps{Indexers: svc}}

	raw, list := listIndexersAs(t, a, auth.RoleManager)
	if strings.Contains(raw, "sekrit") || strings.Contains(raw, "api_key") || strings.Contains(raw, "password") {
		t.Fatalf("a secret reached the response: %s", raw)
	}
	byName := map[string]map[string]any{}
	for _, ix := range list {
		byName[ix["name"].(string)] = ix
	}
	broken, _ := byName["Broken"]["status"].(map[string]any)
	if broken["state"] != "backing_off" || broken["last_error"] != "login failed" || broken["consecutive_failures"] != float64(2) ||
		broken["backoff_until"] == nil || broken["failing_since"] == nil || broken["failures_24h"] != float64(3) {
		t.Errorf("Broken status = %+v", broken)
	}
	if s, _ := byName["Fresh"]["status"].(map[string]any); s["state"] != "unknown" {
		t.Errorf("Fresh status = %+v", s)
	}
	if s, _ := byName["Off"]["status"].(map[string]any); s["state"] != "disabled" {
		t.Errorf("Off status = %+v", s)
	}

	for _, role := range []auth.Role{auth.RoleRequester, auth.RoleReadonly} {
		raw, _ := listIndexersAs(t, a, role)
		if strings.Contains(raw, `"status"`) || strings.Contains(raw, "login failed") {
			t.Errorf("%s sees indexer status: %s", role, raw)
		}
	}
}

// indexerAPI is an api over a fresh indexer service.
func indexerAPI(t *testing.T) (*api, *indexer.Service) {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	svc := indexer.NewService(st.DB(), slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	return &api{deps: Deps{Indexers: svc}}, svc
}

// callIndexer runs one indexer handler as a manager with a JSON body and an optional {id}.
func callIndexer(t *testing.T, h http.HandlerFunc, method, path, id, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := withUser(httptest.NewRequest(method, path, strings.NewReader(body)), &auth.User{ID: 1, Username: "boss", Role: auth.RoleManager})
	r.Header.Set("Content-Type", "application/json")
	if id != "" {
		r.SetPathValue("id", id)
	}
	w := httptest.NewRecorder()
	h(w, r)
	return w
}

// POST /indexers/test checks settings without saving them: no row is created, a blank key
// with an id uses the saved one, and a typed key is used as typed and never stored.
func TestTestIndexerSettings(t *testing.T) {
	var lastKey atomic.Value
	tz := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.URL.Query().Get("apikey")
		lastKey.Store(key)
		if key != "saved-key" {
			_, _ = w.Write([]byte(`<error code="100" description="Incorrect user credentials"/>`))
			return
		}
		_, _ = w.Write([]byte(`<caps><searching><movie-search available="yes" supportedParams="q,imdbid"/></searching><categories><category id="2000"/></categories></caps>`))
	}))
	t.Cleanup(tz.Close)
	a, svc := indexerAPI(t)
	ctx := context.Background()
	row, _ := svc.Create(ctx, indexer.Indexer{Name: "Saved", Kind: indexer.KindTorznab, URL: tz.URL, APIKey: "saved-key", Enabled: true})

	decode := func(w *httptest.ResponseRecorder) map[string]any {
		t.Helper()
		if w.Code != http.StatusOK {
			t.Fatalf("status %d: %s", w.Code, w.Body.String())
		}
		var out map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return out
	}
	out := decode(callIndexer(t, a.handleTestIndexerSettings, http.MethodPost, "/api/v1/indexers/test", "",
		`{"name":"New","kind":"torznab","url":"`+tz.URL+`","api_key":"typed-key"}`))
	if out["ok"] != false || out["error"] != "Incorrect user credentials" || lastKey.Load() != "typed-key" {
		t.Fatalf("typed key: %+v (sent %v)", out, lastKey.Load())
	}
	out = decode(callIndexer(t, a.handleTestIndexerSettings, http.MethodPost, "/api/v1/indexers/test", "",
		`{"id":`+strconv.FormatInt(row.ID, 10)+`,"name":"Saved","kind":"torznab","url":"`+tz.URL+`","api_key":""}`))
	if out["ok"] != true || out["caps_summary"] != "Movies (imdbid) · 1 category" {
		t.Fatalf("saved key: %+v", out)
	}
	list, _ := svc.List(ctx)
	if len(list) != 1 || list[0].APIKey != "saved-key" || list[0].CapsJSON != "" {
		t.Fatalf("testing settings changed the rows: %+v", list)
	}
	if w := callIndexer(t, a.handleTestIndexerSettings, http.MethodPost, "/api/v1/indexers/test", "", `{"kind":"torznab"}`); w.Code != http.StatusBadRequest {
		t.Fatalf("no url = %d", w.Code)
	}
}

// A row synced from Prowlarr keeps the name, address and key Prowlarr gave it whatever an
// edit sends; the owner's own settings on it still save.
func TestUpdateIndexerIgnoresProwlarrOwnedFields(t *testing.T) {
	a, svc := indexerAPI(t)
	ctx := context.Background()
	row, err := svc.Create(ctx, indexer.Indexer{Name: "Prowlarr · TL", Kind: indexer.KindTorznab, URL: "http://p:9696/1/api", APIKey: "pk", Priority: 25, Enabled: true, ProwlarrID: 1})
	if err != nil {
		t.Fatal(err)
	}
	id := strconv.FormatInt(row.ID, 10)
	w := callIndexer(t, a.handleUpdateIndexer, http.MethodPut, "/api/v1/indexers/"+id, id,
		`{"name":"Mine","kind":"newznab","url":"http://evil/api","api_key":"new","priority":3,"media_types":["book"],"enabled":true}`)
	if w.Code != http.StatusNoContent {
		t.Fatalf("update = %d: %s", w.Code, w.Body.String())
	}
	got, _ := svc.Get(ctx, row.ID)
	if got.Name != "Prowlarr · TL" || got.Kind != indexer.KindTorznab || got.URL != "http://p:9696/1/api" || got.APIKey != "pk" {
		t.Fatalf("Prowlarr's fields changed: %+v", got)
	}
	if got.Priority != 3 || len(got.MediaTypes) != 1 || got.MediaTypes[0] != "book" {
		t.Fatalf("the owner's settings didn't save: %+v", got)
	}

	// An ordinary row still takes every field.
	plain, _ := svc.Create(ctx, indexer.Indexer{Name: "Plain", Kind: indexer.KindTorznab, URL: "http://a/api", Enabled: true})
	pid := strconv.FormatInt(plain.ID, 10)
	w = callIndexer(t, a.handleUpdateIndexer, http.MethodPut, "/api/v1/indexers/"+pid, pid, `{"name":"Renamed","kind":"torznab","url":"http://b/api","enabled":true}`)
	if w.Code != http.StatusNoContent {
		t.Fatalf("update = %d: %s", w.Code, w.Body.String())
	}
	if got, _ := svc.Get(ctx, plain.ID); got.Name != "Renamed" || got.URL != "http://b/api" {
		t.Fatalf("plain row = %+v", got)
	}
	if w := callIndexer(t, a.handleUpdateIndexer, http.MethodPut, "/api/v1/indexers/999", "999", `{"name":"x","kind":"torznab"}`); w.Code != http.StatusNotFound {
		t.Fatalf("missing row = %d", w.Code)
	}
}
