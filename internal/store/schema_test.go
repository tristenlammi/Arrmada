package store

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// An older build started on a database a newer one upgraded (a rollback without the
// database) must refuse, change nothing, and say how to get out of it.
func TestOpenRefusesNewerSchema(t *testing.T) {
	dir := t.TempDir()
	newer := memFS(map[string]string{
		"0001_a.sql": `CREATE TABLE a (id INTEGER PRIMARY KEY);`,
		"0003_c.sql": `CREATE TABLE c (id INTEGER PRIMARY KEY);`,
	})
	st, err := openWith(t, dir, newer, Options{})
	if err != nil {
		t.Fatal(err)
	}
	_ = st.Close()
	before := backupFiles(t, dir)

	// The older build has a migration of its own still pending, which must not run either.
	older := memFS(map[string]string{
		"0001_a.sql": `CREATE TABLE a (id INTEGER PRIMARY KEY);`,
		"0002_b.sql": `CREATE TABLE b (id INTEGER PRIMARY KEY);`,
	})
	_, err = openWith(t, dir, older, Options{})
	if !errors.Is(err, ErrNewerSchema) {
		t.Fatalf("err = %v, want ErrNewerSchema", err)
	}
	var nse *NewerSchemaError
	if !errors.As(err, &nse) || len(nse.Unknown) != 1 || nse.Unknown[0] != "0003_c" || nse.Latest != "0002_b" {
		t.Fatalf("error details = %+v", nse)
	}
	for _, want := range []string{"0003_c", "0002_b", "./update.sh --rollback --with-db", "ARRMADA_ALLOW_NEWER_SCHEMA"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("message doesn't name %q: %v", want, err)
		}
	}
	if after := backupFiles(t, dir); len(after) != len(before) {
		t.Errorf("a refused start took a snapshot: %v", after)
	}

	// The override starts it, loudly, and only then do its own migrations run.
	st, err = openWith(t, dir, older, Options{AllowNewerSchema: true})
	if err != nil {
		t.Fatalf("with the override: %v", err)
	}
	if !hasTable(t, st.DB(), "b") {
		t.Error("the override didn't let the build's own migration run")
	}
	_ = st.Close()

	// A matching build starts normally again.
	if _, err := openWith(t, dir, memFS(map[string]string{
		"0001_a.sql": `CREATE TABLE a (id INTEGER PRIMARY KEY);`,
		"0002_b.sql": `CREATE TABLE b (id INTEGER PRIMARY KEY);`,
		"0003_c.sql": `CREATE TABLE c (id INTEGER PRIMARY KEY);`,
	}), Options{}); err != nil {
		t.Fatalf("matching build: %v", err)
	}
}

func TestSchemaStatus(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	s, err := st.Schema(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if s.Latest == "" || s.Applied != s.Latest || len(s.Pending) != 0 || len(s.Unknown) != 0 {
		t.Fatalf("fresh database: %+v", s)
	}
	if _, err := st.DB().Exec(`INSERT INTO schema_migrations (version) VALUES ('9999_future')`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().Exec(`DELETE FROM schema_migrations WHERE version = ?`, s.Latest); err != nil {
		t.Fatal(err)
	}
	s, err = st.Schema(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if s.Applied != "9999_future" || len(s.Unknown) != 1 || s.Unknown[0] != "9999_future" ||
		len(s.Pending) != 1 || s.Pending[0] != LatestMigration() {
		t.Fatalf("after a newer version: %+v", s)
	}
}
