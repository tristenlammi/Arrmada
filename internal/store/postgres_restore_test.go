package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPostgresOfflineNativeRestore(t *testing.T) {
	tools := archiveTestTools(t)
	ctx, admin, source, unrelated := postgresFixture(t)
	db, err := openPostgresPool(ctx, source)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := migratePostgres(ctx, db, postgresMigrations(), nil); err != nil {
		t.Fatal(err)
	}
	value := "Unicode é / tab\t newline\n backslash\\ and literal \\N \\.\n" + strings.Repeat("large-row-", 20000)
	if _, err := db.ExecContext(ctx, `INSERT INTO settings (key, value) VALUES ('restore-value', ?)`, value); err != nil {
		t.Fatal(err)
	}
	blob := []byte{0, 1, 2, '\n', '\t', '\\', 127, 128, 255}
	if _, err := db.ExecContext(ctx, `INSERT INTO metadata_cache (key, value, stored_at, expires_at) VALUES ('restore-blob', ?, 0, 1)`, blob); err != nil {
		t.Fatal(err)
	}
	userID, err := InsertID(ctx, db, `INSERT INTO users (username, password_hash) VALUES ('recovered-user', 'opaque-password-hash')`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO user_notifications (user_id, title, ref) VALUES (?, 'Recovered notification', 'original-ref')`, userID); err != nil {
		t.Fatal(err)
	}
	bookID, err := InsertID(ctx, db, `INSERT INTO books (ol_key, title, series_position, size_bytes, edits_json) VALUES ('restore-book', 'Edited title', ?, ?, '{"title":"catalogue title"}')`, math.Nextafter(1, 2), int64(math.MaxInt64))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO subtitle_jobs (id, media_key, kind, title, priority, state, queued_at)
		VALUES (900, 'movie:1', 'movie', 'Old job', 1, 'done', 1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM subtitle_jobs`); err != nil {
		t.Fatal(err)
	}
	// Native archive DDL is untrusted, even when it names the designated
	// schema. This function must not be installed by Arrmada's data recovery.
	if _, err := db.ExecContext(ctx, `CREATE FUNCTION archive_untrusted_function() RETURNS void LANGUAGE plpgsql AS $$
		BEGIN INSERT INTO settings (key, value) VALUES ('untrusted-function-ran', 'unsafe'); END $$`); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(t.TempDir(), "source.pgdump")
	_, release, err := lockPostgresSchema(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	err = postgresSnapshotLocked(ctx, db, source, tools, archive)
	release()
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(archive)
	if err != nil {
		t.Fatal(err)
	}
	// Optional second disposable server proves cross-major recovery locally;
	// CI's single-major matrix still runs the same full recovery assertions.
	if otherServer := os.Getenv("ARRMADA_TEST_POSTGRES_RESTORE_ADMIN_URL"); otherServer != "" {
		t.Setenv("ARRMADA_TEST_POSTGRES_ADMIN_URL", otherServer)
	}
	_, targetAdmin, destination, _ := postgresFixture(t)
	if err := restorePostgres(ctx, destination, archive, t.TempDir(), tools); err != nil {
		t.Fatal(err)
	}
	restored, err := openPostgresPool(ctx, destination)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	var got string
	if err := restored.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = 'restore-value'`).Scan(&got); err != nil || got != value {
		t.Fatalf("recovered text changed: match=%v, err=%v", got == value, err)
	}
	var actual []byte
	if err := restored.QueryRowContext(ctx, `SELECT value FROM metadata_cache WHERE key = 'restore-blob'`).Scan(&actual); err != nil || !bytes.Equal(actual, blob) {
		t.Fatalf("recovered blob changed: %v", err)
	}
	var high int64
	if err := restored.QueryRowContext(ctx, `SELECT pg_sequence_last_value(pg_get_serial_sequence('subtitle_jobs', 'id')::regclass)`).Scan(&high); err != nil || high != 900 {
		t.Fatalf("pruned sequence high-water lost: %d, %v", high, err)
	}
	var password, title string
	if err := restored.QueryRowContext(ctx, `SELECT u.password_hash, n.title FROM users u JOIN user_notifications n ON n.user_id = u.id WHERE u.id = ?`, userID).Scan(&password, &title); err != nil || password != "opaque-password-hash" || title != "Recovered notification" {
		t.Fatalf("recovered user identity/relationship changed: %v", err)
	}
	var position float64
	var size int64
	if err := restored.QueryRowContext(ctx, `SELECT series_position, size_bytes FROM books WHERE id = ?`, bookID).Scan(&position, &size); err != nil || position != math.Nextafter(1, 2) || size != math.MaxInt64 {
		t.Fatalf("recovered float/int lost precision: %v", err)
	}
	if _, err := restored.ExecContext(ctx, `UPDATE books SET title = 'incoming catalogue title' WHERE id = ?`, bookID); err != nil {
		t.Fatal(err)
	}
	if err := restored.QueryRowContext(ctx, `SELECT title FROM books WHERE id = ?`, bookID).Scan(&title); err != nil || title != "Edited title" {
		t.Fatalf("recovered book edit trigger changed: %q, %v", title, err)
	}
	var unsafe bool
	if err := restored.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace
		WHERE n.nspname = current_schema() AND p.proname = 'archive_untrusted_function')`).Scan(&unsafe); err != nil || unsafe {
		t.Fatalf("archive function was installed: %v, %v", unsafe, err)
	}
	// Recovered user triggers are enabled, including explicit subtitle ids.
	if _, err := restored.ExecContext(ctx, `INSERT INTO subtitle_jobs (id, media_key, kind, title, priority, state, queued_at)
		VALUES (950, 'movie:1', 'movie', 'New job', 1, 'done', 1)`); err != nil {
		t.Fatal(err)
	}
	if err := restored.QueryRowContext(ctx, `SELECT pg_sequence_last_value(pg_get_serial_sequence('subtitle_jobs', 'id')::regclass)`).Scan(&high); err != nil || high != 950 {
		t.Fatalf("recovered trigger not enabled: %d, %v", high, err)
	}
	if err := restorePostgres(ctx, destination, archive, t.TempDir(), tools); !errors.Is(err, ErrPostgresSchemaOccupied) {
		t.Fatalf("occupied restore destination = %v", err)
	}
	if err := restored.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = 'restore-value'`).Scan(&got); err != nil || got != value {
		t.Fatal("occupied destination was changed")
	}
	after, err := os.ReadFile(archive)
	if err != nil || sha256.Sum256(before) != sha256.Sum256(after) {
		t.Fatalf("source archive changed: %v", err)
	}
	// Both fixtures' unrelated schemas remain intact.
	var sentinel string
	if err := admin.QueryRow(ctx, `SELECT value FROM `+`"`+unrelated+`".sentinel`).Scan(&sentinel); err != nil || sentinel != "unrelated application" {
		t.Fatalf("unrelated source schema changed: %q, %v", sentinel, err)
	}
	if err := targetAdmin.QueryRow(ctx, `SELECT value FROM `+`"`+destination.Schema+`_other".sentinel`).Scan(&sentinel); err != nil || sentinel != "unrelated application" {
		t.Fatalf("unrelated destination schema changed: %q, %v", sentinel, err)
	}
}

func TestPostgresOfflineRestoreRejectsInvalidDataAndCancellation(t *testing.T) {
	tools := archiveTestTools(t)
	ctx, _, source, _ := postgresFixture(t)
	db, err := openPostgresPool(ctx, source)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := migratePostgres(ctx, db, postgresMigrations(), nil); err != nil {
		t.Fatal(err)
	}
	user, err := InsertID(ctx, db, `INSERT INTO users (username, password_hash) VALUES ('restore-user', 'opaque-hash')`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO user_notifications (user_id, title, ref) VALUES (?, 'Notification', 'ref')`, user); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO audio_passwords (user_id, hash, updated_at) VALUES (?, 'opaque-hash', 1)`, user); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO settings (key, value) VALUES ('large-row', ?)`, strings.Repeat("large", 50000)); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(t.TempDir(), "valid.pgdump")
	if err := postgresArchiveTo(ctx, source, tools, archive); err != nil {
		t.Fatal(err)
	}
	var decoded bytes.Buffer
	if err := nativePostgresTool(ctx, tools.restore, []string{}, &decoded, "--data-only", "--file=-", archive); err != nil {
		t.Fatal(err)
	}
	_, _, destination, _ := postgresFixture(t)
	for _, test := range []struct {
		name   string
		change func(string) string
		cancel bool
	}{
		{name: "foreign-key", change: func(s string) string { return changeArchiveCell(t, s, "audio_passwords", "user_id", "999999") }},
		{name: "lossy-number", change: func(s string) string { return changeArchiveCell(t, s, "users", "disabled", "+0") }},
		{name: "sequence-high-water", change: func(s string) string {
			return strings.Replace(s, "users_id_seq', 1, true)", "users_id_seq', 1, false)", 1)
		}},
		{name: "cancel-during-copy", change: func(s string) string { return s }, cancel: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			owner, err := openPostgresOwner(ctx, destination)
			if err != nil {
				t.Fatal(err)
			}
			defer owner.Close()
			input := test.change(decoded.String())
			copyCtx, cancel := context.WithCancel(ctx)
			defer cancel()
			var reader io.Reader = strings.NewReader(input)
			if test.cancel {
				reader = &cancelRecoveryReader{r: reader, cancel: cancel, after: 100000}
			}
			if err := restorePostgresDataLocked(copyCtx, owner, reader); err == nil {
				t.Fatal("invalid/cancelled restore succeeded")
			}
			_ = owner.Close()
			check, err := openPostgresPool(ctx, destination)
			if err != nil {
				t.Fatal(err)
			}
			defer check.Close()
			var count int
			if err := check.QueryRowContext(ctx, `SELECT COUNT(*) FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace WHERE n.nspname = current_schema()`).Scan(&count); err != nil || count != 0 {
				t.Fatalf("failed transfer left initialized relations: %d, %v", count, err)
			}
		})
	}
	// The exact same destination can now accept the valid source archive.
	if err := restorePostgres(ctx, destination, archive, t.TempDir(), tools); err != nil {
		t.Fatalf("retry after failed transfers: %v", err)
	}
}

