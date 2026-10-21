package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

// ConvertSQLiteToPostgres copies a current, unmodified SQLite installation into
// an empty pre-provisioned PostgreSQL schema. Stop all source writers first.
// The SQLite database/WAL are opened read-only and are never upgraded, replaced
// or checkpointed. Destination initialization and data share one transaction.
func ConvertSQLiteToPostgres(ctx context.Context, sourceFile string, destination PostgresOptions, workDir string) error {
	return convertSQLiteToPostgres(ctx, sourceFile, destination, workDir, nil)
}

func convertSQLiteToPostgres(ctx context.Context, sourceFile string, destination PostgresOptions, workDir string, beforeCommit func() error) error {
	canonical, err := filepath.EvalSymlinks(sourceFile)
	if err != nil {
		return errors.New("SQLite conversion source must be an existing regular database file")
	}
	sourceFile, err = filepath.Abs(canonical)
	if err != nil {
		return err
	}
	info, err := os.Stat(sourceFile)
	if err != nil || !info.Mode().IsRegular() || !sqliteSingleLink(info) {
		return errors.New("SQLite conversion source must be a regular file without hard-link aliases")
	}
	reference, model, cleanup, err := conversionReference(ctx, workDir)
	if err != nil {
		return err
	}
	defer cleanup()
	defer reference.Close()
	lease, err := lockSQLiteUse(filepath.Dir(sourceFile), true)
	if err != nil {
		return err
	}
	defer lease.Close()
	if pending, err := PendingRestore(filepath.Dir(sourceFile)); err != nil || pending != nil {
		return errors.New("resolve the source's staged SQLite restore before conversion")
	}
	source, err := openSQLiteConversionSource(ctx, sourceFile)
	if err != nil {
		return err
	}
	defer source.Close()
	sourceTx, err := source.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return err
	}
	defer sourceTx.Rollback()
	if err := requireSQLiteConversionSchema(ctx, sourceTx, model); err != nil {
		return err
	}
	high, err := conversionSQLiteHighWater(ctx, sourceTx, model)
	if err != nil {
		return err
	}
	expected := map[string]conversionDigest{}
	for _, table := range model.tables {
		digest, err := sqliteConversionDigest(ctx, sourceTx, table, false, high)
		if err != nil {
			return err
		}
		expected[table.name] = digest
	}
	owner, err := openPostgresOwner(ctx, destination)
	if err != nil {
		return err
	}
	defer owner.Close()
	if err := emptyPostgresConversionDestination(ctx, owner); err != nil {
		return err
	}
	return owner.conn.Raw(func(raw any) error {
		tx, err := raw.(*stdlib.Conn).Conn().BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
		if err != nil {
			return err
		}
		defer rollbackConversionPG(tx)
		if err := initializePostgresRecovery(ctx, tx); err != nil {
			return err
		}
		seqs, err := requirePostgresConversionSchema(ctx, tx, model)
		if err != nil {
			return err
		}
		qualified := make([]string, len(model.tables))
		for i, table := range model.tables {
			qualified[i] = pgx.Identifier{destination.Schema, table.name}.Sanitize()
			if _, err := tx.Exec(ctx, `ALTER TABLE `+qualified[i]+` DISABLE TRIGGER USER`); err != nil {
				return errors.New("could not suspend trusted conversion triggers")
			}
		}
		if _, err := tx.Exec(ctx, `SET CONSTRAINTS ALL DEFERRED; TRUNCATE `+strings.Join(qualified, ", ")); err != nil {
			return errors.New("could not clear fresh PostgreSQL seed rows")
		}
		for _, table := range model.tables {
			rows, err := sourceTx.QueryContext(ctx, `SELECT `+recoveryColumnSQL(table.names())+` FROM `+pgx.Identifier{table.name}.Sanitize())
			if err != nil {
				return err
			}
			stream := &sqliteConversionCopySource{rows: rows, table: table}
			count, err := tx.CopyFrom(ctx, pgx.Identifier{destination.Schema, table.name}, table.names(), stream)
			_ = rows.Close()
			if stream.err != nil {
				return stream.err
			}
			if err != nil || count != expected[table.name].rows || stream.digest != expected[table.name] {
				return fmt.Errorf("PostgreSQL conversion COPY verification failed for %s (row values omitted)", table.name)
			}
			digest, err := postgresConversionDigest(ctx, tx, table, false, nil)
			if err != nil || digest != expected[table.name] {
				return fmt.Errorf("PostgreSQL transferred-data verification failed for %s", table.name)
			}
		}
		for _, seq := range seqs {
			n := high[seq.table]
			value, called := n, true
			if value == 0 {
				value = 1
				called = false
			}
			if _, err := tx.Exec(ctx, `SELECT pg_catalog.setval($1::regclass,$2,$3)`, pgx.Identifier{destination.Schema, seq.name}.Sanitize(), value, called); err != nil {
				return errors.New("could not set converted PostgreSQL identity high-water marks")
			}
		}
		if _, err := tx.Exec(ctx, `SET CONSTRAINTS ALL IMMEDIATE`); err != nil {
			return errors.New("converted PostgreSQL relationships are invalid")
		}
		for _, name := range qualified {
			if _, err := tx.Exec(ctx, `ALTER TABLE `+name+` ENABLE TRIGGER USER`); err != nil {
				return errors.New("could not re-enable trusted PostgreSQL triggers")
			}
		}
		if beforeCommit != nil {
			if err := beforeCommit(); err != nil {
				return err
			}
		}
		if err := tx.Commit(ctx); err != nil {
			return errors.New("PostgreSQL conversion COMMIT outcome is uncertain; inspect the destination before retrying")
		}
		return nil
	})
}

