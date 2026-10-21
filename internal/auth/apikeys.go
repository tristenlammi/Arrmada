package auth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"database/sql"
	"encoding/base32"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/tristenlammi/arrmada/internal/store"
)

// Personal API keys: what someone makes on their Me page so a script or an integration
// (n8n, Home Assistant, curl) can call Arrmada as them. Not the outside services' keys
// (TMDB and the like, internal/apikeys) — those are Arrmada's keys for other services.
//
// A key is "arrk_" and 52 random base32 characters (256 bits). Only its SHA-256 hash is
// stored, plus its last four characters so the owner can tell keys apart; the key itself
// is shown once, when it's made. A hash of a 256-bit random value can't be reversed or
// guessed, so a slow password hash would add nothing but cost on every request.
//
// What a key may do: the account's role, capped twice. By the role the account had when
// the key was made (a later promotion never widens an old key; a demotion narrows it at
// once, because the current role is read on every request), and by the key's scope:
// "read" only reaches GET routes. Which routes a key reaches at all is the route table's
// business (httpapi/apikey_routes.go). A disabled account's keys stop working while it's
// disabled and work again when it's re-enabled (disabling is a pause, like the rest of
// SetDisabled); a deleted account's keys go with it (ON DELETE CASCADE). A password change
// — the owner's or an admin's reset — and an admin's "sign out everywhere" revoke them:
// those are what someone does after a suspected compromise.

// APIKeyPrefix starts every personal API key, so one is recognisable in a config file and
// a leaked one can be found by searching for it.
const APIKeyPrefix = "arrk_"

// apiKeyBodyLen is the length of the random part: 32 bytes of base32, unpadded.
const apiKeyBodyLen = 52

// MaxAPIKeysPerUser bounds how many unexpired keys one account holds.
const MaxAPIKeysPerUser = 25

// maxAPIKeyName is the longest name a key may have, in characters.
const maxAPIKeyName = 60

// keyUsedEvery is how stale a key's last use may get before a request writes it again, as
// for sessions: "5 minutes ago" accuracy without a write on every call.
const keyUsedEvery = lastSeenEvery

// keyMovedEvery is the sooner write when a key turns up on another network, so "last used
// from" catches a move quickly — but a script bouncing between two networks still costs at
// most one write a minute.
const keyMovedEvery = time.Minute

// KeyMintWindow is how fresh the session must be to make a key on an account with no
// password to ask for (Plex or Jellyfin sign-in only): a key outlives the session, so a
// browser left signed in for days isn't enough on its own.
const KeyMintWindow = 15 * time.Minute

var keyEncoding = base32.NewEncoding("abcdefghijklmnopqrstuvwxyz234567").WithPadding(base32.NoPadding)

// KeyScope is what a key may do within its role.
type KeyScope string

const (
	// KeyScopeRead reaches GET routes only.
	KeyScopeRead KeyScope = "read"
	// KeyScopeFull reaches everything the role allows on the routes keys may use.
	KeyScopeFull KeyScope = "full"
)

// APIKeyExpiryDays are the lifetimes a key can be made with; 0 is "never".
var APIKeyExpiryDays = []int{0, 30, 90, 365}

// Errors CreateAPIKey's callers turn into a 400 (ErrNotFound: no such account).
var (
	ErrAPIKeyName    = errors.New("give the key a name of up to 60 characters")
	ErrAPIKeyScope   = errors.New("scope must be read or full")
	ErrAPIKeyExpiry  = errors.New("expiry must be never, 30, 90 or 365 days")
	ErrTooManyKeys   = errors.New("you have the most keys one account may hold; revoke one you no longer use first")
	ErrAPIKeyAccount = errors.New("this account can't make keys while it's disabled")
)

