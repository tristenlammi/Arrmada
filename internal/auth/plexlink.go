package auth

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// ErrPlexAlreadyLinked: the Plex account is linked to another Arrmada account. UserID and
// Username say which, for the caller to show (only an admin is told the id).
type ErrPlexAlreadyLinked struct {
	UserID   int64
	Username string
	Role     Role
}

func (e *ErrPlexAlreadyLinked) Error() string {
	return fmt.Sprintf("this Plex account is already linked to %s", e.Username)
}

// ErrPlexOnlyLogin: the account has no password anyone knows, so Plex is its only way in
// and unlinking it would lock its owner out.
var ErrPlexOnlyLogin = errors.New("this account signs in only with Plex — set a password for it before unlinking Plex")

// PlexLink is an account's Plex link, as its owner sees it.
type PlexLink struct {
	Linked       bool   `json:"linked"`
	PlexUsername string `json:"plex_username,omitempty"`
	// CanUnlink: the account has a password, so unlinking Plex leaves a way in.
	CanUnlink bool `json:"can_unlink"`
}

// PlexLinkFor reports userID's Plex link.
func (s *Service) PlexLinkFor(ctx context.Context, userID int64) (PlexLink, error) {
	var plexID sql.NullString
	var l PlexLink
	var pw int
	err := s.db.QueryRowContext(ctx, `SELECT plex_id, plex_username, password_login FROM users WHERE id = ?`, userID).Scan(&plexID, &l.PlexUsername, &pw)
	if errors.Is(err, sql.ErrNoRows) {
		return l, ErrNotFound
	}
	if err != nil {
		return l, err
	}
	l.Linked = strings.TrimSpace(plexID.String) != ""
	l.CanUnlink = l.Linked && pw == 1
	if !l.Linked {
		l.PlexUsername = ""
	}
	return l, nil
}

// LinkPlex ties a Plex account to an existing account, replacing any Plex account it was
// linked to before. A Plex account linked to someone else is refused with
// *ErrPlexAlreadyLinked (the unique index settles a race the same way): one Plex account
// is one Arrmada account. Linking grants nothing by itself — whether Plex sign-in may
// enter a staff account is the sign-in's decision.
func (s *Service) LinkPlex(ctx context.Context, userID int64, plexID, plexUsername string) error {
	plexID = strings.TrimSpace(plexID)
	if plexID == "" {
		return errors.New("missing plex id")
	}
	if holder, err := s.plexHolder(ctx, plexID); err != nil {
		return err
	} else if holder != nil && holder.UserID != userID {
		return holder
	}
	res, err := s.db.ExecContext(ctx, `UPDATE users SET plex_id = ?, plex_username = ? WHERE id = ?`, plexID, strings.TrimSpace(plexUsername), userID)
	if isUniqueViolation(err) {
		if holder, herr := s.plexHolder(ctx, plexID); herr == nil && holder != nil {
			return holder
		}
		return &ErrPlexAlreadyLinked{Username: "another account"}
	}
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// plexHolder is the account a Plex id is linked to, or nil.
func (s *Service) plexHolder(ctx context.Context, plexID string) (*ErrPlexAlreadyLinked, error) {
	var h ErrPlexAlreadyLinked
	err := s.db.QueryRowContext(ctx, `SELECT id, username, role FROM users WHERE plex_id = ?`, plexID).Scan(&h.UserID, &h.Username, &h.Role)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &h, nil
}

// UnlinkPlex removes an account's Plex link. Its next Plex sign-in is then a stranger's:
// it gets (or makes) the account linked to that Plex id, never this one. checkLogin
// refuses when Plex is the account's only way in (ErrPlexOnlyLogin); an admin removing the
// link from Settings → Users passes false.
func (s *Service) UnlinkPlex(ctx context.Context, userID int64, checkLogin bool) error {
	q := `UPDATE users SET plex_id = NULL, plex_username = '' WHERE id = ?`
	if checkLogin {
		q += ` AND (password_login = 1 OR plex_id IS NULL)`
	}
	res, err := s.db.ExecContext(ctx, q, userID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		if _, err := s.UserByID(ctx, userID); err != nil {
			return ErrNotFound
		}
		return ErrPlexOnlyLogin
	}
	return nil
}
