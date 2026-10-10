package plex

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"time"
)

// Plex sign-in uses a PIN-based OAuth: Arrmada asks plex.tv for a short-lived PIN,
// the user authorizes it in a plex.tv popup, and Arrmada polls until plex.tv hands
// back an auth token. This is the same flow Tautulli/Overseerr use — no manual
// X-Plex-Token hunting.

const plexAuthUI = "https://app.plex.tv/auth#?"

// tvBase is where plex.tv lives. Tests point it at a fake (SetTVBaseForTest), so no test
// ever talks to the real plex.tv.
var tvBase atomic.Pointer[string]

func init() {
	b := "https://plex.tv"
	tvBase.Store(&b)
}

// plexTV is the plex.tv base URL every account call goes to.
func plexTV() string { return *tvBase.Load() }

// SetTVBaseForTest points every plex.tv call at base (an httptest server) and returns the
// function that puts the real address back. Tests only.
func SetTVBaseForTest(base string) (restore func()) {
	old := tvBase.Load()
	tvBase.Store(&base)
	return func() { tvBase.Store(old) }
}

var plexHTTP = &http.Client{Timeout: 15 * time.Second}

// PIN is a pending Plex sign-in.
type PIN struct {
	ID   int    `json:"id"`
	Code string `json:"code"`
}

func plexHeaders(req *http.Request, clientID, product string) {
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Plex-Client-Identifier", clientID)
	if product != "" {
		req.Header.Set("X-Plex-Product", product)
	}
}

// RequestPIN starts a sign-in: asks plex.tv for a PIN the user will authorize.
func RequestPIN(ctx context.Context, clientID, product string) (PIN, error) {
	form := url.Values{"strong": {"true"}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, plexTV()+"/api/v2/pins", strings.NewReader(form.Encode()))
	if err != nil {
		return PIN{}, err
	}
	plexHeaders(req, clientID, product)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := plexHTTP.Do(req)
	if err != nil {
		return PIN{}, fmt.Errorf("plex: request PIN failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return PIN{}, fmt.Errorf("plex: request PIN HTTP %d", resp.StatusCode)
	}
	var p PIN
	if err := json.NewDecoder(resp.Body).Decode(&p); err != nil {
		return PIN{}, err
	}
	if p.ID == 0 || p.Code == "" {
		return PIN{}, fmt.Errorf("plex: empty PIN response")
	}
	return p, nil
}

// CheckPIN polls a PIN. It returns the auth token once the user has authorized the
// sign-in, or an empty string while it's still pending.
func CheckPIN(ctx context.Context, clientID string, pinID int) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("%s/api/v2/pins/%d", plexTV(), pinID), nil)
	if err != nil {
		return "", err
	}
	plexHeaders(req, clientID, "")
	resp, err := plexHTTP.Do(req)
	if err != nil {
		return "", fmt.Errorf("plex: check PIN failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return "", fmt.Errorf("plex: sign-in expired — start again")
	}
	if resp.StatusCode >= 300 {
		return "", fmt.Errorf("plex: check PIN HTTP %d", resp.StatusCode)
	}
	var r struct {
		AuthToken string `json:"authToken"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return "", err
	}
	return r.AuthToken, nil // "" until authorized
}

// AuthURL is the plex.tv page the user opens to authorize a PIN. forwardURL, when set, is
// where plex.tv sends the browser once the user approves: the full-page redirect used
// when a popup can't be opened (iPhone Safari, the installed app, a popup blocker).
// Empty leaves it out, and Plex just says the window can be closed.
func AuthURL(clientID, code, product, forwardURL string) string {
	v := url.Values{}
	v.Set("clientID", clientID)
	v.Set("code", code)
	v.Set("context[device][product]", product)
	if forwardURL != "" {
		v.Set("forwardUrl", forwardURL)
	}
	return plexAuthUI + v.Encode()
}

// DiscoverServer finds a Plex server the token owns and returns a base URL the
// Arrmada container can reach — preferring a plain-HTTP LAN connection. Empty (no
// error) when nothing suitable is found, so the caller can leave the URL for the
// user to enter.
func DiscoverServer(ctx context.Context, clientID, token string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, plexTV()+"/api/v2/resources?includeHttps=1", nil)
	if err != nil {
		return "", err
	}
	plexHeaders(req, clientID, "")
	req.Header.Set("X-Plex-Token", token)
	resp, err := plexHTTP.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return "", fmt.Errorf("plex: resources HTTP %d", resp.StatusCode)
	}
	var resources []struct {
		Provides    string `json:"provides"`
		Connections []struct {
			URI      string `json:"uri"`
			Address  string `json:"address"`
			Port     int    `json:"port"`
			Protocol string `json:"protocol"`
			Local    bool   `json:"local"`
			Relay    bool   `json:"relay"`
		} `json:"connections"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&resources); err != nil {
		return "", err
	}
	var fallback string
	for _, res := range resources {
		if !strings.Contains(res.Provides, "server") {
			continue
		}
		for _, c := range res.Connections {
			if c.Relay {
				continue
			}
			// A plain-HTTP LAN connection is what a same-network container reaches best.
			if c.Local && c.Protocol == "http" && c.Address != "" {
				return fmt.Sprintf("http://%s:%d", c.Address, c.Port), nil
			}
			if fallback == "" && c.URI != "" {
				fallback = c.URI
			}
		}
	}
	return fallback, nil
}
