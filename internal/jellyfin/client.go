package jellyfin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// The kinds of failure callers act on. Every error the client returns for a failed call is
// an *Error that wraps one of these (errors.Is works), or ErrNotConfigured on its own.
var (
	// ErrNotConfigured means no server URL is set, or a call that needs a key has none.
	ErrNotConfigured = errors.New("jellyfin is not configured")
	// ErrUnauthorized is Jellyfin refusing the key or token (HTTP 401): revoked, mistyped,
	// or a wrong password at sign-in. Retrying won't help.
	ErrUnauthorized = errors.New("jellyfin rejected the key or sign-in")
	// ErrForbidden is a valid token without the right (HTTP 403): a non-admin user's token
	// on an admin call, or Quick Connect switched off.
	ErrForbidden = errors.New("jellyfin refused the request")
	// ErrNotFound is HTTP 404: the item, user or Quick Connect request doesn't exist.
	ErrNotFound = errors.New("jellyfin has no such item")
	// ErrUnreachable is no answer at all: refused, timed out, DNS, TLS.
	ErrUnreachable = errors.New("jellyfin isn't reachable")
	// ErrUnsupported is a call the server kind doesn't offer at all (Quick Connect on Emby).
	// It is returned on its own, before anything is sent.
	ErrUnsupported = errors.New("this media server doesn't offer that")
)

// The server kinds the client speaks to. Emby is the server Jellyfin was forked from: the
// API is nearly the same, but it is reached under /emby, takes its credentials in the
// X-Emby-Authorization and X-Emby-Token headers, and differs in a few calls (see the
// package notes).
const (
	ServerJellyfin = "jellyfin"
	ServerEmby     = "emby"
)

// NormalizeServerKind is "emby" for Emby (any case) and "jellyfin" for anything else, so an
// unset or unknown value always means Jellyfin.
func NormalizeServerKind(kind string) string {
	if strings.EqualFold(strings.TrimSpace(kind), ServerEmby) {
		return ServerEmby
	}
	return ServerJellyfin
}

// ServerLabel is the product's name as people write it: "Jellyfin" or "Emby".
func ServerLabel(kind string) string {
	if NormalizeServerKind(kind) == ServerEmby {
		return "Emby"
	}
	return "Jellyfin"
}

// Error is one failed call. Its message names the call (method and path, never the query,
// which can carry a Quick Connect secret) and what went wrong in plain words. It never holds
// the API key, a token or a password.
type Error struct {
	Op     string // "GET /Items"
	Status int    // HTTP status; 0 when there was no answer
	Kind   error  // one of the Err* values above, or nil for any other status
	Detail string // the network failure, or a short server message
}

func (e *Error) Error() string {
	var b strings.Builder
	switch {
	case e.Kind == ErrUnreachable:
		b.WriteString(ErrUnreachable.Error())
	case e.Kind != nil:
		b.WriteString(e.Kind.Error())
		fmt.Fprintf(&b, " (HTTP %d)", e.Status)
	default:
		fmt.Fprintf(&b, "jellyfin returned HTTP %d", e.Status)
	}
	if e.Op != "" {
		b.WriteString(" on " + e.Op)
	}
	if e.Detail != "" {
		b.WriteString(": " + e.Detail)
	}
	return b.String()
}

func (e *Error) Unwrap() error { return e.Kind }

// Client talks to one Jellyfin server. A Client is safe for concurrent use; WithToken
// derives one that acts as a signed-in user instead of with the API key.
type Client struct {
	emby     bool // an Emby server rather than Jellyfin (NewFor)
	base     string
	token    string // the API key, or a user's access token
	deviceID string
	version  string
	http     *http.Client
}

// ClientName is how Arrmada names itself to Jellyfin: the dashboard's device list and
// session list show it.
const ClientName = "Arrmada"

// New builds a client for the server at baseURL using an API key (Dashboard → API Keys).
// deviceID should be stable per Arrmada install: Jellyfin files sessions and tokens under
// it, and signing in again from the same device replaces that user's earlier token there.
// version is Arrmada's build. hc may be nil for the default client (15 s per call).
func New(baseURL, apiKey, deviceID, version string, hc *http.Client) *Client {
	if hc == nil {
		hc = defaultHTTPClient()
	}
	if deviceID == "" {
		deviceID = "arrmada"
	}
	if version == "" {
		version = "0"
	}
	return &Client{
		base:     strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		token:    apiKey,
		deviceID: deviceID,
		version:  version,
		http:     hc,
	}
}

