// Package insights is Arrmada's Plex watch-monitoring module (the Tautulli replacement). This
// first slice (I0) handles the Plex connection: storing the server URL + token and validating
// them. The live-activity view, poller/recorder, stats, graphs, and buffering-history build on
// top of this.
package insights

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"log/slog"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/tristenlammi/arrmada/internal/eventbus"
	"github.com/tristenlammi/arrmada/internal/geoip"
	"github.com/tristenlammi/arrmada/internal/plex"
	"github.com/tristenlammi/arrmada/internal/settings"
)

const (
	keyURL      = "insights_plex_url"
	keyToken    = "insights_plex_token"
	keyEnabled  = "insights_enabled"
	keyPoll     = "insights_poll_seconds"
	keyClientID = "insights_plex_client_id" // stable X-Plex-Client-Identifier for sign-in
	plexProduct = "Arrmada"
	// The connected server's machine identifier and name, from its own /identity and root
	// endpoint: stored on sign-in, on a save that changes the URL or token, and on a Test of
	// the saved connection. Plex sign-in gates on the machine id, and app.plex.tv deep
	// links need it.
	KeyMachineID  = "insights_plex_machine_id"
	keyServerName = "insights_plex_server_name"
	// probeTimeout bounds each connection tried while finding the server, so a dead
	// address (a LAN IP from another network, a firewalled remote) costs seconds, not 15.
	probeTimeout = 3 * time.Second
)

// Service owns the Plex connection config, the poller/recorder, and history queries.
type Service struct {
	settings *settings.Service
	geo      *geoip.Resolver
	repo     *repo
	bus      *eventbus.Bus
	log      *slog.Logger

	live map[string]*liveSession // in-flight sessions (poller goroutine only)
	// ownerLookupAt rate-limits the plex.tv account lookup. Poller-goroutine only, same
	// as live — nothing else touches it.
	ownerLookupAt time.Time

	// playing is how many streams were playing at the last successful poll, and playingAt
	// when that was — read from other goroutines (Convert pauses while someone watches).
	playing   atomic.Int32
	playingAt atomic.Int64

	// health is how polls (or identity probes) have been going, for the health panel.
	health pollHealth
}

// Watching reports whether anyone is playing something on Plex right now. A paused stream
// doesn't count, and neither does a reading older than a few minutes (Plex unreachable, or
// monitoring switched off) — then nobody is known to be watching.
func (s *Service) Watching() bool {
	at := s.playingAt.Load()
	if at == 0 || time.Since(time.Unix(at, 0)) > 3*time.Minute {
		return false
	}
	return s.playing.Load() > 0
}

// NewService wires the module. geo may be nil (geolocation then only flags LAN as "Local").
func NewService(db *sql.DB, set *settings.Service, geo *geoip.Resolver, bus *eventbus.Bus, log *slog.Logger) *Service {
	if geo == nil {
		geo = geoip.New("")
	}
	return &Service{settings: set, geo: geo, repo: &repo{db: db}, bus: bus, log: log, live: map[string]*liveSession{}}
}

// Config is the connection config exposed to the UI (token is never returned in full).
type Config struct {
	URL      string `json:"url"`
	TokenSet bool   `json:"token_set"`
	Enabled  bool   `json:"enabled"`
	// EnabledSet: someone has chosen monitoring on or off. Until then the form starts
	// with it on, since connecting Plex without recording anything is never the point.
	EnabledSet  bool   `json:"enabled_set"`
	PollSeconds int    `json:"poll_seconds"`
	ServerName  string `json:"server_name,omitempty"`
	MachineID   string `json:"machine_id,omitempty"`
}

// Config returns the current connection settings.
func (s *Service) Config(ctx context.Context) Config {
	return Config{
		URL:         s.settings.Get(ctx, keyURL, ""),
		TokenSet:    s.settings.Get(ctx, keyToken, "") != "",
		Enabled:     s.settings.GetBool(ctx, keyEnabled, false),
		EnabledSet:  s.settings.Get(ctx, keyEnabled, "") != "",
		PollSeconds: s.pollSeconds(ctx),
		ServerName:  s.settings.Get(ctx, keyServerName, ""),
		MachineID:   s.settings.Get(ctx, KeyMachineID, ""),
	}
}

func (s *Service) pollSeconds(ctx context.Context) int {
	n := 5
	if v := s.settings.Get(ctx, keyPoll, ""); v != "" {
		if p, err := strconv.Atoi(v); err == nil && p > 0 {
			n = p
		}
	}
	if n < 2 {
		n = 2 // don't hammer the server
	}
	if n > 60 {
		n = 60 // cap the sampling period: wider than this makes every duration/skew bound too coarse
	}
	return n
}

