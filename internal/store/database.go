package store

import (
	"context"
	"database/sql"
)

// Dialect selects SQL that genuinely differs between the two database engines.
// Queries otherwise use portable SQL and question-mark parameters; rebinding is
// deliberately not a SQL dialect translator.
type Dialect string

const (
	SQLite   Dialect = "sqlite"
	Postgres Dialect = "postgres"
)

// Pool is the repository-facing database capability. A native SQLite pool still
// satisfies it, keeping existing callers and fixtures source-compatible; the
// PostgreSQL implementation must be Database so parameter binding is not lost.
// Transactions are deliberately started through WithTx, not through this API.
type Pool interface {
	Execer
	Exec(string, ...any) (sql.Result, error)
	Query(string, ...any) (*sql.Rows, error)
	QueryRow(string, ...any) *sql.Row
	Prepare(string) (*sql.Stmt, error)
	PrepareContext(context.Context, string) (*sql.Stmt, error)
	PingContext(context.Context) error
	Stats() sql.DBStats
	Close() error
}

// DialectOf identifies the explicit query boundary. Native pools are retained
// only for SQLite callers; PostgreSQL pools are never exposed without binding.
func DialectOf(q Execer) Dialect {
	if q, ok := q.(interface{ Dialect() Dialect }); ok {
		return q.Dialect()
	}
	return SQLite
}

// Database is the query boundary shared by repositories. Keeping the underlying
// pool private prevents a query from accidentally bypassing parameter binding.
// Rows, results and errors remain database/sql values.
type Database struct {
	pool    *sql.DB
	dialect Dialect
}

func (db *Database) Dialect() Dialect { return db.dialect }

func (db *Database) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return db.pool.ExecContext(ctx, db.dialect.bind(query), args...)
}

func (db *Database) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return db.pool.QueryContext(ctx, db.dialect.bind(query), args...)
}

func (db *Database) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	return db.pool.QueryRowContext(ctx, db.dialect.bind(query), args...)
}

func (db *Database) Exec(query string, args ...any) (sql.Result, error) {
	return db.ExecContext(context.Background(), query, args...)
}

func (db *Database) Query(query string, args ...any) (*sql.Rows, error) {
	return db.QueryContext(context.Background(), query, args...)
}

func (db *Database) QueryRow(query string, args ...any) *sql.Row {
	return db.QueryRowContext(context.Background(), query, args...)
}

func (db *Database) PrepareContext(ctx context.Context, query string) (*sql.Stmt, error) {
	return db.pool.PrepareContext(ctx, db.dialect.bind(query))
}

func (db *Database) Prepare(query string) (*sql.Stmt, error) {
	return db.PrepareContext(context.Background(), query)
}

func (db *Database) PingContext(ctx context.Context) error { return db.pool.PingContext(ctx) }
func (db *Database) Close() error                          { return db.pool.Close() }
func (db *Database) Stats() sql.DBStats                    { return db.pool.Stats() }

// Transaction uses the same dialect as its parent pool. Creation stays inside
// store.WithTx, which owns rollback and concurrency policy.
type Transaction struct {
	tx      *sql.Tx
	dialect Dialect
}

func (tx *Transaction) Dialect() Dialect { return tx.dialect }

func (tx *Transaction) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return tx.tx.ExecContext(ctx, tx.dialect.bind(query), args...)
}

func (tx *Transaction) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return tx.tx.QueryContext(ctx, tx.dialect.bind(query), args...)
}

func (tx *Transaction) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	return tx.tx.QueryRowContext(ctx, tx.dialect.bind(query), args...)
}

func (tx *Transaction) Exec(query string, args ...any) (sql.Result, error) {
	return tx.ExecContext(context.Background(), query, args...)
}

func (tx *Transaction) Query(query string, args ...any) (*sql.Rows, error) {
	return tx.QueryContext(context.Background(), query, args...)
}

func (tx *Transaction) QueryRow(query string, args ...any) *sql.Row {
	return tx.QueryRowContext(context.Background(), query, args...)
}

func (tx *Transaction) PrepareContext(ctx context.Context, query string) (*sql.Stmt, error) {
	return tx.tx.PrepareContext(ctx, tx.dialect.bind(query))
}

var (
	_ Pool   = (*Database)(nil)
	_ Pool   = (*sql.DB)(nil)
	_ Execer = (*Database)(nil)
	_ Execer = (*Transaction)(nil)
)
