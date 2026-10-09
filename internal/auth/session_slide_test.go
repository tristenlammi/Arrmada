package auth

import (
	"context"
	"testing"
	"time"
)

// Sessions slide on a fake clock: extending moves the expiry a full TTL from "now", and an
// unextended session still expires on time.
func TestSessionSlidesWithFakeClock(t *testing.T) {
	ctx := context.Background()
	s := newService(t)
	day0 := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	clock := day0
	s.now = func() time.Time { return clock }
	u, _ := s.CreateUser(ctx, "kid@example.com", "supersecret", RoleRequester, false)

	used, _, _ := s.CreateSession(ctx, u.ID)
	idle, _, _ := s.CreateSession(ctx, u.ID)

	// Day 5: valid, expiry still day 30 (the caller decides not to extend this early).
	clock = day0.Add(5 * 24 * time.Hour)
	_, exp, err := s.ValidateSessionInfo(ctx, used)
	if err != nil {
		t.Fatal(err)
	}
	if want := day0.Add(30 * 24 * time.Hour); !exp.Equal(want) {
		t.Errorf("day 5 expiry = %v, want %v", exp, want)
	}

	// Day 20: extending moves it to day 50.
	clock = day0.Add(20 * 24 * time.Hour)
	newExp, err := s.ExtendSession(ctx, used)
	if err != nil {
		t.Fatal(err)
	}
	if want := day0.Add(50 * 24 * time.Hour); !newExp.Equal(want) {
		t.Errorf("extended expiry = %v, want %v", newExp, want)
	}
	if _, exp, _ := s.ValidateSessionInfo(ctx, used); !exp.Equal(newExp) {
		t.Errorf("stored expiry = %v, want %v", exp, newExp)
	}

	// Day 31: the idle session has expired; the extended one hasn't.
	clock = day0.Add(31 * 24 * time.Hour)
	if _, err := s.ValidateSession(ctx, idle); err != ErrNotFound {
		t.Errorf("idle session on day 31: %v, want ErrNotFound", err)
	}
	if _, err := s.ValidateSession(ctx, used); err != nil {
		t.Errorf("extended session on day 31: %v", err)
	}
	// Day 51: it runs out too if nobody uses it.
	clock = day0.Add(51 * 24 * time.Hour)
	if _, err := s.ValidateSession(ctx, used); err != ErrNotFound {
		t.Errorf("extended session on day 51: %v, want ErrNotFound", err)
	}
	if _, err := s.ExtendSession(ctx, "nope"); err != ErrNotFound {
		t.Errorf("extending an unknown session: %v, want ErrNotFound", err)
	}
}

// A disabled user's session is still refused, expiry or not.
func TestDisabledSessionStillRejected(t *testing.T) {
	ctx := context.Background()
	s := newService(t)
	u, _ := s.CreateUser(ctx, "kid@example.com", "supersecret", RoleRequester, false)
	tok, _, _ := s.CreateSession(ctx, u.ID)
	if _, err := s.db.Exec(`UPDATE users SET disabled = 1 WHERE id = ?`, u.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.ValidateSessionInfo(ctx, tok); err != ErrNotFound {
		t.Errorf("disabled user's session: %v, want ErrNotFound", err)
	}
}
