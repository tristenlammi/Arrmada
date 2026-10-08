package auth

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// The delete dialog's counts must be right, and the payload must never carry anything
// that names a book (audiobook privacy: how much, never what).
func TestUserImpactCounts(t *testing.T) {
	ctx := context.Background()
	s := newService(t)
	u, err := s.CreateUser(ctx, "kid", "password123", RoleRequester, false)
	if err != nil {
		t.Fatal(err)
	}
	other, err := s.CreateUser(ctx, "parent", "password123", RoleAdmin, false)
	if err != nil {
		t.Fatal(err)
	}
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := s.db.ExecContext(ctx, q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	for _, uid := range []int64{u.ID, other.ID} {
		exec(`INSERT INTO listen_progress (user_id, item_key, updated_at) VALUES (?, 'book-a', 1), (?, 'book-b', 1)`, uid, uid)
	}
	exec(`INSERT INTO listen_progress (user_id, item_key, updated_at) VALUES (?, 'book-c', 1)`, u.ID)
	exec(`INSERT INTO listen_log (session_id, user_id, started_at, ended_at, seconds) VALUES ('s1', ?, 1, 2, 3600), ('s2', ?, 1, 2, 1800), ('s3', ?, 1, 2, 7200)`, u.ID, u.ID, other.ID)
	exec(`INSERT INTO listen_bookmarks (user_id, item_key, time, created_at) VALUES (?, 'book-a', 10, 1)`, u.ID)
	exec(`INSERT INTO audio_tokens (user_id, hash, kind, family, created_at, revoked) VALUES
		(?, 'h1', 'access', 'phone', 1, 0), (?, 'h2', 'refresh', 'phone', 1, 0),
		(?, 'h3', 'access', 'tablet', 1, 0), (?, 'h4', 'access', 'old', 1, 1)`, u.ID, u.ID, u.ID, u.ID)
	exec(`INSERT INTO requests (media_type, tmdb_id, title, requested_by) VALUES ('movie', 1, 'A', ?), ('series', 2, 'B', ?), ('movie', 3, 'C', ?)`, u.ID, u.ID, other.ID)
	exec(`INSERT INTO requests (media_type, ol_key, title, requested_by) VALUES ('book', 'OL1W', 'D', ?)`, u.ID)
	exec(`INSERT INTO push_subscriptions (user_id, endpoint, p256dh, auth) VALUES (?, 'https://push.example/1', 'k', 'a')`, u.ID)
	if _, _, err := s.CreateSession(ctx, u.ID); err != nil {
		t.Fatal(err)
	}

	imp, err := s.DeletionImpact(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	want := UserImpact{Places: 3, ListeningHours: 1.5, Bookmarks: 1, Devices: 2, Requests: 3, Sessions: 1, PushSubscriptions: 1}
	if imp != want {
		t.Fatalf("impact = %+v\nwant     %+v", imp, want)
	}
	if !imp.HasListeningData() {
		t.Error("a user with places and hours has listening data")
	}

	b, _ := json.Marshal(imp)
	for _, banned := range []string{"item_key", "title", "book-a", "book_id", "key"} {
		if strings.Contains(string(b), banned) {
			t.Errorf("impact JSON mentions %q: %s", banned, b)
		}
	}

	if _, err := s.DeletionImpact(ctx, 9999); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown user: err = %v, want ErrNotFound", err)
	}
}

// A brand-new account has nothing to lose, so no typed confirmation is needed.
func TestUserImpactEmptyAccount(t *testing.T) {
	ctx := context.Background()
	s := newService(t)
	u, err := s.CreateUser(ctx, "new", "password123", RoleRequester, false)
	if err != nil {
		t.Fatal(err)
	}
	imp, err := s.DeletionImpact(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if imp != (UserImpact{}) || imp.HasListeningData() {
		t.Fatalf("impact = %+v, want all zero", imp)
	}
}