// SetConfig persists connection settings. An empty token leaves the stored one untouched (so the
// UI can save other fields without re-entering the secret). A save that changes the URL or
// token re-reads the server's identity, so the stored machine id and name never describe a
// different server than the one saved.
func (s *Service) SetConfig(ctx context.Context, url string, token *string, enabled *bool, poll *int) error {
	changed := url != s.settings.Get(ctx, keyURL, "")
	if err := s.settings.Set(ctx, keyURL, url); err != nil {
		return err
	}
	if token != nil && *token != "" {
		changed = changed || *token != s.settings.Get(ctx, keyToken, "")
		if err := s.settings.Set(ctx, keyToken, *token); err != nil {
			return err
		}
	}
	if enabled != nil {
		if err := s.settings.SetBool(ctx, keyEnabled, *enabled); err != nil {
			return err
		}
	}
	if poll != nil {
		if err := s.settings.Set(ctx, keyPoll, strconv.Itoa(*poll)); err != nil {
			return err
		}
	}
	s.enableIfUnset(ctx)
	if changed {
		s.refreshIdentity(ctx)
	}
	return nil
}

// clientID returns the stable X-Plex-Client-Identifier for this install, generating
// and persisting one on first use (Plex ties the sign-in PIN to it).
func (s *Service) clientID(ctx context.Context) string {
	if id := s.settings.Get(ctx, keyClientID, ""); id != "" {
		return id
	}
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	id := hex.EncodeToString(b)
	_ = s.settings.Set(ctx, keyClientID, id)
	return id
}

// PlexAuth is a started sign-in the UI opens + polls.
type PlexAuth struct {
	ID      int    `json:"id"`
	AuthURL string `json:"auth_url"`
}

// StartPlexAuth begins a Plex sign-in and returns the PIN id + the URL to open. forward,
// when not nil, gives the address plex.tv should send the browser back to for that PIN
// (the full-page redirect used when no popup can open).
func (s *Service) StartPlexAuth(ctx context.Context, forward func(pinID int) string) (PlexAuth, error) {
	cid := s.clientID(ctx)
	pin, err := plex.RequestPIN(ctx, cid, plexProduct)
	if err != nil {
		return PlexAuth{}, err
	}
	back := ""
	if forward != nil {
		back = forward(pin.ID)
	}
	return PlexAuth{ID: pin.ID, AuthURL: plex.AuthURL(cid, pin.Code, plexProduct, back)}, nil
}

// PollPlexAuth (signing in finds and checks the server) is in connect.go.

// SeedFromEnv stores a URL/token supplied via env on startup, but only for fields not already
// set in the DB — so the UI stays the source of truth once the admin edits it there.
func (s *Service) SeedFromEnv(ctx context.Context, url, token string) {
	if url != "" && s.settings.Get(ctx, keyURL, "") == "" {
		_ = s.settings.Set(ctx, keyURL, url)
	}
	if token != "" && s.settings.Get(ctx, keyToken, "") == "" {
		_ = s.settings.Set(ctx, keyToken, token)
	}
}

// client builds a Plex client from the stored config (or the values under test).
func (s *Service) client(ctx context.Context) *plex.Client {
	return plex.New(s.settings.Get(ctx, keyURL, ""), s.settings.Get(ctx, keyToken, ""))
}

// TestResult reports whether a connection works, plus a quick server summary.
type TestResult struct {
	OK         bool           `json:"ok"`
	Error      string         `json:"error,omitempty"`
	MachineID  string         `json:"machine_id,omitempty"`
	ServerName string         `json:"server_name,omitempty"`
	Version    string         `json:"version,omitempty"`
	Libraries  []plex.Library `json:"libraries,omitempty"`
}

// Test validates a connection. If url/token are provided they're tested directly (before saving);
// otherwise the stored config is used. A passing test of the saved connection also refreshes
// the stored machine id and server name.
func (s *Service) Test(ctx context.Context, url, token string) TestResult {
	savedURL, savedToken := s.settings.Get(ctx, keyURL, ""), s.settings.Get(ctx, keyToken, "")
	if url == "" {
		url = savedURL
	}
	if token == "" {
		token = savedToken
	}
	c := plex.New(url, token)
	id, err := c.Identity(ctx)
	if err != nil {
		return TestResult{OK: false, Error: err.Error()}
	}
	name, _ := c.ServerName(ctx)
	if url == savedURL && token == savedToken {
		s.saveIdentity(ctx, id.MachineIdentifier, name)
	}
	libs, _ := c.Libraries(ctx) // best-effort; connection already proven
	return TestResult{OK: true, MachineID: id.MachineIdentifier, ServerName: name, Version: id.Version, Libraries: libs}
}
