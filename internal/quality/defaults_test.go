package quality

import (
	"context"
	"testing"

	"github.com/tristenlammi/arrmada/internal/settings"
	"github.com/tristenlammi/arrmada/internal/store"
)

// The default profile lives in the app's settings: a default saved through quality is
// what the settings service reads, one saved through the settings service is what
// quality uses, and a profile delete that hands the role on is visible to both.
func TestDefaultProfileRoundTripsThroughSettings(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()
	set, err := settings.Open(ctx, st.DB())
	if err != nil {
		t.Fatal(err)
	}
	q := NewServiceWith(st.DB(), set)
	a := mustProfile(t, q, ctx, MediaMovie, "A")
	b := mustProfile(t, q, ctx, MediaMovie, "B")

	if err := q.SetDefaultProfile(ctx, MediaMovie, a); err != nil {
		t.Fatal(err)
	}
	if got := set.Get(ctx, "default_profile:"+MediaMovie, ""); got != a {
		t.Errorf("settings holds %q, want %q", got, a)
	}
	if err := set.Set(ctx, "default_profile:"+MediaMovie, b); err != nil {
		t.Fatal(err)
	}
	if got := q.DefaultProfile(ctx, MediaMovie); got != b {
		t.Errorf("DefaultProfile = %q after a settings save, want %q", got, b)
	}

	// Deleting the default moves the role to the target, in settings too.
	if _, to, err := q.Delete(ctx, profileID(b), a); err != nil || to != a {
		t.Fatalf("delete: to=%q err=%v", to, err)
	}
	if got := set.Get(ctx, "default_profile:"+MediaMovie, ""); got != a {
		t.Errorf("after deleting the default, settings holds %q, want %q", got, a)
	}
	// And the next boot agrees.
	again, err := settings.Open(ctx, st.DB())
	if err != nil {
		t.Fatal(err)
	}
	if got := again.Get(ctx, "default_profile:"+MediaMovie, ""); got != a {
		t.Errorf("reloaded default = %q, want %q", got, a)
	}
}

// Deleting a profile that isn't the default leaves the default alone.
func TestDeleteNonDefaultKeepsTheDefault(t *testing.T) {
	s, _, ctx := storeService(t)
	a := mustProfile(t, s, ctx, MediaMovie, "A")
	b := mustProfile(t, s, ctx, MediaMovie, "B")
	c := mustProfile(t, s, ctx, MediaMovie, "C")
	if err := s.SetDefaultProfile(ctx, MediaMovie, a); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Delete(ctx, profileID(b), c); err != nil {
		t.Fatal(err)
	}
	if got := s.DefaultProfile(ctx, MediaMovie); got != a {
		t.Errorf("default = %q, want it left on %q", got, a)
	}
}