// NewFor is New for a server of the given kind (ServerJellyfin or ServerEmby; anything
// else is Jellyfin). New is NewFor(ServerJellyfin, …).
func NewFor(kind, baseURL, apiKey, deviceID, version string, hc *http.Client) *Client {
	c := New(baseURL, apiKey, deviceID, version, hc)
	c.emby = NormalizeServerKind(kind) == ServerEmby
	return c
}

// Kind is the server kind the client speaks to (ServerJellyfin or ServerEmby).
func (c *Client) Kind() string {
	if c.emby {
		return ServerEmby
	}
	return ServerJellyfin
}

// IsEmby reports whether the client speaks to an Emby server.
func (c *Client) IsEmby() bool { return c.emby }

// apiBase is where API paths go: the address itself for Jellyfin; for Emby the address
// plus /emby (the API root Emby documents), unless the saved address already ends in it.
func (c *Client) apiBase() string {
	if !c.emby || c.base == "" {
		return c.base
	}
	if strings.HasSuffix(strings.ToLower(c.base), "/emby") {
		return c.base
	}
	return c.base + "/emby"
}

// setAuth puts the credentials and device fields on a request the way the server reads
// them: Jellyfin's Authorization: MediaBrowser header, or Emby's X-Emby-Authorization
// header plus X-Emby-Token when there is a token.
func (c *Client) setAuth(req *http.Request) {
	if !c.emby {
		req.Header.Set("Authorization", c.authorization())
		return
	}
	req.Header.Set("X-Emby-Authorization", c.authorization())
	if c.token != "" {
		req.Header.Set("X-Emby-Token", c.token)
	}
}

// defaultHTTPClient fails fast on a dead address (5 s to connect) but gives a big library
// page time to arrive.
func defaultHTTPClient() *http.Client {
	tr := &http.Transport{Proxy: http.ProxyFromEnvironment}
	if dt, ok := http.DefaultTransport.(*http.Transport); ok {
		tr = dt.Clone()
	}
	tr.DialContext = (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext
	tr.TLSHandshakeTimeout = 10 * time.Second
	tr.ResponseHeaderTimeout = 15 * time.Second
	tr.MaxIdleConnsPerHost = 4
	return &http.Client{Timeout: 30 * time.Second, Transport: tr}
}

// WithToken returns a client that acts as the user who owns token (from AuthenticateByName
// or Quick Connect) — for Logout, or anything else that must run as that user.
func (c *Client) WithToken(token string) *Client {
	cp := *c
	cp.token = token
	return &cp
}

// BaseURL is the server address the client talks to (no trailing slash).
func (c *Client) BaseURL() string { return c.base }

// authorization is the MediaBrowser header. Jellyfin reads the device fields even without a
// token (sign-in, Quick Connect): a token it issues is tied to that device.
func (c *Client) authorization() string {
	var b strings.Builder
	if c.emby {
		b.WriteString("Emby ")
	} else {
		b.WriteString("MediaBrowser ")
	}
	if c.token != "" {
		b.WriteString(`Token="` + headerValue(c.token) + `", `)
	}
	b.WriteString(`Client="` + ClientName + `", Device="` + ClientName + `", DeviceId="` + headerValue(c.deviceID) +
		`", Version="` + headerValue(c.version) + `"`)
	return b.String()
}

// headerValue keeps a value from breaking out of its quotes in the header. Jellyfin
// URL-decodes each value, so escaping is lossless.
func headerValue(s string) string {
	if !strings.ContainsAny(s, "\",\\\r\n") {
		return s
	}
	return url.QueryEscape(s)
}

// call sends one request and decodes a JSON answer into out (nil to discard it). needKey is
// false only for the calls Jellyfin answers without a token (public info, sign-in, Quick
// Connect's own steps).
func (c *Client) call(ctx context.Context, method, path string, query url.Values, body, out any, needKey bool) error {
	if c.base == "" || (needKey && c.token == "") {
		return ErrNotConfigured
	}
	op := method + " " + path
	u := c.apiBase() + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	var rd io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return &Error{Op: op, Detail: "could not encode the request"}
		}
		rd = bytes.NewReader(buf)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rd)
	if err != nil {
		return &Error{Op: op, Kind: ErrNotConfigured, Detail: "the server address isn't a valid URL"}
	}
	req.Header.Set("Accept", "application/json")
	c.setAuth(req)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return &Error{Op: op, Kind: ErrUnreachable, Detail: netDetail(err)}
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		// Read a little so the connection is reused, and keep a short message for the
		// log. Jellyfin's error bodies are plain text or problem+json, never an echo of
		// the credentials we sent.
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return statusError(op, resp.StatusCode, msg)
	}
	if out == nil {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return &Error{Op: op, Status: resp.StatusCode, Detail: "unexpected answer: " + err.Error()}
	}
	return nil
}

