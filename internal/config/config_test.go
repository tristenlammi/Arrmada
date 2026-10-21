package config

import (
	"strings"
	"testing"
)

func TestDatabaseBackendSelection(t *testing.T) {
	t.Setenv("ARRMADA_DATABASE_BACKEND", "")
	t.Setenv("ARRMADA_POSTGRES_URL", "")
	t.Setenv("ARRMADA_POSTGRES_SCHEMA", "")
	t.Setenv("ARRMADA_POSTGRES_MAX_CONNECTIONS", "")
	c, err := Load()
	if err != nil || c.DatabaseBackend != "sqlite" {
		t.Fatalf("SQLite default = %q, %v", c.DatabaseBackend, err)
	}
	t.Setenv("ARRMADA_DATABASE_BACKEND", "postgres")
	if _, err := Load(); err == nil {
		t.Fatal("incomplete PostgreSQL configuration allowed implicit SQLite fallback")
	}
	t.Setenv("ARRMADA_POSTGRES_URL", "postgres://app:do-not-log@localhost/shared")
	t.Setenv("ARRMADA_POSTGRES_SCHEMA", "arrmada")
	t.Setenv("ARRMADA_POSTGRES_MAX_CONNECTIONS", "1")
	c, err = Load()
	if err != nil || c.DatabaseBackend != "postgres" || c.PostgresMaxConnections != 1 {
		t.Fatalf("explicit PostgreSQL = %q, %v", c.DatabaseBackend, err)
	}
	for _, invalid := range []string{"-1", "0", "101", "not-a-number"} {
		t.Setenv("ARRMADA_POSTGRES_MAX_CONNECTIONS", invalid)
		if _, err := Load(); err == nil || strings.Contains(err.Error(), "do-not-log") {
			t.Fatalf("invalid pool limit accepted or URL leaked: %v", err)
		}
	}
	t.Setenv("ARRMADA_POSTGRES_MAX_CONNECTIONS", "4")
	t.Setenv("ARRMADA_DATABASE_BACKEND", "posgres")
	if _, err := Load(); err == nil {
		t.Fatal("misspelled backend silently used SQLite")
	}
}

// ARRMADA_BASE_URL is no longer an option. A leftover sub-path value is kept only so
// startup can warn about it; empty and "/" already meant the root, so they don't warn.
func TestBaseURLIgnored(t *testing.T) {
	for _, tc := range []struct {
		val  string
		want string
	}{
		{"", ""},
		{"/", ""},
		{" // ", ""},
		{"/arrmada", "/arrmada"},
		{"arrmada/", "arrmada/"},
	} {
		t.Setenv("ARRMADA_BASE_URL", tc.val)
		c, err := Load()
		if err != nil {
			t.Fatal(err)
		}
		if c.BaseURLIgnored != tc.want {
			t.Errorf("ARRMADA_BASE_URL=%q: BaseURLIgnored = %q, want %q", tc.val, c.BaseURLIgnored, tc.want)
		}
	}
}
