package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestPostgresLifecycleOwnership(t *testing.T) {
	ctx, _, opt, _ := postgresFixture(t)
	opt.MaxConnections = 1
	owner, err := openPostgresOwner(ctx, opt)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = owner.Close() })
	if err := migratePostgresLocked(ctx, owner.conn, postgresMigrations(), nil); err != nil {
		t.Fatal(err)
	}
	if second, err := openPostgresOwner(ctx, opt); !errors.Is(err, ErrDatabaseInUse) {
		if second != nil {
			_ = second.Close()
		}
		t.Fatalf("second startup while migrating = %v", err)
	}
	db, err := owner.activate(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if db.Stats().MaxOpenConnections != 1 {
		t.Fatal("ownership changed the application connection limit")
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO settings (key, value) VALUES ('owner-test', 'value')`); err != nil {
		t.Fatal(err)
	}
	if second, err := openPostgresOwner(ctx, opt); !errors.Is(err, ErrDatabaseInUse) {
		if second != nil {
			_ = second.Close()
		}
		t.Fatalf("second runtime owner = %v", err)
	}
	// Even a migration caller using the old connection-only boundary must not
	// overlap application sessions guarded by shared schema locks.
	unguarded, err := openPostgresPool(ctx, opt)
	if err != nil {
		t.Fatal(err)
	}
	defer unguarded.Close()
	if err := migratePostgres(ctx, unguarded, postgresMigrations(), nil); !errors.Is(err, ErrDatabaseInUse) {
		t.Fatalf("migration while runtime is active = %v", err)
	}
	if err := owner.Close(); err != nil {
		t.Fatal(err)
	}
	if err := db.PingContext(ctx); err == nil {
		t.Fatal("closed owner left repositories usable")
	}
	next, err := openPostgresOwner(ctx, opt)
	if err != nil {
		t.Fatalf("clean shutdown did not release ownership: %v", err)
	}
	defer next.Close()
	if err := migratePostgresLocked(ctx, next.conn, postgresMigrations(), nil); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresLifecycleLostOwnerFencesActiveTransaction(t *testing.T) {
	ctx, admin, opt, _ := postgresFixture(t)
	owner, err := openPostgresOwner(ctx, opt)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = owner.Close() })
	if err := migratePostgresLocked(ctx, owner.conn, postgresMigrations(), nil); err != nil {
		t.Fatal(err)
	}
	db, err := owner.activate(ctx)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := db.pool.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO settings (key, value) VALUES ('in-flight-owner', 'value')`); err != nil {
		t.Fatal(err)
	}
	// Terminate only the disposable fixture's coordination connection, never
	// application sessions or any user database session.
	if _, err := admin.Exec(ctx, `SELECT pg_terminate_backend($1)`, owner.pid); err != nil {
		t.Fatal(err)
	}
	// The old transaction's session lock must fence recovery independently of
	// heartbeat timing, even if the heartbeat has already closed the pool.
	if second, err := openPostgresOwner(ctx, opt); !errors.Is(err, ErrDatabaseInUse) {
		if second != nil {
			_ = second.Close()
		}
		t.Fatalf("offline ownership overlapped an in-flight old transaction: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	// If monitor hasn't run yet, remove idle sessions as well. New sessions
	// must refuse admission because the original owner identity is gone.
	db.pool.SetMaxIdleConns(0)
	if err := db.PingContext(ctx); err == nil {
		t.Fatal("replacement connection admitted after ownership was lost")
	}
	if err := owner.Close(); err != nil {
		t.Fatal(err)
	}
	next, err := openPostgresOwner(ctx, opt)
	if err != nil {
		t.Fatalf("drained old sessions still block offline recovery: %v", err)
	}
	defer next.Close()
	// Old pool remains closed even after a new owner comes and goes.
	if err := db.PingContext(ctx); err == nil {
		t.Fatal("stale process silently rejoined a new lifecycle owner")
	}
}

func TestPostgresLifecycleHeartbeatClosesLostOwner(t *testing.T) {
	ctx, admin, opt, _ := postgresFixture(t)
	owner, err := openPostgresOwner(ctx, opt)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = owner.Close() })
	db, err := owner.activate(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, `SELECT pg_terminate_backend($1)`, owner.pid); err != nil {
		t.Fatal(err)
	}
	select {
	case <-owner.done:
	case <-time.After(10 * time.Second):
		t.Fatal("lost lifecycle session did not stop the application pool")
	}
	// done announces shutdown; wait for the pool to actually be closed using
	// its own Stats rather than relying on Close scheduling after the signal.
	deadline := time.Now().Add(5 * time.Second)
	for db.Stats().OpenConnections != 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if err := db.PingContext(ctx); err == nil {
		t.Fatal("heartbeat left application pool usable")
	}
}

func TestPostgresLifecycleSchemaIsolation(t *testing.T) {
	ctx, _, opt, _ := postgresFixture(t)
	_, _, other, _ := postgresFixture(t)
	one, err := openPostgresOwner(ctx, opt)
	if err != nil {
		t.Fatal(err)
	}
	defer one.Close()
	two, err := openPostgresOwner(ctx, other)
	if err != nil {
		t.Fatalf("unrelated designated schema was locked: %v", err)
	}
	defer two.Close()
}

func TestPostgresLifecycleBackupBeforeUpgrade(t *testing.T) {
	tools := archiveTestTools(t)
	ctx, _, opt, _ := postgresFixture(t)
	opt.MaxConnections = 1
	owner, err := openPostgresOwner(ctx, opt)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	old := postgresMigrationsBeforeSubtitleIDs(t)
	if err := migratePostgresLocked(ctx, owner.conn, old, nil); err != nil {
		t.Fatal(err)
	}
	backedUp := false
	if err := migratePostgresLocked(ctx, owner.conn, postgresMigrations(), func(ctx context.Context, _ []string) error {
		if err := postgresSnapshotLocked(ctx, owner.control, opt, tools, filepath.Join(t.TempDir(), "before-upgrade.pgdump")); err != nil {
			return err
		}
		backedUp = true
		return nil
	}); err != nil || !backedUp {
		t.Fatalf("lifecycle upgrade with real native backup = %v, backed up %v", err, backedUp)
	}
}
