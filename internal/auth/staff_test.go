package auth

import (
	"context"
	"testing"
)

// StaffIDs is the enabled managers and admins: requesters and disabled staff don't hear
// that a request is waiting.
func TestStaffIDs(t *testing.T) {
	s := newService(t)
	ctx := context.Background()
	mk := func(name string, role Role) int64 {
		u, err := s.CreateUser(ctx, name, "password123", role, false)
		if err != nil {
			t.Fatal(err)
		}
		return u.ID
	}
	admin := mk("admin", RoleAdmin)
	manager := mk("manager", RoleManager)
	mk("requester", RoleRequester)
	gone := mk("gone", RoleManager)
	if err := s.SetDisabled(ctx, gone, true); err != nil {
		t.Fatal(err)
	}
	got, err := s.StaffIDs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != admin || got[1] != manager {
		t.Errorf("StaffIDs = %v, want [%d %d]", got, admin, manager)
	}
}