func changeArchiveCell(t *testing.T, input, table, column, value string) string {
	t.Helper()
	lines := strings.Split(input, "\n")
	for i, line := range lines {
		match := archiveCopyHeader.FindStringSubmatch(line)
		if match == nil || strings.Trim(match[2], `"`) != table {
			continue
		}
		cols := strings.Split(match[3], ", ")
		for j, col := range cols {
			if strings.Trim(col, `"`) == column {
				cells := strings.Split(lines[i+1], "\t")
				cells[j] = value
				lines[i+1] = strings.Join(cells, "\t")
				return strings.Join(lines, "\n")
			}
		}
	}
	t.Fatal("test archive mutation did not find its column")
	return ""
}

type cancelRecoveryReader struct {
	r      io.Reader
	cancel context.CancelFunc
	after  int
}

func (r *cancelRecoveryReader) Read(p []byte) (int, error) {
	n, err := r.r.Read(p)
	r.after -= n
	if r.after <= 0 {
		r.cancel()
	}
	return n, err
}

func TestPostgresOfflineRestoreFailureLeavesEmptyDestination(t *testing.T) {
	tools := archiveTestTools(t)
	ctx, _, source, _ := postgresFixture(t)
	db, err := openPostgresPool(ctx, source)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := migratePostgres(ctx, db, postgresMigrations(), nil); err != nil {
		t.Fatal(err)
	}
	// A checksum mismatch must fail after the COPY transfer, rolling back all
	// fresh tables/functions/sequences as well as data and migration metadata.
	if _, err := db.ExecContext(ctx, `UPDATE schema_migrations SET checksum = 'incompatible' WHERE version = '0001_baseline'`); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(t.TempDir(), "incompatible.pgdump")
	if err := postgresArchiveTo(ctx, source, tools, archive); err != nil {
		t.Fatal(err)
	}
	_, _, destination, _ := postgresFixture(t)
	scratch := t.TempDir()
	if err := restorePostgres(ctx, destination, archive, scratch, tools); err == nil {
		t.Fatal("incompatible archive was accepted")
	}
	check, err := openPostgresPool(ctx, destination)
	if err != nil {
		t.Fatal(err)
	}
	defer check.Close()
	var objects int
	if err := check.QueryRowContext(ctx, `SELECT COUNT(*) FROM pg_depend d JOIN pg_namespace n
		ON d.refclassid = 'pg_namespace'::regclass AND d.refobjid = n.oid WHERE n.nspname = current_schema()`).Scan(&objects); err != nil || objects != 0 {
		t.Fatalf("failed restore left destination objects: %d, %v", objects, err)
	}
	entries, err := os.ReadDir(scratch)
	if err != nil || len(entries) != 0 {
		t.Fatalf("restore left decoded data files: %v, %v", entries, err)
	}
	// The failed destination is immediately reusable, not falsely initialized.
	if err := migratePostgres(context.Background(), check, postgresMigrations(), nil); err != nil {
		t.Fatalf("failed restore destination cannot be safely initialized: %v", err)
	}
}
