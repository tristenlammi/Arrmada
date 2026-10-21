package auth

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"math/big"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/tristenlammi/arrmada/internal/store"
)

// Two-factor sign-in: after the right password, a six-digit code from an authenticator
// app (or a one-time recovery code). It's optional and per account, and it guards only
// Arrmada's own password sign-in: Plex and Jellyfin sign-in are checked by those servers
// (their sign-in is the factor), API keys are separate credentials, and audiobook apps use
// their own audiobook password because Audiobookshelf apps can't ask for a code.
//
// The shared secret is stored as the app stores its other credentials (there is no
// app-wide key to encrypt it with). It is shown once, while setting up, and never again:
// nothing returns it afterwards, nothing logs it, and the support bundle blanks it.

var (
	ErrTwoFactorOn    = errors.New("two-factor sign-in is already on")
	ErrTwoFactorOff   = errors.New("two-factor sign-in isn't on")
	ErrTwoFactorSetup = errors.New("start setting up two-factor sign-in again")
	ErrBadCode        = errors.New("that code isn't right")
)

// RecoveryCodeCount is how many one-time recovery codes a setup (or a regenerate) makes.
const RecoveryCodeCount = 10

// recoveryCodeLen is a recovery code's length without its dash: ten characters from a
// 31-letter alphabet, about 49 bits each.
const recoveryCodeLen = 10

// recoveryAlphabet leaves out characters that are easy to misread (0/o, 1/l/i).
const recoveryAlphabet = "abcdefghjkmnpqrstuvwxyz23456789"

// recoveryCost is the bcrypt cost recovery codes are hashed with (the password's).
// Tests lower it.
var recoveryCost = bcrypt.DefaultCost

// TwoFactorStatus is what the account page shows: never the secret.
type TwoFactorStatus struct {
	Enabled           bool       `json:"enabled"`
	EnabledAt         *time.Time `json:"enabled_at,omitempty"`
	RecoveryCodesLeft int        `json:"recovery_codes_left"`
}

// TOTPSetup is a new secret to show while setting up: the text form to type in and the
// otpauth link for the QR code. It is the only time the secret leaves the server.
type TOTPSetup struct {
	Secret string
	URI    string
}

// TwoFactor reports an account's two-factor state.
func (s *Service) TwoFactor(ctx context.Context, userID int64) (TwoFactorStatus, error) {
	var st TwoFactorStatus
	var enabledAt sql.NullString
	err := s.db.QueryRowContext(ctx,
		`SELECT `+store.TimestampText(s.db, "enabled_at")+` FROM user_totp WHERE user_id = ? AND enabled = 1`, userID).Scan(&enabledAt)
	if errors.Is(err, sql.ErrNoRows) {
		return st, nil
	}
	if err != nil {
		return st, err
	}
	st.Enabled = true
	if t := parseSQLTime(enabledAt.String); !t.IsZero() {
		st.EnabledAt = &t
	}
	err = s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM user_recovery_codes WHERE user_id = ? AND used_at IS NULL`, userID).Scan(&st.RecoveryCodesLeft)
	return st, err
}

// TwoFactorEnabled reports whether signing in to the account needs a code after the
// password. A failure to read reports true with the error, so a caller that ignores the
// error still fails closed.
func (s *Service) TwoFactorEnabled(ctx context.Context, userID int64) (bool, error) {
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM user_totp WHERE user_id = ? AND enabled = 1`, userID).Scan(&n); err != nil {
		return true, err
	}
	return n > 0, nil
}

