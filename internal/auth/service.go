// Package auth owns identity and access: local users (bcrypt-hashed passwords),
// browser sessions, and API keys. Only hashes are ever stored — never the raw
// password, session token, or API key. Roles drive RBAC.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// Sentinel errors callers can branch on.
var (
	ErrInvalidCredentials = errors.New("invalid username or password")
	ErrUserExists         = errors.New("username already taken")
	ErrWeakPassword       = errors.New("password must be at least 8 characters")
	ErrUsernameRequired   = errors.New("username is required")
	ErrNotFound           = errors.New("not found")
)

// Role is an RBAC role. Order (most→least privileged): admin, manager,
// requester, readonly.
type Role string

const (
	RoleAdmin     Role = "admin"
	RoleManager   Role = "manager"
	RoleRequester Role = "requester"
	RoleReadonly  Role = "readonly"
)

var roleRank = map[Role]int{RoleReadonly: 0, RoleRequester: 1, RoleManager: 2, RoleAdmin: 3}

// ValidRole reports whether r is a known role.
func ValidRole(r Role) bool { _, ok := roleRank[r]; return ok }

// AtLeast reports whether r is at least as privileged as min.
func (r Role) AtLeast(min Role) bool { return roleRank[r] >= roleRank[min] }

// User is a lightweight identity used across requests.
type User struct {
	ID          int64  `json:"id"`
	Username    string `json:"username"`
	Role        Role   `json:"role"`
	Disabled    bool   `json:"disabled"`
	AutoApprove bool   `json:"auto_approve"`
	CreatedAt   string `json:"created_at,omitempty"`
}

// Service provides authentication operations backed by the database.
type Service struct {
	db         *sql.DB
	sessionTTL time.Duration
}

// NewService builds an auth service over the given database pool.
func NewService(db *sql.DB) *Service {
	return &Service{db: db, sessionTTL: 30 * 24 * time.Hour}
}

// UserCount returns the number of user accounts (0 means first-run setup needed).
func (s *Service) UserCount(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&n)
	return n, err
}

// CreateUser creates a user with a bcrypt-hashed password.
func (s *Service) CreateUser(ctx context.Context, username, password string, role Role, autoApprove bool) (*User, error) {
	username = strings.TrimSpace(username)
	if username == "" {
		return nil, ErrUsernameRequired
	}
	if len(password) < 8 {
		return nil, ErrWeakPassword
	}
	if !ValidRole(role) {
		role = RoleAdmin
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return nil, err
	}

	res, err := s.db.ExecContext(ctx,
		`INSERT INTO users (username, password_hash, role, auto_approve) VALUES (?, ?, ?, ?)`,
		username, string(hash), string(role), boolToInt(autoApprove))
	if err != nil {
		if isUniqueViolation(err) {
			return nil, ErrUserExists
		}
		return nil, err
	}
	id, _ := res.LastInsertId()
	return &User{ID: id, Username: username, Role: role, AutoApprove: autoApprove}, nil
}

// FindOrCreatePlexUser returns the user linked to a Plex account, creating a passwordless one
// (its username derived from the Plex name, de-duplicated) if none exists yet. An existing link
// keeps its current role/auto-approve — only new users get the provided defaults. A disabled
// linked user is returned as-is so the caller can refuse the sign-in.
func (s *Service) FindOrCreatePlexUser(ctx context.Context, plexID, plexUsername string, role Role, autoApprove bool) (*User, error) {
	if strings.TrimSpace(plexID) == "" {
		return nil, errors.New("missing plex id")
	}
	var u User
	var disabled, aa int
	err := s.db.QueryRowContext(ctx,
		`SELECT id, username, role, disabled, auto_approve FROM users WHERE plex_id = ?`, plexID).
		Scan(&u.ID, &u.Username, &u.Role, &disabled, &aa)
	if err == nil {
		u.Disabled = disabled == 1
		u.AutoApprove = aa == 1
		return &u, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if !ValidRole(role) {
		role = RoleRequester
	}
	pw := make([]byte, 24) // unusable password — Plex-linked accounts sign in via Plex only
	if _, err := rand.Read(pw); err != nil {
		return nil, err
	}
	hash, err := bcrypt.GenerateFromPassword(pw, bcrypt.DefaultCost)
	if err != nil {
		return nil, err
	}
	username := s.uniqueUsername(ctx, plexUsername)
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO users (username, password_hash, role, auto_approve, plex_id) VALUES (?, ?, ?, ?, ?)`,
		username, string(hash), string(role), boolToInt(autoApprove), plexID)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return &User{ID: id, Username: username, Role: role, AutoApprove: autoApprove}, nil
}

// uniqueUsername returns base (trimmed), appending "-N" until it's free.
func (s *Service) uniqueUsername(ctx context.Context, base string) string {
	base = strings.TrimSpace(base)
	if base == "" {
		base = "plex-user"
	}
	name := base
	for i := 2; i < 1000; i++ {
		var n int
		if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE username = ?`, name).Scan(&n); err == nil && n == 0 {
			return name
		}
		name = base + "-" + strconv.Itoa(i)
	}
	return base + "-" + strconv.FormatInt(time.Now().UnixNano(), 10)
}

