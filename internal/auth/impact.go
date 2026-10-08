package auth

import (
	"context"
	"database/sql"
	"errors"
)

// UserImpact is what deleting an account would take with it, as counts only. It is shown
// to an admin before the delete, so by the audiobook privacy rule it says how much someone
// listened, never what: no item keys, no titles, nothing that names a book.
type UserImpact struct {
	Places            int     `json:"places"`             // audiobooks with a saved position
	ListeningHours    float64 `json:"listening_hours"`    // total time listened
	Bookmarks         int     `json:"bookmarks"`          // saved bookmarks
	Devices           int     `json:"devices"`            // signed-in listening apps
	Requests          int     `json:"requests"`           // their requests (kept after delete)
	Sessions          int     `json:"sessions"`           // browser sign-ins
	PushSubscriptions int     `json:"push_subscriptions"` // phones/browsers getting alerts
	PlexLinked        bool    `json:"plex_linked"`        // can sign straight back in with Plex
}

// HasListeningData reports whether the delete would erase audiobook places or history —
// the part a family member can't get back, so it needs the username typed to confirm.
func (i UserImpact) HasListeningData() bool { return i.Places > 0 || i.ListeningHours > 0 }

// DeletionImpact counts what deleting user id would remove. Every query is a plain count
// keyed by user_id; none selects an item key or any book column.
func (s *Service) DeletionImpact(ctx context.Context, id int64) (UserImpact, error) {
	var imp UserImpact
	err := s.db.QueryRowContext(ctx, `SELECT plex_id IS NOT NULL FROM users WHERE id = ?`, id).Scan(&imp.PlexLinked)
	if errors.Is(err, sql.ErrNoRows) {
		return imp, ErrNotFound
	}
	if err != nil {
		return imp, err
	}
	counts := []struct {
		q   string
		dst any
	}{
		{`SELECT COUNT(*) FROM listen_progress WHERE user_id = ?`, &imp.Places},
		{`SELECT COALESCE(SUM(seconds), 0) / 3600.0 FROM listen_log WHERE user_id = ?`, &imp.ListeningHours},
		{`SELECT COUNT(*) FROM listen_bookmarks WHERE user_id = ?`, &imp.Bookmarks},
		{`SELECT COUNT(DISTINCT family) FROM audio_tokens WHERE user_id = ? AND revoked = 0`, &imp.Devices},
		{`SELECT COUNT(*) FROM requests WHERE requested_by = ?`, &imp.Requests},
		{`SELECT COUNT(*) FROM sessions WHERE user_id = ?`, &imp.Sessions},
		{`SELECT COUNT(*) FROM push_subscriptions WHERE user_id = ?`, &imp.PushSubscriptions},
	}
	for _, c := range counts {
		if err := s.db.QueryRowContext(ctx, c.q, id).Scan(c.dst); err != nil {
			return imp, err
		}
	}
	return imp, nil
}
