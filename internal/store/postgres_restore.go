package store

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

// RestorePostgres restores an Arrmada native archive into an EMPTY,
// pre-provisioned schema. It never drops/replaces an existing schema or executes
// archive SQL. Stop Arrmada first; after successful recovery point configuration
// at this schema. The source archive and any existing installation are unchanged.
//
// Use a build matching the backup's migration history. Upgrade after recovery,
// not while interpreting an archive made by an unknown/older schema version.
func RestorePostgres(ctx context.Context, opt PostgresOptions, archive, scratchDir string) error {
	return restorePostgres(ctx, opt, archive, scratchDir, postgresTools(opt))
}

func restorePostgres(ctx context.Context, opt PostgresOptions, archive, scratchDir string, tools postgresArchiveTools) error {
	if err := opt.validate(); err != nil {
		return err
	}
	info, err := os.Stat(archive)
	if err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
		return errors.New("PostgreSQL restore requires a non-empty regular archive file")
	}
	if _, err := postgresClientMajor(ctx, tools.restore); err != nil {
		return err
	}
	owner, err := openPostgresOwner(ctx, opt)
	if err != nil {
		return err
	}
	defer owner.Close()
	var occupied bool
	if err := owner.conn.QueryRowContext(ctx, `SELECT EXISTS (
		SELECT 1 FROM pg_catalog.pg_depend d JOIN pg_catalog.pg_namespace n
		ON d.refclassid = 'pg_catalog.pg_namespace'::regclass AND d.refobjid = n.oid
		WHERE n.nspname = current_schema())`).Scan(&occupied); err != nil {
		return errors.New("could not inspect PostgreSQL restore destination")
	}
	if occupied {
		return ErrPostgresSchemaOccupied
	}
	if err := mkPrivateDir(scratchDir); err != nil {
		return err
	}
	decoded, err := os.CreateTemp(scratchDir, ".postgres-restore-*")
	if err != nil {
		return err
	}
	defer os.Remove(decoded.Name())
	defer decoded.Close()
	// Decoding is deliberately offline: no database URL, service, role, native
	// connection or native restore privilege is supplied to pg_restore.
	writer := &postgresArchiveDiskWriter{w: decoded, dir: scratchDir}
	if err := nativePostgresTool(ctx, tools.restore, []string{}, writer,
		"--data-only", "--no-owner", "--no-privileges", "--file=-", archive); err != nil {
		if writer.err != nil {
			return writer.err
		}
		return fmt.Errorf("decode PostgreSQL recovery archive: %w", err)
	}
	if _, err := decoded.Seek(0, io.SeekStart); err != nil {
		return err
	}
	return restorePostgresDataLocked(ctx, owner, decoded)
}

// The caller owns an empty schema exclusively. Keeping the entire import in a
// single helper makes cancellation/error rollback cover trusted initialization
// just as strictly as the COPY data itself.
func restorePostgresDataLocked(ctx context.Context, owner *postgresOwner, input io.Reader) error {
	return owner.conn.Raw(func(raw any) error {
		native := raw.(*stdlib.Conn).Conn()
		tx, err := native.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
		if err != nil {
			return err
		}
		defer func() {
			cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = tx.Rollback(cleanup)
		}()
		if err := initializePostgresRecovery(ctx, tx); err != nil {
			return err
		}
		if err := importPostgresArchiveData(ctx, tx, owner.opt.Schema, input); err != nil {
			return err
		}
		if err := tx.Commit(ctx); err != nil {
			return errors.New("could not commit PostgreSQL recovery; inspect the destination before retrying")
		}
		return nil
	})
}

func initializePostgresRecovery(ctx context.Context, tx pgx.Tx) error {
	names, err := listMigrations(postgresMigrations())
	if err != nil {
		return err
	}
	// All trusted migrations and transferred data share ONE transaction. A
	// failed restore rolls back even the initialization/migration metadata.
	for _, name := range names {
		raw, err := fs.ReadFile(postgresMigrations(), name)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
			version text PRIMARY KEY, checksum text NOT NULL,
			applied_at timestamp with time zone NOT NULL DEFAULT CURRENT_TIMESTAMP)`); err != nil {
			return errors.New("could not initialize PostgreSQL recovery metadata")
		}
		if _, err := tx.Exec(ctx, string(raw)); err != nil {
			return fmt.Errorf("initialize trusted PostgreSQL migration %s: failed", name)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO schema_migrations (version, checksum) VALUES ($1, $2)`, strings.TrimSuffix(name, ".sql"), migrationChecksum(raw)); err != nil {
			return errors.New("could not initialize PostgreSQL recovery migration history")
		}
	}
	return nil
}

