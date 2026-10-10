package requests

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
)

// Per-person notification choices: which notices reach their phones (Web Push) and their
// personal Apprise link. The in-app inbox is never gated — it is the record, its unique
// (user, ref) index is what stops a retry telling anyone twice, and the ready sweep relies
// on it — so a choice only ever silences the push, never the bell.

// The event keys a person can turn off.
const (
	PrefApproved   = "approved"
	PrefDeclined   = "declined"
	PrefReady      = "ready"
	PrefNewRequest = "new_request" // staff: someone asked for something
)

// NotifyPrefKeys are the keys the API accepts, in the order the Me page lists them.
var NotifyPrefKeys = []string{PrefApproved, PrefDeclined, PrefReady, PrefNewRequest}

// NotifyPrefs maps an event key to on/off. A key it doesn't name is on.
type NotifyPrefs map[string]bool

// parseNotifyPrefs reads the stored JSON. Empty or unreadable means everything on: a
// broken value must never make someone silently miss their notices.
func parseNotifyPrefs(s string) NotifyPrefs {
	p := NotifyPrefs{}
	if s == "" || json.Unmarshal([]byte(s), &p) != nil || p == nil { // "null" unmarshals to nil
		return NotifyPrefs{}
	}
	return p
}

// Wants reports whether key should be delivered: on unless explicitly turned off.
func (p NotifyPrefs) Wants(key string) bool {
	v, ok := p[key]
	return !ok || v
}

// Full is every known key with its effective value, for the Me page.
func (p NotifyPrefs) Full() NotifyPrefs {
	out := NotifyPrefs{}
	for _, k := range NotifyPrefKeys {
		out[k] = p.Wants(k)
	}
	return out
}

// prefKey is the choice an inbox notice kind answers to. A kind it doesn't know (one added
// later) answers to none, so it always delivers.
func prefKey(kind string) string {
	switch kind {
	case "request-ready", "request-season-ready":
		return PrefReady
	case "request-approved":
		return PrefApproved
	case "request-declined":
		return PrefDeclined
	}
	return ""
}

// wantsPush reports whether userID wants a notice of this kind pushed. Reading the choice
// can fail; then it delivers, the same as for an unknown kind.
func (s *Service) wantsPush(ctx context.Context, userID int64, key string) bool {
	if key == "" {
		return true
	}
	p, err := s.repo.getNotifyPrefs(ctx, userID)
	if err != nil {
		s.log.Warn("notify prefs: couldn't read them, delivering", "user", userID, "err", err)
		return true
	}
	return p.Wants(key)
}

func (r *Repo) getNotifyPrefs(ctx context.Context, userID int64) (NotifyPrefs, error) {
	var raw string
	err := r.db.QueryRowContext(ctx, `SELECT notify_prefs FROM users WHERE id = ?`, userID).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) { // no users row: the defaults
		return NotifyPrefs{}, nil
	}
	if err != nil {
		return nil, err
	}
	return parseNotifyPrefs(raw), nil
}

func (r *Repo) setNotifyPrefs(ctx context.Context, userID int64, p NotifyPrefs) error {
	b, err := json.Marshal(p)
	if err != nil {
		return err
	}
	_, err = r.db.ExecContext(ctx, `UPDATE users SET notify_prefs = ? WHERE id = ?`, string(b), userID)
	return err
}

// NotifyPrefs is userID's choices, every known key filled in.
func (s *Service) NotifyPrefs(ctx context.Context, userID int64) (NotifyPrefs, error) {
	p, err := s.repo.getNotifyPrefs(ctx, userID)
	if err != nil {
		return nil, err
	}
	return p.Full(), nil
}

// SetNotifyPrefs changes the keys named in change (known keys only; the rest are ignored)
// and leaves every other choice as it was. It returns the result.
func (s *Service) SetNotifyPrefs(ctx context.Context, userID int64, change map[string]bool) (NotifyPrefs, error) {
	p, err := s.repo.getNotifyPrefs(ctx, userID)
	if err != nil {
		return nil, err
	}
	for _, k := range NotifyPrefKeys {
		if v, ok := change[k]; ok {
			p[k] = v
		}
	}
	if err := s.repo.setNotifyPrefs(ctx, userID, p); err != nil {
		return nil, err
	}
	return p.Full(), nil
}
