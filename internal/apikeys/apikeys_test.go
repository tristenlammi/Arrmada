package apikeys

import (
	"context"
	"testing"
)

// memStore is an in-memory SettingStore for tests.
type memStore map[string]string

func (m memStore) Get(_ context.Context, k, def string) string {
	if v, ok := m[k]; ok {
		return v
	}
	return def
}
func (m memStore) Set(_ context.Context, k, v string) error { m[k] = v; return nil }

// A key set in settings wins over the env var; with neither, it's unconfigured. This is
// the whole point: an already-installed instance can add a key from the UI without
// touching its compose file.
func TestResolutionPrefersSettingsThenEnv(t *testing.T) {
	ctx := context.Background()
	st := memStore{}
	s := NewStore(st)

	if s.Value(ctx, "tvdb") != "" {
		t.Error("unconfigured key should resolve to empty")
	}

	t.Setenv("ARRMADA_TVDB_API_KEY", "from-env")
	if got := s.Value(ctx, "tvdb"); got != "from-env" {
		t.Errorf("with only env set, got %q, want the env value", got)
	}

	if err := s.Set(ctx, "tvdb", "from-settings"); err != nil {
		t.Fatal(err)
	}
	if got := s.Value(ctx, "tvdb"); got != "from-settings" {
		t.Errorf("settings must win over env, got %q", got)
	}

	// Clearing the settings value falls back to env, not to empty.
	if err := s.Set(ctx, "tvdb", ""); err != nil {
		t.Fatal(err)
	}
	if got := s.Value(ctx, "tvdb"); got != "from-env" {
		t.Errorf("after clearing, should fall back to env, got %q", got)
	}
}

// Values are trimmed, so a newline pasted with a key doesn't silently break auth.
func TestValueIsTrimmed(t *testing.T) {
	s := NewStore(memStore{})
	_ = s.Set(context.Background(), "tmdb", "  abc123\n")
	if got := s.Value(context.Background(), "tmdb"); got != "abc123" {
		t.Errorf("got %q, want trimmed", got)
	}
}

// Func binds a resolver for injecting into a client that reads its key lazily — so a key
// added later takes effect without reconstructing the client.
func TestFuncReadsLatestValue(t *testing.T) {
	s := NewStore(memStore{})
	f := s.Func("omdb")
	if f() != "" {
		t.Error("resolver should start empty")
	}
	_ = s.Set(context.Background(), "omdb", "later")
	if f() != "later" {
		t.Error("resolver must observe a value set after it was created")
	}
}

// Status is what the browser sees. It must report state and a hint but never the secret.
func TestStatusNeverLeaksSecrets(t *testing.T) {
	ctx := context.Background()
	st := memStore{}
	s := NewStore(st)
	_ = s.Set(ctx, "tvdb", "d4e9978a-76f9-4601-a072-8c42585d150b")
	_ = s.Set(ctx, "opensubtitles_username", "alice")

	byID := map[string]KeyStatus{}
	for _, k := range s.Status(ctx) {
		byID[k.ID] = k
	}

	tvdb := byID["tvdb"]
	if !tvdb.Configured || tvdb.Source != "settings" {
		t.Errorf("tvdb status wrong: %+v", tvdb)
	}
	if tvdb.Hint == "d4e9978a-76f9-4601-a072-8c42585d150b" {
		t.Fatal("the full secret leaked into the status hint")
	}
	if tvdb.Hint != "…150b" {
		t.Errorf("secret hint = %q, want the last 4 chars", tvdb.Hint)
	}

	// A username is not a secret and is shown in full — you need to see which account.
	if got := byID["opensubtitles_username"].Hint; got != "alice" {
		t.Errorf("username hint = %q, want it shown in full", got)
	}

	// An unset key reports unconfigured with no hint.
	if byID["omdb"].Configured {
		t.Error("omdb should be unconfigured")
	}
}

// The catalog is the contract with the UI — every entry needs the fields the settings
// page renders, or a key shows up unusable.
func TestCatalogIsComplete(t *testing.T) {
	seen := map[string]bool{}
	for _, k := range Catalog {
		if k.ID == "" || k.Label == "" || k.EnvVar == "" || k.HelpURL == "" || k.Steps == "" {
			t.Errorf("catalog entry missing fields: %+v", k)
		}
		if seen[k.ID] {
			t.Errorf("duplicate key id %q", k.ID)
		}
		seen[k.ID] = true
	}
}
