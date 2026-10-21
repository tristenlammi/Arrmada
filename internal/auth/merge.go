package auth

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/tristenlammi/arrmada/internal/store"
)

// A duplicate requester is the same person under two accounts: someone (often the owner)
// signed in with Plex or Jellyfin before their own account was linked, or the Overseerr
// import made a Plex-linked row for them. Their requests, inbox and phones then split across
// the two. Merging moves the duplicate's things onto the account they should be on, moves
// its Plex and Jellyfin links there, and deletes the duplicate — all in one transaction.

// ErrMergeRefused explains why two accounts can't be merged (the message is for the admin).
type ErrMergeRefused struct{ Reason string }

func (e *ErrMergeRefused) Error() string { return e.Reason }

// MergePreview is what a merge would move, as counts. By the audiobook privacy rule it says
// whether the duplicate has listening data, never which books.
type MergePreview struct {
	From string `json:"from"`
	To   string `json:"to"`
	// The links that move, and the names they were made with.
	PlexLink          bool   `json:"plex_link"`
	PlexUsername      string `json:"plex_username,omitempty"`
	JellyfinLink      bool   `json:"jellyfin_link"`
	JellyfinUsername  string `json:"jellyfin_username,omitempty"`
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
	// Personal API keys are revoked, not moved, for the same reason: a key handed over
	// would act as the other account.
	APIKeys int `json:"api_keys"`
	// TwoFactor: the duplicate had two-factor sign-in on. It is turned off with the
	// duplicate, not moved: the target's own sign-in (and its own two-factor, if any) is
	// what protects the merged account, and moving a second authenticator onto it would
	// lock its owner out of a sign-in that worked yesterday.
	TwoFactor bool `json:"two_factor"`
}

type querier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// mergeAccount is one side of a merge: its name and links.
type mergeAccount struct {
	name, role             string
	plexID, plexName       string
	jellyfinID, jellyfinNm string
}

func loadMergeAccount(ctx context.Context, q querier, id int64) (mergeAccount, error) {
	var m mergeAccount
	var plexID, jfID sql.NullString
	err := q.QueryRowContext(ctx, `SELECT username, role, plex_id, plex_username, jellyfin_id, jellyfin_username FROM users WHERE id = ?`, id).
		Scan(&m.name, &m.role, &plexID, &m.plexName, &jfID, &m.jellyfinNm)
	if errors.Is(err, sql.ErrNoRows) {
		return m, ErrNotFound
	}
	m.plexID, m.jellyfinID = plexID.String, jfID.String
	return m, err
}

// mergeCheck loads both accounts and refuses anything but a non-staff account holding a
// Plex or Jellyfin link merging into a different account. Each link moves only when the
// target has none of that kind; a duplicate whose link would collide with the target's own
// is refused rather than losing that link (its next sign-in would just make a new
// duplicate).
func mergeCheck(ctx context.Context, q querier, targetID, fromID int64) (target, from mergeAccount, err error) {
	if targetID == fromID {
		return target, from, &ErrMergeRefused{"an account can't be merged into itself"}
	}
	if from, err = loadMergeAccount(ctx, q, fromID); err != nil {
		return target, from, err
	}
	if target, err = loadMergeAccount(ctx, q, targetID); err != nil {
		return target, from, err
	}
	switch {
	case from.plexID == "" && from.jellyfinID == "":
		return target, from, &ErrMergeRefused{from.name + " isn't linked to Plex or Jellyfin, so there's no link to move"}
	case Role(from.role).AtLeast(RoleManager):
		return target, from, &ErrMergeRefused{from.name + " is a staff account; only a requester's account can be merged away"}
	case from.plexID != "" && target.plexID != "":
		return target, from, &ErrMergeRefused{target.name + " is already linked to a Plex account; unlink it first"}
	case from.jellyfinID != "" && target.jellyfinID != "":
		return target, from, &ErrMergeRefused{target.name + " is already linked to a Jellyfin user; unlink it first"}
	}
	return target, from, nil
}

// PlexMergePreview is MergePreview under its old name.
func (s *Service) PlexMergePreview(ctx context.Context, targetID, fromID int64) (MergePreview, error) {
	return s.MergePreview(ctx, targetID, fromID)
}