// APIKey is one key as its owner (or, without the network, an admin) sees it. Never the
// key or its hash.
type APIKey struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	// Hint is the prefix and the last four characters: "arrk_…7f3q".
	Hint  string   `json:"hint"`
	Scope KeyScope `json:"scope"`
	// Role is the role the key was made under: it never acts above it.
	Role       Role       `json:"role"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at"`
	// LastUsedNetwork is "local", a coarse address ("203.0.113.x") or "". Only the key's
	// owner is shown it, as with sessions.
	LastUsedNetwork string     `json:"last_used_network,omitempty"`
	ExpiresAt       *time.Time `json:"expires_at"`
	Expired         bool       `json:"expired"`
	// UserID and Username: the admin's list only.
	UserID   int64  `json:"user_id,omitempty"`
	Username string `json:"username,omitempty"`
}

// NewAPIKey is what a key is made with. ExpiresInDays is one of APIKeyExpiryDays.
type NewAPIKey struct {
	Name          string
	Scope         KeyScope
	ExpiresInDays int
}

// KeyUse is a validated key: who it acts as (Role already capped) and what it may do.
type KeyUse struct {
	User  *User
	KeyID int64
	Scope KeyScope
}

// LooksLikeAPIKey reports whether raw has a personal API key's shape. Anything else in an
// X-Api-Key or Authorization header (an audiobook app's token, another service's key) is
// none of this feature's business and is left alone.
func LooksLikeAPIKey(raw string) bool {
	body, ok := strings.CutPrefix(raw, APIKeyPrefix)
	if !ok || len(body) != apiKeyBodyLen {
		return false
	}
	for i := 0; i < len(body); i++ {
		c := body[i]
		if !('a' <= c && c <= 'z' || '2' <= c && c <= '7') {
			return false
		}
	}
	return true
}

// Validate checks what a key would be made with (ErrAPIKeyName, ErrAPIKeyScope or
// ErrAPIKeyExpiry), so a caller can refuse a bad form before asking for a password or a
// two-factor code.
func (n NewAPIKey) Validate() error {
	name := strings.TrimSpace(n.Name)
	if name == "" || utf8.RuneCountInString(name) > maxAPIKeyName || strings.ContainsAny(name, "\r\n\t") {
		return ErrAPIKeyName
	}
	if n.Scope != KeyScopeRead && n.Scope != KeyScopeFull {
		return ErrAPIKeyScope
	}
	for _, d := range APIKeyExpiryDays {
		if d == n.ExpiresInDays {
			return nil
		}
	}
	return ErrAPIKeyExpiry
}

// lowerRole is the less privileged of two roles; an unknown one counts as read-only.
func lowerRole(a, b Role) Role {
	if !ValidRole(a) || !ValidRole(b) {
		return RoleReadonly
	}
	if roleRank[a] <= roleRank[b] {
		return a
	}
	return b
}

// CreateAPIKey makes a key for userID and returns it raw, once, with its listing. The key
// takes the account's current role as its ceiling.
func (s *Service) CreateAPIKey(ctx context.Context, userID int64, n NewAPIKey) (string, APIKey, error) {
	if err := n.Validate(); err != nil {
		return "", APIKey{}, err
	}
	name := strings.TrimSpace(n.Name)
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", APIKey{}, err
	}
	raw := APIKeyPrefix + keyEncoding.EncodeToString(b)
	now := s.now().UTC().Truncate(time.Second)
	var expires *time.Time
	if n.ExpiresInDays > 0 {
		e := now.Add(time.Duration(n.ExpiresInDays) * 24 * time.Hour)
		expires = &e
	}
	key := APIKey{Name: name, Hint: keyHint(raw[len(raw)-4:]), Scope: n.Scope, CreatedAt: now, ExpiresAt: expires}
	err := store.WithTx(ctx, s.db, func(tx *store.Transaction) error {
		var role Role
		var disabled int
		err := tx.QueryRowContext(ctx, `SELECT role, disabled FROM users WHERE id = ?`, userID).Scan(&role, &disabled)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if disabled != 0 {
			return ErrAPIKeyAccount
		}
		if !ValidRole(role) {
			role = RoleReadonly
		}
		key.Role = role
		var held int
		if err := tx.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM api_keys WHERE user_id = ? AND (expires_at IS NULL OR expires_at > ?)`,
			userID, sqlTime(now)).Scan(&held); err != nil {
			return err
		}
		if held >= MaxAPIKeysPerUser {
			return ErrTooManyKeys
		}
		var exp any
		if expires != nil {
			exp = sqlTime(*expires)
		}
		key.ID, err = store.InsertID(ctx, tx,
			`INSERT INTO api_keys (user_id, name, key_hash, created_at, scope, role, last4, expires_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			userID, name, hashToken(raw), sqlTime(now), string(n.Scope), string(key.Role), raw[len(raw)-4:], exp)
		return err
	})
	if err != nil {
		return "", APIKey{}, err
	}
	return raw, key, nil
}

