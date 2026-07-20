// Package apikeys manages the external service credentials Arrmada uses — letting them be
// set inside the app rather than only through environment variables at install time.
//
// The resolution order is settings-first, env-fallback: a value saved in the settings menu
// wins, and an env var supplied at install still works as a default. This means an already-
// installed instance can add a key from the UI without editing its compose file or
// restarting, while existing env-based setups keep working untouched.
//
// Nothing here ever returns a full secret to a caller that would send it to the browser —
// Status reports only whether a key is set, where from, and a short hint.
package apikeys

import (
	"context"
	"os"
	"strings"
)

// Key describes one credential: what it's for, how to get it, and where it's read from.
type Key struct {
	ID      string `json:"id"`       // stable identifier used by the API ("tvdb")
	Label   string `json:"label"`    // shown in the settings UI ("TheTVDB")
	Purpose string `json:"purpose"`  // one line: what breaks without it
	HelpURL string `json:"help_url"` // where the user obtains it
	Steps   string `json:"steps"`    // brief how-to, shown under the field
	EnvVar  string `json:"env_var"`  // install-time fallback
	Secret  bool   `json:"secret"`   // masked in the UI (a username is not)
}

// settingKey is where a credential is persisted. Namespaced so it can't collide with an
// ordinary preference.
func (k Key) settingKey() string { return "apikey:" + k.ID }

// Catalog is every credential Arrmada knows how to use, in display order.
var Catalog = []Key{
	{
		ID: "tmdb", Label: "TMDB", Purpose: "Movie and TV metadata, artwork, and discovery. Required.",
		HelpURL: "https://www.themoviedb.org/settings/api", EnvVar: "ARRMADA_TMDB_API_KEY", Secret: true,
		Steps: "Create a free account, then request an API key under Settings → API. Use the v3 key.",
	},
	{
		ID: "tvdb", Label: "TheTVDB", Purpose: "Anime episode numbering. Optional — anime falls back to TMDB without it.",
		HelpURL: "https://www.thetvdb.com/dashboard/account/apikey", EnvVar: "ARRMADA_TVDB_API_KEY", Secret: true,
		Steps: "Create an account, then generate a v4 API key. Personal/non-commercial use is free (requires attribution).",
	},
	{
		ID: "omdb", Label: "OMDb", Purpose: "IMDb, Rotten Tomatoes and Metacritic scores. Optional.",
		HelpURL: "https://www.omdbapi.com/apikey.aspx", EnvVar: "ARRMADA_OMDB_API_KEY", Secret: true,
		Steps: "Request a free key; it arrives by email and needs one click to activate.",
	},
	{
		ID: "opensubtitles_api", Label: "OpenSubtitles API key", Purpose: "Subtitle search. Optional.",
		HelpURL: "https://www.opensubtitles.com/en/consumers", EnvVar: "ARRMADA_OPENSUBTITLES_API_KEY", Secret: true,
		Steps: "Register a consumer under your account to get an API key. Downloading also needs the username and password below.",
	},
	{
		ID: "opensubtitles_username", Label: "OpenSubtitles username", Purpose: "Needed to download subtitles, not just search.",
		HelpURL: "https://www.opensubtitles.com", EnvVar: "ARRMADA_OPENSUBTITLES_USERNAME", Secret: false,
		Steps: "Your opensubtitles.com account username.",
	},
	{
		ID: "opensubtitles_password", Label: "OpenSubtitles password", Purpose: "Needed to download subtitles, not just search.",
		HelpURL: "https://www.opensubtitles.com", EnvVar: "ARRMADA_OPENSUBTITLES_PASSWORD", Secret: true,
		Steps: "Your opensubtitles.com account password.",
	},
}

func keyByID(id string) (Key, bool) {
	for _, k := range Catalog {
		if k.ID == id {
			return k, true
		}
	}
	return Key{}, false
}

// SettingStore is the persistence the Store needs — satisfied by settings.Service.
type SettingStore interface {
	Get(ctx context.Context, key, def string) string
	Set(ctx context.Context, key, value string) error
}

// Store resolves and persists credentials.
type Store struct{ s SettingStore }

// NewStore wires the store over the settings persistence.
func NewStore(s SettingStore) *Store { return &Store{s: s} }

// Value returns the effective credential: the saved value if present, else the env var,
// else "". Trimmed, so a stray newline in a pasted key doesn't break auth.
func (s *Store) Value(ctx context.Context, id string) string {
	k, ok := keyByID(id)
	if !ok {
		return ""
	}
	if v := strings.TrimSpace(s.s.Get(ctx, k.settingKey(), "")); v != "" {
		return v
	}
	return strings.TrimSpace(os.Getenv(k.EnvVar))
}

// Func returns a resolver bound to this id, for injecting into a client that reads its key
// lazily. Uses a background context — credential reads are quick and request-independent.
func (s *Store) Func(id string) func() string {
	return func() string { return s.Value(context.Background(), id) }
}

// Set saves a credential. An empty value clears it (falling back to the env var, if any).
func (s *Store) Set(ctx context.Context, id, value string) error {
	k, ok := keyByID(id)
	if !ok {
		return nil
	}
	return s.s.Set(ctx, k.settingKey(), strings.TrimSpace(value))
}

// KeyStatus is the browser-safe view of a credential — never the value itself.
type KeyStatus struct {
	Key
	Configured bool   `json:"configured"`
	Source     string `json:"source"`         // "settings" | "env" | ""
	Hint       string `json:"hint,omitempty"` // last 4 chars of a secret, or the whole username
}

// Status reports every credential's state for the settings UI, without exposing secrets.
func (s *Store) Status(ctx context.Context) []KeyStatus {
	out := make([]KeyStatus, 0, len(Catalog))
	for _, k := range Catalog {
		st := KeyStatus{Key: k}
		if v := strings.TrimSpace(s.s.Get(ctx, k.settingKey(), "")); v != "" {
			st.Configured, st.Source, st.Hint = true, "settings", hint(v, k.Secret)
		} else if v := strings.TrimSpace(os.Getenv(k.EnvVar)); v != "" {
			st.Configured, st.Source, st.Hint = true, "env", hint(v, k.Secret)
		}
		out = append(out, st)
	}
	return out
}

// hint shows enough to recognise a value without revealing it: the last 4 characters of a
// secret ("…a150b"), or a non-secret (a username) in full.
func hint(v string, secret bool) string {
	if !secret {
		return v
	}
	if len(v) <= 4 {
		return "…"
	}
	return "…" + v[len(v)-4:]
}
