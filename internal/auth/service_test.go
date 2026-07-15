package auth

import (
	"context"
	"testing"

	"github.com/tristenlammi/arrmada/internal/store"
)

func newService(t *testing.T) *Service {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return NewService(st.DB())
}

func TestCreateAndAuthenticate(t *testing.T) {
	ctx := context.Background()
	s := newService(t)

	if n, _ := s.UserCount(ctx); n != 0 {
		t.Fatalf("expected 0 users, got %d", n)
	}

	u, err := s.CreateUser(ctx, "tristen", "supersecret", RoleAdmin, false)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if u.Role != RoleAdmin || u.Username != "tristen" {
		t.Fatalf("unexpected user: %+v", u)
	}

	if _, err := s.Authenticate(ctx, "tristen", "supersecret"); err != nil {
		t.Errorf("expected auth success, got %v", err)
	}
	if _, err := s.Authenticate(ctx, "tristen", "wrongpass"); err != ErrInvalidCredentials {
		t.Errorf("expected ErrInvalidCredentials, got %v", err)
	}
	if _, err := s.Authenticate(ctx, "ghost", "whatever1"); err != ErrInvalidCredentials {
		t.Errorf("expected ErrInvalidCredentials for unknown user, got %v", err)
	}
}

func TestValidations(t *testing.T) {
	ctx := context.Background()
	s := newService(t)

	if _, err := s.CreateUser(ctx, "u", "short", RoleAdmin, false); err != ErrWeakPassword {
		t.Errorf("expected ErrWeakPassword, got %v", err)
	}
	if _, err := s.CreateUser(ctx, "", "longenough", RoleAdmin, false); err != ErrUsernameRequired {
		t.Errorf("expected ErrUsernameRequired, got %v", err)
	}
	if _, err := s.CreateUser(ctx, "dup", "longenough", RoleAdmin, false); err != nil {
		t.Fatalf("first create: %v", err)
	}
	if _, err := s.CreateUser(ctx, "dup", "longenough", RoleAdmin, false); err != ErrUserExists {
		t.Errorf("expected ErrUserExists, got %v", err)
	}
}

func TestSessionLifecycle(t *testing.T) {
	ctx := context.Background()
	s := newService(t)
	u, _ := s.CreateUser(ctx, "tristen", "supersecret", RoleAdmin, false)

	token, _, err := s.CreateSession(ctx, u.ID)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	got, err := s.ValidateSession(ctx, token)
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if got.ID != u.ID {
		t.Errorf("wrong user from session: %+v", got)
	}

	if err := s.DeleteSession(ctx, token); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := s.ValidateSession(ctx, token); err != ErrNotFound {
		t.Errorf("expected ErrNotFound after logout, got %v", err)
	}
}

func TestAPIKeyLifecycle(t *testing.T) {
	ctx := context.Background()
	s := newService(t)
	u, _ := s.CreateUser(ctx, "svc", "supersecret", RoleManager, false)

	key, err := s.CreateAPIKey(ctx, u.ID, "automation")
	if err != nil {
		t.Fatalf("create key: %v", err)
	}

	got, err := s.ValidateAPIKey(ctx, key)
	if err != nil {
		t.Fatalf("validate key: %v", err)
	}
	if got.ID != u.ID || got.Role != RoleManager {
		t.Errorf("wrong user from key: %+v", got)
	}
	if _, err := s.ValidateAPIKey(ctx, "arr_bogus"); err != ErrNotFound {
		t.Errorf("expected ErrNotFound for bad key, got %v", err)
	}
}

func TestRoleAtLeast(t *testing.T) {
	if !RoleAdmin.AtLeast(RoleReadonly) {
		t.Error("admin should outrank readonly")
	}
	if RoleReadonly.AtLeast(RoleManager) {
		t.Error("readonly should not meet manager")
	}
	if !RoleManager.AtLeast(RoleManager) {
		t.Error("role should meet itself")
	}
}
