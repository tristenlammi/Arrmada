package audioserver

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/tristenlammi/arrmada/internal/auth"
)

// Sign-in for listening apps. Each person sets an audiobook-server password in Arrmada;
// without one they can't connect, and their Arrmada password is never accepted here.
// A sign-in on a device is a token "family": a long-lived
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

// Accounts handles audiobook-server passwords and tokens.
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

// Login checks a username and audiobook-server password and issues a token family.
func (a *Accounts) Login(ctx context.Context, username, password, client string) (*auth.User, Tokens, error) {
	u, err := a.verify(ctx, username, password)
	if err != nil {
		return nil, Tokens{}, err
	}
	if !a.allowed(ctx, u) {
		return nil, Tokens{}, errNoAccess
	}
	t, err := a.issue(ctx, u, client)
	return u, t, err
}

func (a *Accounts) verify(ctx context.Context, username, password string) (*auth.User, error) {
	u, err := a.users.UserByUsername(ctx, username)
	var hash string
	if err == nil {
		hash = a.passwordHash(ctx, u.ID)
	}
	if err != nil || hash == "" || u.Disabled {
		// Spend a real bcrypt cycle anyway, so timing doesn't reveal which usernames
		// exist or have a password set.
		_ = bcrypt.CompareHashAndPassword(dummyHash, []byte(password))
		return nil, errBadLogin
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) != nil {
		return nil, errBadLogin
	}
	return u, nil
}

var dummyHash, _ = bcrypt.GenerateFromPassword([]byte("arrmada-audiobook-timing-guard"), bcrypt.DefaultCost)

func (a *Accounts) passwordHash(ctx context.Context, userID int64) string {
	var h string
	_ = a.db.QueryRowContext(ctx, `SELECT hash FROM audio_passwords WHERE user_id = ?`, userID).Scan(&h)
	return h
}

// HasPassword reports whether a user has set an audiobook-server password.
func (a *Accounts) HasPassword(ctx context.Context, userID int64) bool {
	return a.passwordHash(ctx, userID) != ""
}

// PasswordsSet returns the ids of users who have set a password.
func (a *Accounts) PasswordsSet(ctx context.Context) map[int64]bool {
	out := map[int64]bool{}
	rows, err := a.db.QueryContext(ctx, `SELECT user_id FROM audio_passwords`)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		if rows.Scan(&id) == nil {
			out[id] = true
		}
	}
	return out
}

// MinPasswordLength is the shortest audiobook-server password accepted.
const MinPasswordLength = 8

// SetPassword sets (or changes) a user's audiobook-server password. signOut also signs
// out every device already connected — what you want after a change, not the first time.
func (a *Accounts) SetPassword(ctx context.Context, userID int64, password string, signOut bool) error {
	if len([]rune(password)) < MinPasswordLength {
		return fmt.Errorf("use at least %d characters", MinPasswordLength)
	}
	if len(password) > 72 {
		return errors.New("use at most 72 characters")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	if _, err := a.db.ExecContext(ctx,
		`INSERT INTO audio_passwords (user_id, hash, updated_at) VALUES (?, ?, ?)
		 ON CONFLICT(user_id) DO UPDATE SET hash = excluded.hash, updated_at = excluded.updated_at`,
		userID, string(hash), a.now().UnixMilli()); err != nil {
		return err
	}
	if signOut {
		_, err = a.db.ExecContext(ctx, `UPDATE audio_tokens SET revoked = 1 WHERE user_id = ?`, userID)
	}
	return err
}

// RemovePassword removes a user's password and signs out all their devices.
func (a *Accounts) RemovePassword(ctx context.Context, userID int64) error {
	if _, err := a.db.ExecContext(ctx, `DELETE FROM audio_passwords WHERE user_id = ?`, userID); err != nil {
		return err
	}
	_, err := a.db.ExecContext(ctx, `UPDATE audio_tokens SET revoked = 1 WHERE user_id = ?`, userID)
	return err
}

func (a *Accounts) issue(ctx context.Context, u *auth.User, client string) (Tokens, error) {
	userID := u.ID
	now := a.now()
	t := Tokens{Legacy: jwtShaped(u, "", now, time.Time{}), Access: jwtShaped(u, "access", now, now.Add(accessTTL)),
		Refresh: randToken(), Family: randToken()[:16]}
	for _, row := range []struct {
		tok, kind string
		exp       int64
	}{
		{t.Legacy, "legacy", 0},
		{t.Access, "access", now.Add(accessTTL).UnixMilli()},
		{t.Refresh, "refresh", now.Add(refreshTTL).UnixMilli()},
	} {
		if _, err := a.db.ExecContext(ctx,
			`INSERT INTO audio_tokens (user_id, hash, kind, family, client, created_at, expires_at, last_used_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			userID, hashToken(row.tok), row.kind, t.Family, client, now.UnixMilli(), row.exp, now.UnixMilli()); err != nil {
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
	if err != nil || u.Disabled || !a.allowed(ctx, u) || !a.HasPassword(ctx, u.ID) {
		return nil, Tokens{}, errNoAccess
	}
	now := a.now()
	access := jwtShaped(u, "access", now, now.Add(accessTTL))
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
	if err != nil || u.Disabled || !a.allowed(ctx, u) || !a.HasPassword(ctx, u.ID) {
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
	CreatedAt  int64  `json:"created_at"`
	LastUsedAt int64  `json:"last_used_at"`
}

// Devices lists signed-in apps (all users when userID is 0).
func (a *Accounts) Devices(ctx context.Context, userID int64) ([]Device, error) {
	q := `SELECT t.family, t.user_id, u.username, MAX(t.client), MAX(t.device), MIN(t.created_at), MAX(t.last_used_at)
	      FROM audio_tokens t JOIN users u ON u.id = t.user_id
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
		if err := rows.Scan(&d.Family, &d.UserID, &d.Username, &d.Client, &d.Device, &d.CreatedAt, &d.LastUsedAt); err != nil {
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

func randToken() string {
	var b [32]byte
	_, _ = rand.Read(b[:])
	return base64.RawURLEncoding.EncodeToString(b[:])
}

// jwtShaped is a token laid out like Audiobookshelf's JWTs, so apps that read a token's
// expiry to know when to refresh (as newer iOS clients do) find one. It's still checked
// only by looking up its hash: the claims are informational, and the signature part is
// random, so the token is exactly as unguessable as randToken.
func jwtShaped(u *auth.User, kind string, iat, exp time.Time) string {
	enc := base64.RawURLEncoding
	claims := map[string]any{"userId": "u" + itoa(u.ID), "username": u.Username, "iat": iat.Unix()}
	if kind != "" {
		claims["type"] = kind
		claims["jti"] = randToken()[:22]
	}
	if !exp.IsZero() {
		claims["exp"] = exp.Unix()
	}
	body, _ := json.Marshal(claims)
	return enc.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`)) + "." + enc.EncodeToString(body) + "." + randToken()
}

func hashToken(t string) string {
	h := sha256.Sum256([]byte(t))
	return hex.EncodeToString(h[:])
}
