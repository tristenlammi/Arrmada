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
	"log/slog"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/tristenlammi/arrmada/internal/store"
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
	ID       int64  `json:"id"`
	Username string `json:"username"`
	Role     Role   `json:"role"`
	Disabled bool   `json:"disabled"`
	// AutoApprove is true when every media type auto-approves (older clients read it);
	// the per-type flags below are what requests follow (AutoApproves).
	AutoApprove       bool   `json:"auto_approve"`
	AutoApproveMovie  bool   `json:"auto_approve_movie"`
	AutoApproveSeries bool   `json:"auto_approve_series"`
	AutoApproveBook   bool   `json:"auto_approve_book"`
	CreatedAt         string `json:"created_at,omitempty"`
	// PlexLinked: signs in with Plex, so the admin can block that Plex account.
	PlexLinked bool `json:"plex_linked"`
}

// Service provides authentication operations backed by the database.
type Service struct {
	db         *sql.DB
	sessionTTL time.Duration
	log        *slog.Logger
	now        func() time.Time // time.Now; a fake clock in tests
}

// NewService builds an auth service over the given database pool.
func NewService(db *sql.DB) *Service {
	return &Service{db: db, sessionTTL: 30 * 24 * time.Hour, log: slog.New(slog.DiscardHandler), now: time.Now}
}

// SessionTTL is how long a session lasts from its last extension.
func (s *Service) SessionTTL() time.Duration { return s.sessionTTL }

// SetLogger sets where the service reports things the owner should fix (e.g. two accounts
// that differ only by case). Without one those warnings are dropped.
func (s *Service) SetLogger(l *slog.Logger) {
	if l != nil {
		s.log = l
	}
}

// normalizeUsername trims a new account's name and lowercases it when it's an email, so
// 'Mum@Gmail.com' and 'mum@gmail.com' are one account. Other names (the setup admin's
// chosen name) keep their case; lookups ignore case anyway. Only ASCII is folded, to
// match SQLite's lower(), which the case-insensitive lookups use.
func normalizeUsername(name string) string {
	name = strings.TrimSpace(name)
	if !strings.Contains(name, "@") {
		return name
	}
	b := []byte(name)
	for i, c := range b {
		if 'A' <= c && c <= 'Z' {
			b[i] = c + ('a' - 'A')
		}
	}
	return string(b)
}

// UserCount returns the number of user accounts (0 means first-run setup needed).
func (s *Service) UserCount(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&n)
	return n, err
}

// CreateUser creates a user with a bcrypt-hashed password. Emails are stored lowercased,
// and a name that matches an existing one ignoring case is taken.
func (s *Service) CreateUser(ctx context.Context, username, password string, role Role, autoApprove bool) (*User, error) {
	username = normalizeUsername(username)
	if username == "" {
		return nil, ErrUsernameRequired
	}
	if len(password) < 8 {
		return nil, ErrWeakPassword
	}
	if !ValidRole(role) {
		role = RoleAdmin
	}
	var taken int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM users WHERE lower(username) = lower(?)`, username).Scan(&taken); err != nil {
		return nil, err
	}
	if taken > 0 {
		return nil, ErrUserExists
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return nil, err
	}

	aa := boolToInt(autoApprove)
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO users (username, password_hash, role, auto_approve, `+autoApproveCols+`) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		username, string(hash), string(role), aa, aa, aa, aa)
	if err != nil {
		if isUniqueViolation(err) {
			return nil, ErrUserExists
		}
		return nil, err
	}
	id, _ := res.LastInsertId()
	u := &User{ID: id, Username: username, Role: role}
	u.setAutoApproval(aa, aa, aa)
	return u, nil
}

// FindOrCreatePlexUser returns the user linked to a Plex account, creating a passwordless one
// (its username derived from the Plex name, de-duplicated) if none exists yet. An existing link
// keeps its current role/auto-approve — only new users get the provided defaults. A disabled
// linked user is returned as-is so the caller can refuse the sign-in.
func (s *Service) FindOrCreatePlexUser(ctx context.Context, plexID, plexUsername string, role Role, autoApprove AutoApproval) (*User, error) {
	if strings.TrimSpace(plexID) == "" {
		return nil, errors.New("missing plex id")
	}
	var u User
	var disabled, am, as, ab int
	err := s.db.QueryRowContext(ctx,
		`SELECT id, username, role, disabled, `+autoApproveCols+` FROM users WHERE plex_id = ?`, plexID).
		Scan(&u.ID, &u.Username, &u.Role, &disabled, &am, &as, &ab)
	if err == nil {
		u.Disabled = disabled == 1
		u.setAutoApproval(am, as, ab)
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
	am, as, ab = boolToInt(autoApprove.Movie), boolToInt(autoApprove.Series), boolToInt(autoApprove.Book)
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO users (username, password_hash, role, auto_approve, `+autoApproveCols+`, plex_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		username, string(hash), string(role), boolToInt(autoApprove.All()), am, as, ab, plexID)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	nu := &User{ID: id, Username: username, Role: role}
	nu.setAutoApproval(am, as, ab)
	return nu, nil
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
		if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE lower(username) = lower(?)`, name).Scan(&n); err == nil && n == 0 {
			return name
		}
		name = base + "-" + strconv.Itoa(i)
	}
	return base + "-" + strconv.FormatInt(time.Now().UnixNano(), 10)
}

