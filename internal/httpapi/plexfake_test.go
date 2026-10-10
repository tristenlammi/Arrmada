package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/tristenlammi/arrmada/internal/plex"
)

// fakePlexTV stands in for plex.tv in tests: PINs, accounts and server resources. Every
// plex.tv call the code makes goes here while it's installed — no test talks to Plex.
type fakePlexTV struct {
	mu        sync.Mutex
	srv       *httptest.Server
	nextPin   int
	tokens    map[int]string            // pin id → token, once "approved"
	accounts  map[string]plex.Account   // token → who it belongs to
	resources map[string][]fakeResource // token → the servers it can reach
}

type fakeResource struct {
	Name  string
	ID    string // clientIdentifier (machine id)
	Owned bool
	Conns []fakeConn
}

type fakeConn struct {
	URI      string `json:"uri"`
	Address  string `json:"address"`
	Port     int    `json:"port"`
	Protocol string `json:"protocol"`
	Local    bool   `json:"local"`
	Relay    bool   `json:"relay"`
}

func newFakePlexTV(t *testing.T) *fakePlexTV {
	t.Helper()
	f := &fakePlexTV{nextPin: 1000, tokens: map[int]string{}, accounts: map[string]plex.Account{}, resources: map[string][]fakeResource{}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	restore := plex.SetTVBaseForTest(f.srv.URL)
	t.Cleanup(func() { restore(); f.srv.Close() })
	return f
}

// approve marks a PIN as approved by the Plex account behind token.
func (f *fakePlexTV) approve(pin int, token string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tokens[pin] = token
}

// account registers a Plex account and the servers its token reaches.
func (f *fakePlexTV) account(token string, a plex.Account, servers ...fakeResource) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.accounts[token] = a
	f.resources[token] = servers
}

func (f *fakePlexTV) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	token := r.Header.Get("X-Plex-Token")
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/api/v2/pins":
		f.nextPin++
		_ = json.NewEncoder(w).Encode(map[string]any{"id": f.nextPin, "code": "CODE" + strconv.Itoa(f.nextPin)})
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/api/v2/pins/"):
		id, _ := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/api/v2/pins/"))
		if id <= 1000 || id > f.nextPin {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": id, "authToken": f.tokens[id]})
	case r.URL.Path == "/api/v2/user":
		a, ok := f.accounts[token]
		if !ok {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(a)
	case r.URL.Path == "/api/v2/resources":
		if _, ok := f.accounts[token]; !ok {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		out := []map[string]any{}
		for _, s := range f.resources[token] {
			conns := s.Conns
			if conns == nil {
				conns = []fakeConn{}
			}
			out = append(out, map[string]any{"name": s.Name, "provides": "server", "clientIdentifier": s.ID, "owned": s.Owned, "connections": conns})
		}
		_ = json.NewEncoder(w).Encode(out)
	default:
		http.NotFound(w, r)
	}
}

// startPin calls a PIN start endpoint and returns the PIN id, the forwardUrl plex.tv was
// given ("" in popup mode) and the browser-binding cookie.
func startPin(t *testing.T, s *routeServer, path string, c *http.Cookie, hdr map[string]string) (int, string, *http.Cookie) {
	t.Helper()
	r := httptest.NewRequest("POST", "http://arrmada.local"+path, strings.NewReader(""))
	r.RemoteAddr = "192.168.1.20:5000"
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	if c != nil {
		r.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	s.h.ServeHTTP(rec, r)
	if rec.Code != http.StatusOK {
		t.Fatalf("start %s: HTTP %d %s", path, rec.Code, rec.Body)
	}
	var out struct {
		ID      int    `json:"id"`
		AuthURL string `json:"auth_url"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	frag := out.AuthURL[strings.Index(out.AuthURL, "#?")+2:]
	q, _ := url.ParseQuery(frag)
	var pinCookie *http.Cookie
	for _, ck := range rec.Result().Cookies() {
		if strings.HasPrefix(ck.Name, "arrmada_plexpin_") {
			pinCookie = ck
		}
	}
	if pinCookie == nil {
		t.Fatalf("start %s set no PIN cookie", path)
	}
	return out.ID, q.Get("forwardUrl"), pinCookie
}

// doCookies sends a request from the LAN carrying every cookie given.
func (s *routeServer) doCookies(method, path, body string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "http://arrmada.local"+path, strings.NewReader(body))
	r.RemoteAddr = "192.168.1.20:5000"
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	for _, c := range cookies {
		if c != nil {
			r.AddCookie(c)
		}
	}
	rec := httptest.NewRecorder()
	s.h.ServeHTTP(rec, r)
	return rec
}