func statusError(op string, status int, body []byte) *Error {
	e := &Error{Op: op, Status: status}
	switch status {
	case http.StatusUnauthorized:
		e.Kind = ErrUnauthorized
	case http.StatusForbidden:
		e.Kind = ErrForbidden
	case http.StatusNotFound:
		e.Kind = ErrNotFound
	default:
		e.Detail = shortMessage(body)
	}
	return e
}

// shortMessage pulls a one-line reason out of an error body: problem+json's title, else
// the first line of plain text, capped.
func shortMessage(body []byte) string {
	var p struct {
		Title string `json:"title"`
	}
	s := strings.TrimSpace(string(body))
	if json.Unmarshal(body, &p) == nil && p.Title != "" {
		s = p.Title
	}
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = s[:i]
	}
	if len(s) > 160 {
		s = s[:160] + "…"
	}
	return s
}

// netDetail is the network failure without the URL that *url.Error prepends (it would
// carry a Quick Connect secret from the query).
func netDetail(err error) string {
	var ue *url.Error
	if errors.As(err, &ue) {
		err = ue.Err
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timed out"
	}
	if errors.Is(err, context.Canceled) {
		return "canceled"
	}
	return err.Error()
}

// SystemInfo is what the server says about itself. /System/Info/Public fills only the
// identity fields; /System/Info (needs the key) fills the rest.
type SystemInfo struct {
	ID                     string `json:"Id"`
	ServerName             string `json:"ServerName"`
	Version                string `json:"Version"`
	ProductName            string `json:"ProductName"`
	OperatingSystem        string `json:"OperatingSystem"`
	LocalAddress           string `json:"LocalAddress"`
	StartupWizardCompleted bool   `json:"StartupWizardCompleted"`
	HasPendingRestart      bool   `json:"HasPendingRestart"`
	IsShuttingDown         bool   `json:"IsShuttingDown"`
	HasUpdateAvailable     bool   `json:"HasUpdateAvailable"`
}

// SystemInfo validates the key and returns the server's identity (Id is what watch links
// carry as serverId).
func (c *Client) SystemInfo(ctx context.Context) (SystemInfo, error) {
	var si SystemInfo
	err := c.call(ctx, http.MethodGet, "/System/Info", nil, nil, &si, true)
	return si, err
}

// PublicSystemInfo needs no key: it tells a Jellyfin server apart from anything else at the
// address before a key is entered.
func (c *Client) PublicSystemInfo(ctx context.Context) (SystemInfo, error) {
	var si SystemInfo
	err := c.call(ctx, http.MethodGet, "/System/Info/Public", nil, nil, &si, false)
	return si, err
}

// ticksPerSecond: Jellyfin positions and runtimes are .NET ticks (100 ns).
const ticksPerSecond = 10_000_000

// TicksToDuration converts a Jellyfin tick count to a duration.
func TicksToDuration(ticks int64) time.Duration { return time.Duration(ticks) * 100 }

// DurationToTicks converts a duration to Jellyfin ticks.
func DurationToTicks(d time.Duration) int64 { return int64(d / 100) }

// ProviderIDs are the outside ids Jellyfin matched an item to, from its ProviderIds map
// (keys "Tmdb", "Tvdb", "Imdb"; 0 / "" when absent). Raw keeps the whole map.
type ProviderIDs struct {
	TMDB int
	TVDB int
	IMDB string
	Raw  map[string]string
}

func (p *ProviderIDs) UnmarshalJSON(b []byte) error {
	var m map[string]string
	if err := json.Unmarshal(b, &m); err != nil {
		// A provider map with a non-string value isn't fatal to the page it came in.
		m = nil
	}
	*p = providerIDs(m)
	return nil
}

func providerIDs(m map[string]string) ProviderIDs {
	p := ProviderIDs{Raw: m}
	for k, v := range m {
		v = strings.TrimSpace(v)
		switch strings.ToLower(k) {
		case "tmdb":
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				p.TMDB = n
			}
		case "tvdb":
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				p.TVDB = n
			}
		case "imdb":
			if strings.HasPrefix(v, "tt") {
				p.IMDB = v
			}
		}
	}
	return p
}
