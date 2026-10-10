package auth

import (
	"context"
	"fmt"
	"net"
	"strings"
	"time"
)

// Where you're signed in: each browser session remembers what browser it is, roughly
// where it came from and when it was last used, so its owner can recognise it and end it.
// Only the owner ever sees this list; an admin can sign someone out everywhere but is
// never shown their devices or addresses.

// lastSeenEvery is how stale last_seen_at may get before a request writes it again: often
// enough to say "today" or "3 weeks ago", rarely enough not to write on every request.
const lastSeenEvery = 10 * time.Minute

// sessionIDLen is how much of a session's token hash names it in the API: enough to tell
// one person's few sessions apart, and useless for signing in (the hash isn't the token).
const sessionIDLen = 12

// SessionClient is the browser a session is made for.
type SessionClient struct {
	UserAgent string
	IP        string // the client's address; only its coarse form is stored
}

// SessionInfo is one signed-in browser, as its owner sees it.
type SessionInfo struct {
	ID         string    `json:"id"`
	CreatedAt  time.Time `json:"created_at"`
	LastSeenAt time.Time `json:"last_seen_at"`
	// Device is "Chrome on Windows", "Safari on iPhone", "Home Screen app on iPhone"…
	Device string `json:"device"`
	// Network is "local" (the home network), a coarse address ("203.0.113.x"), or "".
	Network string `json:"network"`
	Current bool   `json:"current"`
}

// CreateSessionFrom is CreateSession recording the browser it's for.
func (s *Service) CreateSessionFrom(ctx context.Context, userID int64, c SessionClient) (string, time.Time, error) {
	raw, err := randToken(32)
	if err != nil {
		return "", time.Time{}, err
	}
	now := s.now()
	expires := now.Add(s.sessionTTL)
	ua := c.UserAgent
	if len(ua) > 300 {
		ua = ua[:300]
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO sessions (token_hash, user_id, expires_at, last_seen_at, user_agent, network) VALUES (?, ?, ?, ?, ?, ?)`,
		hashToken(raw), userID, sqlTime(expires), sqlTime(now), ua, coarseNetwork(c.IP))
	if err != nil {
		return "", time.Time{}, err
	}
	return raw, expires, nil
}

// touchSession records that a session was just used, when its last_seen_at (seen; zero
// for never) is older than lastSeenEvery. Best effort: a failed write costs nothing but
// a slightly older "last seen".
func (s *Service) touchSession(ctx context.Context, raw string, seen time.Time) {
	now := s.now()
	if !seen.IsZero() && now.Sub(seen) < lastSeenEvery {
		return
	}
	_, _ = s.db.ExecContext(ctx, `UPDATE sessions SET last_seen_at = ? WHERE token_hash = ?`, sqlTime(now), hashToken(raw))
}

// ListSessions is userID's live sessions, most recently used first, marking currentRaw's.
func (s *Service) ListSessions(ctx context.Context, userID int64, currentRaw string) ([]SessionInfo, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT token_hash, strftime('%Y-%m-%d %H:%M:%S', created_at),
		       strftime('%Y-%m-%d %H:%M:%S', COALESCE(last_seen_at, created_at)), user_agent, network
		FROM sessions WHERE user_id = ? AND expires_at > ?
		ORDER BY COALESCE(last_seen_at, created_at) DESC, created_at DESC`, userID, sqlTime(s.now()))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	current := keepHash(currentRaw)
	out := []SessionInfo{}
	for rows.Next() {
		var hash, created, seen, ua string
		var si SessionInfo
		if err := rows.Scan(&hash, &created, &seen, &ua, &si.Network); err != nil {
			return nil, err
		}
		si.ID = hash[:min(sessionIDLen, len(hash))]
		si.CreatedAt = parseSQLTime(created)
		si.LastSeenAt = parseSQLTime(seen)
		si.Device = uaSummary(ua)
		si.Current = current != "" && hash == current
		out = append(out, si)
	}
	return out, rows.Err()
}

// RevokeSession ends one of userID's sessions by its listed id. Another user's session
// never matches, whatever id is sent. It reports whether one ended.
func (s *Service) RevokeSession(ctx context.Context, userID int64, id string) (bool, error) {
	if !validSessionID(id) {
		return false, nil
	}
	res, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ? AND substr(token_hash, 1, ?) = ?`, userID, sessionIDLen, id)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

func validSessionID(id string) bool {
	if len(id) != sessionIDLen {
		return false
	}
	for _, c := range id {
		if !('0' <= c && c <= '9' || 'a' <= c && c <= 'f') {
			return false
		}
	}
	return true
}

func parseSQLTime(s string) time.Time {
	t, err := time.ParseInLocation("2006-01-02 15:04:05", s, time.UTC)
	if err != nil {
		return time.Time{}
	}
	return t
}

// coarseNetwork is all a session keeps of its address: "local" for the home network (or
// this machine), the IPv4 address with its last part dropped, or an IPv6 /48. Enough to
// tell "that's me at work" from "that's not me", not to find anyone.
func coarseNetwork(ip string) string {
	addr := net.ParseIP(strings.TrimSpace(ip))
	switch {
	case addr == nil:
		return ""
	case addr.IsLoopback() || addr.IsPrivate() || addr.IsLinkLocalUnicast():
		return "local"
	}
	if v4 := addr.To4(); v4 != nil {
		return fmt.Sprintf("%d.%d.%d.x", v4[0], v4[1], v4[2])
	}
	return addr.Mask(net.CIDRMask(48, 128)).String() + "/48"
}

// uaSummary turns a User-Agent into "Chrome on Windows". The installed iPhone app sends
// Safari's agent without the "Safari/" part, so it reads as the Home Screen app.
func uaSummary(ua string) string {
	if strings.TrimSpace(ua) == "" {
		return "Unknown device"
	}
	var device string
	switch {
	case strings.Contains(ua, "iPhone"):
		device = "iPhone"
	case strings.Contains(ua, "iPad"):
		device = "iPad"
	case strings.Contains(ua, "Android"):
		device = "Android"
	case strings.Contains(ua, "CrOS"):
		device = "ChromeOS"
	case strings.Contains(ua, "Windows"):
		device = "Windows"
	case strings.Contains(ua, "Macintosh"), strings.Contains(ua, "Mac OS X"):
		device = "Mac"
	case strings.Contains(ua, "Linux"):
		device = "Linux"
	}
	ios := device == "iPhone" || device == "iPad"
	var browser string
	switch {
	case strings.Contains(ua, "Edg/"), strings.Contains(ua, "EdgA/"), strings.Contains(ua, "EdgiOS/"):
		browser = "Edge"
	case strings.Contains(ua, "OPR/"):
		browser = "Opera"
	case strings.Contains(ua, "SamsungBrowser/"):
		browser = "Samsung Internet"
	case strings.Contains(ua, "Firefox/"), strings.Contains(ua, "FxiOS/"):
		browser = "Firefox"
	case strings.Contains(ua, "Chrome/"), strings.Contains(ua, "CriOS/"):
		browser = "Chrome"
	case ios && !strings.Contains(ua, "Safari/"):
		browser = "Home Screen app"
	case strings.Contains(ua, "Safari/"):
		browser = "Safari"
	}
	switch {
	case browser != "" && device != "":
		return browser + " on " + device
	case browser != "":
		return browser
	case device != "":
		return device
	}
	return "Unknown device"
}
