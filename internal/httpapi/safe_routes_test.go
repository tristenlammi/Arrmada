package httpapi

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/tristenlammi/arrmada/internal/auth"
	"github.com/tristenlammi/arrmada/internal/config"
	"github.com/tristenlammi/arrmada/internal/recyclebin"
	"github.com/tristenlammi/arrmada/internal/settings"
	"github.com/tristenlammi/arrmada/internal/store"
)

// routeServer is the real router (middleware chain included) over a scratch DB, so a
// test can hit a route the way the browser does — role checks and all.
type routeServer struct {
	h    http.Handler
	st   *store.Store
	auth *auth.Service
	deps Deps
}

func newRouteServer(t *testing.T, tweak func(*Deps)) *routeServer {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	set := settings.NewService(st.DB())
	d := Deps{
		Config:   config.Config{DataDir: t.TempDir()},
		Log:      log,
		Store:    st,
		Auth:     auth.NewService(st.DB()),
		Settings: set,
		Recycle:  recyclebin.New(t.TempDir(), set, log),
	}
	if tweak != nil {
		tweak(&d)
	}
	return &routeServer{h: New(d).Handler, st: st, auth: d.Auth, deps: d}
}

// user creates an account with the given role and returns it with a session cookie.
func (s *routeServer) user(t *testing.T, name string, role auth.Role) (*auth.User, *http.Cookie) {
	t.Helper()
	ctx := context.Background()
	u, err := s.auth.CreateUser(ctx, name, "password123", role, false)
	if err != nil {
		t.Fatal(err)
	}
	tok, _, err := s.auth.CreateSession(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	return u, &http.Cookie{Name: sessionCookieName, Value: tok}
}

// do sends a request from a LAN address (so the external gate stays out of the way).
func (s *routeServer) do(method, path string, c *http.Cookie) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "http://arrmada.local"+path, nil)
	r.RemoteAddr = "192.168.1.20:5000"
	if c != nil {
		r.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	s.h.ServeHTTP(rec, r)
	return rec
}

// The recycle mode is staff information (it names a server path); requesters get 403.
func TestRecycleModeRouteIsManagerOnly(t *testing.T) {
	s := newRouteServer(t, nil)
	_, req := s.user(t, "kid@example.com", auth.RoleRequester)
	_, mgr := s.user(t, "mgr@example.com", auth.RoleManager)

	if rec := s.do("GET", "/api/v1/recycle/mode", req); rec.Code != http.StatusForbidden {
		t.Errorf("requester: HTTP %d, want 403", rec.Code)
	}
	if rec := s.do("GET", "/api/v1/recycle/mode", nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("anonymous: HTTP %d, want 401", rec.Code)
	}
	rec := s.do("GET", "/api/v1/recycle/mode", mgr)
	if rec.Code != http.StatusOK {
		t.Fatalf("manager: HTTP %d, want 200: %s", rec.Code, rec.Body)
	}
}
