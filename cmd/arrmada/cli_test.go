package main

import (
	"bytes"
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/tristenlammi/arrmada/internal/buildinfo"
	"github.com/tristenlammi/arrmada/internal/store"
)

// runTestCLI runs a command the way main does and returns its exit code and output.
func runTestCLI(t *testing.T, stdin string, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errOut bytes.Buffer
	c := &cli{stdin: strings.NewReader(stdin), stdout: &out, stderr: &errOut}
	code = runCLIWith(context.Background(), c, args)
	return code, out.String(), errOut.String()
}

// A data dir with a fully migrated database, as the server leaves it.
func testDataDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("ARRMADA_DATA_DIR", dir)
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	_ = st.Close()
	return dir
}

func TestCLIUnknownSubcommandExits2(t *testing.T) {
	for _, args := range [][]string{{"frobnicate"}, {"--port", "8080"}, {"serve"}} {
		code, _, stderr := runTestCLI(t, "", args...)
		if code != exitUsage {
			t.Errorf("%v: exit %d, want 2", args, code)
		}
		if !strings.Contains(stderr, "unknown command") || !strings.Contains(stderr, "Commands:") {
			t.Errorf("%v: stderr doesn't explain: %q", args, stderr)
		}
	}
	// A known command with a bad flag is a usage error too, not a run.
	if code, _, _ := runTestCLI(t, "", "version", "--nope"); code != exitUsage {
		t.Errorf("version --nope: exit %d, want 2", code)
	}
}

func TestCLIHelp(t *testing.T) {
	code, stdout, _ := runTestCLI(t, "", "help")
	if code != exitOK || !strings.Contains(stdout, "backup") {
		t.Fatalf("help: exit %d, %q", code, stdout)
	}
}

func TestCLIVersion(t *testing.T) {
	code, stdout, _ := runTestCLI(t, "", "version")
	if code != exitOK {
		t.Fatalf("exit %d", code)
	}
	for _, want := range []string{buildinfo.Version, buildinfo.Commit, runtime.Version()} {
		if !strings.Contains(stdout, want) {
			t.Errorf("version output %q lacks %q", stdout, want)
		}
	}
}

// The backup is taken through the CLI while another connection holds the database
// with a write in flight, the way the live server would: it must succeed, contain
// everything committed, and nothing that wasn't.
func TestCLIBackupWhileOpen(t *testing.T) {
	dir := testDataDir(t)
	live, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer live.Close()
	ctx := context.Background()
	if _, err := live.DB().ExecContext(ctx, `INSERT INTO settings (key, value) VALUES ('committed', 'yes')`); err != nil {
		t.Fatal(err)
	}
	tx, err := live.DB().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `INSERT INTO settings (key, value) VALUES ('in-flight', 'no')`); err != nil {
		t.Fatal(err)
	}

	code, stdout, stderr := runTestCLI(t, "", "backup", "--kind", "pre-update")
	if code != exitOK {
		t.Fatalf("backup: exit %d: %s", code, stderr)
	}
	path := strings.TrimSpace(stdout)
	if filepath.Dir(path) != store.BackupsDir(dir) {
		t.Fatalf("backup path %q isn't in the backups folder", path)
	}
	if k, _, ok := store.ParseBackupName(filepath.Base(path)); !ok || k != store.BackupPreUpdate {
		t.Fatalf("backup name %q isn't a pre-update backup", path)
	}

	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro&immutable=1")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM settings WHERE key = 'committed'`).Scan(&n); err != nil || n != 1 {
		t.Errorf("committed row in backup: n=%d err=%v", n, err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM settings WHERE key = 'in-flight'`).Scan(&n); err != nil || n != 0 {
		t.Errorf("uncommitted row in backup: n=%d err=%v", n, err)
	}
}

func TestCLIBackupKeepsThreePreUpdate(t *testing.T) {
	dir := testDataDir(t)
	for i := 0; i < 5; i++ {
		if code, _, stderr := runTestCLI(t, "", "backup", "--kind=pre-update"); code != exitOK {
			t.Fatalf("backup %d: exit %d: %s", i, code, stderr)
		}
	}
	entries, err := os.ReadDir(store.BackupsDir(dir))
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, e := range entries {
		if k, _, ok := store.ParseBackupName(e.Name()); ok && k == store.BackupPreUpdate {
			n++
		}
	}
	if n != 3 {
		t.Errorf("%d pre-update backups kept, want 3", n)
	}
}

// With no database there is nothing to copy, and none must be created.
func TestCLIBackupWithoutDatabase(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("ARRMADA_DATA_DIR", dir)
	code, _, stderr := runTestCLI(t, "", "backup")
	if code != exitFail || !strings.Contains(stderr, "no database") {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	if _, err := os.Stat(filepath.Join(dir, "arrmada.db")); err == nil {
		t.Fatal("backup created an empty database")
	}
}

func TestCLIBackupRejectsOtherKinds(t *testing.T) {
	testDataDir(t)
	for _, kind := range []string{"nightly", "pre-migrate", "../../etc"} {
		if code, _, _ := runTestCLI(t, "", "backup", "--kind", kind); code != exitUsage {
			t.Errorf("--kind %s: exit %d, want 2", kind, code)
		}
	}
}
