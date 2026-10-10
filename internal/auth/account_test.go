package auth

import (
	"context"
	"errors"
	"testing"
)

// Changing your own password keeps the session doing it and ends every other one; the
// new password works and the old one doesn't.
func TestChangePasswordKeepsCurrentSession(t *testing.T) {
	ctx := context.Background()
	s := newService(t)
	u, _ := s.CreateUser(ctx, "kid@example.com", "oldpassword", RoleRequester, false)
	other, _ := s.CreateUser(ctx, "aunt@example.com", "auntpassword", RoleRequester, false)
	here, _, _ := s.CreateSession(ctx, u.ID)
	phone, _, _ := s.CreateSession(ctx, u.ID)
	theirs, _, _ := s.CreateSession(ctx, other.ID)

	if _, err := s.ChangePassword(ctx, u.ID, "short", here); !errors.Is(err, ErrWeakPassword) {
		t.Fatalf("weak password: %v", err)
	}
	ended, err := s.ChangePassword(ctx, u.ID, "newpassword", here)
	if err != nil || ended != 1 {
		t.Fatalf("change: ended %d, %v; want 1", ended, err)
	}
	if _, err := s.ValidateSession(ctx, here); err != nil {
		t.Errorf("the session making the change was signed out: %v", err)
	}
	if _, err := s.ValidateSession(ctx, phone); err == nil {
		t.Error("the other device is still signed in")
	}
	if _, err := s.ValidateSession(ctx, theirs); err != nil {
		t.Errorf("someone else's session ended: %v", err)
	}
	if !s.CheckPassword(ctx, u.ID, "newpassword") || s.CheckPassword(ctx, u.ID, "oldpassword") {
		t.Error("the new password should be the only one that works")
	}
	if _, err := s.Authenticate(ctx, "kid@example.com", "newpassword"); err != nil {
		t.Errorf("sign in with the new password: %v", err)
	}
}

// A Plex-only account has no password anyone knows; setting one marks it known, which is
// what lets it unlink Plex afterwards.
func TestChangePasswordPlexOnlySetsFirst(t *testing.T) {
	ctx := context.Background()
	s := newService(t)
	u, _ := s.FindOrCreatePlexUser(ctx, "4242", "kidplex", RoleRequester, AutoApproval{})
	if has, err := s.HasPassword(ctx, u.ID); err != nil || has {
		t.Fatalf("Plex sign-in account: HasPassword = %v, %v; want false", has, err)
	}
	if err := s.UnlinkPlex(ctx, u.ID, true); !errors.Is(err, ErrPlexOnlyLogin) {
		t.Fatalf("unlink before a password: %v, want ErrPlexOnlyLogin", err)
	}
	if _, err := s.ChangePassword(ctx, u.ID, "firstpassword", ""); err != nil {
		t.Fatal(err)
	}
	if has, _ := s.HasPassword(ctx, u.ID); !has {
		t.Error("HasPassword after setting one = false")
	}
	if l, _ := s.PlexLinkFor(ctx, u.ID); !l.CanUnlink {
		t.Error("can't unlink Plex after setting a password")
	}
	if err := s.UnlinkPlex(ctx, u.ID, true); err != nil {
		t.Errorf("unlink after setting a password: %v", err)
	}
	if _, err := s.HasPassword(ctx, 9999); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing account: %v", err)
	}
}

// Signing out other devices keeps the one asking and never touches anyone else's; with no
// session to keep, every browser goes.
func TestRevokeOtherSessions(t *testing.T) {
	ctx := context.Background()
	s := newService(t)
	u, _ := s.CreateUser(ctx, "kid@example.com", "password1", RoleRequester, false)
	other, _ := s.CreateUser(ctx, "aunt@example.com", "password2", RoleRequester, false)
	here, _, _ := s.CreateSession(ctx, u.ID)
	_, _, _ = s.CreateSession(ctx, u.ID)
	_, _, _ = s.CreateSession(ctx, u.ID)
	theirs, _, _ := s.CreateSession(ctx, other.ID)

	n, err := s.RevokeOtherSessions(ctx, u.ID, here)
	if err != nil || n != 2 {
		t.Fatalf("revoke others: %d, %v; want 2", n, err)
	}
	if _, err := s.ValidateSession(ctx, here); err != nil {
		t.Errorf("current session ended: %v", err)
	}
	if _, err := s.ValidateSession(ctx, theirs); err != nil {
		t.Errorf("another user's session ended: %v", err)
	}
	if n, _ := s.RevokeOtherSessions(ctx, u.ID, ""); n != 1 {
		t.Errorf("with nothing to keep: ended %d, want 1", n)
	}
}
