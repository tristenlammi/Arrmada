package store

import (
	"context"
	"errors"
	"runtime"
	"sync"
	"testing"
	"time"
)

func TestPostgresWithTxReadThenWriteAndAutocommitContention(t *testing.T) {
	ctx, db, _, _ := postgresTestPool(t)
	if err := migratePostgres(ctx, db, postgresMigrations(), nil); err != nil {
		t.Fatal(err)
	}
	const iterations = 30
	errs := make(chan error, 3*iterations)
	var writers sync.WaitGroup
	for range 2 {
		writers.Go(func() {
			for range iterations {
				errs <- WithTx(ctx, db, func(tx *Transaction) error {
					var count int
					if err := tx.QueryRowContext(ctx, `SELECT min_seeders FROM quality_profiles WHERE id = ?`, 1).Scan(&count); err != nil {
						return err
					}
					_, err := tx.ExecContext(ctx, `UPDATE quality_profiles SET min_seeders = ? WHERE id = ?`, count+1, 1)
					return err
				})
			}
		})
	}
	writers.Go(func() {
		for range iterations {
			_, err := db.ExecContext(ctx, `UPDATE quality_profiles SET min_seeders = min_seeders + 1 WHERE id = ?`, 1)
			errs <- err
		}
	})
	writers.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var count int
	if err := db.QueryRowContext(ctx, `SELECT min_seeders FROM quality_profiles WHERE id = 1`).Scan(&count); err != nil || count != 3*iterations {
		t.Fatalf("lost updates: got %d, want %d, err %v", count, 3*iterations, err)
	}
}