// ConvertPostgresToSQLite publishes a new standalone SQLite file only after
// typed transfer, relationship/integrity checks, fingerprints and a checkpoint.
// The source schema is exclusively owned for the operation and read-only.
func ConvertPostgresToSQLite(ctx context.Context, source PostgresOptions, destinationFile string) error {
	return convertPostgresToSQLite(ctx, source, destinationFile, nil)
}

func convertPostgresToSQLite(ctx context.Context, source PostgresOptions, destinationFile string, beforePublish func() error) error {
	if err := conversionDestinationFree(destinationFile); err != nil {
		return err
	}
	parent := filepath.Dir(destinationFile)
	if err := mkPrivateDir(parent); err != nil {
		return err
	}
	lease, err := lockSQLiteUse(parent, true)
	if err != nil {
		return err
	}
	defer lease.Close()
	target, model, cleanup, err := conversionReference(ctx, parent)
	if err != nil {
		return err
	}
	defer cleanup()
	defer target.Close()
	target.db.SetMaxOpenConns(1)
	target.db.SetConnMaxLifetime(0)
	owner, err := openPostgresOwner(ctx, source)
	if err != nil {
		return err
	}
	defer owner.Close()
	err = owner.conn.Raw(func(raw any) error {
		sourceTx, err := raw.(*stdlib.Conn).Conn().BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
		if err != nil {
			return err
		}
		defer rollbackConversionPG(sourceTx)
		seqs, err := requirePostgresConversionSchema(ctx, sourceTx, model)
		if err != nil {
			return err
		}
		high := map[string]int64{}
		for _, seq := range seqs {
			var value int64
			var called bool
			if err := sourceTx.QueryRow(ctx, `SELECT last_value, is_called FROM `+pgx.Identifier{source.Schema, seq.name}.Sanitize()).Scan(&value, &called); err != nil {
				return errors.New("could not read PostgreSQL identity high-water marks")
			}
			if !called {
				value--
			}
			if value < 0 {
				return ErrConversionIncompatible
			}
			high[seq.table] = value
		}
		expected := map[string]conversionDigest{}
		for _, table := range model.tables {
			digest, err := postgresConversionDigest(ctx, sourceTx, table, true, high)
			if err != nil {
				return err
			}
			expected[table.name] = digest
		}
		// A standalone rollback-journal staging file avoids publishing a WAL pair.
		if _, err := target.db.ExecContext(ctx, `PRAGMA journal_mode=DELETE; PRAGMA foreign_keys=OFF`); err != nil {
			return err
		}
		tx, err := target.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		for _, object := range model.objects {
			if object.kind == "trigger" {
				if _, err := tx.ExecContext(ctx, `DROP TRIGGER `+pgx.Identifier{object.name}.Sanitize()); err != nil {
					return err
				}
			}
		}
		for _, table := range model.tables {
			if _, err := tx.ExecContext(ctx, `DELETE FROM `+pgx.Identifier{table.name}.Sanitize()); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM sqlite_sequence`); err != nil {
			return err
		}
		for _, table := range model.tables {
			rows, err := sourceTx.Query(ctx, `SELECT `+recoveryColumnSQL(table.names())+` FROM `+pgx.Identifier{source.Schema, table.name}.Sanitize())
			if err != nil {
				return err
			}
			placeholders := strings.TrimSuffix(strings.Repeat("?,", len(table.columns)), ",")
			statement, err := tx.PrepareContext(ctx, `INSERT INTO `+pgx.Identifier{table.name}.Sanitize()+` (`+recoveryColumnSQL(table.names())+`) VALUES (`+placeholders+`)`)
			if err != nil {
				rows.Close()
				return err
			}
			for rows.Next() {
				values, err := rows.Values()
				if err != nil {
					rows.Close()
					statement.Close()
					return err
				}
				values, err = conversionValues(table, values, true)
				if err != nil {
					rows.Close()
					statement.Close()
					return err
				}
				if err := conversionDiskSpace(parent, values); err != nil {
					rows.Close()
					statement.Close()
					return err
				}
				for i, v := range values {
					if stamp, ok := v.(time.Time); ok {
						values[i] = stamp.Format("2006-01-02 15:04:05.999999")
					}
				}
				if _, err := statement.ExecContext(ctx, values...); err != nil {
					rows.Close()
					statement.Close()
					return fmt.Errorf("SQLite conversion insert failed for %s (row values omitted)", table.name)
				}
			}
			err = rows.Err()
			rows.Close()
			_ = statement.Close()
			if err != nil {
				return err
			}
			digest, err := sqliteConversionDigest(ctx, tx, table, true, nil)
			if err != nil || digest != expected[table.name] {
				return fmt.Errorf("SQLite transferred-data verification failed for %s", table.name)
			}
		}
		// sqlite_sequence also retains bridge high-water entries for non-
		// AUTOINCREMENT identities. SQLite ignores those for normal allocation;
		// returning to PostgreSQL can still preserve its pruned/rollback gaps.
		for name, n := range high {
			if _, err := tx.ExecContext(ctx, `DELETE FROM sqlite_sequence WHERE name=?`, name); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO sqlite_sequence (name,seq) VALUES (?,?)`, name, n); err != nil {
				return err
			}
		}
		for _, object := range model.objects {
			if object.kind == "trigger" {
				if _, err := tx.ExecContext(ctx, object.sql); err != nil {
					return errors.New("could not restore trusted SQLite triggers")
				}
			}
		}
		if err := sqliteConversionIntegrity(ctx, tx); err != nil {
			return err
		}
		if err := tx.Commit(); err != nil {
			return errors.New("could not commit private SQLite conversion staging file")
		}
		return nil
	})
	if err != nil {
		return err
	}
	if _, err := target.db.ExecContext(ctx, `PRAGMA foreign_keys=ON`); err != nil {
		return err
	}
	if err := target.Close(); err != nil {
		return err
	}
	if err := quickCheck(ctx, target.dbPath); err != nil {
		return err
	}
	if err := syncFile(target.dbPath); err != nil {
		return err
	}
	if beforePublish != nil {
		if err := beforePublish(); err != nil {
			return err
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := conversionDestinationFree(destinationFile); err != nil {
		return err
	}
	if err := os.Link(target.dbPath, destinationFile); err != nil {
		return fmt.Errorf("publish SQLite conversion without overwriting an existing destination: %w", err)
	}
	syncDir(parent)
	return nil
}

func openSQLiteConversionSource(ctx context.Context, path string) (*sql.DB, error) {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return nil, errors.New("SQLite conversion source must be an existing regular database file")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	u := url.URL{Scheme: "file", Path: abs}
	q := u.Query()
	q.Set("mode", "ro")
	q.Set("_txlock", "deferred")
	q.Add("_pragma", "query_only(1)")
	q.Add("_pragma", "busy_timeout(5000)")
	u.RawQuery = q.Encode()
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func emptyPostgresConversionDestination(ctx context.Context, owner *postgresOwner) error {
	var occupied bool
	if err := owner.conn.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM pg_depend d JOIN pg_namespace n ON d.refclassid='pg_namespace'::regclass AND d.refobjid=n.oid WHERE n.nspname=current_schema())`).Scan(&occupied); err != nil {
		return errors.New("could not inspect PostgreSQL conversion destination")
	}
	if occupied {
		return ErrPostgresSchemaOccupied
	}
	return nil
}

func rollbackConversionPG(tx pgx.Tx) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = tx.Rollback(ctx)
}

func readSQLiteConversionRow(rows *sql.Rows) ([]any, error) {
	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	values := make([]any, len(cols))
	ptrs := make([]any, len(cols))
	for i := range values {
		ptrs[i] = &values[i]
	}
	if err := rows.Scan(ptrs...); err != nil {
		return nil, err
	}
	return values, nil
}

func sqliteConversionDigest(ctx context.Context, q Execer, table conversionTable, toSQLite bool, high map[string]int64) (conversionDigest, error) {
	var digest conversionDigest
	rows, err := q.QueryContext(ctx, `SELECT `+recoveryColumnSQL(table.names())+` FROM `+pgx.Identifier{table.name}.Sanitize())
	if err != nil {
		return digest, err
	}
	defer rows.Close()
	for rows.Next() {
		values, err := readSQLiteConversionRow(rows)
		if err != nil {
			return digest, err
		}
		values, err = conversionValues(table, values, toSQLite)
		if err != nil {
			return digest, err
		}
		digest.add(values)
		if high != nil {
			conversionMaxIdentity(table, values, high)
		}
	}
	return digest, rows.Err()
}

func postgresConversionDigest(ctx context.Context, tx pgx.Tx, table conversionTable, toSQLite bool, high map[string]int64) (conversionDigest, error) {
	var digest conversionDigest
	rows, err := tx.Query(ctx, `SELECT `+recoveryColumnSQL(table.names())+` FROM `+pgx.Identifier{table.name}.Sanitize())
	if err != nil {
		return digest, err
	}
	defer rows.Close()
	for rows.Next() {
		values, err := rows.Values()
		if err != nil {
			return digest, err
		}
		values, err = conversionValues(table, values, toSQLite)
		if err != nil {
			return digest, err
		}
		digest.add(values)
		if high != nil {
			conversionMaxIdentity(table, values, high)
		}
	}
	return digest, rows.Err()
}

type sqliteConversionCopySource struct {
	rows   *sql.Rows
	table  conversionTable
	digest conversionDigest
	err    error
}

func (s *sqliteConversionCopySource) Next() bool { return s.err == nil && s.rows.Next() }
func (s *sqliteConversionCopySource) Values() ([]any, error) {
	values, err := readSQLiteConversionRow(s.rows)
	if err == nil {
		values, err = conversionValues(s.table, values, false)
	}
	if err != nil {
		s.err = err
		return nil, err
	}
	s.digest.add(values)
	return values, nil
}
func (s *sqliteConversionCopySource) Err() error {
	if s.err != nil {
		return s.err
	}
	return s.rows.Err()
}
