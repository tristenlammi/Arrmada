package auth

import (
	"context"
	"database/sql"
	"errors"

	"golang.org/x/crypto/bcrypt"

	"github.com/tristenlammi/arrmada/internal/store"
)

// Your own account: changing your password and signing your other devices out. An admin
// resetting someone's password is SetPassword (every session ends); here the session
// doing the change survives, so the person isn't thrown out of the page they're on.

// HasPassword reports whether anyone knows a password for the account (users.password_login).
// Accounts made by Plex sign-in or the Overseerr import have a random one nobody knows.
func (s *Service) HasPassword(ctx context.Context, id int64) (bool, error) {
	var pw int
	err := s.db.QueryRowContext(ctx, `SELECT password_login FROM users WHERE id = ?`, id).Scan(&pw)
	if errors.Is(err, sql.ErrNoRows) {
		return false, ErrNotFound
	}
	return pw == 1, err
}

// CheckPassword reports whether pw is the account's password. It always spends a bcrypt
// compare, a missing account included, so the answer's timing says nothing.
func (s *Service) CheckPassword(ctx context.Context, id int64, pw string) bool {
	var hash string
	if err := s.db.QueryRowContext(ctx, `SELECT password_hash FROM users WHERE id = ?`, id).Scan(&hash); err != nil {
		_ = bcrypt.CompareHashAndPassword(dummyHash, []byte(pw))
		return false
	}
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(pw)) == nil
}

// ChangePassword sets the account's password, marks it as one somebody knows (so a
// Plex-only account can then unlink Plex without locking itself out), and signs out every
// session except keepRaw, the one making the change — in one transaction. It says how many
// other sessions ended. Checking the current password is the caller's job.
func (s *Service) ChangePassword(ctx context.Context, id int64, newPw, keepRaw string) (int64, error) {
	if len(newPw) < 8 {
		return 0, ErrWeakPassword
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(newPw), bcrypt.DefaultCost)
	if err != nil {
		return 0, err
	}
	var ended int64
	err = store.WithTx(ctx, s.db, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE users SET password_hash = ?, password_login = 1 WHERE id = ?`, string(hash), id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		res, err = tx.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ? AND token_hash <> ?`, id, keepHash(keepRaw))
		if err != nil {
			return err
		}
		ended, _ = res.RowsAffected()
		return nil
	})
	return ended, err
}

// RevokeOtherSessions signs the account out everywhere but keepRaw (the session asking)
// and says how many sessions ended. With no session to keep (an API-key call), it signs
// out every browser.
func (s *Service) RevokeOtherSessions(ctx context.Context, id int64, keepRaw string) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ? AND token_hash <> ?`, id, keepHash(keepRaw))
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// keepHash is the stored hash of the session to keep; no session matches "".
func keepHash(raw string) string {
	if raw == "" {
		return ""
	}
	return hashToken(raw)
}