// TwoFactorUsers is the set of accounts with two-factor sign-in on (People & access).
func (s *Service) TwoFactorUsers(ctx context.Context) (map[int64]bool, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT user_id FROM user_totp WHERE enabled = 1`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]bool{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}

// BeginTOTP starts setting up two-factor sign-in: a new secret, kept unconfirmed until
// ConfirmTOTP sees a code made from it. Starting again replaces an unconfirmed secret; an
// account that already has it on must turn it off first (ErrTwoFactorOn).
func (s *Service) BeginTOTP(ctx context.Context, userID int64) (TOTPSetup, error) {
	u, err := s.UserByID(ctx, userID)
	if err != nil {
		return TOTPSetup{}, err
	}
	secret, err := newTOTPSecret()
	if err != nil {
		return TOTPSetup{}, err
	}
	// One statement, so a concurrent confirm can't be overwritten: the upsert only
	// touches a row that is still unconfirmed.
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO user_totp (user_id, secret, enabled, last_step) VALUES (?, ?, 0, 0)
		ON CONFLICT(user_id) DO UPDATE SET secret = excluded.secret, last_step = 0, created_at = CURRENT_TIMESTAMP
		WHERE user_totp.enabled = 0`, userID, secret)
	if err != nil {
		return TOTPSetup{}, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return TOTPSetup{}, ErrTwoFactorOn
	}
	return TOTPSetup{Secret: secret, URI: otpauthURI(u.Username, secret)}, nil
}

// ConfirmTOTP turns two-factor sign-in on once code matches the secret BeginTOTP made,
// and returns the account's new recovery codes, to be shown once.
func (s *Service) ConfirmTOTP(ctx context.Context, userID int64, code string) ([]string, error) {
	codes, hashes, err := newRecoveryCodes()
	if err != nil {
		return nil, err
	}
	code = normalizeOTP(code)
	err = store.WithTx(ctx, s.db, func(tx *store.Transaction) error {
		var secret string
		var enabled int
		err := tx.QueryRowContext(ctx, `SELECT secret, enabled FROM user_totp WHERE user_id = ?`, userID).Scan(&secret, &enabled)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrTwoFactorSetup
		}
		if err != nil {
			return err
		}
		if enabled == 1 {
			return ErrTwoFactorOn
		}
		key, err := decodeTOTPSecret(secret)
		if err != nil {
			return ErrTwoFactorSetup
		}
		step, ok := matchTOTP(key, code, s.now(), 0)
		if !ok {
			return ErrBadCode
		}
		if _, err := tx.ExecContext(ctx, `UPDATE user_totp SET enabled = 1, last_step = ?, enabled_at = ? WHERE user_id = ? AND enabled = 0`,
			step, sqlTime(s.now()), userID); err != nil {
			return err
		}
		return replaceRecoveryCodes(ctx, tx, userID, hashes)
	})
	if err != nil {
		return nil, err
	}
	return codes, nil
}

// VerifySecondFactor checks the code typed after the password: six digits are an
// authenticator code (accepted once, ±30 seconds), anything else a recovery code (which
// then can't be used again). recovery says which one worked.
func (s *Service) VerifySecondFactor(ctx context.Context, userID int64, code string) (recovery bool, err error) {
	code = normalizeOTP(code)
	if len(code) == totpDigits && isDigits(code) {
		return false, s.verifyTOTP(ctx, userID, code)
	}
	return true, s.useRecoveryCode(ctx, userID, code)
}

// verifyTOTP accepts an authenticator code for a step newer than the last one used, and
// records that step, so the same code (or an older one) can't be replayed.
func (s *Service) verifyTOTP(ctx context.Context, userID int64, code string) error {
	return store.WithTx(ctx, s.db, func(tx *store.Transaction) error {
		var secret string
		var last int64
		err := tx.QueryRowContext(ctx, `SELECT secret, last_step FROM user_totp WHERE user_id = ? AND enabled = 1`, userID).Scan(&secret, &last)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrTwoFactorOff
		}
		if err != nil {
			return err
		}
		key, err := decodeTOTPSecret(secret)
		if err != nil {
			return err
		}
		step, ok := matchTOTP(key, code, s.now(), last)
		if !ok {
			return ErrBadCode
		}
		// The WHERE makes two sign-ins racing with one code a single success.
		res, err := tx.ExecContext(ctx, `UPDATE user_totp SET last_step = ? WHERE user_id = ? AND last_step < ?`, step, userID, step)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrBadCode
		}
		return nil
	})
}

