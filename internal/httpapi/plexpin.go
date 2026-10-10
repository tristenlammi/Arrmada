package httpapi

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/tristenlammi/arrmada/internal/netutil"
)

// A Plex sign-in is a PIN: Arrmada asks plex.tv for one, the person approves it on
// plex.tv, and polling the PIN then hands back their Plex token. The PIN id travels in
// URLs and plex.tv hands them out in sequence, so the id alone must never be enough to
// collect the token — anyone polling a guessed id would get a session (or a Plex link)
// for whoever approved it. Each PIN is therefore bound to the browser that started it by
// a random cookie, and, for the signed-in flows, to the account that started it.

// The three things a PIN can be for. Each has its own cookie, so connecting Plex in one
// tab doesn't cancel linking your account in another.
const (
	plexFlowLogin   = "login"   // Sign in with Plex on the login page
	plexFlowConnect = "connect" // Settings → Plex: connect the server (stores the token)
	plexFlowLink    = "link"    // link a Plex account to the signed-in account
)

const (
	// plexPinTTL is how long a started PIN can be finished. plex.tv's own PINs last
	// about as long; after that the person starts again.
	plexPinTTL = 20 * time.Minute
	// plexPinMax bounds the table. Starting a login PIN is throttled per address, so
	// this is only a backstop; the oldest entry goes first.
	plexPinMax = 500
)

type plexPinEntry struct {
	kind    string
	userID  int64    // 0 for the login flow (nobody is signed in yet)
	secret  [32]byte // sha256 of the cookie value; the raw value is never kept
	expires time.Time
}

// plexPinGuard remembers which browser and account started each PIN. In memory on
// purpose: a restart in the middle of a sign-in just means starting again.
type plexPinGuard struct {
	mu   sync.Mutex
	pins map[int]plexPinEntry
	now  func() time.Time
}

func newPlexPinGuard() *plexPinGuard {
	return &plexPinGuard{pins: map[int]plexPinEntry{}, now: time.Now}
}

func plexPinCookieName(kind string) string { return "arrmada_plexpin_" + kind }

// add records a PIN, dropping expired entries (and the oldest, at the cap).
func (g *plexPinGuard) add(pinID int, e plexPinEntry) {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := g.now()
	var oldestID int
	var oldest time.Time
	for id, x := range g.pins {
		if now.After(x.expires) {
			delete(g.pins, id)
			continue
		}
		if oldest.IsZero() || x.expires.Before(oldest) {
			oldestID, oldest = id, x.expires
		}
	}
	if len(g.pins) >= plexPinMax && !oldest.IsZero() {
		delete(g.pins, oldestID)
	}
	g.pins[pinID] = e
}

// owns reports whether this PIN was started for kind, by userID, in the browser that
// sent cookie.
func (g *plexPinGuard) owns(pinID int, kind string, userID int64, cookie string) bool {
	if g == nil || cookie == "" {
		return false
	}
	g.mu.Lock()
	e, ok := g.pins[pinID]
	g.mu.Unlock()
	if !ok || e.kind != kind || e.userID != userID || g.now().After(e.expires) {
		return false
	}
	sum := sha256.Sum256([]byte(cookie))
	return subtle.ConstantTimeCompare(sum[:], e.secret[:]) == 1
}

func (g *plexPinGuard) forget(pinID int) {
	if g == nil {
		return
	}
	g.mu.Lock()
	delete(g.pins, pinID)
	g.mu.Unlock()
}

// bindPlexPin ties a freshly started PIN to this browser (a short-lived cookie) and, for
// the signed-in flows, to the account starting it.
func (a *api) bindPlexPin(w http.ResponseWriter, r *http.Request, kind string, userID int64, pinID int) error {
	if a.plexPins == nil {
		return errors.New("plex sign-in isn't wired")
	}
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return err
	}
	secret := base64.RawURLEncoding.EncodeToString(b)
	a.plexPins.add(pinID, plexPinEntry{kind: kind, userID: userID, secret: sha256.Sum256([]byte(secret)), expires: a.plexPins.now().Add(plexPinTTL)})
	http.SetCookie(w, &http.Cookie{
		Name:     plexPinCookieName(kind),
		Value:    secret,
		Path:     "/api/v1/",
		MaxAge:   int(plexPinTTL / time.Second),
		HttpOnly: true,
		Secure:   requestIsHTTPS(r),
		SameSite: http.SameSiteLaxMode,
	})
	return nil
}

// plexPinOwned is the check every poll makes before asking plex.tv about a PIN.
func (a *api) plexPinOwned(r *http.Request, kind string, userID int64, pinID int) bool {
	c, err := r.Cookie(plexPinCookieName(kind))
	if err != nil {
		return false
	}
	return a.plexPins.owns(pinID, kind, userID, c.Value)
}

// errPlexPinElsewhere is the answer to a poll for a PIN this browser didn't start.
const errPlexPinElsewhere = "This Plex sign-in was started somewhere else — start again."

// plexRedirectMode reports whether the client asked for the full-page redirect (no popup).
func plexRedirectMode(r *http.Request) bool { return r.URL.Query().Get("mode") == "redirect" }

// plexReturnBase is this site's address as the browser sees it — scheme and host, no
// path — for plex.tv to send a redirect-mode sign-in back to. It is built from the
// request, never taken from the client's body or query, so it can't be aimed anywhere
// else. Behind a local reverse proxy the host is X-Forwarded-Host (the address the
// person typed); otherwise it's Host. Either must be a bare host[:port]; anything else
// (a path, userinfo, spaces) is refused and ok is false.
func plexReturnBase(r *http.Request) (string, bool) {
	host := r.Host
	if netutil.FromLocalProxy(r) {
		if fh := strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-Host"), ",")[0]); fh != "" {
			host = fh
		}
	}
	if !validReturnHost(host) {
		return "", false
	}
	scheme := "http"
	if requestIsHTTPS(r) {
		scheme = "https"
	}
	return scheme + "://" + host, true
}

// plexForwardURL is plexReturnBase plus an in-app path ("/?plexpin=123").
func plexForwardURL(r *http.Request, path string) (string, bool) {
	base, ok := plexReturnBase(r)
	if !ok {
		return "", false
	}
	return base + path, true
}

// validReturnHost accepts host or host:port (an IPv6 literal in brackets included) and
// nothing else.
func validReturnHost(h string) bool {
	if h == "" || len(h) > 255 || strings.ContainsAny(h, "/\\@?#% \t\r\n") {
		return false
	}
	u, err := url.Parse("http://" + h)
	return err == nil && u.Host == h && u.User == nil && u.Path == "" && u.RawQuery == "" && u.Hostname() != ""
}
