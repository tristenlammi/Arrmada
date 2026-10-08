package httpapi

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/tristenlammi/arrmada/internal/auth"
	"github.com/tristenlammi/arrmada/internal/metadata"
	"github.com/tristenlammi/arrmada/internal/music"
	"github.com/tristenlammi/arrmada/internal/settings"
	"github.com/tristenlammi/arrmada/internal/store"
)

// musicGateServer builds the real router over a temp DB, with one signed-in manager.
func musicGateServer(t *testing.T) (http.Handler, *settings.Service, string) {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()
	authSvc := auth.NewService(st.DB())
	u, err := authSvc.CreateUser(ctx, "manager", "correct-horse-battery", auth.RoleManager, true)
	if err != nil {
		t.Fatal(err)
	}
	session, _, err := authSvc.CreateSession(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	set := settings.NewService(st.DB())
	srv := New(Deps{
		Log: slog.Default(), Store: st, Auth: authSvc, Settings: set,
		Music: music.NewService(st.DB(), metadata.NewMusicBrainz(), slog.Default()),
	})
	return srv.Handler, set, session
}

func musicGateCall(h http.Handler, method, path, session string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, nil)
	r.RemoteAddr = "192.168.1.20:5000" // on the LAN, so the external gate stays out of it
	if session != "" {
		r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: session})
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec
}

// Music is off on a fresh install: its API answers 404 with the reason, the manual
// actions included, and switching it on brings it back without a restart.
func TestMusicRoutesFollowTheModuleToggle(t *testing.T) {
	h, set, session := musicGateServer(t)

	rec := musicGateCall(h, "GET", "/api/v1/music/artists", session)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("fresh install: GET artists = %d, want 404", rec.Code)
	}
	var body struct{ Message string }
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if body.Message != musicOffMessage {
		t.Errorf("message = %q, want the module-off explanation", body.Message)
	}
	for _, p := range []string{"/api/v1/music/artists/1/discography", "/api/v1/music/scan"} {
		if rec := musicGateCall(h, "POST", p, session); rec.Code != http.StatusNotFound {
			t.Errorf("POST %s while off = %d, want 404", p, rec.Code)
		}
	}

	if err := set.SetBool(context.Background(), settings.KeyModuleMusic, true); err != nil {
		t.Fatal(err)
	}
	if rec := musicGateCall(h, "GET", "/api/v1/music/artists", session); rec.Code != http.StatusOK {
		t.Errorf("after switching on: GET artists = %d, want 200", rec.Code)
	}
}

// The gate sits inside the auth check: a stranger gets 401 either way and can't use
// the 404 to learn whether Music is on.
func TestMusicRoutesStillRequireAuth(t *testing.T) {
	h, set, _ := musicGateServer(t)
	if rec := musicGateCall(h, "GET", "/api/v1/music/artists", ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("anonymous while off = %d, want 401", rec.Code)
	}
	_ = set.SetBool(context.Background(), settings.KeyModuleMusic, true)
	if rec := musicGateCall(h, "GET", "/api/v1/music/artists", ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("anonymous while on = %d, want 401", rec.Code)
	}
}

// /status labels Music a preview and reports the live toggle.
func TestStatusReportsMusicAsPreview(t *testing.T) {
	h, set, _ := musicGateServer(t)
	music := func() module {
		var body struct{ Modules []module }
		rec := musicGateCall(h, "GET", "/api/v1/status", "")
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		for _, m := range body.Modules {
			if m.ID == "music" {
				return m
			}
		}
		t.Fatal("no music module in /status")
		return module{}
	}
	if m := music(); m.Enabled || m.Status != "preview" {
		t.Errorf("fresh install: %+v, want disabled preview", m)
	}
	_ = set.SetBool(context.Background(), settings.KeyModuleMusic, true)
	if m := music(); !m.Enabled || m.Status != "preview" {
		t.Errorf("switched on: %+v, want enabled preview", m)
	}
}
