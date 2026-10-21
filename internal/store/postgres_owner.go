package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
)

// postgresOwner is the private lifecycle primitive for both the application and
// offline recovery. Opening owns two schema-scoped locks on a dedicated session:
// a single-owner lock and an exclusive DDL/recovery lock. Migration/recovery runs
// before application repositories get a pool. Only runtime startup may create
// its missing configured schema, after taking these locks; it never drops one.
//
// After activate, every application pool session holds the shared DDL/recovery
// lock, including idle sessions and in-flight queries/transactions. If the owner
// session is lost, another process CANNOT acquire exclusive access while any old
// application session survives. A heartbeat closes the old pool; AfterConnect
// also verifies the original owner's identity before admitting replacement
// sessions. This is fencing, not just a best-effort heartbeat or a PID file.
type postgresOwner struct {
	control *Database
	conn    *sql.Conn
	opt     PostgresOptions
	pid     int
	started time.Time
	key     int64

	mu     sync.Mutex
	app    *Database
	closed bool
	done   chan struct{}
}

func openPostgresOwner(ctx context.Context, opt PostgresOptions) (*postgresOwner, error) {
	return openPostgresLifecycleOwner(ctx, opt, false)
}

func openPostgresStartupOwner(ctx context.Context, opt PostgresOptions) (*postgresOwner, error) {
	return openPostgresLifecycleOwner(ctx, opt, true)
}

func openPostgresLifecycleOwner(ctx context.Context, opt PostgresOptions, allowMissing bool) (*postgresOwner, error) {
	controlOpt := opt
	// The pinned ownership session must not consume the only connection needed
	// by a pre-migration backup callback. The repository pool still honors the
	// configured application connection limit, including a limit of one.
	if controlOpt.MaxConnections == 1 {
		controlOpt.MaxConnections = 2
	}
	control, err := openPostgresPoolChecked(ctx, controlOpt, nil, allowMissing)
	if err != nil {
		return nil, err
	}
	conn, err := control.pool.Conn(ctx)
	if err != nil {
		_ = control.Close()
		return nil, err
	}
	o := &postgresOwner{control: control, conn: conn, opt: opt, done: make(chan struct{})}
	ok := false
	defer func() {
		if !ok {
			_ = o.Close()
		}
	}()
	var ownerKey int64
	err = conn.QueryRowContext(ctx, `SELECT pg_backend_pid(), backend_start,
		hashtextextended('arrmada/owner/' || current_database() || '/' || $1::text, 0),
		hashtextextended('arrmada/schema/' || current_database() || '/' || $1::text, 0)
		FROM pg_catalog.pg_stat_activity WHERE pid = pg_backend_pid()`, opt.Schema).Scan(&o.pid, &o.started, &ownerKey, &o.key)
	if err != nil {
		return nil, errors.New("could not inspect PostgreSQL lifecycle session")
	}
	for _, key := range []int64{ownerKey, o.key} {
		var locked bool
		if err := conn.QueryRowContext(ctx, `SELECT pg_try_advisory_lock($1)`, key).Scan(&locked); err != nil {
			return nil, errors.New("could not acquire PostgreSQL lifecycle locks")
		} else if !locked {
			return nil, ErrDatabaseInUse
		}
	}
	ok = true
	return o, nil
}

// ensureStartupSchema runs only under exclusive lifecycle ownership, after
// runtime preflight. Existing schemas are never adopted by changing ownership.
// Maintenance, restore and conversion do not call this method.
func (o *postgresOwner) ensureStartupSchema(ctx context.Context) error {
	tx, err := o.conn.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return errors.New("could not begin PostgreSQL schema initialization")
	}
	defer tx.Rollback()
	err = checkPostgresSchema(ctx, tx, o.opt.Schema)
	if errors.Is(err, sql.ErrNoRows) {
		if _, err := tx.ExecContext(ctx, `CREATE SCHEMA IF NOT EXISTS `+pgx.Identifier{o.opt.Schema}.Sanitize()+` AUTHORIZATION CURRENT_USER`); err != nil {
			return errors.New("PostgreSQL schema is missing and could not be created; grant CREATE on this database or pre-provision an application-owned schema")
		}
		err = checkPostgresSchema(ctx, tx, o.opt.Schema)
	}
	if err != nil {
		return errors.New("PostgreSQL schema must be owned by the application role, with USAGE and CREATE privileges")
	}
	if err := tx.Commit(); err != nil {
		return errors.New("PostgreSQL schema initialization COMMIT failed; retry startup to inspect its state")
	}
	var schema string
	if err := o.conn.QueryRowContext(ctx, `SELECT current_schema()`).Scan(&schema); err != nil || schema != o.opt.Schema {
		return errors.New("PostgreSQL configured schema is not the active schema after initialization")
	}
	return nil
}

