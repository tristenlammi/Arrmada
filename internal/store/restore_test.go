package store

import (
	"context"
	"crypto/rand"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var (
	restoreV1 = memFS(map[string]string{
		"0001_init.sql": `CREATE TABLE users (id INTEGER PRIMARY KEY, name TEXT); CREATE TABLE marker (v TEXT);`,
	})
	restoreV2 = memFS(map[string]string{
		"0001_init.sql":  `CREATE TABLE users (id INTEGER PRIMARY KEY, name TEXT); CREATE TABLE marker (v TEXT);`,
		"0002_extra.sql": `CREATE TABLE extra (id INTEGER PRIMARY KEY);`,
	})
)

func quietLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// timeZero is a fixed time for names of backups that don't exist.
func timeZero() time.Time { return time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC) }

// restoreFixture makes a data dir whose database holds marker "backup-time" in a manual
// backup and "after" only in the live database. It returns the dir and the backup's name.
func restoreFixture(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	st, err := openWith(t, dir, restoreV1, Options{})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := st.DB().Exec(`INSERT INTO marker (v) VALUES ('backup-time')`); err != nil {
		t.Fatal(err)
	}
	path, err := st.Snapshot(ctx, BackupManual)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().Exec(`INSERT INTO marker (v) VALUES ('after')`); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	return dir, filepath.Base(path)
}

func markers(t *testing.T, path string) []string {
	t.Helper()
	db := openReadOnly(t, path)
	rows, err := db.Query(`SELECT v FROM marker ORDER BY rowid`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			t.Fatal(err)
		}
		out = append(out, v)
	}
	return out
}

func kindFiles(t *testing.T, dataDir string, kind BackupKind) []string {
	t.Helper()
	var out []string
	for _, n := range backupFiles(t, dataDir) {
		if k, _, ok := ParseBackupName(n); ok && k == kind {
			out = append(out, n)
		}
	}
	return out
}

// A staged backup is put in place at the next open: its data is back, the replaced
// database is kept as a pre-restore backup, and the outcome is recorded.
func TestApplyPendingRestore(t *testing.T) {
	dir, name := restoreFixture(t)
	if _, err := stageRestore(dir, name, "admin", versionsIn(restoreV1)); err != nil {
		t.Fatal(err)
	}
	if m, _ := PendingRestore(dir); m == nil || m.Name() != name || m.RequestedBy != "admin" {
		t.Fatalf("pending = %+v", m)
	}

	st, err := openWith(t, dir, restoreV1, Options{})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	rows, err := st.DB().Query(`SELECT v FROM marker ORDER BY rowid`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var v string
		_ = rows.Scan(&v)
		got = append(got, v)
	}
	rows.Close()
	if strings.Join(got, ",") != "backup-time" {
		t.Errorf("markers after restore = %v, want [backup-time]", got)
	}

	pre := kindFiles(t, dir, BackupPreRestore)
	if len(pre) != 1 {
		t.Fatalf("pre-restore copies = %v, want one", pre)
	}
	if m := markers(t, filepath.Join(BackupsDir(dir), pre[0])); strings.Join(m, ",") != "backup-time,after" {
		t.Errorf("pre-restore copy holds %v, want the replaced database", m)
	}
	if m, _ := PendingRestore(dir); m != nil {
		t.Error("the marker is still there after the restore ran")
	}
	res, err := LastRestore(dir)
	if err != nil || res == nil || !res.OK || res.From != name || res.PreRestore != pre[0] {
		t.Errorf("restore result = %+v, %v", res, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "arrmada.db.restore-tmp")); !os.IsNotExist(err) {
		t.Error("the restore's temp file was left behind")
	}
}