type recoveryTable struct {
	name    string
	columns []string
	digest  *archiveDigest
}

type recoverySequence struct {
	name, table, column string
}

func recoveryCatalog(ctx context.Context, tx pgx.Tx) (map[string]*recoveryTable, map[string]recoverySequence, error) {
	tables := map[string]*recoveryTable{}
	rows, err := tx.Query(ctx, `SELECT c.relname, a.attname FROM pg_catalog.pg_class c
		JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
		JOIN pg_catalog.pg_attribute a ON a.attrelid = c.oid
		WHERE n.nspname = current_schema() AND c.relkind = 'r' AND a.attnum > 0 AND NOT a.attisdropped
		ORDER BY c.relname, a.attnum`)
	if err != nil {
		return nil, nil, err
	}
	for rows.Next() {
		var table, column string
		if err := rows.Scan(&table, &column); err != nil {
			rows.Close()
			return nil, nil, err
		}
		if tables[table] == nil {
			tables[table] = &recoveryTable{name: table}
		}
		tables[table].columns = append(tables[table].columns, column)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, nil, err
	}
	sequences := map[string]recoverySequence{}
	rows, err = tx.Query(ctx, `SELECT s.relname, t.relname, a.attname
		FROM pg_catalog.pg_class s JOIN pg_catalog.pg_namespace n ON n.oid = s.relnamespace
		JOIN pg_catalog.pg_depend d ON d.classid = 'pg_catalog.pg_class'::regclass AND d.objid = s.oid AND d.deptype IN ('a', 'i')
		JOIN pg_catalog.pg_class t ON d.refclassid = 'pg_catalog.pg_class'::regclass AND d.refobjid = t.oid
		JOIN pg_catalog.pg_attribute a ON a.attrelid = t.oid AND a.attnum = d.refobjsubid
		WHERE n.nspname = current_schema() AND s.relkind = 'S'`)
	if err != nil {
		return nil, nil, err
	}
	for rows.Next() {
		var s recoverySequence
		if err := rows.Scan(&s.name, &s.table, &s.column); err != nil {
			rows.Close()
			return nil, nil, err
		}
		sequences[s.name] = s
	}
	err = rows.Err()
	rows.Close()
	return tables, sequences, err
}

func recoveryColumnSQL(columns []string) string {
	quoted := make([]string, len(columns))
	for i, column := range columns {
		quoted[i] = pgx.Identifier{column}.Sanitize()
	}
	return strings.Join(quoted, ", ")
}

