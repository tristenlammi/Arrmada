package store

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func runtimePostgresOptions(t *testing.T, opt PostgresOptions) PostgresOptions {
	t.Helper()
	tools := archiveTestTools(t)
	if filepath.IsAbs(tools.dump) {
		opt.BinDir = filepath.Dir(tools.dump)
	}
	return opt
}

func TestPostgresStoreRuntimeAndOnlineSnapshot(t *testing.T) {
	ctx, _, opt, _ := postgresFixture(t)
	opt = runtimePostgresOptions(t, opt)
	opt.MaxConnections = 1
	dir := t.TempDir()
	oldSQLite := []byte("untouched former SQLite database")
	if err := os.WriteFile(filepath.Join(dir, "arrmada.db"), oldSQLite, 0600); err != nil {
		t.Fatal(err)
	}
	st, err := OpenWith(dir, Options{Backend: Postgres, Postgres: opt})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if st.Backend() != Postgres || DialectOf(st.DB()) != Postgres {
		t.Fatal("runtime did not inject the PostgreSQL query boundary")
	}
	if err := st.Ping(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().ExecContext(ctx, `INSERT INTO settings (key, value) VALUES ('runtime', 'postgres')`); err != nil {
		t.Fatal(err)
	}
	status, err := st.Schema(ctx)
	if err != nil || len(status.Pending) != 0 || status.Applied != "0004_subtitle_job_ids" {
		t.Fatalf("runtime schema = %+v, %v", status, err)
	}
	path, err := st.Snapshot(ctx, BackupManual)
	if err != nil {
		t.Fatal(err)
	}
	if BackupBackend(filepath.Base(path)) != Postgres {
		t.Fatalf("wrong backup format: %s", path)
	}
	version, err := InspectBackupVersion(ctx, path, opt.BinDir)
	if err != nil || version != status.Applied {
		t.Fatalf("native backup metadata = %q, %v", version, err)
	}
	other, err := OpenWith(dir, Options{Backend: Postgres, Postgres: opt})
	if other != nil {
		_ = other.Close()
	}
	if !errors.Is(err, ErrDatabaseInUse) {
		t.Fatalf("second runtime startup = %v", err)
	}
	maintenance, err := OpenPostgresNoMigrate(ctx, dir, opt)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := maintenance.Snapshot(ctx, BackupPreUpdate); err != nil {
		t.Fatal(err)
	}
	if err := maintenance.Close(); err != nil {
		t.Fatal(err)
	}
	actual, err := os.ReadFile(filepath.Join(dir, "arrmada.db"))
	if err != nil || string(actual) != string(oldSQLite) {
		t.Fatal("PostgreSQL startup or backup touched arrmada.db")
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenWith(dir, Options{Backend: Postgres, Postgres: opt})
	if err != nil {
		t.Fatalf("clean runtime restart: %v", err)
	}
	defer reopened.Close()
	var value string
	if err := reopened.DB().QueryRowContext(ctx, `SELECT value FROM settings WHERE key = 'runtime'`).Scan(&value); err != nil || value != "postgres" {
		t.Fatalf("runtime persistence = %q, %v", value, err)
	}
}

func TestPostgresStoreUpgradeUsesNativeBackup(t *testing.T) {
	ctx, _, opt, _ := postgresFixture(t)
	opt = runtimePostgresOptions(t, opt)
	db, err := openPostgresPool(ctx, opt)
	if err != nil {
		t.Fatal(err)
	}
	if err := migratePostgres(ctx, db, postgresMigrationsBeforeSubtitleIDs(t), nil); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	dir := t.TempDir()
	st, err := OpenWith(dir, Options{Backend: Postgres, Postgres: opt})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	entries, err := os.ReadDir(BackupsDir(dir))
	if err != nil || len(entries) != 1 {
		t.Fatalf("upgrade backups: %v, %v", entries, err)
	}
	version, err := InspectBackupVersion(ctx, filepath.Join(BackupsDir(dir), entries[0].Name()), opt.BinDir)
	if err != nil || version != "0003_writer_lock" {
		t.Fatalf("pre-upgrade archive version = %q, %v", version, err)
	}
}

func TestPostgresStoreRefusesUnsafeOverridesAndMissingTools(t *testing.T) {
	for _, option := range []Options{
		{Backend: Postgres, SkipMigrationSnapshot: true},
		{Backend: Postgres, AllowNewerSchema: true},
		{Backend: "unsupported"},
	} {
		dir := t.TempDir()
		if st, err := OpenWith(dir, option); err == nil {
			_ = st.Close()
			t.Fatal("unsafe backend option accepted")
		}
		if _, err := os.Stat(filepath.Join(dir, "arrmada.db")); !os.IsNotExist(err) {
			t.Fatal("failed PostgreSQL selection created SQLite")
		}
	}
	ctx, _, opt, _ := postgresFixture(t)
	opt.BinDir = t.TempDir()
	dir := t.TempDir()
	if st, err := OpenWith(dir, Options{Backend: Postgres, Postgres: opt}); err == nil {
		_ = st.Close()
		t.Fatal("runtime started without native backup clients")
	}
	db, err := openPostgresPool(ctx, opt)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	status, err := postgresSchema(ctx, db, postgresMigrations())
	if err != nil || status.Applied != "" {
		t.Fatalf("missing tools initialized a schema: %+v, %v", status, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "arrmada.db")); !os.IsNotExist(err) {
		t.Fatal("missing native tools triggered a SQLite fallback")
	}
}

func TestPostgresStoreFailedUpgradeKeepsOldSchemaAndBackups(t *testing.T) {
	ctx, _, opt, _ := postgresFixture(t)
	tools := archiveTestTools(t)
	db, err := openPostgresPool(ctx, opt)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := migratePostgres(ctx, db, postgresMigrationsBeforeSubtitleIDs(t), nil); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	fakeDump := "#!/bin/sh\nif [ \"$1\" = --version ]; then echo 'pg_dump (PostgreSQL) 18.6'; exit 0; fi\necho partial-private-data\nexit 23\n"
	if err := os.WriteFile(filepath.Join(bin, "pg_dump"), []byte(fakeDump), 0700); err != nil {
		t.Fatal(err)
	}
	restore, err := exec.LookPath(tools.restore)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(restore, filepath.Join(bin, "pg_restore")); err != nil {
		t.Fatal(err)
	}
	opt.BinDir = bin
	dir := t.TempDir()
	if err := os.MkdirAll(BackupsDir(dir), 0700); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 7; i++ {
		if err := os.WriteFile(filepath.Join(BackupsDir(dir), BackupNameFor(Postgres, BackupPreMigrate, time.Unix(int64(i), 0))), []byte("prior recovery copy"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if st, err := OpenWith(dir, Options{Backend: Postgres, Postgres: opt}); !errors.Is(err, ErrMigrationBackupNeeded) {
		if st != nil {
			_ = st.Close()
		}
		t.Fatalf("upgrade without a successful native dump = %v", err)
	}
	status, err := postgresSchema(ctx, db, postgresMigrations())
	if err != nil || status.Applied != "0003_writer_lock" || len(status.Pending) != 1 {
		t.Fatalf("failed backup applied migrations: %+v, %v", status, err)
	}
	files, err := os.ReadDir(BackupsDir(dir))
	if err != nil || len(files) != 7 {
		t.Fatalf("failed upgrade pruned recovery copies or left a partial dump: %v, %v", files, err)
	}
}

func TestPostgresMaintenanceRefusesUninitializedBackup(t *testing.T) {
	ctx, _, opt, _ := postgresFixture(t)
	dir := t.TempDir()
	st, err := OpenPostgresNoMigrate(ctx, dir, opt)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := st.Snapshot(ctx, BackupManual); err == nil {
		t.Fatal("uninitialized schema published as a usable backup")
	}
	status, err := st.Schema(ctx)
	if err != nil || status.Applied != "" {
		t.Fatalf("maintenance initialized a schema: %+v, %v", status, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "arrmada.db")); !os.IsNotExist(err) {
		t.Fatal("maintenance opened/created SQLite")
	}
}

func TestPostgresSelectionPreservesPendingSQLiteRestore(t *testing.T) {
	dir := t.TempDir()
	sqlite, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	copy, err := sqlite.Snapshot(context.Background(), BackupManual)
	if err != nil {
		t.Fatal(err)
	}
	_ = sqlite.Close()
	if _, err := StageRestore(dir, filepath.Base(copy), "test"); err != nil {
		t.Fatal(err)
	}
	_, err = OpenWith(dir, Options{Backend: Postgres, Postgres: PostgresOptions{URL: "postgres://app@localhost/not-connected", Schema: "arrmada"}})
	if err == nil || !strings.Contains(err.Error(), "staged SQLite restore") {
		t.Fatalf("PostgreSQL selection ignored/executed a SQLite restore: %v", err)
	}
	if marker, err := PendingRestore(dir); err != nil || marker == nil {
		t.Fatalf("pending SQLite restore was discarded: %v", err)
	}
}

func TestPostgresSnapshotGuardLossDoesNotPublish(t *testing.T) {
	tools := archiveTestTools(t)
	ctx, _, opt, _ := postgresFixture(t)
	db, err := openPostgresPool(ctx, opt)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := migratePostgres(ctx, db, postgresMigrations(), nil); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "lost.pgdump")
	err = postgresArchiveToChecked(ctx, opt, tools, path, func() error { return errors.New("lost session") })
	if err == nil {
		t.Fatal("backup published after loss of schema protection")
	}
	files, err := os.ReadDir(dir)
	if err != nil || len(files) != 0 {
		t.Fatalf("guard failure left backup files: %v, %v", files, err)
	}
}

func TestBackupRetentionSeparatesBackends(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 6; i++ {
		for _, backend := range []Dialect{SQLite, Postgres} {
			name := BackupNameFor(backend, BackupManual, time.Unix(int64(i), 0))
			if err := os.WriteFile(filepath.Join(dir, name), []byte("copy"), 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	if removed, err := PruneBackupsFor(Postgres, dir, BackupManual, 2); err != nil || len(removed) != 4 {
		t.Fatalf("PostgreSQL retention = %v, %v", removed, err)
	}
	if removed, err := PruneBackups(dir, BackupManual, 3); err != nil || len(removed) != 3 {
		t.Fatalf("SQLite retention = %v, %v", removed, err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	counts := map[Dialect]int{}
	for _, e := range entries {
		counts[BackupBackend(e.Name())]++
	}
	if counts[SQLite] != 3 || counts[Postgres] != 2 {
		t.Fatalf("cross-backend retention changed recovery copies: %v", counts)
	}
	if _, _, ok := ParseBackupName(strings.Replace(BackupNameFor(Postgres, BackupManual, time.Now()), ".pgdump", ".pgdump.gz", 1)); ok {
		t.Fatal("compressed name accepted as a raw native archive")
	}
}

func TestPostgresBackupDirectoryPublicationAndPrivacy(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := checkPostgresBackupDir(dir); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(dir)
	if err != nil || info.Mode().Perm()&0077 != 0 {
		t.Fatalf("backup directory is not private: %v, %v", info, err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("publication preflight left files: %v, %v", entries, err)
	}
}
