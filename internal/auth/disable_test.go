package auth

import (
	"context"
	"testing"
)

// Disabling signs someone out of the web app and the audiobook apps at once, and deletes
// nothing: re-enabling gives back their listening places.
func TestDisableUserRevokesSessionsAndAudioTokens(t *testing.T) {
	ctx := context.Background()
	s := newService(t)
	u, err := s.CreateUser(ctx, "kid@example.com", "supersecret", RoleRequester, false)
	if err != nil {
		t.Fatal(err)
	}
	other, _ := s.CreateUser(ctx, "mum@example.com", "supersecret", RoleRequester, false)
	tok, _, _ := s.CreateSession(ctx, u.ID)
	otherTok, _, _ := s.CreateSession(ctx, other.ID)
	for _, row := range []struct {
		uid  int64
		hash string
	}{{u.ID, "h1"}, {other.ID, "h2"}} {
		if _, err := s.db.Exec(`INSERT INTO audio_tokens (user_id, hash, kind, family, created_at) VALUES (?, ?, 'access', ?, 1)`, row.uid, row.hash, row.hash); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.db.Exec(`INSERT INTO listen_progress (user_id, item_key, updated_at) VALUES (?, 'b1', 1)`, u.ID); err != nil {
		t.Fatal(err)
	}

	if err := s.SetDisabled(ctx, u.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ValidateSession(ctx, tok); err == nil {
		t.Error("a disabled user's session still validates")
	}
	var n int
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM sessions WHERE user_id = ?`, u.ID).Scan(&n)
	if n != 0 {
		t.Errorf("%d sessions left after disable", n)
	}
	revoked := func(hash string) bool {
		var r int
		_ = s.db.QueryRow(`SELECT revoked FROM audio_tokens WHERE hash = ?`, hash).Scan(&r)
		return r == 1
	}
	if !revoked("h1") {
		t.Error("the disabled user's audiobook token wasn't revoked")
	}
	if revoked("h2") {
		t.Error("someone else's audiobook token was revoked")
	}
	if _, err := s.ValidateSession(ctx, otherTok); err != nil {
		t.Errorf("someone else's session was dropped: %v", err)
	}
	if _, err := s.Authenticate(ctx, "kid@example.com", "supersecret"); err != ErrInvalidCredentials {
		t.Errorf("disabled user signed in: %v", err)
	}

	if err := s.SetDisabled(ctx, u.ID, false); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Authenticate(ctx, "kid@example.com", "supersecret"); err != nil {
		t.Errorf("re-enabled user can't sign in: %v", err)
	}
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM listen_progress WHERE user_id = ?`, u.ID).Scan(&n)
	if n != 1 {
		t.Error("listening places were lost across disable/enable")
	}
	if err := s.SetDisabled(ctx, 9999, true); err != ErrNotFound {
		t.Errorf("unknown user: %v, want ErrNotFound", err)
	}
}
