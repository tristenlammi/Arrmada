package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/tristenlammi/arrmada/internal/apikeys"
	"github.com/tristenlammi/arrmada/internal/auth"
	"github.com/tristenlammi/arrmada/internal/automation"
	"github.com/tristenlammi/arrmada/internal/config"
	"github.com/tristenlammi/arrmada/internal/download"
	"github.com/tristenlammi/arrmada/internal/indexer"
	"github.com/tristenlammi/arrmada/internal/insights"
	"github.com/tristenlammi/arrmada/internal/jellyfinsvc"
	"github.com/tristenlammi/arrmada/internal/settings"
	"github.com/tristenlammi/arrmada/internal/store"
)

// checklistAPI is a fresh install: every service wired, nothing configured.
func checklistAPI(t *testing.T) *api {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	set := settings.NewService(st.DB())
	dl := download.NewService(st.DB(), log)
	return &api{deps: Deps{
		Store: st, Log: log, Settings: set,
		Config:     config.Config{DataDir: t.TempDir()},
		APIKeys:    apikeys.NewStore(set),
		Indexers:   indexer.NewService(st.DB(), log, nil),
		Downloads:  dl,
		Insights:   insights.NewService(st.DB(), set, nil, nil, log),
		Automation: automation.New(nil, nil, dl, nil, st.DB(), nil, log, ""),
	}}
}

