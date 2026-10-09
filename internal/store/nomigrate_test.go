package store

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
)

// The CLI opens the database beside a live server, so it must never create one where
// none exists, nor upgrade one it finds.
func TestOpenNoMigrateDoesNotMigrate(t *testing.T) {
	dir := t.TempDir()
	if st, err := OpenNoMigrate(dir); err == nil {
		_ = st.Close()
		t.Fatal("opened a data dir with no database")
	}
	if _, err := os.Stat(filepath.Join(dir, "arrmada.db")); err == nil {
		t.Fatal("an empty database was created")
	}

	// An existing database that has never been migrated stays that way.
	raw, err := sql.Open("sqlite", "file:"+filepath.Join(dir, "arrmada.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`CREATE TABLE t (id INTEGER PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	_ = raw.Close()

	st, err := OpenNoMigrate(dir)
	if err != nil {
		t.Fatalf("open existing: %v", err)
	}
	defer st.Close()
	if got := tableNames(t, st); len(got) != 1 || got[0] != "t" {
		t.Fatalf("tables after OpenNoMigrate = %v, want just t", got)
	}
}
