package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"sync"
	"testing"
)

func txTestDB(t *testing.T) *sql.DB {
	t.Helper()
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	db := st.DB()
	for _, q := range []string{
		`CREATE TABLE tx_counter (id INTEGER PRIMARY KEY, n INTEGER NOT NULL)`,
		`INSERT INTO tx_counter (id, n) VALUES (1, 0)`,
		`CREATE TABLE tx_log (id INTEGER PRIMARY KEY AUTOINCREMENT, note TEXT)`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	return db
}

func counter(t *testing.T, db *sql.DB) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT n FROM tx_counter WHERE id = 1`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// Read-then-write transactions racing each other and a stream of autocommit writes all
// go through. Under DEFERRED transactions the read half takes a snapshot, another
// connection commits, and the write half fails with BUSY_SNAPSHOT — which no busy timeout
// can wait out. IMMEDIATE makes them queue instead.
func TestWithTxReadThenWriteUnderContention(t *testing.T) {
	db := txTestDB(t)
	ctx := context.Background()
	const iterations = 200

	var wg sync.WaitGroup
	errs := make(chan error, 3*iterations)
	for w := 0; w < 2; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				errs <- WithTx(ctx, db, func(tx *sql.Tx) error {
					var n int
					if err := tx.QueryRowContext(ctx, `SELECT n FROM tx_counter WHERE id = 1`).Scan(&n); err != nil {
						return err
					}
					_, err := tx.ExecContext(ctx, `UPDATE tx_counter SET n = ? WHERE id = 1`, n+1)
					return err
				})
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			_, err := db.ExecContext(ctx, `INSERT INTO tx_log (note) VALUES ('autocommit')`)
			errs <- err
		}
	}()
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("unexpected error under contention: %v", err)
		}
	}
	// No lost updates: every increment read the value the previous one wrote.
	if got := counter(t, db); got != 2*iterations {
		t.Errorf("counter = %d, want %d", got, 2*iterations)
	}
}

func TestWithTxRollsBackOnError(t *testing.T) {
	db := txTestDB(t)
	boom := errors.New("boom")
	err := WithTx(context.Background(), db, func(tx *sql.Tx) error {
		if _, err := tx.Exec(`UPDATE tx_counter SET n = 99 WHERE id = 1`); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the callback's error", err)
	}
	if got := counter(t, db); got != 0 {
		t.Errorf("counter = %d after a failed transaction, want 0", got)
	}
}

func TestWithTxRollsBackOnPanicAndRepanics(t *testing.T) {
	db := txTestDB(t)
	func() {
		defer func() {
			if p := recover(); p != "kaboom" {
				t.Errorf("recovered %v, want the original panic", p)
			}
		}()
		_ = WithTx(context.Background(), db, func(tx *sql.Tx) error {
			if _, err := tx.Exec(`UPDATE tx_counter SET n = 99 WHERE id = 1`); err != nil {
				return err
			}
			panic("kaboom")
		})
		t.Error("WithTx swallowed the panic")
	}()
	if got := counter(t, db); got != 0 {
		t.Errorf("counter = %d after a panicking transaction, want 0", got)
	}
	// The connection went back to the pool clean: a new transaction works.
	if err := WithTx(context.Background(), db, func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE tx_counter SET n = 1 WHERE id = 1`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if got := counter(t, db); got != 1 {
		t.Errorf("counter = %d, want 1", got)
	}
}

func TestWithTxCommits(t *testing.T) {
	db := txTestDB(t)
	if err := WithTx(context.Background(), db, func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE tx_counter SET n = 7 WHERE id = 1`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if got := counter(t, db); got != 7 {
		t.Errorf("counter = %d, want 7", got)
	}
}

func TestIsBusy(t *testing.T) {
	if IsBusy(nil) || IsBusy(errors.New("database is locked")) {
		t.Error("only a SQLite error carrying SQLITE_BUSY counts as busy")
	}
}

// While another connection holds the write lock, WithTx gives up after its retries with
// the busy error rather than hanging or pretending it ran.
func TestWithTxGivesUpWhileLocked(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	ctx := context.Background()
	holder, err := st.DB().BeginTx(ctx, nil) // IMMEDIATE: takes the write lock now
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = holder.Rollback() }()

	// A second pool with no busy timeout, so BEGIN reports BUSY at once.
	other, err := sql.Open("sqlite", "file:"+filepath.Join(dir, "arrmada.db")+"?_txlock=immediate")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = other.Close() }()
	ran := false
	err = WithTx(ctx, other, func(*sql.Tx) error { ran = true; return nil })
	if !IsBusy(err) {
		t.Fatalf("err = %v, want SQLITE_BUSY", err)
	}
	if ran {
		t.Error("the callback ran without a transaction")
	}
}