func TestPostgresAllApplicationTablesParticipateInWriterLock(t *testing.T) {
	ctx, db, _, _ := postgresTestPool(t)
	if err := migratePostgres(ctx, db, postgresMigrations(), nil); err != nil {
		t.Fatal(err)
	}
	rows, err := db.QueryContext(ctx, `SELECT c.relname FROM pg_catalog.pg_class c
		JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = current_schema() AND c.relkind = 'r' AND c.relname != 'schema_migrations'
		AND NOT EXISTS (SELECT 1 FROM pg_catalog.pg_trigger t WHERE t.tgrelid = c.oid
			AND t.tgname = 'arrmada_serialize_writes' AND t.tgenabled = 'O' AND NOT t.tgisinternal
			AND t.tgtype::integer = 62)`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var table string
		if err := rows.Scan(&table); err != nil {
			t.Fatal(err)
		}
		t.Errorf("application table %s lacks its BEFORE STATEMENT INSERT/UPDATE/DELETE/TRUNCATE writer lock", table)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresWithTxFailureAndPanicDoNotReplayOrLeakLocks(t *testing.T) {
	ctx, db, _, _ := postgresTestPool(t)
	if err := migratePostgres(ctx, db, postgresMigrations(), nil); err != nil {
		t.Fatal(err)
	}
	boom := errors.New("callback failed")
	attempts := 0
	err := WithTx(ctx, db, func(tx *Transaction) error {
		attempts++
		if _, err := tx.ExecContext(ctx, `UPDATE quality_profiles SET min_seeders = 99 WHERE id = 1`); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) || attempts != 1 {
		t.Fatalf("callback replay/error = %d, %v", attempts, err)
	}
	func() {
		defer func() {
			if got := recover(); got != "callback panic" {
				t.Errorf("panic not preserved: %v", got)
			}
		}()
		_ = WithTx(ctx, db, func(tx *Transaction) error {
			if _, err := tx.ExecContext(ctx, `UPDATE quality_profiles SET min_seeders = 100 WHERE id = 1`); err != nil {
				return err
			}
			panic("callback panic")
		})
	}()
	if err := WithTx(ctx, db, func(tx *Transaction) error {
		var count int
		if err := tx.QueryRowContext(ctx, `SELECT min_seeders FROM quality_profiles WHERE id = 1`).Scan(&count); err != nil {
			return err
		}
		if count != 0 {
			t.Errorf("failed/panicking transaction committed %d", count)
		}
		return nil
	}); err != nil {
		t.Fatalf("failed callback leaked a lock: %v", err)
	}
	if err := WithTx(ctx, db.pool, func(*Transaction) error { return nil }); err == nil {
		t.Fatal("raw PostgreSQL pool bypassed binding")
	}
}

func TestPostgresWithTxCancelledWriterDoesNotRunCallback(t *testing.T) {
	ctx, db, _, _ := postgresTestPool(t)
	if err := migratePostgres(ctx, db, postgresMigrations(), nil); err != nil {
		t.Fatal(err)
	}
	held := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- WithTx(ctx, db, func(*Transaction) error {
			close(held)
			<-release
			return nil
		})
	}()
	select {
	case <-held:
	case err := <-done:
		t.Fatalf("holder did not acquire writer lock: %v", err)
	case <-ctx.Done():
		close(release)
		t.Fatal(ctx.Err())
	}
	waitCtx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	ran := false
	err := WithTx(waitCtx, db, func(*Transaction) error { ran = true; return nil })
	close(release)
	if holdErr := <-done; holdErr != nil {
		t.Fatal(holdErr)
	}
	if err == nil || ran {
		t.Fatalf("cancelled writer ran callback: ran=%v, err=%v", ran, err)
	}
	if err := WithTx(ctx, db, func(*Transaction) error { return nil }); err != nil {
		t.Fatalf("cancelled writer leaked lock: %v", err)
	}
}

func TestPostgresWithTxGoexitReleasesWriterLock(t *testing.T) {
	ctx, db, _, _ := postgresTestPool(t)
	if err := migratePostgres(ctx, db, postgresMigrations(), nil); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := WithTx(ctx, db, func(tx *Transaction) error {
			if _, err := tx.ExecContext(ctx, `UPDATE quality_profiles SET min_seeders = 99 WHERE id = 1`); err != nil {
				return err
			}
			runtime.Goexit()
			return nil
		}); err != nil {
			t.Errorf("transaction before Goexit: %v", err)
		}
	}()
	<-done
	if err := WithTx(ctx, db, func(tx *Transaction) error {
		var count int
		if err := tx.QueryRowContext(ctx, `SELECT min_seeders FROM quality_profiles WHERE id = 1`).Scan(&count); err != nil {
			return err
		}
		if count != 0 {
			t.Errorf("Goexit committed %d", count)
		}
		return nil
	}); err != nil {
		t.Fatalf("Goexit leaked writer lock: %v", err)
	}
}

func TestPostgresWriterLockDoesNotBlockAnotherSchema(t *testing.T) {
	ctx, first, _, _ := postgresTestPool(t)
	otherCtx, second, _, _ := postgresTestPool(t)
	for _, db := range []*Database{first, second} {
		if err := migratePostgres(ctx, db, postgresMigrations(), nil); err != nil {
			t.Fatal(err)
		}
	}
	var once sync.Once
	release := make(chan struct{})
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	held := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- WithTx(ctx, first, func(*Transaction) error {
			close(held)
			<-release
			return nil
		})
	}()
	select {
	case <-held:
	case err := <-done:
		t.Fatalf("first schema lock failed: %v", err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	bounded, cancel := context.WithTimeout(otherCtx, 10*time.Second)
	defer cancel()
	err := WithTx(bounded, second, func(tx *Transaction) error {
		_, err := tx.ExecContext(bounded, `UPDATE quality_profiles SET min_seeders = 1 WHERE id = 1`)
		return err
	})
	once.Do(func() { close(release) })
	if holdErr := <-done; holdErr != nil {
		t.Fatal(holdErr)
	}
	if err != nil {
		t.Fatalf("write in another schema was blocked: %v", err)
	}
}
