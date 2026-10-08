// Package settings is a tiny persisted key/value store for app-level preferences
// (things the user sets once and expects to stick across sessions and devices).
package settings

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
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

// Service reads and writes settings in the shared settings table.
type Service struct{ db *sql.DB }

// NewService wires the settings service.
func NewService(db *sql.DB) *Service { return &Service{db: db} }

// Get returns a setting's value, or def if unset.
func (s *Service) Get(ctx context.Context, key, def string) string {
	var v string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) || err != nil {
		return def
	}
	return v
}

// Set upserts a setting.
func (s *Service) Set(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO settings (key, value) VALUES (?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = CURRENT_TIMESTAMP`,
		key, value)
	return err
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
	var v string
	err = s.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, key).Scan(&v)
	switch {
	case err == nil:
		return false, nil // saved already, whatever it says
	case !errors.Is(err, sql.ErrNoRows):
		return false, err
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