// The swap removes the old database's -wal and -shm, so SQLite can't replay them into the
// restored file.
func TestApplyPendingRestoreDropsOldWAL(t *testing.T) {
	dir, name := restoreFixture(t)
	db := filepath.Join(dir, "arrmada.db")
	for _, side := range []string{"-wal", "-shm"} {
		if err := os.WriteFile(db+side, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := stageRestore(dir, name, "admin", versionsIn(restoreV1)); err != nil {
		t.Fatal(err)
	}
	applyPendingRestore(dir, versionsIn(restoreV1), quietLog())
	for _, side := range []string{"-wal", "-shm", "-journal"} {
		if _, err := os.Stat(db + side); !os.IsNotExist(err) {
			t.Errorf("stale %s next to the restored database", side)
		}
	}
	if m := markers(t, db); strings.Join(m, ",") != "backup-time" {
		t.Errorf("restored database holds %v", m)
	}
}

// When the backup can't be read at boot, the original database stays, the marker is
// cleared (so the next boot doesn't try again) and the result says why.
func TestRestoreFailureKeepsOriginal(t *testing.T) {
	dir, name := restoreFixture(t)
	if _, err := stageRestore(dir, name, "admin", versionsIn(restoreV1)); err != nil {
		t.Fatal(err)
	}
	// The backup turns to garbage between staging and the restart.
	if err := os.WriteFile(filepath.Join(BackupsDir(dir), name), []byte("not a database at all"), 0o644); err != nil {
		t.Fatal(err)
	}
	st, err := openWith(t, dir, restoreV1, Options{})
	if err != nil {
		t.Fatalf("boot failed after a failed restore: %v", err)
	}
	if n := count(t, st.DB(), `SELECT COUNT(*) FROM marker`); n != 2 {
		t.Errorf("%d markers, want the original 2", n)
	}
	if m, _ := PendingRestore(dir); m != nil {
		t.Error("the marker survived a failed restore")
	}
	res, _ := LastRestore(dir)
	if res == nil || res.OK || res.Error == "" {
		t.Errorf("restore result = %+v, want ok:false with an error", res)
	}
	if pre := kindFiles(t, dir, BackupPreRestore); len(pre) != 0 {
		t.Errorf("a failed validation still copied the database: %v", pre)
	}
}

// A marker pointing anywhere but a backup in the backups folder is refused at boot.
func TestRestoreMarkerCantPointOutside(t *testing.T) {
	dir, _ := restoreFixture(t)
	outside := filepath.Join(t.TempDir(), "evil.db")
	if err := copyFileSync(filepath.Join(dir, "arrmada.db"), outside); err != nil {
		t.Fatal(err)
	}
	for _, file := range []string{outside, "backups/../arrmada.db", "../x/" + BackupName(BackupManual, timeZero())} {
		if err := writeJSONFile(filepath.Join(dir, restorePendingFile), RestoreMarker{File: file}); err != nil {
			t.Fatal(err)
		}
		applyPendingRestore(dir, versionsIn(restoreV1), quietLog())
		res, _ := LastRestore(dir)
		if res == nil || res.OK {
			t.Errorf("marker %q: result %+v, want a refusal", file, res)
		}
		if m, _ := PendingRestore(dir); m != nil {
			t.Errorf("marker %q was left in place", file)
		}
	}
}

// A database too damaged for VACUUM INTO is still replaced, and kept byte for byte.
func TestRestoreOverDamagedDatabase(t *testing.T) {
	dir, name := restoreFixture(t)
	db := filepath.Join(dir, "arrmada.db")
	junk := append([]byte("SQLite format 3\x00"), make([]byte, 4096)...)
	_, _ = rand.Read(junk[16:])
	if err := os.WriteFile(db, junk, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := stageRestore(dir, name, "admin", versionsIn(restoreV1)); err != nil {
		t.Fatal(err)
	}
	applyPendingRestore(dir, versionsIn(restoreV1), quietLog())
	res, _ := LastRestore(dir)
	if res == nil || !res.OK {
		t.Fatalf("restore result = %+v", res)
	}
	if m := markers(t, db); strings.Join(m, ",") != "backup-time" {
		t.Errorf("restored database holds %v", m)
	}
	kept, err := os.ReadFile(filepath.Join(BackupsDir(dir), res.PreRestore))
	if err != nil || string(kept) != string(junk) {
		t.Errorf("the damaged database wasn't kept as it was (%v)", err)
	}
}

func TestValidateBackupRejectsNewerSchema(t *testing.T) {
	dir := t.TempDir()
	st, err := openWith(t, dir, restoreV2, Options{})
	if err != nil {
		t.Fatal(err)
	}
	path, err := st.Snapshot(context.Background(), BackupManual)
	if err != nil {
		t.Fatal(err)
	}
	info, err := ValidateBackup(path, versionsIn(restoreV1))
	if !errors.Is(err, ErrNewerSchema) {
		t.Fatalf("err = %v, want ErrNewerSchema", err)
	}
	if len(info.Unknown) != 1 || info.Unknown[0] != "0002_extra" || !strings.Contains(err.Error(), "0002_extra") {
		t.Errorf("unknown = %v, err = %v; want 0002_extra named", info.Unknown, err)
	}
	if info, err := ValidateBackup(path, versionsIn(restoreV2)); err != nil || info.SchemaVersion != "0002_extra" {
		t.Errorf("same schema: %+v, %v", info, err)
	}
	// Nothing is staged for a refused backup.
	if _, err := stageRestore(dir, filepath.Base(path), "admin", versionsIn(restoreV1)); !errors.Is(err, ErrNewerSchema) {
		t.Errorf("stage err = %v", err)
	}
	if m, _ := PendingRestore(dir); m != nil {
		t.Error("a refused backup was staged")
	}
}

func TestValidateBackupRejectsCorrupt(t *testing.T) {
	dir := t.TempDir()
	random := make([]byte, 8192)
	_, _ = rand.Read(random)
	write := func(name string, b []byte) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, b, 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	if _, err := ValidateBackup(write("random.db", random), nil); !errors.Is(err, ErrNotSQLite) {
		t.Errorf("random bytes: err = %v, want ErrNotSQLite", err)
	}
	if _, err := ValidateBackup(write("empty.db", nil), nil); !errors.Is(err, ErrNotSQLite) {
		t.Errorf("empty file: err = %v, want ErrNotSQLite", err)
	}
	withHeader := append([]byte("SQLite format 3\x00"), random...)
	if _, err := ValidateBackup(write("header.db", withHeader), nil); !errors.Is(err, ErrCorrupt) {
		t.Errorf("header then garbage: err = %v, want ErrCorrupt", err)
	}

	// A sound SQLite file that isn't Arrmada's.
	other := filepath.Join(dir, "other.db")
	db, err := openDB(other)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE notes (x TEXT)`); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	if _, err := ValidateBackup(other, nil); !errors.Is(err, ErrNotArrmada) {
		t.Errorf("foreign database: err = %v, want ErrNotArrmada", err)
	}
}

func TestStageRestoreRefusesBadNames(t *testing.T) {
	dir, _ := restoreFixture(t)
	for _, n := range []string{"", "arrmada.db", "../arrmada.db", "backups/" + BackupName(BackupManual, timeZero()), BackupName(BackupManual, timeZero())} {
		_, err := stageRestore(dir, n, "admin", versionsIn(restoreV1))
		if err == nil {
			t.Errorf("%q staged", n)
		}
	}
	if m, _ := PendingRestore(dir); m != nil {
		t.Error("something was staged")
	}
}

// Cancelling removes the marker; the backup it pointed at is protected from pruning
// until then.
func TestCancelRestoreAndPruneSkipsStaged(t *testing.T) {
	dir, name := restoreFixture(t)
	if _, err := stageRestore(dir, name, "admin", versionsIn(restoreV1)); err != nil {
		t.Fatal(err)
	}
	removed, err := PruneBackups(BackupsDir(dir), BackupManual, 0)
	if err != nil || len(removed) != 0 {
		t.Errorf("pruned %v (%v); the staged backup must be kept", removed, err)
	}
	if ok, err := CancelRestore(dir); !ok || err != nil {
		t.Errorf("cancel = %v, %v", ok, err)
	}
	if ok, _ := CancelRestore(dir); ok {
		t.Error("cancelled twice")
	}
	if removed, _ := PruneBackups(BackupsDir(dir), BackupManual, 0); len(removed) != 1 {
		t.Errorf("after cancel, pruned %v", removed)
	}
}
