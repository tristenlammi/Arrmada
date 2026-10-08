package store

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The copy must hold what was committed a moment ago (WAL rows included), pass its own
// integrity check, and pruning must keep only the newest copies of that one kind.
func TestSafetyCopyHoldsCommittedRowsAndPrunesItsKind(t *testing.T) {
	dataDir := t.TempDir()
	st, err := Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()
	if _, err := st.DB().ExecContext(ctx, `INSERT INTO settings (key, value) VALUES ('probe', 'still here')`); err != nil {
		t.Fatal(err)
	}

	backups := filepath.Join(dataDir, "backups")
	if err := os.MkdirAll(backups, 0o755); err != nil {
		t.Fatal(err)
	}
	// Older copies of the same kind (to be pruned) and of another kind (left alone).
	for _, n := range []string{
		"arrmada-pre-delete-user-20200101T000000Z.db",
		"arrmada-pre-delete-user-20200102T000000Z.db",
		"arrmada-pre-delete-user-20200103T000000Z.db",
		"arrmada-nightly-20200101T000000Z.db",
		"notes.txt",
	} {
		if err := os.WriteFile(filepath.Join(backups, n), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	path, err := st.SafetyCopy(ctx, dataDir, "pre-delete-user", 3)
	if err != nil {
		t.Fatalf("SafetyCopy: %v", err)
	}
	if filepath.Dir(path) != backups || !strings.HasPrefix(filepath.Base(path), "arrmada-pre-delete-user-") {
		t.Fatalf("copy written to %s, want backups/arrmada-pre-delete-user-*.db", path)
	}

	cp, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer cp.Close()
	var v string
	if err := cp.QueryRow(`SELECT value FROM settings WHERE key = 'probe'`).Scan(&v); err != nil || v != "still here" {
		t.Fatalf("copy is missing the committed row: %q, %v", v, err)
	}

	left, _ := os.ReadDir(backups)
	have := map[string]bool{}
	for _, e := range left {
		have[e.Name()] = true
	}
	if have["arrmada-pre-delete-user-20200101T000000Z.db"] {
		t.Error("the oldest pre-delete-user copy should have been pruned (keep 3)")
	}
	for _, n := range []string{"arrmada-pre-delete-user-20200102T000000Z.db", "arrmada-pre-delete-user-20200103T000000Z.db", filepath.Base(path), "arrmada-nightly-20200101T000000Z.db", "notes.txt"} {
		if !have[n] {
			t.Errorf("%s should still be there", n)
		}
	}
	for n := range have {
		if strings.HasSuffix(n, ".tmp") {
			t.Errorf("temp file %s left behind", n)
		}
	}
}

// A kind that could steer the file name somewhere else is refused outright.
func TestSafetyCopyRejectsOddKinds(t *testing.T) {
	dataDir := t.TempDir()
	st, err := Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	for _, k := range []string{"", "../x", "a/b", "Nightly", "pre_delete"} {
		if _, err := st.SafetyCopy(context.Background(), dataDir, k, 3); err == nil {
			t.Errorf("kind %q accepted", k)
		}
	}
}

// When the backups folder can't be created the copy fails, and nothing pretends otherwise.
func TestSafetyCopyFailsWhenBackupsIsAFile(t *testing.T) {
	dataDir := t.TempDir()
	st, err := Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := os.WriteFile(filepath.Join(dataDir, "backups"), []byte("not a folder"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SafetyCopy(context.Background(), dataDir, "pre-delete-user", 3); err == nil {
		t.Fatal("expected an error when backups/ is a regular file")
	}
}
