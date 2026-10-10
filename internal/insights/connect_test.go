package insights

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tristenlammi/arrmada/internal/plex"
)

// fakePMS is a Plex Media Server that answers /identity and / with the given machine id
// and name.
func fakePMS(t *testing.T, machineID, name string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/identity":
			fmt.Fprintf(w, `{"MediaContainer":{"machineIdentifier":%q,"version":"1.40"}}`, machineID)
		case "/":
			fmt.Fprintf(w, `{"MediaContainer":{"friendlyName":%q}}`, name)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// deadURL is an address nothing answers on.
func deadURL(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.NotFoundHandler())
	u := srv.URL
	srv.Close()
	return u
}

type fakeServer struct {
	name, id string
	owned    bool
	uris     []string // tried in this order (all marked remote https, so order is kept)
}

// fakePlexTV approves PIN 77 with token "tok" and lists the given servers for it.
func fakePlexTV(t *testing.T, servers ...fakeServer) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/v2/pins/77":
			_, _ = w.Write([]byte(`{"id":77,"authToken":"tok"}`))
		case r.URL.Path == "/api/v2/resources" && r.Header.Get("X-Plex-Token") == "tok":
			var out []map[string]any
			for _, s := range servers {
				var conns []map[string]any
				for _, u := range s.uris {
					conns = append(conns, map[string]any{"uri": u, "protocol": "https", "local": false})
				}
				out = append(out, map[string]any{"name": s.name, "provides": "server", "clientIdentifier": s.id, "owned": s.owned, "connections": conns})
			}
			_ = json.NewEncoder(w).Encode(out)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(plex.SetTVBaseForTest(srv.URL))
}

// A fresh install: signing in finds the owned server, saves a URL that answered with the
// right machine id, names it, and turns monitoring on.
func TestPollPlexAuthEnablesMonitoringWhenUnset(t *testing.T) {
	s := newTestService(t)
	ctx := context.Background()
	home := fakePMS(t, "M1", "Home")
	friend := fakePMS(t, "F1", "Friend's")
	fakePlexTV(t,
		fakeServer{name: "Friend's", id: "F1", owned: false, uris: []string{friend.URL}},
		fakeServer{name: "Home", id: "M1", owned: true, uris: []string{home.URL}},
	)
	res, err := s.PollPlexAuth(ctx, 77)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Authorized || res.ServerName != "Home" || len(res.Choices) != 0 {
		t.Fatalf("result = %+v, want connected to Home", res)
	}
	cfg := s.Config(ctx)
	if cfg.URL != home.URL || !cfg.TokenSet || !cfg.Enabled || !cfg.EnabledSet || cfg.ServerName != "Home" || cfg.MachineID != "M1" {
		t.Errorf("config = %+v, want Home's URL, monitoring on, name and machine id", cfg)
	}
}

// An owner who switched monitoring off keeps it off through a later sign-in.
func TestPollPlexAuthRespectsExplicitOff(t *testing.T) {
	s := newTestService(t)
	ctx := context.Background()
	home := fakePMS(t, "M1", "Home")
	fakePlexTV(t, fakeServer{name: "Home", id: "M1", owned: true, uris: []string{home.URL}})
	_ = s.settings.SetBool(ctx, keyEnabled, false)
	if _, err := s.PollPlexAuth(ctx, 77); err != nil {
		t.Fatal(err)
	}
	if cfg := s.Config(ctx); cfg.Enabled || cfg.URL != home.URL {
		t.Errorf("config = %+v, want connected with monitoring still off", cfg)
	}
}

// The first listed address is dead and the second answers with the right machine id: the
// second is saved. A third address answering as another server is never taken.
func TestPollPlexAuthPicksRespondingConnection(t *testing.T) {
	s := newTestService(t)
	ctx := context.Background()
	impostor := fakePMS(t, "OTHER", "Impostor")
	home := fakePMS(t, "M1", "Home")
	fakePlexTV(t, fakeServer{name: "Home", id: "M1", owned: true, uris: []string{deadURL(t), impostor.URL, home.URL}})
	res, err := s.PollPlexAuth(ctx, 77)
	if err != nil {
		t.Fatal(err)
	}
	if res.ServerName != "Home" || s.Config(ctx).URL != home.URL {
		t.Errorf("result %+v, saved URL %q; want %q", res, s.Config(ctx).URL, home.URL)
	}
}

// Two owned servers answering: nothing is guessed, the UI gets both to choose from.
func TestPollPlexAuthOffersChoiceOfOwnedServers(t *testing.T) {
	s := newTestService(t)
	ctx := context.Background()
	a := fakePMS(t, "M1", "Home")
	b := fakePMS(t, "M2", "Cabin")
	fakePlexTV(t,
		fakeServer{name: "Home", id: "M1", owned: true, uris: []string{a.URL}},
		fakeServer{name: "Cabin", id: "M2", owned: true, uris: []string{b.URL}},
	)
	res, err := s.PollPlexAuth(ctx, 77)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Choices) != 2 || res.ServerName != "" || s.Config(ctx).URL != "" {
		t.Fatalf("result = %+v (saved URL %q), want two choices and nothing saved", res, s.Config(ctx).URL)
	}
	// Picking one is a normal save; it names the server and turns monitoring on.
	if err := s.SetConfig(ctx, res.Choices[1].URL, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	cfg := s.Config(ctx)
	if !strings.HasPrefix(cfg.URL, "http://127.0.0.1") || cfg.MachineID != res.Choices[1].MachineID || cfg.ServerName != res.Choices[1].Name || !cfg.Enabled {
		t.Errorf("after picking: %+v", cfg)
	}
}

// A saved URL that still answers, for a server this account owns, is kept as it is.
func TestPollPlexAuthKeepsWorkingOwnedURL(t *testing.T) {
	s := newTestService(t)
	ctx := context.Background()
	home := fakePMS(t, "M1", "Home")
	other := fakePMS(t, "M1", "Home")
	fakePlexTV(t, fakeServer{name: "Home", id: "M1", owned: true, uris: []string{other.URL}})
	_ = s.settings.Set(ctx, keyURL, home.URL)
	if _, err := s.PollPlexAuth(ctx, 77); err != nil {
		t.Fatal(err)
	}
	if got := s.Config(ctx).URL; got != home.URL {
		t.Errorf("URL = %q, want the saved %q kept", got, home.URL)
	}
}
