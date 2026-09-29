package audioserver

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/tristenlammi/arrmada/internal/auth"
)

// Sign-in for listening apps. A sign-in on a device is a token "family": a long-lived
// token (older clients keep one forever), an access token and a refresh token (newer
// Audiobookshelf clients refresh when the access token expires). Revoking a device
// revokes its whole family. Tokens are stored hashed.
//
// Access tokens last a month rather than Audiobookshelf's hour: every refresh is a
// chance for a flaky connection to log someone out, and the refresh token isn't rotated
// for the same reason — a lost refresh response must not strand a phone.

const (
	accessTTL  = 30 * 24 * time.Hour
	refreshTTL = 365 * 24 * time.Hour
	touchEvery = time.Minute
)

var (
	errBadLogin = errors.New("invalid username or password")
	errNoAccess = errors.New("this account isn't allowed to use the audiobook server")
	errBadToken = errors.New("invalid or expired token")
)

// Tokens is what a sign-in hands the app.
type Tokens struct {
	Legacy  string
	Access  string
	Refresh string
	Family  string
}

// Accounts handles tokens and app passwords.
type Accounts struct {
	db      *sql.DB
	users   *auth.Service
	allowed func(ctx context.Context, u *auth.User) bool
	now     func() time.Time

	mu      sync.Mutex
	touched map[string]time.Time
}

func newAccounts(db *sql.DB, users *auth.Service, allowed func(context.Context, *auth.User) bool) *Accounts {
	return &Accounts{db: db, users: users, allowed: allowed, now: time.Now, touched: map[string]time.Time{}}
}

// Login checks a username with the account password or an app password and issues a
// token family.
func (a *Accounts) Login(ctx context.Context, username, password, client string) (*auth.User, Tokens, error) {
	u, appPwID, err := a.verify(ctx, username, password)
	if err != nil {
		return nil, Tokens{}, err
	}
	if !a.allowed(ctx, u) {
		return nil, Tokens{}, errNoAccess
	}
	t, err := a.issue(ctx, u.ID, appPwID, client)
	return u, t, err
}

func (a *Accounts) verify(ctx context.Context, username, password string) (*auth.User, int64, error) {
	if u, err := a.users.Authenticate(ctx, username, password); err == nil {
		return u, 0, nil
	}
	u, err := a.users.UserByUsername(ctx, username)
	if err != nil || u.Disabled {
		return nil, 0, errBadLogin
	}
	rows, err := a.db.QueryContext(ctx, `SELECT id, hash FROM audio_app_passwords WHERE user_id = ?`, u.ID)
	if err != nil {
		return nil, 0, err
	}
	type cand struct {
		id   int64
		hash string
	}
	var cands []cand
	for rows.Next() {
		var c cand
		if rows.Scan(&c.id, &c.hash) == nil {
			cands = append(cands, c)
		}
	}
	rows.Close()
	pw := normalizeAppPassword(password)
	for _, c := range cands {
		if bcrypt.CompareHashAndPassword([]byte(c.hash), []byte(pw)) == nil {
			_, _ = a.db.ExecContext(ctx, `UPDATE audio_app_passwords SET last_used_at = ? WHERE id = ?`, a.now().UnixMilli(), c.id)
			return u, c.id, nil
		}
	}
	return nil, 0, errBadLogin
}