// UpdateUser changes a user's role and auto-approve flag.
func (s *Service) UpdateUser(ctx context.Context, id int64, role Role, autoApprove bool) error {
	if !ValidRole(role) {
		return errors.New("invalid role")
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE users SET role = ?, auto_approve = ? WHERE id = ?`, string(role), boolToInt(autoApprove), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// ListUsers returns all accounts (no secrets), oldest first.
func (s *Service) ListUsers(ctx context.Context) ([]User, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, username, role, disabled, auto_approve, created_at FROM users ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []User
	for rows.Next() {
		var u User
		var disabled, autoApprove int
		if err := rows.Scan(&u.ID, &u.Username, &u.Role, &disabled, &autoApprove, &u.CreatedAt); err != nil {
			return nil, err
		}
		u.Disabled = disabled != 0
		u.AutoApprove = autoApprove != 0
		out = append(out, u)
	}
	return out, rows.Err()
}

// CountAdmins returns how many admin accounts exist (used to protect the last admin).
func (s *Service) CountAdmins(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE role = ?`, string(RoleAdmin)).Scan(&n)
	return n, err
}

// DeleteUser removes an account and its sessions/keys (via FK cascade).
func (s *Service) DeleteUser(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM users WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetPassword changes a user's password (admin reset).
func (s *Service) SetPassword(ctx context.Context, id int64, password string) error {
	if len(password) < 8 {
		return ErrWeakPassword
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	res, err := s.db.ExecContext(ctx, `UPDATE users SET password_hash = ? WHERE id = ?`, string(hash), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// Authenticate verifies a username/password and returns the user on success.
func (s *Service) Authenticate(ctx context.Context, username, password string) (*User, error) {
	var (
		u           User
		hash        string
		disabled    int
		autoApprove int
	)
	err := s.db.QueryRowContext(ctx,
		`SELECT id, username, password_hash, role, disabled, auto_approve FROM users WHERE username = ?`,
		strings.TrimSpace(username)).
		Scan(&u.ID, &u.Username, &hash, &u.Role, &disabled, &autoApprove)
	u.AutoApprove = autoApprove != 0
	if errors.Is(err, sql.ErrNoRows) {
		// Spend a bcrypt cycle anyway so timing doesn't leak account existence.
		_ = bcrypt.CompareHashAndPassword([]byte("$2a$10$invalidinvalidinvalidinvalidinvalidinvalidinv"), []byte(password))
		return nil, ErrInvalidCredentials
	}
	if err != nil {
		return nil, err
	}
	if disabled != 0 {
		return nil, ErrInvalidCredentials
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) != nil {
		return nil, ErrInvalidCredentials
	}
	return &u, nil
}

// CreateSession issues a new session token (returned raw once) and stores only
// its hash. Returns the raw token and its expiry.
func (s *Service) CreateSession(ctx context.Context, userID int64) (string, time.Time, error) {
	raw, err := randToken(32)
	if err != nil {
		return "", time.Time{}, err
	}
	expires := time.Now().Add(s.sessionTTL)
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO sessions (token_hash, user_id, expires_at) VALUES (?, ?, ?)`,
		hashToken(raw), userID, sqlTime(expires))
	if err != nil {
		return "", time.Time{}, err
	}
	return raw, expires, nil
}

// ValidateSession returns the user for a non-expired session token.
func (s *Service) ValidateSession(ctx context.Context, raw string) (*User, error) {
	var (
		u           User
		disabled    int
		autoApprove int
	)
	err := s.db.QueryRowContext(ctx, `
		SELECT u.id, u.username, u.role, u.disabled, u.auto_approve
		FROM sessions s JOIN users u ON u.id = s.user_id
		WHERE s.token_hash = ? AND s.expires_at > CURRENT_TIMESTAMP`,
		hashToken(raw)).Scan(&u.ID, &u.Username, &u.Role, &disabled, &autoApprove)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && disabled != 0) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	u.AutoApprove = autoApprove != 0
	return &u, nil
}

// DeleteSession revokes a session by its raw token (logout).
func (s *Service) DeleteSession(ctx context.Context, raw string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE token_hash = ?`, hashToken(raw))
	return err
}

// CreateAPIKey creates a named API key for a user. The raw key is returned once
// (prefixed "arr_"); only its hash is stored.
func (s *Service) CreateAPIKey(ctx context.Context, userID int64, name string) (string, error) {
	raw, err := randToken(32)
	if err != nil {
		return "", err
	}
	key := "arr_" + raw
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO api_keys (user_id, name, key_hash) VALUES (?, ?, ?)`,
		userID, strings.TrimSpace(name), hashToken(key))
	if err != nil {
		return "", err
	}
	return key, nil
}

// ValidateAPIKey returns the user owning the given API key and stamps last-used.
func (s *Service) ValidateAPIKey(ctx context.Context, key string) (*User, error) {
	var (
		u           User
		disabled    int
		autoApprove int
	)
	h := hashToken(key)
	err := s.db.QueryRowContext(ctx, `
		SELECT u.id, u.username, u.role, u.disabled, u.auto_approve
		FROM api_keys k JOIN users u ON u.id = k.user_id
		WHERE k.key_hash = ?`, h).Scan(&u.ID, &u.Username, &u.Role, &disabled, &autoApprove)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && disabled != 0) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	u.AutoApprove = autoApprove != 0
	_, _ = s.db.ExecContext(ctx, `UPDATE api_keys SET last_used_at = CURRENT_TIMESTAMP WHERE key_hash = ?`, h)
	return &u, nil
}

// --- helpers ---

func randToken(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func hashToken(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

// sqlTime formats a time as UTC text so it compares correctly against SQLite's
// CURRENT_TIMESTAMP (also UTC text in the same layout).
func sqlTime(t time.Time) string {
	return t.UTC().Format("2006-01-02 15:04:05")
}

func isUniqueViolation(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}
