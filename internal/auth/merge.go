package auth

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/tristenlammi/arrmada/internal/store"
)

// A duplicate Plex requester is the same person under two accounts: someone (often the
// owner) signed in with Plex before their own account was linked, or the Overseerr import
// made a Plex-linked row for them. Their requests, inbox and phones then split across the
// two. Merging moves the duplicate's things onto the account they should be on, moves the
// Plex link there, and deletes the duplicate — all in one transaction.

// ErrMergeRefused explains why two accounts can't be merged (the message is for the admin).
type ErrMergeRefused struct{ Reason string }

func (e *ErrMergeRefused) Error() string { return e.Reason }

// MergePreview is what a merge would move, as counts. By the audiobook privacy rule it says
// whether the duplicate has listening data, never which books.
type MergePreview struct {
	From              string `json:"from"`
	To                string `json:"to"`
	PlexUsername      string `json:"plex_username,omitempty"`
	Requests          int    `json:"requests"`
	Following         int    `json:"following"`     // requests they follow
	Notifications     int    `json:"notifications"` // inbox entries
	PushDevices       int    `json:"push_devices"`
	QuotaUsage        int    `json:"quota_usage"` // request-limit entries counted against them
	AudiobookProgress bool   `json:"audiobook_progress"`
	AudiobookPassword bool   `json:"audiobook_password"` // moves only if the target has none
	// What the duplicate is signed in on, which is signed out rather than moved: a session
	// handed to another account would open that account (staff, possibly) on whatever
	// device held it.
	SignedInDevices int `json:"signed_in_devices"`
	ListeningApps   int `json:"listening_apps"`
}

type querier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// mergeCheck loads both accounts and refuses anything but a non-staff account holding a
// Plex link merging into a different account without one.
func mergeCheck(ctx context.Context, q querier, targetID, fromID int64) (target, from string, err error) {
	if targetID == fromID {
		return "", "", &ErrMergeRefused{"an account can't be merged into itself"}
	}
	var fromRole string
	var fromPlex, targetPlex sql.NullString
	err = q.QueryRowContext(ctx, `SELECT username, role, plex_id FROM users WHERE id = ?`, fromID).Scan(&from, &fromRole, &fromPlex)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", ErrNotFound
	}
	if err != nil {
		return "", "", err
	}
	err = q.QueryRowContext(ctx, `SELECT username, plex_id FROM users WHERE id = ?`, targetID).Scan(&target, &targetPlex)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", ErrNotFound
	}
	if err != nil {
		return "", "", err
	}
	switch {
	case !fromPlex.Valid || fromPlex.String == "":
		return "", "", &ErrMergeRefused{from + " isn't linked to Plex, so there's no Plex link to move"}
	case Role(fromRole).AtLeast(RoleManager):
		return "", "", &ErrMergeRefused{from + " is a staff account; only a requester's account can be merged away"}
	case targetPlex.Valid && targetPlex.String != "":
		return "", "", &ErrMergeRefused{target + " is already linked to a Plex account; unlink it first"}
	}
	return target, from, nil
}