// UpdateUser changes a user's role and auto-approve flag.
// PlexIDForUser returns the Plex account id linked to an Arrmada user (set when they
// sign in with Plex), or "" if none. Used to tie a signed-in user to their Plex watch
// history, which is keyed by the same numeric account id.
func (s *Service) PlexIDForUser(ctx context.Context, userID int64) string {
	var plexID sql.NullString
	if err := s.db.QueryRowContext(ctx, `SELECT plex_id FROM users WHERE id = ?`, userID).Scan(&plexID); err != nil {
		return ""
	}
	return strings.TrimSpace(plexID.String)
}

func (s *Service) UpdateUser(ctx context.Context, id int64, role Role, autoApprove AutoApproval) error {
	if !ValidRole(role) {
		return errors.New("invalid role")
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE users SET role = ?, auto_approve = ?, auto_approve_movie = ?, auto_approve_series = ?, auto_approve_book = ? WHERE id = ?`,
		string(role), boolToInt(autoApprove.All()), boolToInt(autoApprove.Movie), boolToInt(autoApprove.Series), boolToInt(autoApprove.Book), id)
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
		`SELECT id, username, role, disabled, `+autoApproveCols+`, created_at, COALESCE(plex_id, '') != '' FROM users ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []User
	for rows.Next() {
		var u User
		var disabled, am, as, ab int
		if err := rows.Scan(&u.ID, &u.Username, &u.Role, &disabled, &am, &as, &ab, &u.CreatedAt, &u.PlexLinked); err != nil {
			return nil, err
		}
		u.Disabled = disabled != 0
		u.setAutoApproval(am, as, ab)
		out = append(out, u)
	}
	return out, rows.Err()
}

// StaffIDs returns the enabled managers and admins, oldest first: who hears that a new
// request is waiting.
func (s *Service) StaffIDs(ctx context.Context) ([]int64, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id FROM users WHERE disabled = 0 AND role IN (?, ?) ORDER BY id`, string(RoleManager), string(RoleAdmin))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// UserByID loads one user.
func (s *Service) UserByID(ctx context.Context, id int64) (*User, error) {
	return s.userWhere(ctx, `id = ?`, id)
}

// UserByUsername loads one user by username, exact match first, then ignoring case. Two
// accounts that differ only by case match neither way round (as in Authenticate), so a
// sign-in can never land in the wrong one.
func (s *Service) UserByUsername(ctx context.Context, username string) (*User, error) {
	username = strings.TrimSpace(username)
	if u, err := s.userWhere(ctx, `username = ?`, username); err == nil {
		return u, nil
	}
	ids, err := s.caseInsensitiveIDs(ctx, username)
	if err != nil {
		return nil, err
	}
	if len(ids) != 1 {
		if len(ids) > 1 {
			s.warnAmbiguous(ids)
		}
		return nil, ErrInvalidCredentials
	}
	return s.userWhere(ctx, `id = ?`, ids[0])
}

// caseInsensitiveIDs returns every account whose name matches ignoring case.
func (s *Service) caseInsensitiveIDs(ctx context.Context, username string) ([]int64, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM users WHERE lower(username) = lower(?) ORDER BY id`, username)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (s *Service) warnAmbiguous(ids []int64) {
	s.log.Warn("auth: sign-in refused — the name matches more than one account ignoring case; merge or delete one in Settings → Users",
		"user_ids", ids)
}

// ReportCaseDuplicates logs, once, any accounts whose names differ only by case. Those
// were possible before names were compared ignoring case. Nothing is merged or deleted:
// both keep signing in with their exact spelling, and the owner decides which to keep.
func (s *Service) ReportCaseDuplicates(ctx context.Context) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT group_concat(id, ','), group_concat(username, ', ') FROM users
		GROUP BY lower(username) HAVING COUNT(*) > 1`)
	if err != nil {
		s.log.Warn("auth: could not check for duplicate account names", "err", err)
		return
	}
	defer rows.Close()
	for rows.Next() {
		var ids, names string
		if rows.Scan(&ids, &names) == nil {
			s.log.Warn("auth: these accounts have the same name ignoring case; each still signs in with its exact spelling — merge or delete one in Settings → Users",
				"user_ids", ids, "usernames", names)
		}
	}
}

func (s *Service) userWhere(ctx context.Context, where string, arg any) (*User, error) {
	var u User
	var disabled, am, as, ab int
	err := s.db.QueryRowContext(ctx,
		`SELECT id, username, role, disabled, `+autoApproveCols+`, created_at, COALESCE(plex_id, '') != '' FROM users WHERE `+where+` LIMIT 1`, arg).
		Scan(&u.ID, &u.Username, &u.Role, &disabled, &am, &as, &ab, &u.CreatedAt, &u.PlexLinked)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrInvalidCredentials
	}
	if err != nil {
		return nil, err
	}
	u.Disabled = disabled != 0
	u.setAutoApproval(am, as, ab)
	return &u, nil
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
	// Invalidate every existing session for this user: a password change (often a
	// response to a suspected compromise) must log out any hijacked session, not
	// leave 30-day tokens valid.
	_, _ = s.db.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ?`, id)
	return nil
}