// activate demotes exclusive DDL ownership to shared protection, retaining the
// single-owner lock. The owner session takes its shared lock BEFORE releasing
// its exclusive lock, leaving no unlocked interval. Keep this private until the
// Store/configuration/backup lifecycle is fully wired.
func (o *postgresOwner) activate(ctx context.Context) (*Database, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.closed || o.app != nil {
		return nil, errors.New("PostgreSQL lifecycle owner is closed or already active")
	}
	if _, err := o.conn.ExecContext(ctx, `SELECT pg_advisory_lock_shared($1)`, o.key); err != nil {
		return nil, errors.New("could not protect PostgreSQL application sessions")
	}
	var unlocked bool
	if err := o.conn.QueryRowContext(ctx, `SELECT pg_advisory_unlock($1)`, o.key).Scan(&unlocked); err != nil || !unlocked {
		return nil, errors.New("could not release PostgreSQL startup lock")
	}
	db, err := openPostgresPoolAfterConnect(ctx, o.opt, func(ctx context.Context, conn *pgx.Conn) error {
		var locked bool
		if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock_shared($1)`, o.key).Scan(&locked); err != nil {
			return err
		} else if !locked {
			return ErrDatabaseInUse
		}
		// Shared lock first, identity check second. Otherwise an owner death
		// between the check and acquisition could admit a stale connection
		// after an offline operation had already completed.
		var alive bool
		err := conn.QueryRow(ctx, `SELECT EXISTS (
			SELECT 1 FROM pg_catalog.pg_stat_activity a JOIN pg_catalog.pg_locks l ON l.pid = a.pid
			WHERE a.pid = $1 AND a.backend_start = $2 AND l.locktype = 'advisory'
			AND l.granted AND l.mode = 'ExclusiveLock'
			AND l.classid = ((hashtextextended('arrmada/owner/' || current_database() || '/' || $3::text, 0) >> 32) & 4294967295)::oid
			AND l.objid = (hashtextextended('arrmada/owner/' || current_database() || '/' || $3::text, 0) & 4294967295)::oid
			AND l.objsubid = 1 AND l.database = (SELECT oid FROM pg_catalog.pg_database WHERE datname = current_database())
		)`, o.pid, o.started, o.opt.Schema).Scan(&alive)
		if err != nil || !alive {
			return errors.New("PostgreSQL lifecycle owner session was lost; restart Arrmada")
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	o.app = db
	go o.monitor()
	return db, nil
}

func (o *postgresOwner) monitor() {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-o.done:
			return
		case <-ticker.C:
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			err := o.conn.PingContext(ctx)
			cancel()
			if err != nil {
				_ = o.Close()
				return
			}
		}
	}
}

func (o *postgresOwner) Close() error {
	o.mu.Lock()
	if o.closed {
		o.mu.Unlock()
		return nil
	}
	o.closed = true
	close(o.done)
	app := o.app
	o.mu.Unlock()
	// Close the application pool BEFORE releasing the owner session. Active
	// application operations retain their shared session locks until they end;
	// database/sql Close waits for them, so recovery cannot overlap them.
	var err error
	if app != nil {
		err = app.Close()
	}
	// Discard, don't return a session with lifecycle locks to an idle pool.
	_ = o.conn.Raw(func(any) error { return driver.ErrBadConn })
	_ = o.conn.Close()
	// Closing a socket during cancelled COPY is not a server acknowledgement:
	// the backend can still be unwinding its transaction and holding session
	// locks. Wait for this exact backend identity to disappear before reporting
	// cleanup complete. Never terminate another backend to accelerate recovery.
	if o.pid != 0 {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		for {
			var alive bool
			e := o.control.QueryRowContext(cleanup, `SELECT EXISTS (SELECT 1 FROM pg_catalog.pg_stat_activity
				WHERE pid = ? AND backend_start = ?)`, o.pid, o.started).Scan(&alive)
			if e != nil {
				if err == nil {
					err = errors.New("could not confirm PostgreSQL lifecycle session cleanup; retry after the old session exits")
				}
				break
			}
			if !alive {
				break
			}
			select {
			case <-cleanup.Done():
				if err == nil {
					err = errors.New("PostgreSQL lifecycle session is still closing; retry after the old session exits")
				}
			case <-time.After(10 * time.Millisecond):
				continue
			}
			break
		}
	}
	if e := o.control.Close(); err == nil {
		err = e
	}
	return err
}