func getChecklist(t *testing.T, a *api) setupChecklist {
	t.Helper()
	if a.deps.Downloads != nil {
		a.deps.Downloads.InvalidateSnapshot()
	}
	rec := httptest.NewRecorder()
	a.handleSetupChecklist(rec, httptest.NewRequest(http.MethodGet, "/api/v1/setup/checklist", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("HTTP %d: %s", rec.Code, rec.Body)
	}
	var c setupChecklist
	if err := json.Unmarshal(rec.Body.Bytes(), &c); err != nil {
		t.Fatal(err)
	}
	return c
}

func checklistDone(c setupChecklist) map[string]bool {
	out := map[string]bool{}
	for _, it := range c.Items {
		out[it.Key] = it.Done
	}
	return out
}

// Each item ticks itself from what's actually set up: a half-configured install shows the
// TMDB key ticked and the indexer not, and each later step ticks as it's done.
func TestSetupChecklist(t *testing.T) {
	a := checklistAPI(t)
	ctx := context.Background()

	c := getChecklist(t, a)
	if c.Done || c.Dismissed {
		t.Fatalf("fresh install: done=%v dismissed=%v, want neither", c.Done, c.Dismissed)
	}
	for key, done := range checklistDone(c) {
		if done {
			t.Errorf("fresh install: %s is ticked", key)
		}
	}
	for _, it := range c.Items {
		if it.Link == "" || it.Link[0] != '/' {
			t.Errorf("%s: link %q isn't an in-app path", it.Key, it.Link)
		}
	}

	// Half configured: the TMDB key and the folders, no indexers.
	if err := a.deps.APIKeys.Set(ctx, "tmdb", "test-tmdb-key"); err != nil {
		t.Fatal(err)
	}
	if err := a.deps.Settings.Set(ctx, keyLibMovies, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	got := checklistDone(getChecklist(t, a))
	if !got["tmdb"] || !got["folders"] {
		t.Errorf("tmdb and folders set, but ticked = %v", got)
	}
	if got["indexer"] {
		t.Error("0 indexers, but the indexer item is ticked")
	}

	// A disabled indexer doesn't count; an enabled one does.
	off, err := a.deps.Indexers.Create(ctx, indexer.Indexer{Name: "Off", Kind: indexer.KindTorznab, URL: "http://127.0.0.1:1", Enabled: false})
	if err != nil {
		t.Fatal(err)
	}
	if checklistDone(getChecklist(t, a))["indexer"] {
		t.Error("only a switched-off indexer, but the indexer item is ticked")
	}
	off.Enabled = true
	if err := a.deps.Indexers.Update(ctx, off); err != nil {
		t.Fatal(err)
	}
	if !checklistDone(getChecklist(t, a))["indexer"] {
		t.Error("an enabled indexer, but the indexer item isn't ticked")
	}

	// A download client counts once it answers, not just for existing.
	dead := httptest.NewServer(http.NotFoundHandler())
	deadURL := dead.URL
	dead.Close()
	cl, err := a.deps.Downloads.Create(ctx, download.Client{Name: "qb", Kind: download.KindQbittorrent, URL: deadURL, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if checklistDone(getChecklist(t, a))["download_client"] {
		t.Error("a client that doesn't answer, but download_client is ticked")
	}
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			fmt.Fprint(w, "Ok.")
		case "/api/v2/torrents/info":
			fmt.Fprint(w, "[]")
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(up.Close)
	cl.URL = up.URL
	if _, err := a.deps.Downloads.Update(ctx, cl); err != nil {
		t.Fatal(err)
	}
	if !checklistDone(getChecklist(t, a))["download_client"] {
		t.Error("a client that answers, but download_client isn't ticked")
	}

	// Plex: an address and a token (nothing is contacted to tick it).
	tok := "test-plex-token"
	if err := a.deps.Insights.SetConfig(ctx, "http://127.0.0.1:1", &tok, nil, nil); err != nil {
		t.Fatal(err)
	}
	// A first title, then a first import.
	db := a.deps.Store.DB()
	if _, err := db.Exec(`INSERT INTO movies (tmdb_id, title, monitored, has_file) VALUES (1, 'Test Movie', 1, 0)`); err != nil {
		t.Fatal(err)
	}
	got = checklistDone(getChecklist(t, a))
	if !got["media_server"] || !got["first_title"] || got["first_import"] {
		t.Errorf("plex and a title, no import: %v", got)
	}
	if _, err := db.Exec(`INSERT INTO grabs (movie_id, title, indexer, media_type, info_hash, status) VALUES (1, 'Test.Movie.2020.1080p', 'Off', 'movie', 'abc', 'seeded')`); err != nil {
		t.Fatal(err)
	}
	c = getChecklist(t, a)
	if !c.Done || !c.Dismissed {
		t.Errorf("everything set up: done=%v dismissed=%v (%v), want both", c.Done, c.Dismissed, checklistDone(c))
	}
	// Done once is put away for good: a step coming undone later doesn't bring the card back.
	if _, err := db.Exec(`DELETE FROM movies`); err != nil {
		t.Fatal(err)
	}
	if c = getChecklist(t, a); c.Done || !c.Dismissed {
		t.Errorf("a step undone after finishing: done=%v dismissed=%v, want not done but still dismissed", c.Done, c.Dismissed)
	}
}

// A Jellyfin server on its own ticks the media server step, the way Plex does, and the
// step links to both servers' cards.
func TestSetupChecklistJellyfinOnly(t *testing.T) {
	t.Setenv("ARRMADA_JELLYFIN_API_KEY", "")
	a := checklistAPI(t)
	ctx := context.Background()
	a.deps.Jellyfin = jellyfinsvc.New(jellyfinsvc.Options{Settings: a.deps.Settings, Keys: a.deps.APIKeys, Version: "test", Log: a.deps.Log})
	item := func() checklistItem {
		for _, it := range getChecklist(t, a).Items {
			if it.Key == "media_server" {
				return it
			}
		}
		t.Fatal("no media_server item")
		return checklistItem{}
	}
	if it := item(); it.Done || it.Link != "/settings/connections#media-servers" || it.LinkKey != "mediaServerCards" {
		t.Fatalf("fresh install: %+v", it)
	}
	// An address alone isn't a connection; the key makes it one.
	if err := a.deps.Settings.Set(ctx, jellyfinsvc.KeyURL, "http://127.0.0.1:1"); err != nil {
		t.Fatal(err)
	}
	if item().Done {
		t.Error("a Jellyfin address with no key ticks the step")
	}
	if err := a.deps.APIKeys.Set(ctx, apikeys.Jellyfin, "test-jellyfin-key"); err != nil {
		t.Fatal(err)
	}
	if !item().Done {
		t.Error("Jellyfin connected, but the media server step isn't ticked")
	}
}

// Dismiss hides the card for good, and only an admin may do it (or read the list).
func TestSetupChecklistDismiss(t *testing.T) {
	a := checklistAPI(t)
	rec := httptest.NewRecorder()
	a.handleDismissSetupChecklist(rec, httptest.NewRequest(http.MethodPost, "/api/v1/setup/checklist/dismiss", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("HTTP %d: %s", rec.Code, rec.Body)
	}
	if c := getChecklist(t, a); !c.Dismissed || c.Done {
		t.Errorf("after dismiss: dismissed=%v done=%v, want dismissed and still not done", c.Dismissed, c.Done)
	}

	srv := New(a.deps)
	for role, want := range map[auth.Role]int{auth.RoleRequester: http.StatusForbidden, auth.RoleManager: http.StatusForbidden, auth.RoleAdmin: http.StatusOK} {
		for _, m := range []string{http.MethodGet, http.MethodPost} {
			target := "/api/v1/setup/checklist"
			if m == http.MethodPost {
				target += "/dismiss"
			}
			rec := httptest.NewRecorder()
			srv.Handler.ServeHTTP(rec, lanRequest(m, target, "", role))
			if rec.Code != want {
				t.Errorf("%s %s as %s: HTTP %d, want %d", m, target, role, rec.Code, want)
			}
		}
	}
}

// Restore brings the Getting started card back after a manual dismiss, so an admin can
// return to remaining steps. A fully completed checklist is re-dismissed on the next GET.
func TestSetupChecklistRestore(t *testing.T) {
	a := checklistAPI(t)

	rec := httptest.NewRecorder()
	a.handleDismissSetupChecklist(rec, httptest.NewRequest(http.MethodPost, "/api/v1/setup/checklist/dismiss", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("dismiss HTTP %d: %s", rec.Code, rec.Body)
	}
	if c := getChecklist(t, a); !c.Dismissed {
		t.Fatal("dismissed checklist came back as not dismissed")
	}

	rec = httptest.NewRecorder()
	a.handleRestoreSetupChecklist(rec, httptest.NewRequest(http.MethodPost, "/api/v1/setup/checklist/restore", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("restore HTTP %d: %s", rec.Code, rec.Body)
	}
	var restored setupChecklist
	if err := json.Unmarshal(rec.Body.Bytes(), &restored); err != nil {
		t.Fatal(err)
	}
	if restored.Dismissed {
		t.Fatal("restore returned a dismissed checklist")
	}
	if c := getChecklist(t, a); c.Dismissed {
		t.Fatal("restored checklist was dismissed again while steps remain")
	}
}
