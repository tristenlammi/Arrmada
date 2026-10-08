package settings

import (
	"context"
	"errors"
	"testing"

	"github.com/tristenlammi/arrmada/internal/store"
)

func testService(t *testing.T) *Service {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return NewService(st.DB())
}

func yes(context.Context) (bool, error) { return true, nil }
func no(context.Context) (bool, error)  { return false, nil }

// An install already using a module keeps it on when the default flips to off.
func TestEnsureModuleDefaultKeepsAnActiveModuleOn(t *testing.T) {
	s, ctx := testService(t), context.Background()
	changed, err := s.EnsureModuleDefault(ctx, KeyModuleMusic, yes)
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v; want the module pinned on", changed, err)
	}
	if got := s.Get(ctx, KeyModuleMusic, ""); got != "true" {
		t.Errorf("saved value = %q, want true", got)
	}
	// A second start finds the saved value and leaves it be.
	if changed, err := s.EnsureModuleDefault(ctx, KeyModuleMusic, yes); err != nil || changed {
		t.Errorf("second run changed=%v err=%v; want no change", changed, err)
	}
}

// An unused module is left unset, so it follows the (off) default.
func TestEnsureModuleDefaultLeavesAnUnusedModuleUnset(t *testing.T) {
	s, ctx := testService(t), context.Background()
	if changed, err := s.EnsureModuleDefault(ctx, KeyModuleMusic, no); err != nil || changed {
		t.Fatalf("changed=%v err=%v; want nothing written", changed, err)
	}
	if got := s.Get(ctx, KeyModuleMusic, "unset"); got != "unset" {
		t.Errorf("value = %q, want it left unset", got)
	}
	if s.GetBool(ctx, KeyModuleMusic, ModuleMusicDefault) {
		t.Error("an unset Music toggle must read as off")
	}
}

// The owner's explicit choice is never overwritten, even when the module has data.
func TestEnsureModuleDefaultNeverOverridesAnExplicitValue(t *testing.T) {
	s, ctx := testService(t), context.Background()
	if err := s.SetBool(ctx, KeyModuleMusic, false); err != nil {
		t.Fatal(err)
	}
	called := false
	changed, err := s.EnsureModuleDefault(ctx, KeyModuleMusic, func(context.Context) (bool, error) {
		called = true
		return true, nil
	})
	if err != nil || changed {
		t.Fatalf("changed=%v err=%v; want the explicit off kept", changed, err)
	}
	if called {
		t.Error("keepOn shouldn't even be asked once a value is saved")
	}
	if got := s.Get(ctx, KeyModuleMusic, ""); got != "false" {
		t.Errorf("value = %q, want false", got)
	}
}

// A failing check writes nothing, so it can't decide for the owner.
func TestEnsureModuleDefaultWritesNothingOnError(t *testing.T) {
	s, ctx := testService(t), context.Background()
	boom := errors.New("db locked")
	changed, err := s.EnsureModuleDefault(ctx, KeyModuleMusic, func(context.Context) (bool, error) { return true, boom })
	if !errors.Is(err, boom) || changed {
		t.Fatalf("changed=%v err=%v; want the error back and no change", changed, err)
	}
	if got := s.Get(ctx, KeyModuleMusic, "unset"); got != "unset" {
		t.Errorf("value = %q, want it left unset", got)
	}
}
