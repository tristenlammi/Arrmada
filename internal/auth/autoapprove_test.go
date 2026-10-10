package auth

import (
	"context"
	"testing"
)

func TestAutoApprovesByMediaType(t *testing.T) {
	u := User{AutoApproveMovie: true}
	if !u.AutoApproves("movie") || u.AutoApproves("series") || u.AutoApproves("book") || u.AutoApproves("music") {
		t.Errorf("movie-only user: %+v", u)
	}
	if got := ParseAutoApproval(" Movie, book ,nonsense"); got != (AutoApproval{Movie: true, Book: true}) || got.String() != "movie,book" {
		t.Errorf("ParseAutoApproval = %+v %q", got, got.String())
	}
	if ParseAutoApproval("") != (AutoApproval{}) || !AllTypes(true).All() {
		t.Error("empty list / all types")
	}
}

// Every path that loads a user carries the per-type flags: the list, by id, sign-in, a
// session and an API key; and an update writes them.
func TestAutoApprovalRoundTrips(t *testing.T) {
	s := newService(t)
	ctx := context.Background()
	u, err := s.CreateUser(ctx, "kid@example.com", "password123", RoleRequester, true)
	if err != nil {
		t.Fatal(err)
	}
	if !u.AutoApprove || !u.AutoApproves("series") {
		t.Fatalf("created with auto-approve: %+v", u)
	}
	movieOnly := AutoApproval{Movie: true}
	if err := s.UpdateUser(ctx, u.ID, RoleRequester, movieOnly); err != nil {
		t.Fatal(err)
	}
	check := func(where string, got *User, err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("%s: %v", where, err)
		}
		if got.AutoApproval() != movieOnly || got.AutoApprove {
			t.Errorf("%s: %+v, want movies only", where, got)
		}
	}
	got, err := s.UserByID(ctx, u.ID)
	check("UserByID", got, err)
	got, err = s.Authenticate(ctx, "kid@example.com", "password123")
	check("Authenticate", got, err)
	tok, _, err := s.CreateSession(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	got, err = s.ValidateSession(ctx, tok)
	check("ValidateSession", got, err)
	key, err := s.CreateAPIKey(ctx, u.ID, "k")
	if err != nil {
		t.Fatal(err)
	}
	got, err = s.ValidateAPIKey(ctx, key)
	check("ValidateAPIKey", got, err)
	list, err := s.ListUsers(ctx)
	if err != nil || len(list) != 1 {
		t.Fatal(err)
	}
	check("ListUsers", &list[0], nil)
	var legacy int
	if err := s.db.QueryRowContext(ctx, `SELECT auto_approve FROM users WHERE id = ?`, u.ID).Scan(&legacy); err != nil || legacy != 0 {
		t.Errorf("the old column = %d (%v), want 0 unless every type is on", legacy, err)
	}
}

// A new Plex user gets the defaults asked for; an existing one keeps theirs.
func TestFindOrCreatePlexUserDefaults(t *testing.T) {
	s := newService(t)
	ctx := context.Background()
	u, err := s.FindOrCreatePlexUser(ctx, "1", "Nan", RoleRequester, AutoApproval{Movie: true})
	if err != nil {
		t.Fatal(err)
	}
	if u.AutoApproval() != (AutoApproval{Movie: true}) {
		t.Fatalf("new Plex user: %+v, want movies only", u)
	}
	again, err := s.FindOrCreatePlexUser(ctx, "1", "Nan", RoleRequester, AllTypes(true))
	if err != nil {
		t.Fatal(err)
	}
	if again.ID != u.ID || again.AutoApproval() != (AutoApproval{Movie: true}) {
		t.Errorf("existing Plex user: %+v, want their own movies-only kept", again)
	}
}
