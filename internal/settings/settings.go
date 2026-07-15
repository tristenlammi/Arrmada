// Package settings is a tiny persisted key/value store for app-level preferences
// (things the user sets once and expects to stick across sessions and devices).
package settings

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
)

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