// useRecoveryCode spends one of the account's unused recovery codes.
func (s *Service) useRecoveryCode(ctx context.Context, userID int64, code string) error {
	code = strings.ToLower(code)
	// Anything not shaped like a recovery code is wrong without spending bcrypt on it.
	if len(code) != recoveryCodeLen || strings.Trim(code, recoveryAlphabet) != "" {
		return ErrBadCode
	}
	if on, err := s.TwoFactorEnabled(ctx, userID); err != nil {
		return err
	} else if !on {
		return ErrTwoFactorOff
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, code_hash FROM user_recovery_codes WHERE user_id = ? AND used_at IS NULL`, userID)
	if err != nil {
		return err
	}
	type stored struct {
		id   int64
		hash string
	}
	var list []stored
	for rows.Next() {
		var c stored
		if err := rows.Scan(&c.id, &c.hash); err != nil {
			rows.Close()
			return err
		}
		list = append(list, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, c := range list {
		if bcrypt.CompareHashAndPassword([]byte(c.hash), []byte(code)) != nil {
			continue
		}
		// used_at IS NULL: two sign-ins racing with one code, only one wins.
		res, err := s.db.ExecContext(ctx, `UPDATE user_recovery_codes SET used_at = ? WHERE id = ? AND used_at IS NULL`, sqlTime(s.now()), c.id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 1 {
			return nil
		}
		return ErrBadCode
	}
	return ErrBadCode
}

// RegenerateRecoveryCodes replaces every recovery code (used or not) with a new set, to
// be shown once. Checking a current code first is the caller's job.
func (s *Service) RegenerateRecoveryCodes(ctx context.Context, userID int64) ([]string, error) {
	codes, hashes, err := newRecoveryCodes()
	if err != nil {
		return nil, err
	}
	err = store.WithTx(ctx, s.db, func(tx *store.Transaction) error {
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM user_totp WHERE user_id = ? AND enabled = 1`, userID).Scan(&n); err != nil {
			return err
		}
		if n == 0 {
			return ErrTwoFactorOff
		}
		return replaceRecoveryCodes(ctx, tx, userID, hashes)
	})
	if err != nil {
		return nil, err
	}
	return codes, nil
}

// DisableTwoFactor turns two-factor sign-in off: the secret, the recovery codes and the
// trusted devices all go. It reports whether it was on (an unfinished setup is cleared
// too, but doesn't count). The owner turning it off checks a code first; an admin's reset
// and the reset-2fa command don't.
func (s *Service) DisableTwoFactor(ctx context.Context, userID int64) (bool, error) {
	var was bool
	err := store.WithTx(ctx, s.db, func(tx *store.Transaction) error {
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM user_totp WHERE user_id = ? AND enabled = 1`, userID).Scan(&n); err != nil {
			return err
		}
		was = n > 0
		for _, q := range []string{
			`DELETE FROM user_totp WHERE user_id = ?`,
			`DELETE FROM user_recovery_codes WHERE user_id = ?`,
			`DELETE FROM user_trusted_devices WHERE user_id = ?`,
		} {
			if _, err := tx.ExecContext(ctx, q, userID); err != nil {
				return err
			}
		}
		return nil
	})
	return was, err
}

// replaceRecoveryCodes swaps the account's recovery codes for hashes, inside tx.
func replaceRecoveryCodes(ctx context.Context, tx *store.Transaction, userID int64, hashes []string) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM user_recovery_codes WHERE user_id = ?`, userID); err != nil {
		return err
	}
	for _, h := range hashes {
		if _, err := tx.ExecContext(ctx, `INSERT INTO user_recovery_codes (user_id, code_hash) VALUES (?, ?)`, userID, h); err != nil {
			return err
		}
	}
	return nil
}

// newRecoveryCodes makes RecoveryCodeCount codes ("abcde-fghjk") and their bcrypt hashes.
// The hash is of the code without its dash, which is how a typed code is checked.
func newRecoveryCodes() (codes, hashes []string, err error) {
	size := big.NewInt(int64(len(recoveryAlphabet)))
	for range RecoveryCodeCount {
		b := make([]byte, recoveryCodeLen)
		for i := range b {
			k, err := rand.Int(rand.Reader, size)
			if err != nil {
				return nil, nil, err
			}
			b[i] = recoveryAlphabet[k.Int64()]
		}
		h, err := bcrypt.GenerateFromPassword(b, recoveryCost)
		if err != nil {
			return nil, nil, err
		}
		codes = append(codes, string(b[:5])+"-"+string(b[5:]))
		hashes = append(hashes, string(h))
	}
	return codes, hashes, nil
}
