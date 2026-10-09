package store

import (
	"context"
	"database/sql"
	"errors"
	"math/rand/v2"
	"time"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

// Execer is what a repository method needs to run statements, satisfied by both *sql.DB
// and *sql.Tx. A method that takes one works on its own or inside a caller's transaction.
type Execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

var (
	_ Execer = (*sql.DB)(nil)
	_ Execer = (*sql.Tx)(nil)
)

// beginAttempts is how many times WithTx tries to BEGIN before giving up on a busy
// database. busy_timeout already waits up to five seconds inside each attempt, so this
// only covers the rare case where SQLite reports BUSY straight away.
const beginAttempts = 3

// WithTx runs fn in one transaction and commits it, or rolls it back when fn returns an
// error or panics (the panic carries on after the rollback). It is the one way to make
// several writes land together.
//
// Pools opened by this package BEGIN IMMEDIATE (_txlock=immediate), so the write lock is
// taken up front: a transaction that reads and then writes can't be refused halfway with
// BUSY_SNAPSHOT because another connection committed in between. Keep fn short — never do
// network I/O or probe files inside it — because every other writer waits for it.
func WithTx(ctx context.Context, db *sql.DB, fn func(tx *sql.Tx) error) (err error) {
	tx, err := beginTx(ctx, db)
	if err != nil {
		return err
	}
	defer func() {
		if p := recover(); p != nil {
			_ = tx.Rollback()
			panic(p)
		}
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	if err = fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}

// beginTx starts a transaction, retrying a few times with a little jitter while the
// database reports itself busy.
func beginTx(ctx context.Context, db *sql.DB) (*sql.Tx, error) {
	var lastErr error
	for attempt := 0; attempt < beginAttempts; attempt++ {
		if attempt > 0 {
			wait := 20*time.Millisecond + rand.N(60*time.Millisecond)
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(wait):
			}
		}
		tx, err := db.BeginTx(ctx, nil)
		if err == nil {
			return tx, nil
		}
		if !IsBusy(err) {
			return nil, err
		}
		lastErr = err
	}
	return nil, lastErr
}

// IsBusy reports whether err is SQLite saying the database is locked by another
// connection (SQLITE_BUSY, any extended code).
func IsBusy(err error) bool {
	var se *sqlite.Error
	return errors.As(err, &se) && se.Code()&0xff == sqlite3.SQLITE_BUSY
}