func (a *Accounts) issue(ctx context.Context, userID, appPwID int64, client string) (Tokens, error) {
	now := a.now()
	t := Tokens{Legacy: randToken(), Access: randToken(), Refresh: randToken(), Family: randToken()[:16]}
	for _, row := range []struct {
		tok, kind string
		exp       int64
	}{
		{t.Legacy, "legacy", 0},
		{t.Access, "access", now.Add(accessTTL).UnixMilli()},
		{t.Refresh, "refresh", now.Add(refreshTTL).UnixMilli()},
	} {
		if _, err := a.db.ExecContext(ctx,
			`INSERT INTO audio_tokens (user_id, hash, kind, family, app_password_id, client, created_at, expires_at, last_used_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			userID, hashToken(row.tok), row.kind, t.Family, appPwID, client, now.UnixMilli(), row.exp, now.UnixMilli()); err != nil {
			return Tokens{}, err
		}
	}
	return t, nil
}

// Refresh exchanges a refresh token for a new access token. The refresh token itself
// stays valid (and its expiry slides), so a refresh reply lost on a bad connection
// can simply be retried.
func (a *Accounts) Refresh(ctx context.Context, refreshToken string) (*auth.User, Tokens, error) {
	var userID int64
	var family, client string
	var exp int64
	var revoked int
	err := a.db.QueryRowContext(ctx,
		`SELECT user_id, family, client, expires_at, revoked FROM audio_tokens WHERE hash = ? AND kind = 'refresh'`,
		hashToken(refreshToken)).Scan(&userID, &family, &client, &exp, &revoked)
	if err != nil || revoked != 0 || (exp > 0 && exp < a.now().UnixMilli()) {
		return nil, Tokens{}, errBadToken
	}
	u, err := a.users.UserByID(ctx, userID)
	if err != nil || u.Disabled || !a.allowed(ctx, u) {
		return nil, Tokens{}, errNoAccess
	}
	now := a.now()
	access := randToken()
	if _, err := a.db.ExecContext(ctx,
		`INSERT INTO audio_tokens (user_id, hash, kind, family, client, created_at, expires_at, last_used_at)
		 VALUES (?, ?, 'access', ?, ?, ?, ?, ?)`,
		userID, hashToken(access), family, client, now.UnixMilli(), now.Add(accessTTL).UnixMilli(), now.UnixMilli()); err != nil {
		return nil, Tokens{}, err
	}
	_, _ = a.db.ExecContext(ctx, `UPDATE audio_tokens SET expires_at = ?, last_used_at = ? WHERE hash = ?`,
		now.Add(refreshTTL).UnixMilli(), now.UnixMilli(), hashToken(refreshToken))
	// Old access tokens of this family that have expired are just clutter.
	_, _ = a.db.ExecContext(ctx, `DELETE FROM audio_tokens WHERE family = ? AND kind = 'access' AND expires_at < ?`, family, now.UnixMilli())
	var legacy string // never re-issued; the app keeps the one it has
	return u, Tokens{Legacy: legacy, Access: access, Refresh: refreshToken, Family: family}, nil
}

// Validate resolves a bearer token (access or long-lived) to its user and family.
func (a *Accounts) Validate(ctx context.Context, token string) (*auth.User, string, error) {
	if token == "" {
		return nil, "", errBadToken
	}
	h := hashToken(token)
	var userID, exp int64
	var family, kind string
	var revoked int
	err := a.db.QueryRowContext(ctx,
		`SELECT user_id, family, kind, expires_at, revoked FROM audio_tokens WHERE hash = ?`, h).
		Scan(&userID, &family, &kind, &exp, &revoked)
	if err != nil || revoked != 0 || kind == "refresh" || (exp > 0 && exp < a.now().UnixMilli()) {
		return nil, "", errBadToken
	}
	u, err := a.users.UserByID(ctx, userID)
	if err != nil || u.Disabled || !a.allowed(ctx, u) {
		return nil, "", errNoAccess
	}
	a.touch(ctx, h)
	return u, family, nil
}

// touch records a token's use at most once a minute, not on every request.
func (a *Accounts) touch(ctx context.Context, h string) {
	now := a.now()
	a.mu.Lock()
	last := a.touched[h]
	if now.Sub(last) < touchEvery {
		a.mu.Unlock()
		return
	}
	a.touched[h] = now
	a.mu.Unlock()
	_, _ = a.db.ExecContext(ctx, `UPDATE audio_tokens SET last_used_at = ? WHERE hash = ?`, now.UnixMilli(), h)
}

// NoteDevice records the device name a family plays from (reported when playback
// starts), so the devices list reads "Pixel 8 · Lissen" instead of a token.
func (a *Accounts) NoteDevice(ctx context.Context, family, device, client string) {
	if family == "" || device == "" {
		return
	}
	_, _ = a.db.ExecContext(ctx, `UPDATE audio_tokens SET device = ?, client = CASE WHEN ? != '' THEN ? ELSE client END WHERE family = ?`,
		device, client, client, family)
}

// Logout revokes the family a token belongs to.
func (a *Accounts) Logout(ctx context.Context, token string) {
	var family string
	if a.db.QueryRowContext(ctx, `SELECT family FROM audio_tokens WHERE hash = ?`, hashToken(token)).Scan(&family) == nil {
		_ = a.RevokeFamily(ctx, family, 0)
	}
}

// Device is one signed-in app.
type Device struct {
	Family     string `json:"id"`
	UserID     int64  `json:"user_id"`
	Username   string `json:"username"`
	Client     string `json:"client"`
	Device     string `json:"device"`
	AppPwName  string `json:"app_password,omitempty"`
	CreatedAt  int64  `json:"created_at"`
	LastUsedAt int64  `json:"last_used_at"`
}

// Devices lists signed-in apps (all users when userID is 0).
func (a *Accounts) Devices(ctx context.Context, userID int64) ([]Device, error) {
	q := `SELECT t.family, t.user_id, u.username, MAX(t.client), MAX(t.device), COALESCE(MAX(p.name), ''),
	        MIN(t.created_at), MAX(t.last_used_at)
	      FROM audio_tokens t JOIN users u ON u.id = t.user_id
	      LEFT JOIN audio_app_passwords p ON p.id = t.app_password_id
	      WHERE t.revoked = 0`
	args := []any{}
	if userID > 0 {
		q += ` AND t.user_id = ?`
		args = append(args, userID)
	}
	q += ` GROUP BY t.family ORDER BY MAX(t.last_used_at) DESC`
	rows, err := a.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Device{}
	for rows.Next() {
		var d Device
		if err := rows.Scan(&d.Family, &d.UserID, &d.Username, &d.Client, &d.Device, &d.AppPwName, &d.CreatedAt, &d.LastUsedAt); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// RevokeFamily signs a device out. userID > 0 restricts it to that user's own devices.
func (a *Accounts) RevokeFamily(ctx context.Context, family string, userID int64) error {
	q := `UPDATE audio_tokens SET revoked = 1 WHERE family = ?`
	args := []any{family}
	if userID > 0 {
		q += ` AND user_id = ?`
		args = append(args, userID)
	}
	_, err := a.db.ExecContext(ctx, q, args...)
	return err
}

// AppPassword is a password made for a listening app (the secret is only shown once).
type AppPassword struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	CreatedAt  int64  `json:"created_at"`
	LastUsedAt int64  `json:"last_used_at"`
	Password   string `json:"password,omitempty"`
}

// CreateAppPassword makes a new app password and returns it (the only time it's shown).
func (a *Accounts) CreateAppPassword(ctx context.Context, userID int64, name string) (AppPassword, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		name = "Listening app"
	}
	if len(name) > 60 {
		name = name[:60]
	}
	plain := newAppPassword()
	hash, err := bcrypt.GenerateFromPassword([]byte(normalizeAppPassword(plain)), bcrypt.DefaultCost)
	if err != nil {
		return AppPassword{}, err
	}
	now := a.now().UnixMilli()
	res, err := a.db.ExecContext(ctx,
		`INSERT INTO audio_app_passwords (user_id, name, hash, created_at) VALUES (?, ?, ?, ?)`, userID, name, string(hash), now)
	if err != nil {
		return AppPassword{}, err
	}
	id, _ := res.LastInsertId()
	return AppPassword{ID: id, Name: name, CreatedAt: now, Password: plain}, nil
}

// AppPasswords lists a user's app passwords (without secrets).
func (a *Accounts) AppPasswords(ctx context.Context, userID int64) ([]AppPassword, error) {
	rows, err := a.db.QueryContext(ctx,
		`SELECT id, name, created_at, last_used_at FROM audio_app_passwords WHERE user_id = ? ORDER BY id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AppPassword{}
	for rows.Next() {
		var p AppPassword
		if err := rows.Scan(&p.ID, &p.Name, &p.CreatedAt, &p.LastUsedAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// DeleteAppPassword removes an app password and signs out every device that used it.
func (a *Accounts) DeleteAppPassword(ctx context.Context, userID, id int64) error {
	res, err := a.db.ExecContext(ctx, `DELETE FROM audio_app_passwords WHERE id = ? AND user_id = ?`, id, userID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return errors.New("app password not found")
	}
	_, err = a.db.ExecContext(ctx, `UPDATE audio_tokens SET revoked = 1 WHERE app_password_id = ? AND user_id = ?`, id, userID)
	return err
}

func randToken() string {
	var b [32]byte
	_, _ = rand.Read(b[:])
	return base64.RawURLEncoding.EncodeToString(b[:])
}

func hashToken(t string) string {
	h := sha256.Sum256([]byte(t))
	return hex.EncodeToString(h[:])
}

// newAppPassword makes a readable password: four groups of four letters, easy to type
// on a phone ("kfmt-qwzr-…"). 16 letters from a 23-letter alphabet ≈ 72 bits.
func newAppPassword() string {
	const alphabet = "abcdefghjkmnpqrstuvwxyz"
	var b [16]byte
	_, _ = rand.Read(b[:])
	var sb strings.Builder
	for i, c := range b {
		if i > 0 && i%4 == 0 {
			sb.WriteByte('-')
		}
		sb.WriteByte(alphabet[int(c)%len(alphabet)])
	}
	return sb.String()
}

// normalizeAppPassword ignores case, spaces and dashes, so a typed app password matches
// however it was entered.
func normalizeAppPassword(p string) string {
	var sb strings.Builder
	for _, r := range strings.ToLower(p) {
		if r != '-' && r != ' ' {
			sb.WriteRune(r)
		}
	}
	return sb.String()
}
