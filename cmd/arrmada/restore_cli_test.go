package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tristenlammi/arrmada/internal/store"
)

// cliDataDir is a scratch data dir with a database and one manual backup of it.
func cliDataDir(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	path, err := st.Snapshot(context.Background(), store.BackupManual)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	return dir, filepath.Base(path)
}

func TestCLIRestoreStagesMarker(t *testing.T) {
	dir, name := cliDataDir(t)
	var out, errb bytes.Buffer

	if code := restoreCommand(dir, []string{name}, &out, &errb); code != 0 {
		t.Fatalf("restore %s: exit %d: %s", name, code, errb.String())
	}
	m, err := store.PendingRestore(dir)
	if err != nil || m == nil || m.Name() != name || m.RequestedBy != "cli" {
		t.Fatalf("pending = %+v, %v", m, err)
	}

	out.Reset()
	if code := backupsCommand(dir, nil, &out, &errb); code != 0 || !strings.Contains(out.String(), name) || !strings.Contains(out.String(), "staged") {
		t.Errorf("backups: exit %d:\n%s", code, out.String())
	}

	out.Reset()
	if code := restoreCommand(dir, []string{"--cancel"}, &out, &errb); code != 0 {
		t.Fatalf("cancel: exit %d", code)
	}
	if m, _ := store.PendingRestore(dir); m != nil {
		t.Error("still staged after --cancel")
	}

	// The next start on a staged restore runs it.
	if code := restoreCommand(dir, []string{name}, &out, &errb); code != 0 {
		t.Fatal(errb.String())
	}
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	_ = st.Close()
	if res, _ := store.LastRestore(dir); res == nil || !res.OK || res.From != name {
		t.Errorf("restore result = %+v", res)
	}
}

// A file from outside the data dir (a downloaded .db.gz) is copied in as an uploaded
// backup and staged; a path or name that isn't a usable backup stages nothing.
func TestCLIRestoreFromPath(t *testing.T) {
	dir, name := cliDataDir(t)
	raw, err := os.ReadFile(filepath.Join(store.BackupsDir(dir), name))
	if err != nil {
		t.Fatal(err)
	}
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	_, _ = zw.Write(raw)
	_ = zw.Close()
	outside := filepath.Join(t.TempDir(), name+".gz")
	if err := os.WriteFile(outside, gz.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}

	var out, errb bytes.Buffer
	if code := restoreCommand(dir, []string{outside}, &out, &errb); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	m, _ := store.PendingRestore(dir)
	if m == nil {
		t.Fatal("nothing staged")
	}
	if k, _, ok := store.ParseBackupName(m.Name()); !ok || k != store.BackupUploaded {
		t.Errorf("staged %q, want an uploaded copy", m.Name())
	}
	_, _ = store.CancelRestore(dir)

	junk := filepath.Join(t.TempDir(), "junk.db")
	_ = os.WriteFile(junk, []byte("not a database"), 0o644)
	for _, bad := range []string{junk, "arrmada-nightly-20200101T000000Z.db", filepath.Join(t.TempDir(), "missing.db")} {
		errb.Reset()
		if code := restoreCommand(dir, []string{bad}, &out, &errb); code != 1 {
			t.Errorf("%s: exit %d, want 1", bad, code)
		}
		if m, _ := store.PendingRestore(dir); m != nil {
			t.Errorf("%s was staged", bad)
		}
	}
	if code := restoreCommand(dir, nil, &out, &errb); code != 2 {
		t.Errorf("no argument: exit %d, want 2 (usage)", code)
	}
}

func TestRunSubcommandLeavesServerArgsAlone(t *testing.T) {
	for _, args := range [][]string{nil, {}, {"serve"}, {"--debug"}} {
		if _, ok := runSubcommand(args, &bytes.Buffer{}, &bytes.Buffer{}); ok {
			t.Errorf("%q was taken as a maintenance command", args)
		}
	}
}
