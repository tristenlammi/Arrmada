// Package settings is a tiny persisted key/value store for app-level preferences
// (things the user sets once and expects to stick across sessions and devices).
//
// Every setting is loaded into memory once, at boot, and served from there. A write goes
// to the database first and reaches memory only once it is saved. So a database hiccup
// can no longer make a setting silently read as its default for one call (the music root
// reverting mid-import, a module toggle flipping), and a failed save leaves the old value
// in effect and says so. This service is the only code that touches the settings table.
package settings

import (
	"context"
	"database/sql"
	"fmt"
	"maps"
	"strconv"
	"sync"
	"time"
)

// Module toggles. They live here rather than in the HTTP layer because the scheduler and
// the coordinator read them too: switching a module off has to stop its background work,
// not just hide its nav entry.
const (
	KeyModuleMusic = "module_music_enabled"
	KeyModuleBooks = "module_books_enabled"
)

// ModuleMusicDefault is off: Music is a preview, and a fresh install shouldn't start
// searching indexers for albums before the owner has chosen to try it.
const ModuleMusicDefault = false

// Service reads settings from memory and writes them through to the shared settings
// table.
type Service struct {
	db *sql.DB

	// writeMu serialises writes, so the database and the map always change in the same
	// order: two saves of one key can't land in the table one way round and in memory
	// the other.
	writeMu sync.Mutex

	mu     sync.RWMutex
	values map[string]string
}

// loadTimeout bounds the boot-time read of the settings table.
const loadTimeout = 30 * time.Second

// Open loads every setting into memory. It fails when the settings table can't be read:
// running on without them would mean running on defaults the owner never chose.
func Open(ctx context.Context, db *sql.DB) (*Service, error) {
	s := &Service{db: db}
	if err := s.Reload(ctx); err != nil {
		return nil, err
	}
	return s, nil
}

// NewService is Open for callers that can't handle an error, which in practice means
// tests over a freshly migrated database. It panics if the settings can't be loaded; the
// app itself boots through Open and stops with a clear message instead.
func NewService(db *sql.DB) *Service {
	ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
	defer cancel()
	s, err := Open(ctx, db)
	if err != nil {
		panic(err)
	}
	return s
}

// Reload re-reads every setting from the database, replacing what is in memory. Nothing
// changes when the read fails.
func (s *Service) Reload(ctx context.Context) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	rows, err := s.db.QueryContext(ctx, `SELECT key, value FROM settings`)
	if err != nil {
		return fmt.Errorf("read settings: %w", err)
	}
	defer rows.Close()
	values := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return fmt.Errorf("read settings: %w", err)
		}
		values[k] = v
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("read settings: %w", err)
	}
	s.mu.Lock()
	s.values = values
	s.mu.Unlock()
	return nil
}

// Lookup returns a setting's value and whether it has been saved at all.
func (s *Service) Lookup(key string) (string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	v, ok := s.values[key]
	return v, ok
}

// All returns a copy of every saved setting.
func (s *Service) All() map[string]string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return maps.Clone(s.values)
}

// Get returns a setting's value, or def if it has never been saved. It reads memory, so
// it can't fail; ctx is kept so callers didn't have to change.
func (s *Service) Get(_ context.Context, key, def string) string {
	if v, ok := s.Lookup(key); ok {
		return v
	}
	return def
}

// Set saves a setting. The database is written first and memory only once that has
// succeeded, so a failed save returns its error and leaves the old value in effect.
func (s *Service) Set(ctx context.Context, key, value string) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO settings (key, value) VALUES (?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = CURRENT_TIMESTAMP`,
		key, value); err != nil {
		return fmt.Errorf("save setting %s: %w", key, err)
	}
	s.mu.Lock()
	if s.values == nil {
		s.values = map[string]string{}
	}
	s.values[key] = value
	s.mu.Unlock()
	return nil
}

// GetBool returns a boolean setting, or def if unset/unparseable.
func (s *Service) GetBool(ctx context.Context, key string, def bool) bool {
	v := s.Get(ctx, key, "")
	if v == "" {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return def
	}
	return b
}

// SetBool persists a boolean setting.
func (s *Service) SetBool(ctx context.Context, key string, value bool) error {
	return s.Set(ctx, key, strconv.FormatBool(value))
}

// EnsureModuleDefault pins a module ON for an install that is already using it, before a
// changed default could switch it off underneath them.
//
// It only ever acts on a key that has never been saved: an explicit value, on or off, is
// the owner's choice and is never touched. keepOn says whether the install is using the
// module (it has data in it); when it errors nothing is written, so a transient failure
// can't decide for the owner either way — the next start tries again.
func (s *Service) EnsureModuleDefault(ctx context.Context, key string, keepOn func(context.Context) (bool, error)) (changed bool, err error) {
	if _, saved := s.Lookup(key); saved {
		return false, nil // saved already, whatever it says
	}
	on, err := keepOn(ctx)
	if err != nil || !on {
		return false, err
	}
	if err := s.SetBool(ctx, key, true); err != nil {
		return false, err
	}
	return true, nil
}
