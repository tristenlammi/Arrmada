package auth

import (
	"context"
	"errors"
	"testing"
)

// A local account (the setup admin here) takes a Plex link; the next Plex sign-in as that
// Plex account finds it instead of making a new requester.
func TestLinkPlexSetsPlexID(t *testing.T) {
	ctx := context.Background()
	s := newService(t)
	admin, _ := s.CreateUser(ctx, "owner", "supersecret", RoleAdmin, false)
	if err := s.LinkPlex(ctx, admin.ID, "123", "ownerplex"); err != nil {
		t.Fatal(err)
	}
	if got := s.PlexIDForUser(ctx, admin.ID); got != "123" {
		t.Errorf("plex id = %q, want 123", got)
	}
	l, _ := s.PlexLinkFor(ctx, admin.ID)
	if !l.Linked || l.PlexUsername != "ownerplex" || !l.CanUnlink {
		t.Errorf("link = %+v", l)
	}
	u, err := s.FindOrCreatePlexUser(ctx, "123", "ownerplex", RoleRequester, AutoApproval{})
	if err != nil || u.ID != admin.ID || u.Role != RoleAdmin {
		t.Errorf("Plex lookup = %+v, %v; want the linked admin row", u, err)
	}
	// Relinking to the same Plex account is fine; to another replaces it.
	if err := s.LinkPlex(ctx, admin.ID, "123", "ownerplex"); err != nil {
		t.Errorf("relink same: %v", err)
	}
	if err := s.LinkPlex(ctx, admin.ID, "456", "other"); err != nil || s.PlexIDForUser(ctx, admin.ID) != "456" {
		t.Errorf("relink other: %v, plex id %q", err, s.PlexIDForUser(ctx, admin.ID))
	}
}

// A Plex account already linked elsewhere is refused and names who has it.
func TestLinkPlexConflict(t *testing.T) {
	ctx := context.Background()
	s := newService(t)
	kid, _ := s.FindOrCreatePlexUser(ctx, "777", "kid", RoleRequester, AutoApproval{})
	mum, _ := s.CreateUser(ctx, "mum@example.com", "supersecret", RoleRequester, false)
	err := s.LinkPlex(ctx, mum.ID, "777", "kid")
	var taken *ErrPlexAlreadyLinked
	if !errors.As(err, &taken) || taken.UserID != kid.ID || taken.Username != "kid" || taken.Role != RoleRequester {
		t.Fatalf("err = %v, want ErrPlexAlreadyLinked naming kid", err)
	}
	if s.PlexIDForUser(ctx, mum.ID) != "" || s.PlexIDForUser(ctx, kid.ID) != "777" {
		t.Error("a refused link changed something")
	}
}

// Unlink clears the link; a Plex-only account can't unlink itself into a lock-out, but
// an admin can, and setting a password lifts the guard.
func TestUnlinkPlex(t *testing.T) {
	ctx := context.Background()
	s := newService(t)
	mum, _ := s.CreateUser(ctx, "mum@example.com", "supersecret", RoleRequester, false)
	_ = s.LinkPlex(ctx, mum.ID, "123", "mum")
	if err := s.UnlinkPlex(ctx, mum.ID, true); err != nil {
		t.Fatal(err)
	}
	if l, _ := s.PlexLinkFor(ctx, mum.ID); l.Linked || l.PlexUsername != "" {
		t.Errorf("after unlink: %+v", l)
	}
	// Their next Plex sign-in is a new requester, not mum.
	if u, _ := s.FindOrCreatePlexUser(ctx, "123", "mum", RoleRequester, AutoApproval{}); u == nil || u.ID == mum.ID {
		t.Errorf("Plex sign-in after unlink landed in %+v", u)
	}

	kid, _ := s.FindOrCreatePlexUser(ctx, "999", "kid", RoleRequester, AutoApproval{})
	if l, _ := s.PlexLinkFor(ctx, kid.ID); l.CanUnlink {
		t.Error("a Plex-only account says it can unlink")
	}
	if err := s.UnlinkPlex(ctx, kid.ID, true); !errors.Is(err, ErrPlexOnlyLogin) {
		t.Errorf("self-unlink of a Plex-only account: %v, want ErrPlexOnlyLogin", err)
	}
	if err := s.SetPassword(ctx, kid.ID, "a-new-password"); err != nil {
		t.Fatal(err)
	}
	if err := s.UnlinkPlex(ctx, kid.ID, true); err != nil {
		t.Errorf("unlink after a password was set: %v", err)
	}
	if err := s.UnlinkPlex(ctx, 9999, true); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown user: %v", err)
	}
}
