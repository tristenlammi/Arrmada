package auth

import (
	"context"
	"testing"
	"time"
)

const chromeWindows = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/129.0.0.0 Safari/537.36"

// The list is the user's own live sessions, this one marked, with the browser named and
// only a coarse network kept.
func TestListSessionsMarksCurrent(t *testing.T) {
	ctx := context.Background()
	s := newService(t)
	u, _ := s.CreateUser(ctx, "kid@example.com", "password1", RoleRequester, false)
	other, _ := s.CreateUser(ctx, "aunt@example.com", "password2", RoleRequester, false)
	here, _, _ := s.CreateSessionFrom(ctx, u.ID, SessionClient{UserAgent: chromeWindows, IP: "203.0.113.77"})
	_, _, _ = s.CreateSessionFrom(ctx, u.ID, SessionClient{UserAgent: "Mozilla/5.0 (iPhone; CPU iPhone OS 17_5 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Mobile/15E148", IP: "192.168.1.40"})
	_, _, _ = s.CreateSession(ctx, other.ID)

	list, err := s.ListSessions(ctx, u.ID, here)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("listed %d sessions, want 2 (never another user's)", len(list))
	}
	var current *SessionInfo
	for i := range list {
		if len(list[i].ID) != 12 {
			t.Errorf("id %q isn't 12 characters", list[i].ID)
		}
		if list[i].Current {
			current = &list[i]
		}
	}
	if current == nil || current.Device != "Chrome on Windows" || current.Network != "203.0.113.x" {
		t.Fatalf("current = %+v", current)
	}
	var stored string
	_ = s.db.QueryRow(`SELECT group_concat(network, ',') FROM sessions WHERE user_id = ?`, u.ID).Scan(&stored)
	if stored != "203.0.113.x,local" && stored != "local,203.0.113.x" {
		t.Errorf("stored networks = %q: a full address must never be kept", stored)
	}
}

// One person can't end another's session, whatever id they send; their own ends.
func TestRevokeSessionScopedToUser(t *testing.T) {
	ctx := context.Background()
	s := newService(t)
	u, _ := s.CreateUser(ctx, "kid@example.com", "password1", RoleRequester, false)
	other, _ := s.CreateUser(ctx, "aunt@example.com", "password2", RoleRequester, false)
	mine, _, _ := s.CreateSession(ctx, u.ID)
	theirs, _, _ := s.CreateSession(ctx, other.ID)
	theirID := hashToken(theirs)[:sessionIDLen]

	if ended, err := s.RevokeSession(ctx, u.ID, theirID); err != nil || ended {
		t.Fatalf("revoking another user's session: %v, %v", ended, err)
	}
	if _, err := s.ValidateSession(ctx, theirs); err != nil {
		t.Errorf("another user's session ended: %v", err)
	}
	for _, bad := range []string{"", "%", "zzzzzzzzzzzz", hashToken(mine)[:6], hashToken(mine)} {
		if ended, _ := s.RevokeSession(ctx, u.ID, bad); ended {
			t.Errorf("id %q ended a session", bad)
		}
	}
	if ended, err := s.RevokeSession(ctx, u.ID, hashToken(mine)[:sessionIDLen]); err != nil || !ended {
		t.Fatalf("revoking my own: %v, %v", ended, err)
	}
	if _, err := s.ValidateSession(ctx, mine); err == nil {
		t.Error("my revoked session still works")
	}
}

// "Last seen" moves at most every ten minutes, on a fake clock.
func TestLastSeenThrottled(t *testing.T) {
	ctx := context.Background()
	s := newService(t)
	t0 := time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)
	clock := t0
	s.now = func() time.Time { return clock }
	u, _ := s.CreateUser(ctx, "kid@example.com", "password1", RoleRequester, false)
	tok, _, _ := s.CreateSession(ctx, u.ID)
	seen := func() time.Time {
		t.Helper()
		list, err := s.ListSessions(ctx, u.ID, tok)
		if err != nil || len(list) != 1 {
			t.Fatalf("list: %v %v", list, err)
		}
		return list[0].LastSeenAt
	}
	if got := seen(); !got.Equal(t0) {
		t.Fatalf("last seen at sign-in = %v, want %v", got, t0)
	}
	clock = t0.Add(9 * time.Minute)
	if _, err := s.ValidateSession(ctx, tok); err != nil {
		t.Fatal(err)
	}
	if got := seen(); !got.Equal(t0) {
		t.Errorf("after 9 minutes last seen = %v, want unchanged %v", got, t0)
	}
	clock = t0.Add(11 * time.Minute)
	_, _ = s.ValidateSession(ctx, tok)
	if got := seen(); !got.Equal(clock) {
		t.Errorf("after 11 minutes last seen = %v, want %v", got, clock)
	}
}

// An admin's "Sign out everywhere" ends every one of that user's sessions and no one else's.
func TestRevokeUserSessionsCounts(t *testing.T) {
	ctx := context.Background()
	s := newService(t)
	u, _ := s.CreateUser(ctx, "kid@example.com", "password1", RoleRequester, false)
	other, _ := s.CreateUser(ctx, "aunt@example.com", "password2", RoleRequester, false)
	_, _, _ = s.CreateSession(ctx, u.ID)
	_, _, _ = s.CreateSession(ctx, u.ID)
	theirs, _, _ := s.CreateSession(ctx, other.ID)
	if n, err := s.RevokeUserSessions(ctx, u.ID); err != nil || n != 2 {
		t.Fatalf("revoke: %d, %v; want 2", n, err)
	}
	if _, err := s.ValidateSession(ctx, theirs); err != nil {
		t.Errorf("another user's session ended: %v", err)
	}
}

func TestUASummary(t *testing.T) {
	for ua, want := range map[string]string{
		chromeWindows: "Chrome on Windows",
		"Mozilla/5.0 (iPhone; CPU iPhone OS 17_5 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.5 Mobile/15E148 Safari/604.1": "Safari on iPhone",
		"Mozilla/5.0 (iPhone; CPU iPhone OS 17_5 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Mobile/15E148":                           "Home Screen app on iPhone",
		"Mozilla/5.0 (Linux; Android 14; Pixel 7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/129.0.0.0 Mobile Safari/537.36":                   "Chrome on Android",
		"Mozilla/5.0 (Macintosh; Intel Mac OS X 14_5) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.5 Safari/605.1.15":                      "Safari on Mac",
		"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/129.0.0.0 Safari/537.36 Edg/129.0.0.0":           "Edge on Windows",
		"Mozilla/5.0 (X11; Linux x86_64; rv:131.0) Gecko/20100101 Firefox/131.0":                                                                  "Firefox on Linux",
		"curl/8.5.0": "Unknown device",
		"":           "Unknown device",
	} {
		if got := uaSummary(ua); got != want {
			t.Errorf("uaSummary(%q) = %q, want %q", ua, got, want)
		}
	}
}

func TestCoarseNetwork(t *testing.T) {
	for ip, want := range map[string]string{
		"203.0.113.77":        "203.0.113.x",
		"192.168.1.40":        "local",
		"10.0.0.2":            "local",
		"127.0.0.1":           "local",
		"::1":                 "local",
		"fe80::1":             "local",
		"2001:db8:abcd:12::1": "2001:db8:abcd::/48",
		"::ffff:198.51.100.9": "198.51.100.x",
		"not an address":      "",
		"":                    "",
	} {
		if got := coarseNetwork(ip); got != want {
			t.Errorf("coarseNetwork(%q) = %q, want %q", ip, got, want)
		}
	}
}
