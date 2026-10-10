package insights

import (
	"context"
	"strconv"

	"github.com/tristenlammi/arrmada/internal/plex"
)

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

// configured: a URL and a token are both saved.
func (s *Service) configured(ctx context.Context) bool {
	return s.settings.Get(ctx, keyURL, "") != "" && s.settings.Get(ctx, keyToken, "") != ""
}

// enableIfUnset turns monitoring on the first time Plex is fully connected. Once anyone
// has switched it on or off, that choice stands: a later sign-in never overrides it.
func (s *Service) enableIfUnset(ctx context.Context) {
	if s.settings.Get(ctx, keyEnabled, "") != "" || !s.configured(ctx) {
		return
	}
	if err := s.settings.SetBool(ctx, keyEnabled, true); err != nil {
		s.log.Warn("insights: could not switch monitoring on", "err", err)
	}
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

// refreshIdentity stores the saved server's machine id and name, or clears them when it
// doesn't answer (a stale id would gate Plex sign-in against the wrong server).
func (s *Service) refreshIdentity(ctx context.Context) {
	id, name, err := s.probe(ctx, s.settings.Get(ctx, keyURL, ""), s.settings.Get(ctx, keyToken, ""))
	if err != nil {
		id, name = "", ""
	}
	s.saveIdentity(ctx, id, name)
}

func (s *Service) saveIdentity(ctx context.Context, machineID, name string) {
	if err := s.settings.Set(ctx, KeyMachineID, machineID); err != nil {
		s.log.Warn("insights: could not save the Plex server id", "err", err)
	}
	_ = s.settings.Set(ctx, keyServerName, name)
}

// probe asks a server who it is, within probeTimeout: its machine id, and its name
// (best-effort — an unnamed server still counts as answering).
func (s *Service) probe(ctx context.Context, url, token string) (machineID, name string, err error) {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	c := plex.New(url, token)
	id, err := c.Identity(ctx)
	if err != nil {
		return "", "", err
	}
	name, _ = c.ServerName(ctx)
	return id.MachineIdentifier, name, nil
}

// ServerChoice is one owned server that answered during sign-in, for the UI's picker when
// there is more than one.
type ServerChoice struct {
	Name      string `json:"name"`
	MachineID string `json:"machine_id"`
	URL       string `json:"url"`
}

// PlexAuthResult is a sign-in poll's answer. Authorized with ServerName set: connected.
// Authorized with Choices: several servers answered and the person picks one. Authorized
// with neither: the token is saved but no server could be reached, so the URL is theirs
// to type.
type PlexAuthResult struct {
	Authorized bool           `json:"authorized"`
	ServerName string         `json:"server_name,omitempty"`
	Choices    []ServerChoice `json:"choices,omitempty"`
}

// PollPlexAuth checks whether the user has authorized the sign-in. On success it stores
// the token, then makes sure the saved URL is a server that answers: the stored one if it
// still does, else the owned server plex.tv lists that answers (with the machine id
// plex.tv gave for it). A friend's server shared with the account is never picked.
// Monitoring is switched on unless someone already chose.
func (s *Service) PollPlexAuth(ctx context.Context, pinID int) (PlexAuthResult, error) {
	cid := s.clientID(ctx)
	token, err := plex.CheckPIN(ctx, cid, pinID)
	if err != nil {
		return PlexAuthResult{}, err
	}
	if token == "" {
		return PlexAuthResult{}, nil // still pending
	}
	if err := s.settings.Set(ctx, keyToken, token); err != nil {
		return PlexAuthResult{}, err
	}
	res := PlexAuthResult{Authorized: true}
	cands, listErr := plex.DiscoverServers(ctx, cid, token)

	// The saved URL stays when it still answers and is a server this account owns (or
	// plex.tv can't be asked right now, so ownership can't be checked either way).
	if url := s.settings.Get(ctx, keyURL, ""); url != "" {
		if id, name, err := s.probe(ctx, url, token); err == nil && (listErr != nil || ownsMachine(cands, id)) {
			s.saveIdentity(ctx, id, name)
			s.enableIfUnset(ctx)
			res.ServerName = name
			return res, nil
		}
	}
	if listErr != nil {
		s.log.Warn("insights: could not list your Plex servers after sign-in", "err", listErr)
		return res, nil
	}
	var answering []ServerChoice
	for _, c := range cands {
		for _, conn := range c.Conns {
			id, name, err := s.probe(ctx, conn.URL(), token)
			if err != nil || id != c.MachineID {
				continue // dead, or something else answering at that address
			}
			if name == "" {
				name = c.Name
			}
			answering = append(answering, ServerChoice{Name: name, MachineID: id, URL: conn.URL()})
			break
		}
	}
	switch len(answering) {
	case 0:
		s.log.Warn("insights: signed in with Plex, but none of your servers answered", "servers", len(cands))
	case 1:
		ch := answering[0]
		if err := s.settings.Set(ctx, keyURL, ch.URL); err != nil {
			return res, err
		}
		s.saveIdentity(ctx, ch.MachineID, ch.Name)
		s.enableIfUnset(ctx)
		res.ServerName = ch.Name
	default:
		res.Choices = answering
	}
	return res, nil
}

func ownsMachine(cands []plex.ServerCandidate, machineID string) bool {
	for _, c := range cands {
		if c.MachineID == machineID {
			return true
		}
	}
	return false
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