func importPostgresArchiveData(ctx context.Context, tx pgx.Tx, schema string, input io.Reader) error {
	tables, sequences, err := recoveryCatalog(ctx, tx)
	if err != nil {
		return errors.New("could not inspect trusted recovery catalog")
	}
	names := make([]string, 0, len(tables))
	for name := range tables {
		names = append(names, name)
	}
	sort.Strings(names)
	qualified := make([]string, len(names))
	for i, name := range names {
		qualified[i] = pgx.Identifier{schema, name}.Sanitize()
		// Only trusted freshly installed user triggers are disabled, never FK
		// constraints or unrelated objects. Ownership suffices; no superuser.
		if _, err := tx.Exec(ctx, `ALTER TABLE `+qualified[i]+` DISABLE TRIGGER USER`); err != nil {
			return errors.New("could not suspend trusted recovery triggers")
		}
	}
	if _, err := tx.Exec(ctx, `SET CONSTRAINTS ALL DEFERRED`); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `TRUNCATE `+strings.Join(qualified, ", ")); err != nil {
		return errors.New("could not clear fresh recovery seed rows")
	}
	// Pin canonical COPY output independently of role/server defaults. Source
	// archives produced by Arrmada use the same settings.
	if _, err := tx.Exec(ctx, `SET LOCAL bytea_output = 'hex'; SET LOCAL extra_float_digits = 3`); err != nil {
		return err
	}
	r := newArchiveDataReader(input)
	for {
		name, columns, data, err := r.next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		table := tables[name]
		if table == nil || !reflect.DeepEqual(columns, table.columns) {
			return errors.New("PostgreSQL archive tables/columns do not match this build")
		}
		command := `COPY ` + pgx.Identifier{schema, table.name}.Sanitize() + ` (` + recoveryColumnSQL(table.columns) + `) FROM STDIN`
		tag, err := tx.Conn().PgConn().CopyFrom(ctx, data, command)
		if err != nil || data.err != nil || !data.terminated || data.digest.partial || tag.RowsAffected() != data.digest.rows {
			return fmt.Errorf("restore PostgreSQL table %s: invalid or incomplete COPY data", table.name)
		}
		table.digest = data.digest
	}
	if len(r.seen) != len(tables) || len(r.sequences) != len(sequences) {
		return errors.New("PostgreSQL archive is missing table or sequence data")
	}
	if err := verifyRecoveryMigrations(ctx, tx); err != nil {
		return err
	}
	for _, name := range names {
		table := tables[name]
		if table.digest == nil {
			return errors.New("PostgreSQL archive omitted a table")
		}
		actual := newArchiveDigest()
		command := `COPY (SELECT ` + recoveryColumnSQL(table.columns) + ` FROM ` + pgx.Identifier{schema, name}.Sanitize() + `) TO STDOUT`
		if _, err := tx.Conn().PgConn().CopyTo(ctx, actual, command); err != nil || actual.partial || actual.rows != table.digest.rows || actual.sum != table.digest.sum {
			return fmt.Errorf("PostgreSQL table %s failed transferred-data verification", name)
		}
	}
	for name, seq := range sequences {
		state, ok := r.sequences[name]
		if !ok {
			return errors.New("PostgreSQL archive sequence set does not match this build")
		}
		var max int64
		if err := tx.QueryRow(ctx, `SELECT COALESCE(MAX(`+pgx.Identifier{seq.column}.Sanitize()+`), 0) FROM `+pgx.Identifier{schema, seq.table}.Sanitize()).Scan(&max); err != nil {
			return errors.New("could not verify recovered identity high-water mark")
		}
		// SQLite INTEGER PRIMARY KEY also backs keyed references such as
		// audio_passwords.user_id. Those are always explicitly assigned by the
		// application, so their compatibility sequence can legitimately be
		// unused/behind. Preserve it exactly rather than rejecting valid data.
		if seq.column == "id" && (state.value < max || (!state.called && state.value <= max)) {
			return errors.New("PostgreSQL archive has a sequence behind its restored identities")
		}
		if _, err := tx.Exec(ctx, `SELECT pg_catalog.setval($1::regclass, $2, $3)`, pgx.Identifier{schema, seq.name}.Sanitize(), state.value, state.called); err != nil {
			return errors.New("could not recover PostgreSQL identity high-water mark")
		}
	}
	// Validate all deferred relationships BEFORE the transaction can publish an
	// initialized schema, then re-enable exactly the trusted user triggers.
	if _, err := tx.Exec(ctx, `SET CONSTRAINTS ALL IMMEDIATE`); err != nil {
		return errors.New("PostgreSQL archive has invalid relationships")
	}
	for _, table := range qualified {
		if _, err := tx.Exec(ctx, `ALTER TABLE `+table+` ENABLE TRIGGER USER`); err != nil {
			return errors.New("could not re-enable trusted recovery triggers")
		}
	}
	return nil
}

func verifyRecoveryMigrations(ctx context.Context, tx pgx.Tx) error {
	names, err := listMigrations(postgresMigrations())
	if err != nil {
		return err
	}
	expected := map[string]string{}
	for _, name := range names {
		raw, err := fs.ReadFile(postgresMigrations(), name)
		if err != nil {
			return err
		}
		expected[strings.TrimSuffix(name, ".sql")] = migrationChecksum(raw)
	}
	rows, err := tx.Query(ctx, `SELECT version, checksum FROM schema_migrations`)
	if err != nil {
		return errors.New("could not read recovered migration history")
	}
	defer rows.Close()
	for rows.Next() {
		var version, checksum string
		if err := rows.Scan(&version, &checksum); err != nil {
			return err
		}
		if want, ok := expected[version]; !ok || want != checksum {
			return errors.New("PostgreSQL archive migration history does not match this build; restore with the matching build before upgrading")
		}
		delete(expected, version)
	}
	if rows.Err() != nil || len(expected) != 0 {
		return errors.New("PostgreSQL archive migration history is incomplete; restore with the matching build")
	}
	return nil
}
