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
)

// Service owns the Plex connection config, the poller/recorder, and history queries.
type Service struct {
	settings *settings.Service
	geo      *geoip.Resolver
	repo     *repo
	bus      *eventbus.Bus
	log      *slog.Logger

	live map[string]*liveSession // in-flight sessions (poller goroutine only)
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
	URL         string `json:"url"`
	TokenSet    bool   `json:"token_set"`
	Enabled     bool   `json:"enabled"`
	PollSeconds int    `json:"poll_seconds"`
}

// Config returns the current connection settings.
func (s *Service) Config(ctx context.Context) Config {
	return Config{
		URL:         s.settings.Get(ctx, keyURL, ""),
		TokenSet:    s.settings.Get(ctx, keyToken, "") != "",
		Enabled:     s.settings.GetBool(ctx, keyEnabled, false),
		PollSeconds: s.pollSeconds(ctx),
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
	return n
}

// SetConfig persists connection settings. An empty token leaves the stored one untouched (so the
// UI can save other fields without re-entering the secret).
func (s *Service) SetConfig(ctx context.Context, url string, token *string, enabled *bool, poll *int) error {
	if err := s.settings.Set(ctx, keyURL, url); err != nil {
		return err
	}
	if token != nil && *token != "" {
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

// StartPlexAuth begins a Plex sign-in and returns the PIN id + the URL to open.
func (s *Service) StartPlexAuth(ctx context.Context) (PlexAuth, error) {
	cid := s.clientID(ctx)
	pin, err := plex.RequestPIN(ctx, cid, plexProduct)
	if err != nil {
		return PlexAuth{}, err
	}
	return PlexAuth{ID: pin.ID, AuthURL: plex.AuthURL(cid, pin.Code, plexProduct)}, nil
}

// PollPlexAuth checks whether the user has authorized the sign-in. On success it
// stores the token and, if no URL is set yet, auto-discovers the server URL.
func (s *Service) PollPlexAuth(ctx context.Context, pinID int) (bool, error) {
	cid := s.clientID(ctx)
	token, err := plex.CheckPIN(ctx, cid, pinID)
	if err != nil {
		return false, err
	}
	if token == "" {
		return false, nil // still pending
	}
	if err := s.settings.Set(ctx, keyToken, token); err != nil {
		return false, err
	}
	if s.settings.Get(ctx, keyURL, "") == "" {
		if url, e := plex.DiscoverServer(ctx, cid, token); e == nil && url != "" {
			_ = s.settings.Set(ctx, keyURL, url)
		}
	}
	return true, nil
}

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
	OK        bool           `json:"ok"`
	Error     string         `json:"error,omitempty"`
	MachineID string         `json:"machine_id,omitempty"`
	Version   string         `json:"version,omitempty"`
	Libraries []plex.Library `json:"libraries,omitempty"`
}

// Test validates a connection. If url/token are provided they're tested directly (before saving);
// otherwise the stored config is used.
func (s *Service) Test(ctx context.Context, url, token string) TestResult {
	if url == "" {
		url = s.settings.Get(ctx, keyURL, "")
	}
	if token == "" {
		token = s.settings.Get(ctx, keyToken, "")
	}
	c := plex.New(url, token)
	id, err := c.Identity(ctx)
	if err != nil {
		return TestResult{OK: false, Error: err.Error()}
	}
	libs, _ := c.Libraries(ctx) // best-effort; connection already proven
	return TestResult{OK: true, MachineID: id.MachineIdentifier, Version: id.Version, Libraries: libs}
}