func keyHint(last4 string) string { return APIKeyPrefix + "…" + last4 }

// ValidateAPIKey checks a raw key and says who it acts as, stamping its last use (time
// and coarse network of ip) at most every keyUsedEvery. Any failure — wrong, revoked,
// expired, a disabled account — is ErrNotFound, so the answer says nothing about which.
func (s *Service) ValidateAPIKey(ctx context.Context, raw, ip string) (*KeyUse, error) {
	if !LooksLikeAPIKey(raw) {
		return nil, ErrNotFound
	}
	h := hashToken(raw)
	now := s.now()
	var (
		u                 User
		keyID             int64
		stored            string
		scope, keyRole    string
		disabled          int
		am, as, ab        int
		lastUsed, network sql.NullString
	)
	// The lookup is by hash, so the database's comparison never sees the key itself; the
	// hash is compared again in constant time below as a second, timing-safe check.
	err := s.db.QueryRowContext(ctx, `
		SELECT k.id, k.key_hash, k.scope, k.role, `+store.TimestampText(s.db, "k.last_used_at")+`, k.last_used_network,
		       u.id, u.username, u.role, u.disabled, u.auto_approve_movie, u.auto_approve_series, u.auto_approve_book
		FROM api_keys k JOIN users u ON u.id = k.user_id
		WHERE k.key_hash = ? AND (k.expires_at IS NULL OR k.expires_at > ?)`,
		h, sqlTime(now)).Scan(&keyID, &stored, &scope, &keyRole, &lastUsed, &network,
		&u.ID, &u.Username, &u.Role, &disabled, &am, &as, &ab)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if subtle.ConstantTimeCompare([]byte(stored), []byte(h)) != 1 || disabled != 0 {
		return nil, ErrNotFound
	}
	sc := KeyScope(scope)
	if sc != KeyScopeRead && sc != KeyScopeFull {
		return nil, ErrNotFound // fail closed on anything the code doesn't know
	}
	u.setAutoApproval(am, as, ab)
	u.Role = lowerRole(u.Role, Role(keyRole))
	nw := coarseNetwork(ip)
	if seen := parseSQLTime(lastUsed.String); seen.IsZero() || now.Sub(seen) >= keyUsedEvery ||
		(nw != network.String && now.Sub(seen) >= keyMovedEvery) {
		_, _ = s.db.ExecContext(ctx, `UPDATE api_keys SET last_used_at = ?, last_used_network = ? WHERE id = ?`,
			sqlTime(now), nw, keyID)
	}
	return &KeyUse{User: &u, KeyID: keyID, Scope: sc}, nil
}

// apiKeyCols are what scanAPIKey reads, in its order.
func (s *Service) apiKeyCols() string {
	return `k.id, k.name, k.last4, k.scope, k.role, ` + store.TimestampText(s.db, "k.created_at") + `,
	COALESCE(` + store.TimestampText(s.db, "k.last_used_at") + `, ''), k.last_used_network,
	COALESCE(` + store.TimestampText(s.db, "k.expires_at") + `, ''), k.user_id, u.username`
}

