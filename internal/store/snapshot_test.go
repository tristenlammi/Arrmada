package store

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/tristenlammi/arrmada/internal/diskspace"
)

var (
	snapV1 = memFS(map[string]string{
		"0001_t.sql": `CREATE TABLE t (id INTEGER PRIMARY KEY, v TEXT); INSERT INTO t (id, v) VALUES (1, 'before');`,
	})
	snapV2 = memFS(map[string]string{
		"0001_t.sql":    `CREATE TABLE t (id INTEGER PRIMARY KEY, v TEXT); INSERT INTO t (id, v) VALUES (1, 'before');`,
		"0002_drop.sql": `DROP TABLE t;`,
	})
)

func openWith(t *testing.T, dir string, fsys fstest.MapFS, opt Options) (*Store, error) {
	t.Helper()
	opt.migrations = fsys
	st, err := OpenWith(dir, opt)
	if err == nil {
		t.Cleanup(func() { _ = st.Close() })
	}
	return st, err
}

// backupFiles lists every file in dir's backups folder.
func backupFiles(t *testing.T, dataDir string) []string {
	t.Helper()
	entries, err := os.ReadDir(BackupsDir(dataDir))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return names
}

func openReadOnly(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro&immutable=1")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func hasTable(t *testing.T, db *sql.DB, name string) bool {
	t.Helper()
	return count(t, db, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='`+name+`'`) == 1
}

func TestOpenSnapshotsBeforePendingMigrations(t *testing.T) {
	dir := t.TempDir()
	st, err := openWith(t, dir, snapV1, Options{})
	if err != nil {
		t.Fatal(err)
	}
	_ = st.Close()

	var logs bytes.Buffer
	st, err = openWith(t, dir, snapV2, Options{Log: slog.New(slog.NewTextHandler(&logs, nil))})
	if err != nil {
		t.Fatalf("upgrade: %v", err)
	}
	if hasTable(t, st.DB(), "t") {
		t.Fatal("live database still has t after 0002")
	}

	files := backupFiles(t, dir)
	if len(files) != 1 {
		t.Fatalf("backups = %v, want exactly one snapshot", files)
	}
	kind, _, ok := ParseBackupName(files[0])
	if !ok || kind != BackupPreMigrate {
		t.Fatalf("snapshot name %q isn't a pre-migrate backup", files[0])
	}
	snap := openReadOnly(t, filepath.Join(BackupsDir(dir), files[0]))
	var integrity, v string
	if err := snap.QueryRow(`PRAGMA integrity_check`).Scan(&integrity); err != nil || integrity != "ok" {
		t.Fatalf("snapshot integrity_check = %q, %v", integrity, err)
	}
	if err := snap.QueryRow(`SELECT v FROM t WHERE id = 1`).Scan(&v); err != nil || v != "before" {
		t.Fatalf("snapshot row = %q, %v; want the pre-upgrade row", v, err)
	}
	if n := count(t, snap, `SELECT COUNT(*) FROM schema_migrations`); n != 1 {
		t.Fatalf("snapshot schema_migrations rows = %d, want the pre-upgrade 1", n)
	}

	out := logs.String()
	for _, want := range []string{"database snapshot taken before migrations", files[0], "from=0001_t", "to=0002_drop", "duration=", "size="} {
		if !strings.Contains(out, want) {
			t.Errorf("log lacks %q:\n%s", want, out)
		}
	}
}

func TestOpenFreshInstallNoSnapshot(t *testing.T) {
	dir := t.TempDir()
	if _, err := openWith(t, dir, snapV2, Options{}); err != nil {
		t.Fatal(err)
	}
	if files := backupFiles(t, dir); len(files) != 0 {
		t.Fatalf("fresh install wrote %v", files)
	}
}

func TestOpenNoPendingNoSnapshot(t *testing.T) {
	dir := t.TempDir()
	st, err := openWith(t, dir, snapV1, Options{})
	if err != nil {
		t.Fatal(err)
	}
	_ = st.Close()
	if _, err := openWith(t, dir, snapV1, Options{}); err != nil {
		t.Fatal(err)
	}
	if files := backupFiles(t, dir); len(files) != 0 {
		t.Fatalf("restart with nothing pending wrote %v", files)
	}
}

func TestSnapshotFailureBlocksMigration(t *testing.T) {
	dir := t.TempDir()
	st, err := openWith(t, dir, snapV1, Options{})
	if err != nil {
		t.Fatal(err)
	}
	_ = st.Close()
	// A regular file where the backups folder should be: MkdirAll fails on every OS.
	if err := os.WriteFile(BackupsDir(dir), []byte("not a folder"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err = openWith(t, dir, snapV2, Options{})
	if err == nil {
		t.Fatal("upgrade went ahead without a snapshot")
	}
	for _, want := range []string{"from 0001_t to 0002_drop", "nothing was changed", "ARRMADA_SKIP_MIGRATION_SNAPSHOT=1"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error lacks %q: %v", want, err)
		}
	}
	st, err = openWith(t, dir, snapV1, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !hasTable(t, st.DB(), "t") || count(t, st.DB(), `SELECT COUNT(*) FROM schema_migrations`) != 1 {
		t.Fatal("live database changed even though the snapshot failed")
	}
	_ = st.Close()

	// The escape hatch upgrades anyway, loudly.
	var logs bytes.Buffer
	st, err = openWith(t, dir, snapV2, Options{SkipMigrationSnapshot: true, Log: slog.New(slog.NewTextHandler(&logs, nil))})
	if err != nil {
		t.Fatalf("upgrade with skip flag: %v", err)
	}
	if hasTable(t, st.DB(), "t") {
		t.Fatal("skip flag didn't upgrade")
	}
	if !strings.Contains(logs.String(), "level=WARN") || !strings.Contains(logs.String(), "WITHOUT a snapshot") {
		t.Fatalf("skip flag didn't warn:\n%s", logs.String())
	}
}

func TestSnapshotRefusesWhenDiskIsFull(t *testing.T) {
	dir := t.TempDir()
	st, err := openWith(t, dir, snapV1, Options{})
	if err != nil {
		t.Fatal(err)
	}
	orig := freeSpace
	t.Cleanup(func() { freeSpace = orig })
	freeSpace = func(string) (diskspace.Usage, bool) {
		return diskspace.Usage{TotalBytes: 1 << 30, FreeBytes: 10}, true
	}

	_, err = st.Snapshot(context.Background(), BackupManual)
	if !errors.Is(err, ErrNoSpace) {
		t.Fatalf("err = %v, want ErrNoSpace", err)
	}
	if !strings.Contains(err.Error(), "free") {
		t.Fatalf("error doesn't say what's free: %v", err)
	}
	if files := backupFiles(t, dir); len(files) != 0 {
		t.Fatalf("refused snapshot left files behind: %v", files)
	}
}

func TestSnapshotIncludesWALCommittedRow(t *testing.T) {
	dir := t.TempDir()
	st, err := openWith(t, dir, snapV1, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().Exec(`INSERT INTO t (id, v) VALUES (2, 'in the wal')`); err != nil {
		t.Fatal(err)
	}
	// The row is committed but not yet checkpointed into arrmada.db.
	if fi, err := os.Stat(filepath.Join(dir, "arrmada.db-wal")); err != nil || fi.Size() == 0 {
		t.Fatalf("expected a non-empty WAL: %v", err)
	}

	path, err := st.Snapshot(context.Background(), BackupManual)
	if err != nil {
		t.Fatal(err)
	}
	var v string
	if err := openReadOnly(t, path).QueryRow(`SELECT v FROM t WHERE id = 2`).Scan(&v); err != nil || v != "in the wal" {
		t.Fatalf("snapshot row = %q, %v", v, err)
	}
	for _, f := range backupFiles(t, dir) {
		if strings.HasSuffix(f, ".tmp") {
			t.Fatalf("temp file left behind: %s", f)
		}
	}
}

func TestSnapshotSameSecondKeepsBoth(t *testing.T) {
	dir := t.TempDir()
	st, err := openWith(t, dir, snapV1, Options{})
	if err != nil {
		t.Fatal(err)
	}
	a, err := st.Snapshot(context.Background(), BackupManual)
	if err != nil {
		t.Fatal(err)
	}
	b, err := st.Snapshot(context.Background(), BackupManual)
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatalf("second snapshot replaced the first: %s", a)
	}
}

func TestSnapshotConcurrentCallsKeepBoth(t *testing.T) {
	dir := t.TempDir()
	st, err := openWith(t, dir, snapV1, Options{})
	if err != nil {
		t.Fatal(err)
	}
	paths := make([]string, 2)
	errs := make([]error, 2)
	var wg sync.WaitGroup
	for i := range paths {
		wg.Add(1)
		go func() {
			defer wg.Done()
			paths[i], errs[i] = st.Snapshot(context.Background(), BackupManual)
		}()
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("snapshot %d: %v", i, err)
		}
	}
	if paths[0] == paths[1] {
		t.Fatalf("both snapshots got %s", paths[0])
	}
	if files := backupFiles(t, dir); len(files) != 2 {
		t.Fatalf("backups = %v, want two finished copies", files)
	}
}

// A migration that keeps failing puts the container in a restart loop, and every
// boot snapshots the half-upgraded database. None of that may cost the copy taken
// before the upgrade began.
func TestFailingUpgradeKeepsThePreUpgradeSnapshot(t *testing.T) {
	dir := t.TempDir()
	st, err := openWith(t, dir, snapV1, Options{})
	if err != nil {
		t.Fatal(err)
	}
	_ = st.Close()

	v1 := `CREATE TABLE t (id INTEGER PRIMARY KEY, v TEXT); INSERT INTO t (id, v) VALUES (1, 'before');`
	broken := memFS(map[string]string{
		"0001_t.sql":    v1,
		"0002_wipe.sql": `DELETE FROM t;`,
		"0003_typo.sql": `CREAT TABLE nope (id INTEGER);`,
	})
	const boots = preMigrateKeep + 2
	for i := 0; i < boots; i++ {
		if _, err := openWith(t, dir, broken, Options{}); err == nil {
			t.Fatalf("boot %d: the broken upgrade succeeded", i)
		}
	}
	files := backupFiles(t, dir)
	if len(files) != boots {
		t.Fatalf("backups = %v, want all %d kept while the upgrade keeps failing", files, boots)
	}
	// Names sort by time, so the first is the copy from before 0002 ran.
	snap := openReadOnly(t, filepath.Join(BackupsDir(dir), files[0]))
	var v string
	if err := snap.QueryRow(`SELECT v FROM t WHERE id = 1`).Scan(&v); err != nil || v != "before" {
		t.Fatalf("oldest snapshot row = %q, %v; want the pre-upgrade row", v, err)
	}
	if n := count(t, snap, `SELECT COUNT(*) FROM schema_migrations`); n != 1 {
		t.Fatalf("oldest snapshot schema_migrations rows = %d, want 1", n)
	}

	// Once the upgrade goes through, the usual limit applies again.
	fixed := memFS(map[string]string{
		"0001_t.sql":    v1,
		"0002_wipe.sql": `DELETE FROM t;`,
		"0003_typo.sql": `CREATE TABLE fine (id INTEGER);`,
	})
	if _, err := openWith(t, dir, fixed, Options{}); err != nil {
		t.Fatalf("fixed upgrade: %v", err)
	}
	if files := backupFiles(t, dir); len(files) != preMigrateKeep {
		t.Fatalf("backups after a good upgrade = %v, want %d", files, preMigrateKeep)
	}
}

func TestPruneBackupsKeepsNewestPerKind(t *testing.T) {
	dir := t.TempDir()
	base := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	touch := func(name string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	var pre []string
	for i := 0; i < 7; i++ {
		n := BackupName(BackupPreMigrate, base.Add(time.Duration(i)*time.Hour))
		pre = append(pre, n)
		touch(n)
	}
	others := []string{
		BackupName(BackupNightly, base.Add(-48*time.Hour)),
		BackupName(BackupNightly, base.Add(-72*time.Hour)),
		"notes.txt",
		"arrmada.db",
		"arrmada-pre-migrate-garbage.db",
	}
	for _, n := range others {
		touch(n)
	}

	removed, err := PruneBackups(dir, BackupPreMigrate, 5)
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(removed)
	if want := pre[:2]; strings.Join(removed, ",") != strings.Join(want, ",") {
		t.Fatalf("removed %v, want the two oldest %v", removed, want)
	}
	for _, n := range append(pre[2:], others...) {
		if _, err := os.Stat(filepath.Join(dir, n)); err != nil {
			t.Errorf("%s should still exist: %v", n, err)
		}
	}
}

func TestOpenPrunesOldPreMigrateSnapshots(t *testing.T) {
	dir := t.TempDir()
	st, err := openWith(t, dir, snapV1, Options{})
	if err != nil {
		t.Fatal(err)
	}
	_ = st.Close()
	if err := os.MkdirAll(BackupsDir(dir), 0o755); err != nil {
		t.Fatal(err)
	}
	old := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 5; i++ {
		n := BackupName(BackupPreMigrate, old.Add(time.Duration(i)*time.Minute))
		if err := os.WriteFile(filepath.Join(BackupsDir(dir), n), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := openWith(t, dir, snapV2, Options{}); err != nil {
		t.Fatal(err)
	}
	files := backupFiles(t, dir)
	if len(files) != preMigrateKeep {
		t.Fatalf("backups = %v, want %d", files, preMigrateKeep)
	}
	if files[0] == BackupName(BackupPreMigrate, old) {
		t.Fatalf("the oldest snapshot survived: %v", files)
	}
}

func TestParseBackupName(t *testing.T) {
	at := time.Date(2026, 10, 9, 15, 4, 5, 0, time.UTC)
	name := BackupName(BackupPreDeleteUser, at)
	if name != "arrmada-pre-delete-user-20261009T150405Z.db" {
		t.Fatalf("BackupName = %q", name)
	}
	kind, got, ok := ParseBackupName(name)
	if !ok || kind != BackupPreDeleteUser || !got.Equal(at) {
		t.Fatalf("round trip = %q %v %v", kind, got, ok)
	}

	for _, bad := range []string{
		"../arrmada.db",
		"arrmada.db",
		"arrmada-nightly-x.db",
		"arrmada-weekly-20261009T150405Z.db",
		"/data/backups/arrmada-nightly-20261009T150405Z.db",
		"../arrmada-nightly-20261009T150405Z.db",
		"arrmada-nightly-20261009T150405Z.db.tmp",
		"arrmada-nightly-20261399T150405Z.db",
	} {
		if _, _, ok := ParseBackupName(bad); ok {
			t.Errorf("ParseBackupName(%q) accepted it", bad)
		}
	}
}