// PlexMergePreview counts what MergePlexDuplicate(targetID, fromID) would move.
func (s *Service) PlexMergePreview(ctx context.Context, targetID, fromID int64) (MergePreview, error) {
	to, from, err := mergeCheck(ctx, s.db, targetID, fromID)
	if err != nil {
		return MergePreview{}, err
	}
	p := MergePreview{From: from, To: to}
	var places int
	counts := []struct {
		q   string
		dst any
	}{
		{`SELECT plex_username FROM users WHERE id = ?`, &p.PlexUsername},
		{`SELECT COUNT(*) FROM requests WHERE requested_by = ?`, &p.Requests},
		{`SELECT COUNT(*) FROM request_subscribers WHERE user_id = ?`, &p.Following},
		{`SELECT COUNT(*) FROM user_notifications WHERE user_id = ?`, &p.Notifications},
		{`SELECT COUNT(*) FROM push_subscriptions WHERE user_id = ?`, &p.PushDevices},
		{`SELECT COUNT(*) FROM request_usage WHERE user_id = ?`, &p.QuotaUsage},
		{`SELECT COUNT(*) FROM listen_progress WHERE user_id = ?`, &places},
		{`SELECT COUNT(*) > 0 FROM audio_passwords WHERE user_id = ?`, &p.AudiobookPassword},
		{`SELECT COUNT(*) FROM sessions WHERE user_id = ?`, &p.SignedInDevices},
		{`SELECT COUNT(DISTINCT family) FROM audio_tokens WHERE user_id = ? AND revoked = 0`, &p.ListeningApps},
	}
	for _, c := range counts {
		if err := s.db.QueryRowContext(ctx, c.q, fromID).Scan(c.dst); err != nil {
			return MergePreview{}, err
		}
	}
	p.AudiobookProgress = places > 0
	if p.AudiobookPassword {
		var targetHas bool
		if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) > 0 FROM audio_passwords WHERE user_id = ?`, targetID).Scan(&targetHas); err != nil {
			return MergePreview{}, err
		}
		p.AudiobookPassword = !targetHas
	}
	return p, nil
}

// MergePlexDuplicate moves fromID's requests, followed requests, inbox, push devices,
// request-limit usage and audiobook data onto targetID, moves the Plex link, and deletes
// fromID. It is one transaction: any failure leaves both accounts exactly as they were.
//
// Where both accounts have the same thing, the target's wins: an inbox entry for the same
// title, a followed request, an audiobook place or bookmark for the same book, and the
// audiobook password. The duplicate's browser sessions and listening-app sign-ins are
// signed out, not moved (see MergePreview.SignedInDevices).
func (s *Service) MergePlexDuplicate(ctx context.Context, targetID, fromID int64) error {
	return store.WithTx(ctx, s.db, func(tx *sql.Tx) error {
		to, _, err := mergeCheck(ctx, tx, targetID, fromID)
		if err != nil {
			return err
		}
		var plexID, plexName string
		if err := tx.QueryRowContext(ctx, `SELECT plex_id, plex_username FROM users WHERE id = ?`, fromID).Scan(&plexID, &plexName); err != nil {
			return err
		}
		steps := []struct {
			what string
			q    string
			args []any
		}{
			{"requests", `UPDATE requests SET requested_by = ?, requested_by_name = ? WHERE requested_by = ?`, []any{targetID, to, fromID}},
			// Following: one row per request and person; the target keeps its own, and never
			// follows a request it now owns.
			{"followed requests", `UPDATE OR IGNORE request_subscribers SET user_id = ?, user_name = ? WHERE user_id = ?`, []any{targetID, to, fromID}},
			{"followed requests", `DELETE FROM request_subscribers WHERE user_id = ?`, []any{fromID}},
			{"followed requests", `DELETE FROM request_subscribers WHERE user_id = ? AND request_id IN (SELECT id FROM requests WHERE requested_by = ?)`, []any{targetID, targetID}},
			{"inbox", `UPDATE OR IGNORE user_notifications SET user_id = ? WHERE user_id = ?`, []any{targetID, fromID}},
			{"inbox", `DELETE FROM user_notifications WHERE user_id = ?`, []any{fromID}},
			{"push devices", `UPDATE push_subscriptions SET user_id = ? WHERE user_id = ?`, []any{targetID, fromID}},
			{"request limits", `UPDATE request_usage SET user_id = ? WHERE user_id = ?`, []any{targetID, fromID}},
			// Audiobooks: places and bookmarks move unless the target has its own for that
			// book; history, sessions and the listening log all move (they're keyed by
			// session or row id, so nothing collides).
			{"audiobook places", `UPDATE OR IGNORE listen_progress SET user_id = ? WHERE user_id = ?`, []any{targetID, fromID}},
			{"audiobook bookmarks", `UPDATE OR IGNORE listen_bookmarks SET user_id = ? WHERE user_id = ?`, []any{targetID, fromID}},
			{"audiobook history", `UPDATE listen_history SET user_id = ? WHERE user_id = ?`, []any{targetID, fromID}},
			{"audiobook sessions", `UPDATE listen_sessions SET user_id = ? WHERE user_id = ?`, []any{targetID, fromID}},
			{"listening log", `UPDATE listen_log SET user_id = ? WHERE user_id = ?`, []any{targetID, fromID}},
			{"audiobook password", `INSERT OR IGNORE INTO audio_passwords (user_id, hash, updated_at) SELECT ?, hash, updated_at FROM audio_passwords WHERE user_id = ?`, []any{targetID, fromID}},
			{"personal alerts", `UPDATE users SET apprise_url = (SELECT apprise_url FROM users WHERE id = ?) WHERE id = ? AND apprise_url = ''`, []any{fromID, targetID}},
			// Their per-event notification choices, when the target never made any.
			{"notification choices", `UPDATE users SET notify_prefs = (SELECT notify_prefs FROM users WHERE id = ?) WHERE id = ? AND notify_prefs = ''`, []any{fromID, targetID}},
			// The Plex link: off the duplicate first (plex_id is unique), then onto the target.
			{"Plex link", `UPDATE users SET plex_id = NULL, plex_username = '' WHERE id = ?`, []any{fromID}},
			{"Plex link", `UPDATE users SET plex_id = ?, plex_username = ? WHERE id = ?`, []any{plexID, plexName, targetID}},
			// Sessions, API keys, listening-app sign-ins and anything not moved go with the
			// row (ON DELETE CASCADE).
			{"the duplicate account", `DELETE FROM users WHERE id = ?`, []any{fromID}},
		}
		for _, st := range steps {
			if _, err := tx.ExecContext(ctx, st.q, st.args...); err != nil {
				return fmt.Errorf("merge: moving %s: %w", st.what, err)
			}
		}
		return nil
	})
}

// UserByPlexID is the account linked to a Plex id, or nil when there is none.
func (s *Service) UserByPlexID(ctx context.Context, plexID string) (*User, error) {
	u, err := s.userWhere(ctx, "plex_id = ?", plexID)
	if errors.Is(err, ErrInvalidCredentials) {
		return nil, nil
	}
	return u, err
}