func (s *Service) scanAPIKey(r rowScanner) (APIKey, error) {
	var k APIKey
	var last4, created, used, expires string
	if err := r.Scan(&k.ID, &k.Name, &last4, &k.Scope, &k.Role, &created, &used, &k.LastUsedNetwork, &expires, &k.UserID, &k.Username); err != nil {
		return k, err
	}
	k.Hint = keyHint(last4)
	k.CreatedAt = parseSQLTime(created)
	if t := parseSQLTime(used); !t.IsZero() {
		k.LastUsedAt = &t
	}
	if t := parseSQLTime(expires); !t.IsZero() {
		k.ExpiresAt = &t
		k.Expired = !s.now().Before(t)
	}
	return k, nil
}

func (s *Service) listAPIKeys(ctx context.Context, where string, args ...any) ([]APIKey, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+s.apiKeyCols()+` FROM api_keys k JOIN users u ON u.id = k.user_id `+where+
		` ORDER BY k.created_at DESC, k.id DESC`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []APIKey{}
	for rows.Next() {
		k, err := s.scanAPIKey(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

// ListAPIKeys is userID's own keys, newest first, expired ones included (marked) so they
// can be cleared away.
func (s *Service) ListAPIKeys(ctx context.Context, userID int64) ([]APIKey, error) {
	keys, err := s.listAPIKeys(ctx, `WHERE k.user_id = ?`, userID)
	for i := range keys {
		keys[i].UserID, keys[i].Username = 0, ""
	}
	return keys, err
}

// ListAllAPIKeys is everyone's keys for the admin: whose, what and when, never where
// from (the network is the owner's, like their devices).
func (s *Service) ListAllAPIKeys(ctx context.Context) ([]APIKey, error) {
	keys, err := s.listAPIKeys(ctx, ``)
	for i := range keys {
		keys[i].LastUsedNetwork = ""
	}
	return keys, err
}

// RevokeAPIKey deletes one of userID's keys. Another account's key never matches, whatever
// id is sent. It reports whether a key went. The next request with it fails.
func (s *Service) RevokeAPIKey(ctx context.Context, userID, id int64) (bool, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM api_keys WHERE id = ? AND user_id = ?`, id, userID)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// APIKeyCount is how many keys userID holds, expired ones included: what a password
// change or "sign out everywhere" is about to revoke.
func (s *Service) APIKeyCount(ctx context.Context, userID int64) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM api_keys WHERE user_id = ?`, userID).Scan(&n)
	return n, err
}

// SessionSignedInRecently reports whether the session raw (of userID) was made within
// KeyMintWindow — signed in, not merely used. An unknown, expired or someone else's
// session is never recent.
func (s *Service) SessionSignedInRecently(ctx context.Context, userID int64, raw string) (bool, error) {
	if raw == "" {
		return false, nil
	}
	var created string
	err := s.db.QueryRowContext(ctx,
		`SELECT `+store.TimestampText(s.db, "created_at")+` FROM sessions WHERE token_hash = ? AND user_id = ? AND expires_at > ?`,
		hashToken(raw), userID, sqlTime(s.now())).Scan(&created)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	t := parseSQLTime(created)
	return !t.IsZero() && s.now().Sub(t) < KeyMintWindow, nil
}

// RevokeAnyAPIKey is the admin's revoke: any account's key by id. It returns whose it was
// (0 when there was no such key).
func (s *Service) RevokeAnyAPIKey(ctx context.Context, id int64) (int64, error) {
	var owner int64
	err := store.WithTx(ctx, s.db, func(tx *store.Transaction) error {
		err := tx.QueryRowContext(ctx, `SELECT user_id FROM api_keys WHERE id = ?`, id).Scan(&owner)
		if errors.Is(err, sql.ErrNoRows) {
			owner = 0
			return nil
		}
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `DELETE FROM api_keys WHERE id = ?`, id)
		return err
	})
	return owner, err
}
