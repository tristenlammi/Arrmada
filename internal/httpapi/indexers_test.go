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
