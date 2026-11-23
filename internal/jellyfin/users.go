package jellyfin

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// User is a Jellyfin account.
type User struct {
	ID               string     `json:"Id"`
	Name             string     `json:"Name"`
	ServerID         string     `json:"ServerId"`
	HasPassword      bool       `json:"HasPassword"`
	PrimaryImageTag  string     `json:"PrimaryImageTag"`
	LastLoginDate    *time.Time `json:"LastLoginDate"`
	LastActivityDate *time.Time `json:"LastActivityDate"`
	Policy           UserPolicy `json:"Policy"`
}

// UserPolicy is the part of a user's policy Arrmada reads.
type UserPolicy struct {
	IsAdministrator    bool `json:"IsAdministrator"` // closest to Plex's server owner
	IsDisabled         bool `json:"IsDisabled"`
	IsHidden           bool `json:"IsHidden"`
	EnableRemoteAccess bool `json:"EnableRemoteAccess"`
}

// Users lists the server's accounts (admin key). Emby's current API lists them at
// /Users/Query ({Items, TotalRecordCount}); a 404 there (an older Emby) falls back to the
// bare list at /Users.
func (c *Client) Users(ctx context.Context) ([]User, error) {
	var out []User
	if c.emby {
		var page struct {
			Items []User `json:"Items"`
		}
		err := c.call(ctx, http.MethodGet, "/Users/Query", nil, nil, &page, true)
		if !errors.Is(err, ErrNotFound) {
			return page.Items, err
		}
	}
	err := c.call(ctx, http.MethodGet, "/Users", nil, nil, &out, true)
	return out, err
}

// AuthResult is a successful sign-in: the user and a token that acts as them. The token is
// a credential: use it (Logout at least) and drop it; never log or store it.
type AuthResult struct {
	User        User   `json:"User"`
	AccessToken string `json:"AccessToken"`
	ServerID    string `json:"ServerId"`
}

// AuthenticateByName signs a user in with their password. A wrong name or password is
// ErrUnauthorized. The password goes to the server in the body and nowhere else: not into
// an error, not into a log.
func (c *Client) AuthenticateByName(ctx context.Context, username, password string) (AuthResult, error) {
	if strings.TrimSpace(username) == "" {
		return AuthResult{}, errors.New("no username")
	}
	body := struct {
		Username string `json:"Username"`
		Pw       string `json:"Pw"`
	}{username, password}
	var r AuthResult
	if err := c.anon().call(ctx, http.MethodPost, "/Users/AuthenticateByName", nil, body, &r, false); err != nil {
		return AuthResult{}, err
	}
	if r.AccessToken == "" {
		return AuthResult{}, &Error{Op: "POST /Users/AuthenticateByName", Status: http.StatusOK, Detail: "no token in the answer"}
	}
	return r, nil
}

// anon is the client without its key: sign-in calls must not present the admin key, or the
// session they create would be filed under it.
func (c *Client) anon() *Client { return c.WithToken("") }

// QuickConnectState is one Quick Connect request. Code is what the person types into a
// signed-in Jellyfin app; Secret is what this side polls with — keep it to yourself.
type QuickConnectState struct {
	Secret        string    `json:"Secret"`
	Code          string    `json:"Code"`
	Authenticated bool      `json:"Authenticated"`
	DeviceID      string    `json:"DeviceId"`
	DeviceName    string    `json:"DeviceName"`
	AppName       string    `json:"AppName"`
	AppVersion    string    `json:"AppVersion"`
	DateAdded     time.Time `json:"DateAdded"`
}

// QuickConnectEnabled reports whether the server allows Quick Connect (Dashboard →
// General → "Enable Quick Connect"; on by default in 12.2). It needs no key.
//
// Emby has no Quick Connect: every Quick Connect call on an Emby client is ErrUnsupported,
// and this one is simply false.
func (c *Client) QuickConnectEnabled(ctx context.Context) (bool, error) {
	if c.emby {
		return false, nil
	}
	var on bool
	err := c.anon().call(ctx, http.MethodGet, "/QuickConnect/Enabled", nil, nil, &on, false)
	return on, err
}

// QuickConnectInitiate starts a Quick Connect request. With Quick Connect off it is
// ErrUnauthorized (12.2 answers 401).
func (c *Client) QuickConnectInitiate(ctx context.Context) (QuickConnectState, error) {
	var st QuickConnectState
	if c.emby {
		return st, ErrUnsupported
	}
	err := c.anon().call(ctx, http.MethodPost, "/QuickConnect/Initiate", nil, nil, &st, false)
	return st, err
}

// QuickConnectConnect polls a request: Authenticated turns true once someone approved the
// code. An expired or unknown secret is ErrNotFound.
func (c *Client) QuickConnectConnect(ctx context.Context, secret string) (QuickConnectState, error) {
	var st QuickConnectState
	if c.emby {
		return st, ErrUnsupported
	}
	if secret == "" {
		return st, errors.New("no quick connect secret")
	}
	err := c.anon().call(ctx, http.MethodGet, "/QuickConnect/Connect", url.Values{"secret": {secret}}, nil, &st, false)
	return st, err
}

// QuickConnectAuthorize approves a code as the user this client acts for (WithToken). It is
// what a Jellyfin app does when someone types the code in; Arrmada only needs it in tests.
func (c *Client) QuickConnectAuthorize(ctx context.Context, code string) error {
	if c.emby {
		return ErrUnsupported
	}
	if code == "" {
		return errors.New("no quick connect code")
	}
	var ok bool
	if err := c.call(ctx, http.MethodPost, "/QuickConnect/Authorize", url.Values{"code": {code}}, nil, &ok, true); err != nil {
		return err
	}
	if !ok {
		return &Error{Op: "POST /QuickConnect/Authorize", Status: http.StatusOK, Detail: "the code was not approved"}
	}
	return nil
}

// AuthenticateWithQuickConnect trades an approved request's secret for a sign-in.
func (c *Client) AuthenticateWithQuickConnect(ctx context.Context, secret string) (AuthResult, error) {
	if c.emby {
		return AuthResult{}, ErrUnsupported
	}
	if secret == "" {
		return AuthResult{}, errors.New("no quick connect secret")
	}
	body := struct {
		Secret string `json:"Secret"`
	}{secret}
	var r AuthResult
	if err := c.anon().call(ctx, http.MethodPost, "/Users/AuthenticateWithQuickConnect", nil, body, &r, false); err != nil {
		return AuthResult{}, err
	}
	if r.AccessToken == "" {
		return AuthResult{}, &Error{Op: "POST /Users/AuthenticateWithQuickConnect", Status: http.StatusOK, Detail: "no token in the answer"}
	}
	return r, nil
}

// Logout ends the session of the token this client carries (WithToken(userToken)), which
// revokes that token. Called with the API key it does nothing useful; don't.
func (c *Client) Logout(ctx context.Context) error {
	return c.call(ctx, http.MethodPost, "/Sessions/Logout", nil, nil, nil, true)
}
