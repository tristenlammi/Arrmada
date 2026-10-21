package store

import (
	"context"
	"database/sql"
	"errors"
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"
)

func TestPostgresSubtitleSequenceUpgradeBackfillsAndKeepsHighWater(t *testing.T) {
	ctx, db, _, _ := postgresTestPool(t)
	old := postgresMigrationsBeforeSubtitleIDs(t)
	if err := migratePostgres(ctx, db, old, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO subtitle_jobs (id, media_key, kind, title, priority, state, queued_at)
		VALUES (900, 'movie:1', 'movie', 'Old job', 1, 'done', 1)`); err != nil {
		t.Fatal(err)
	}
	var before sql.NullInt64
	if err := db.QueryRowContext(ctx, `SELECT pg_catalog.pg_sequence_last_value(pg_catalog.pg_get_serial_sequence('subtitle_jobs', 'id')::regclass)`).Scan(&before); err != nil || before.Valid {
		t.Fatalf("explicit pre-upgrade id unexpectedly advanced sequence: %+v, %v", before, err)
	}
	if err := migratePostgres(ctx, db, postgresMigrations(), nil); !errors.Is(err, ErrMigrationBackupNeeded) {
		t.Fatalf("upgrade without backup = %v", err)
	}
	backedUp := false
	if err := migratePostgres(ctx, db, postgresMigrations(), func(context.Context, []string) error {
		// Migration-engine seam only; native backup/restore is tested separately
		// once its lifecycle implementation is available.
		backedUp = true
		return nil
	}); err != nil || !backedUp {
		t.Fatalf("upgrade = %v, backup callback called %v", err, backedUp)
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM subtitle_jobs`); err != nil {
		t.Fatal(err)
	}
	var after int64
	if err := db.QueryRowContext(ctx, `SELECT pg_catalog.pg_sequence_last_value(pg_catalog.pg_get_serial_sequence('subtitle_jobs', 'id')::regclass)`).Scan(&after); err != nil || after != 900 {
		t.Fatalf("backfilled id was lost after pruning: %d, %v", after, err)
	}
	boom := errors.New("abort insert")
	err := WithTx(ctx, db, func(tx *Transaction) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO subtitle_jobs (id, media_key, kind, title, priority, state, queued_at)
			VALUES (1000, 'movie:1', 'movie', 'Rolled back', 1, 'done', 1)`); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatal(err)
	}
	var count int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM subtitle_jobs`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("failed insert left rows: %d, %v", count, err)
	}
	if err := db.QueryRowContext(ctx, `SELECT pg_catalog.pg_sequence_last_value(pg_catalog.pg_get_serial_sequence('subtitle_jobs', 'id')::regclass)`).Scan(&after); err != nil || after != 1000 {
		t.Fatalf("PostgreSQL identity rollback gap was reused: %d, %v", after, err)
	}
}

func postgresMigrationsBeforeSubtitleIDs(t *testing.T) fs.FS {
	t.Helper()
	old := fstest.MapFS{}
	entries, err := fs.ReadDir(postgresMigrations(), ".")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".sql") || entry.Name() >= "0004_subtitle_job_ids.sql" {
			continue
		}
		raw, err := fs.ReadFile(postgresMigrations(), entry.Name())
		if err != nil {
			t.Fatal(err)
		}
		old[entry.Name()] = &fstest.MapFile{Data: raw}
	}
	return old
}
