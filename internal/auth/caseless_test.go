package auth

import (
	"context"
	"testing"

	"golang.org/x/crypto/bcrypt"
)

// An email entered with capitals is stored lowercased and signs in however it's typed.
func TestAuthenticateCaseInsensitive(t *testing.T) {
	ctx := context.Background()
	s := newService(t)
	u, err := s.CreateUser(ctx, "  Mum@Gmail.com ", "supersecret", RoleRequester, false)
	if err != nil {
		t.Fatal(err)
	}
	if u.Username != "mum@gmail.com" {
		t.Errorf("stored %q, want it lowercased", u.Username)
	}
	for _, name := range []string{"mum@gmail.com", "MUM@gmail.com", "Mum@Gmail.com"} {
		got, err := s.Authenticate(ctx, name, "supersecret")
		if err != nil || got.ID != u.ID {
			t.Errorf("sign in as %q: %v", name, err)
		}
	}
	if _, err := s.Authenticate(ctx, "MUM@gmail.com", "wrongpass"); err != ErrInvalidCredentials {
		t.Errorf("wrong password: %v, want ErrInvalidCredentials", err)
	}
	if got, err := s.UserByUsername(ctx, "MUM@GMAIL.COM"); err != nil || got.ID != u.ID {
		t.Errorf("UserByUsername ignoring case: %v", err)
	}
}

// Creating an account that differs from an existing one only by case is a conflict.
func TestCreateUserRejectsCaseDuplicate(t *testing.T) {
	ctx := context.Background()
	s := newService(t)
	if _, err := s.CreateUser(ctx, "mum@gmail.com", "supersecret", RoleRequester, false); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateUser(ctx, "MUM@gmail.com", "supersecret", RoleRequester, false); err != ErrUserExists {
		t.Errorf("got %v, want ErrUserExists", err)
	}
	// Non-email names are compared ignoring case too, even though they keep their case.
	if _, err := s.CreateUser(ctx, "Tristen", "supersecret", RoleAdmin, false); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateUser(ctx, "tristen", "supersecret", RoleAdmin, false); err != ErrUserExists {
		t.Errorf("got %v, want ErrUserExists", err)
	}
}

// A name that isn't an email (the setup admin's, or one derived from Plex) keeps its case.
func TestCreateUserKeepsPlexNameCase(t *testing.T) {
	ctx := context.Background()
	s := newService(t)
	u, err := s.CreateUser(ctx, "Tristen", "supersecret", RoleAdmin, false)
	if err != nil {
		t.Fatal(err)
	}
	if u.Username != "Tristen" {
		t.Errorf("stored %q, want the case kept", u.Username)
	}
	p, err := s.FindOrCreatePlexUser(ctx, "123", "GrandMa", RoleRequester, AutoApproval{})
	if err != nil {
		t.Fatal(err)
	}
	if p.Username != "GrandMa" {
		t.Errorf("plex user stored as %q, want the case kept", p.Username)
	}
	// A second Plex name that only differs by case gets de-duplicated, not collided.
	p2, err := s.FindOrCreatePlexUser(ctx, "456", "grandma", RoleRequester, AutoApproval{})
	if err != nil {
		t.Fatal(err)
	}
	if p2.Username != "grandma-2" {
		t.Errorf("case-colliding plex name stored as %q, want grandma-2", p2.Username)
	}
}

// insertRaw adds an account directly, bypassing CreateUser's checks — the shape of an
// existing instance that already has two accounts differing only by case.
func insertRaw(t *testing.T, s *Service, name, password string) int64 {
	t.Helper()
	h, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	res, err := s.db.Exec(`INSERT INTO users (username, password_hash, role) VALUES (?, ?, 'requester')`, name, string(h))
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	return id
}

// Two legacy accounts that differ only by case: each still signs in with its exact
// spelling, and any other spelling fails closed instead of picking one.
func TestAuthenticateAmbiguousCaseDuplicatesFailsClosed(t *testing.T) {
	ctx := context.Background()
	s := newService(t)
	a := insertRaw(t, s, "Mum@gmail.com", "password-a")
	b := insertRaw(t, s, "mum@gmail.com", "password-b")

	if u, err := s.Authenticate(ctx, "Mum@gmail.com", "password-a"); err != nil || u.ID != a {
		t.Errorf("exact spelling A: %v", err)
	}
	if u, err := s.Authenticate(ctx, "mum@gmail.com", "password-b"); err != nil || u.ID != b {
		t.Errorf("exact spelling B: %v", err)
	}
	for _, pw := range []string{"password-a", "password-b"} {
		if _, err := s.Authenticate(ctx, "MUM@GMAIL.COM", pw); err != ErrInvalidCredentials {
			t.Errorf("ambiguous spelling with %s: %v, want ErrInvalidCredentials", pw, err)
		}
	}
	if _, err := s.UserByUsername(ctx, "MUM@GMAIL.COM"); err == nil {
		t.Error("UserByUsername must not pick one of two case-duplicates")
	}
	// Nothing was merged or removed.
	if n, _ := s.UserCount(ctx); n != 2 {
		t.Errorf("user count = %d, want both kept", n)
	}
	s.ReportCaseDuplicates(ctx) // must not fail with duplicates present
}

// A legacy mixed-case account (created before names were lowercased) still signs in.
func TestAuthenticateLegacyMixedCase(t *testing.T) {
	ctx := context.Background()
	s := newService(t)
	id := insertRaw(t, s, "Dad@Example.com", "password-d")
	for _, name := range []string{"Dad@Example.com", "dad@example.com"} {
		if u, err := s.Authenticate(ctx, name, "password-d"); err != nil || u.ID != id {
			t.Errorf("sign in as %q: %v", name, err)
		}
	}
}
