package store_test

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tristenlammi/arrmada/internal/backup"
	"github.com/tristenlammi/arrmada/internal/settings"
	"github.com/tristenlammi/arrmada/internal/store"
)

func TestPostgresBuiltInBackupService(t *testing.T) {
	ctx, _, opt, _ := store.PostgresFixture(t)
	opt.BinDir = os.Getenv("ARRMADA_TEST_POSTGRES_BIN")
	dir := t.TempDir()
	st, err := store.OpenWith(dir, store.Options{Backend: store.Postgres, Postgres: opt})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	set := settings.NewService(st.DB())
	if err := set.Set(ctx, "backup_test", "preserved"); err != nil {
		t.Fatal(err)
	}
	svc := backup.New(st, set, nil)
	if svc.CanRestore() || svc.Backend() != store.Postgres {
		t.Fatal("PostgreSQL exposed in-app restore")
	}
	manual, err := svc.Create(ctx, store.BackupManual)
	if err != nil || manual.Backend != store.Postgres || manual.SchemaVersion != "0004_subtitle_job_ids" {
		t.Fatalf("native manual backup = %+v, %v", manual, err)
	}
	f, _, err := svc.Open(manual.Name)
	if err != nil {
		t.Fatal(err)
	}
	header := make([]byte, 5)
	_, err = io.ReadFull(f, header)
	_ = f.Close()
	if err != nil || string(header) != "PGDMP" {
		t.Fatalf("download is not a native custom archive: %v", err)
	}
	if _, err := svc.StageRestore(manual.Name, "admin"); !errors.Is(err, backup.ErrOfflineRestoreOnly) {
		t.Fatalf("in-app restore = %v", err)
	}
	if _, err := svc.Import(ctx, strings.NewReader("not read")); !errors.Is(err, backup.ErrOfflineRestoreOnly) {
		t.Fatalf("PostgreSQL upload = %v", err)
	}
	// A recent SQLite nightly in the retained directory must not suppress the
	// first PostgreSQL nightly, or be pruned by PostgreSQL retention.
	sqliteName := store.BackupName(store.BackupNightly, time.Now())
	if err := os.WriteFile(filepath.Join(svc.Dir(), sqliteName), []byte("former SQLite recovery copy"), 0600); err != nil {
		t.Fatal(err)
	}
	if !svc.LastNightly().IsZero() {
		t.Fatal("SQLite nightly suppressed PostgreSQL backup schedule")
	}
	if err := svc.RunNightly(ctx); err != nil {
		t.Fatal(err)
	}
	if svc.LastNightly().IsZero() {
		t.Fatal("PostgreSQL nightly was not recorded")
	}
	before, err := svc.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.RunNightly(ctx); err != nil {
		t.Fatal(err)
	}
	after, err := svc.List(ctx)
	if err != nil || len(after) != len(before) {
		t.Fatalf("restart/hourly tick repeated a nightly: %v", err)
	}
	if err := svc.Delete(manual.Name); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(svc.Dir(), manual.Name)); !os.IsNotExist(err) {
		t.Fatal("manual archive was not deleted")
	}
	if _, err := os.Stat(filepath.Join(svc.Dir(), sqliteName)); err != nil {
		t.Fatal("PostgreSQL retention touched SQLite recovery copy")
	}
}