// SetDisabled turns an account's sign-in off or back on. Disabling also signs them out
// everywhere: web sessions are dropped and audiobook-app tokens revoked, in one
// transaction. Nothing else is touched — requests, listening places and history stay,
// so re-enabling gives back exactly what they had (the apps just sign in again).
func (s *Service) SetDisabled(ctx context.Context, id int64, disabled bool) error {
	return store.WithTx(ctx, s.db, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE users SET disabled = ? WHERE id = ?`, boolToInt(disabled), id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		if disabled {
			if _, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ?`, id); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `UPDATE audio_tokens SET revoked = 1 WHERE user_id = ?`, id); err != nil {
				return err
			}
		}
		return nil
	})
}

// RevokeUserSessions logs a user out of every device (also used on disable).
func (s *Service) RevokeUserSessions(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ?`, id)
	return err
}

// Authenticate verifies a username/password and returns the user on success. The exact
// name is tried first, so a legacy mixed-case account still signs in as typed; then the
// name ignoring case, but only when exactly one account matches. Two accounts differing
// only by case fail closed rather than guess which one was meant.
func (s *Service) Authenticate(ctx context.Context, username, password string) (*User, error) {
	var (
		u          User
		hash       string
		disabled   int
		am, as, ab int
	)
	username = strings.TrimSpace(username)
	const cols = `SELECT id, username, password_hash, role, disabled, ` + autoApproveCols + ` FROM users WHERE `
	err := s.db.QueryRowContext(ctx, cols+`username = ?`, username).
		Scan(&u.ID, &u.Username, &hash, &u.Role, &disabled, &am, &as, &ab)
	if errors.Is(err, sql.ErrNoRows) {
		var ids []int64
		if ids, err = s.caseInsensitiveIDs(ctx, username); err != nil {
			return nil, err
		}
		switch {
		case len(ids) == 1:
			err = s.db.QueryRowContext(ctx, cols+`id = ?`, ids[0]).
				Scan(&u.ID, &u.Username, &hash, &u.Role, &disabled, &am, &as, &ab)
		case len(ids) > 1:
			s.warnAmbiguous(ids)
			_ = bcrypt.CompareHashAndPassword(dummyHash, []byte(password))
			return nil, ErrInvalidCredentials
		default:
			err = sql.ErrNoRows
		}
	}
	u.setAutoApproval(am, as, ab)
	if errors.Is(err, sql.ErrNoRows) {
		// Spend a REAL bcrypt cycle so response time doesn't leak whether the
		// username exists. The previous placeholder was too short to parse, so
		// CompareHashAndPassword returned instantly — no cycle spent — and the
		// timing gap cleanly enumerated valid accounts.
		_ = bcrypt.CompareHashAndPassword(dummyHash, []byte(password))
		return nil, ErrInvalidCredentials
	}
	if err != nil {
		return nil, err
	}
	// Always run the compare (even for a disabled user) so a disabled account
	// isn't distinguishable by timing either, THEN reject.
	pwOK := bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
	if disabled != 0 || !pwOK {
		return nil, ErrInvalidCredentials
	}
	return &u, nil
}

// dummyHash is a valid 60-char bcrypt hash of a random string, computed once, so
// the no-such-user path spends a genuine bcrypt cycle (see Authenticate). No
// password ever verifies against it.
var dummyHash = func() []byte {
	h, _ := bcrypt.GenerateFromPassword([]byte("arrmada-timing-guard"), bcrypt.DefaultCost)
	return h
}()

// CreateSession issues a new session token (returned raw once) and stores only
// its hash. Returns the raw token and its expiry.
func (s *Service) CreateSession(ctx context.Context, userID int64) (string, time.Time, error) {
	raw, err := randToken(32)
	if err != nil {
		return "", time.Time{}, err
	}
	expires := s.now().Add(s.sessionTTL)
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
	u, _, err := s.ValidateSessionInfo(ctx, raw)
	return u, err
}

// ValidateSessionInfo is ValidateSession plus the session's current expiry, so the caller
// can decide whether to extend it.
func (s *Service) ValidateSessionInfo(ctx context.Context, raw string) (*User, time.Time, error) {
	var (
		u          User
		disabled   int
		am, as, ab int
		expires    sql.NullString
	)
	// strftime hands the expiry back as plain text whatever the driver makes of a
	// TIMESTAMP column, in the same layout sqlTime writes.
	err := s.db.QueryRowContext(ctx, `
		SELECT u.id, u.username, u.role, u.disabled, u.auto_approve_movie, u.auto_approve_series, u.auto_approve_book, strftime('%Y-%m-%d %H:%M:%S', s.expires_at)
		FROM sessions s JOIN users u ON u.id = s.user_id
		WHERE s.token_hash = ? AND s.expires_at > ?`,
		hashToken(raw), sqlTime(s.now())).Scan(&u.ID, &u.Username, &u.Role, &disabled, &am, &as, &ab, &expires)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && disabled != 0) {
		return nil, time.Time{}, ErrNotFound
	}
	if err != nil {
		return nil, time.Time{}, err
	}
	u.setAutoApproval(am, as, ab)
	exp, err := time.ParseInLocation("2006-01-02 15:04:05", expires.String, time.UTC)
	if err != nil {
		// Still a valid session (the database said it hasn't expired); the zero time just
		// tells the caller there's nothing to go on.
		exp = time.Time{}
	}
	return &u, exp, nil
}

// ExtendSession pushes a session's expiry to a full TTL from now and returns the new
// expiry. Sessions slide: one used at least every few weeks never runs out mid-use.
func (s *Service) ExtendSession(ctx context.Context, raw string) (time.Time, error) {
	expires := s.now().Add(s.sessionTTL)
	res, err := s.db.ExecContext(ctx, `UPDATE sessions SET expires_at = ? WHERE token_hash = ?`,
		sqlTime(expires), hashToken(raw))
	if err != nil {
		return time.Time{}, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return time.Time{}, ErrNotFound
	}
	return expires, nil
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
		u          User
		disabled   int
		am, as, ab int
	)
	h := hashToken(key)
	err := s.db.QueryRowContext(ctx, `
		SELECT u.id, u.username, u.role, u.disabled, u.auto_approve_movie, u.auto_approve_series, u.auto_approve_book
		FROM api_keys k JOIN users u ON u.id = k.user_id
		WHERE k.key_hash = ?`, h).Scan(&u.ID, &u.Username, &u.Role, &disabled, &am, &as, &ab)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && disabled != 0) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	u.setAutoApproval(am, as, ab)
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