// MergePreview counts what MergeDuplicate(targetID, fromID) would move.
func (s *Service) MergePreview(ctx context.Context, targetID, fromID int64) (MergePreview, error) {
	to, from, err := mergeCheck(ctx, s.db, targetID, fromID)
	if err != nil {
		return MergePreview{}, err
	}
	p := MergePreview{
		From: from.name, To: to.name,
		PlexLink: from.plexID != "", PlexUsername: from.plexName,
		JellyfinLink: from.jellyfinID != "", JellyfinUsername: from.jellyfinNm,
	}
	var places int
	counts := []struct {
		q   string
		dst any
	}{
		{`SELECT COUNT(*) FROM requests WHERE requested_by = ?`, &p.Requests},
		{`SELECT COUNT(*) FROM request_subscribers WHERE user_id = ?`, &p.Following},
		{`SELECT COUNT(*) FROM user_notifications WHERE user_id = ?`, &p.Notifications},
		{`SELECT COUNT(*) FROM push_subscriptions WHERE user_id = ?`, &p.PushDevices},
		{`SELECT COUNT(*) FROM request_usage WHERE user_id = ?`, &p.QuotaUsage},
		{`SELECT COUNT(*) FROM listen_progress WHERE user_id = ?`, &places},
		{`SELECT COUNT(*) > 0 FROM audio_passwords WHERE user_id = ?`, &p.AudiobookPassword},
		{`SELECT COUNT(*) FROM sessions WHERE user_id = ?`, &p.SignedInDevices},
		{`SELECT COUNT(DISTINCT family) FROM audio_tokens WHERE user_id = ? AND revoked = 0`, &p.ListeningApps},
		{`SELECT COUNT(*) FROM api_keys WHERE user_id = ?`, &p.APIKeys},
		{`SELECT COUNT(*) > 0 FROM user_totp WHERE user_id = ? AND enabled = 1`, &p.TwoFactor},
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

// MergePlexDuplicate is MergeDuplicate under its old name.
func (s *Service) MergePlexDuplicate(ctx context.Context, targetID, fromID int64) error {
	return s.MergeDuplicate(ctx, targetID, fromID)
}

// MergeDuplicate moves fromID's requests, followed requests, inbox, push devices,
// request-limit usage and audiobook data onto targetID, moves its Plex and Jellyfin links,
// and deletes fromID. It is one transaction: any failure leaves both accounts exactly as
// they were. A new per-user table (or per-user column on users) must be added here.
//
// Where both accounts have the same thing, the target's wins: an inbox entry for the same
// title, a followed request, an audiobook place or bookmark for the same book, and the
// audiobook password. The duplicate's browser sessions and listening-app sign-ins are
// signed out, not moved (see MergePreview.SignedInDevices), and its two-factor sign-in is
// turned off, not moved (MergePreview.TwoFactor).
func (s *Service) MergeDuplicate(ctx context.Context, targetID, fromID int64) error {
	return store.WithTx(ctx, s.db, func(tx *store.Transaction) error {
		target, from, err := mergeCheck(ctx, tx, targetID, fromID)
		if err != nil {
			return err
		}
		to := target.name
		type step struct {
			what string
			q    string
			args []any
		}
		// The links: off the duplicate first (each id is unique), then onto the target.
		// mergeCheck made sure the target has none of a kind the duplicate holds.
		var links []step
		if from.plexID != "" {
			links = append(links,
				step{"Plex link", `UPDATE users SET plex_id = NULL, plex_username = '' WHERE id = ?`, []any{fromID}},
				step{"Plex link", `UPDATE users SET plex_id = ?, plex_username = ? WHERE id = ?`, []any{from.plexID, from.plexName, targetID}})
		}
		if from.jellyfinID != "" {
			links = append(links,
				step{"Jellyfin link", `UPDATE users SET jellyfin_id = NULL, jellyfin_username = '' WHERE id = ?`, []any{fromID}},
				step{"Jellyfin link", `UPDATE users SET jellyfin_id = ?, jellyfin_username = ? WHERE id = ?`, []any{from.jellyfinID, from.jellyfinNm, targetID}})
		}
		steps := []step{
			{"requests", `UPDATE requests SET requested_by = ?, requested_by_name = ? WHERE requested_by = ?`, []any{targetID, to, fromID}},
			// Following: one row per request and person; the target keeps its own, and never
			// follows a request it now owns.
			{"followed requests", `UPDATE request_subscribers SET user_id = ?, user_name = ? WHERE user_id = ?
				AND NOT EXISTS (SELECT 1 FROM request_subscribers kept WHERE kept.request_id = request_subscribers.request_id AND kept.user_id = ?)`, []any{targetID, to, fromID, targetID}},
			{"followed requests", `DELETE FROM request_subscribers WHERE user_id = ?`, []any{fromID}},
			{"followed requests", `DELETE FROM request_subscribers WHERE user_id = ? AND request_id IN (SELECT id FROM requests WHERE requested_by = ?)`, []any{targetID, targetID}},
			{"inbox", `UPDATE user_notifications SET user_id = ? WHERE user_id = ?
				AND NOT EXISTS (SELECT 1 FROM user_notifications kept WHERE kept.ref = user_notifications.ref AND kept.user_id = ?)`, []any{targetID, fromID, targetID}},
			{"inbox", `DELETE FROM user_notifications WHERE user_id = ?`, []any{fromID}},
			{"push devices", `UPDATE push_subscriptions SET user_id = ? WHERE user_id = ?`, []any{targetID, fromID}},
			{"request limits", `UPDATE request_usage SET user_id = ? WHERE user_id = ?`, []any{targetID, fromID}},
			// Reported problems: one row per issue and person; where both reported the same
			// one, the target's own report stays.
			{"reported problems", `UPDATE issues SET reported_by = ?, reported_by_name = ? WHERE reported_by = ?`, []any{targetID, to, fromID}},
			{"reported problems", `UPDATE issue_reporters SET user_id = ?, user_name = ? WHERE user_id = ?
				AND NOT EXISTS (SELECT 1 FROM issue_reporters kept WHERE kept.issue_id = issue_reporters.issue_id AND kept.user_id = ?)`, []any{targetID, to, fromID, targetID}},
			{"reported problems", `DELETE FROM issue_reporters WHERE user_id = ?`, []any{fromID}},
			// Audiobooks: places and bookmarks move unless the target has its own for that
			// book; history, sessions and the listening log all move (they're keyed by
			// session or row id, so nothing collides).
			{"audiobook places", `UPDATE listen_progress SET user_id = ? WHERE user_id = ?
				AND NOT EXISTS (SELECT 1 FROM listen_progress kept WHERE kept.item_key = listen_progress.item_key AND kept.user_id = ?)`, []any{targetID, fromID, targetID}},
			{"audiobook bookmarks", `UPDATE listen_bookmarks SET user_id = ? WHERE user_id = ?
				AND NOT EXISTS (SELECT 1 FROM listen_bookmarks kept WHERE kept.item_key = listen_bookmarks.item_key AND kept.time = listen_bookmarks.time AND kept.user_id = ?)`, []any{targetID, fromID, targetID}},
			{"audiobook history", `UPDATE listen_history SET user_id = ? WHERE user_id = ?`, []any{targetID, fromID}},
			{"audiobook sessions", `UPDATE listen_sessions SET user_id = ? WHERE user_id = ?`, []any{targetID, fromID}},
			{"listening log", `UPDATE listen_log SET user_id = ? WHERE user_id = ?`, []any{targetID, fromID}},
			{"audiobook password", `INSERT INTO audio_passwords (user_id, hash, updated_at) SELECT ?, hash, updated_at FROM audio_passwords WHERE user_id = ? ON CONFLICT(user_id) DO NOTHING`, []any{targetID, fromID}},
			{"personal alerts", `UPDATE users SET apprise_url = (SELECT apprise_url FROM users WHERE id = ?) WHERE id = ? AND apprise_url = ''`, []any{fromID, targetID}},
			// Their per-event notification choices, when the target never made any.
			{"notification choices", `UPDATE users SET notify_prefs = (SELECT notify_prefs FROM users WHERE id = ?) WHERE id = ? AND notify_prefs = ''`, []any{fromID, targetID}},
			// Their Send-to-Kindle address, when the target has none.
			{"Kindle address", `UPDATE users SET kindle_email = (SELECT kindle_email FROM users WHERE id = ?) WHERE id = ? AND kindle_email = ''`, []any{fromID, targetID}},
		}
		steps = append(steps, links...)
		steps = append(steps,
			// The duplicate's calendar feed link stops working, like its sessions: it was
			// made for that account's view, and the person can make a new one in a click.
			step{"calendar feed", `DELETE FROM calendar_feeds WHERE user_id = ?`, []any{fromID}},
			// Personal API keys are revoked, never moved: a script holding one would
			// otherwise start acting as the target account.
			step{"personal API keys", `DELETE FROM api_keys WHERE user_id = ?`, []any{fromID}},
			// Two-factor sign-in, its recovery codes and trusted devices are the duplicate's
			// own and go with it (see MergePreview.TwoFactor); the target's stay as they are.
			step{"two-factor sign-in", `DELETE FROM user_totp WHERE user_id = ?`, []any{fromID}},
			step{"two-factor sign-in", `DELETE FROM user_recovery_codes WHERE user_id = ?`, []any{fromID}},
			step{"two-factor sign-in", `DELETE FROM user_trusted_devices WHERE user_id = ?`, []any{fromID}},
			// Sessions, listening-app sign-ins and anything not moved go with the row
			// (ON DELETE CASCADE).
			step{"the duplicate account", `DELETE FROM users WHERE id = ?`, []any{fromID}},
		)
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
