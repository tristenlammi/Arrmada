package settings

import (
	"context"
	"errors"
	"strconv"
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

// A saved value is what every reader sees next, and a second service over the same
// database (the next boot) loads it.
func TestSetThenGet(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()
	s, err := Open(ctx, st.DB())
	if err != nil {
		t.Fatal(err)
	}
	if got := s.Get(ctx, "music_root", "/default"); got != "/default" {
		t.Errorf("unset = %q, want the default", got)
	}
	if err := s.Set(ctx, "music_root", "/music"); err != nil {
		t.Fatal(err)
	}
	if got := s.Get(ctx, "music_root", "/default"); got != "/music" {
		t.Errorf("after Set = %q, want /music", got)
	}
	if v, ok := s.Lookup("music_root"); !ok || v != "/music" {
		t.Errorf("Lookup = %q, %v", v, ok)
	}
	// An empty value is a saved value, not an absent one.
	if err := s.Set(ctx, "tmdb_region", ""); err != nil {
		t.Fatal(err)
	}
	if got := s.Get(ctx, "tmdb_region", "US"); got != "" {
		t.Errorf("saved empty value read as %q", got)
	}
	again, err := Open(ctx, st.DB())
	if err != nil {
		t.Fatal(err)
	}
	if got := again.Get(ctx, "music_root", ""); got != "/music" {
		t.Errorf("next boot reads %q, want /music", got)
	}
	all := again.All()
	all["music_root"] = "changed"
	if got := again.Get(ctx, "music_root", ""); got != "/music" {
		t.Error("All must return a copy")
	}
}

// Once loaded, reads come from memory: a database that has stopped answering can't turn
// a saved setting back into its default. A save against it fails and changes nothing.
func TestBrokenDatabaseKeepsSettings(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	s, err := Open(ctx, st.DB())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Set(ctx, "music_root", "/music"); err != nil {
		t.Fatal(err)
	}
	_ = st.Close()

	if got := s.Get(ctx, "music_root", "/env-default"); got != "/music" {
		t.Errorf("with the database gone Get = %q, want the saved /music", got)
	}
	if err := s.Set(ctx, "music_root", "/elsewhere"); err == nil {
		t.Fatal("Set against a closed database reported success")
	}
	if got := s.Get(ctx, "music_root", ""); got != "/music" {
		t.Errorf("after a failed Set = %q, want the old value", got)
	}
	if err := s.Reload(ctx); err == nil {
		t.Error("Reload against a closed database reported success")
	}
	if got := s.Get(ctx, "music_root", ""); got != "/music" {
		t.Errorf("after a failed Reload = %q, want the old value", got)
	}
}

// The app refuses to start on settings it couldn't read, rather than on defaults.
func TestOpenFailsWhenTableUnreadable(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if _, err := st.DB().Exec(`DROP TABLE settings`); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(context.Background(), st.DB()); err == nil {
		t.Fatal("Open succeeded without a settings table")
	}
}

// Reload picks up what is in the table now (a restored database, say).
func TestReload(t *testing.T) {
	s, ctx := testService(t), context.Background()
	if err := s.Set(ctx, "a", "1"); err != nil {
		t.Fatal(err)
	}
	other := NewService(s.db) // writes behind the first service's back
	if err := other.Set(ctx, "a", "2"); err != nil {
		t.Fatal(err)
	}
	if got := s.Get(ctx, "a", ""); got != "1" {
		t.Fatalf("before Reload = %q, want the cached 1", got)
	}
	if err := s.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	if got := s.Get(ctx, "a", ""); got != "2" {
		t.Errorf("after Reload = %q, want 2", got)
	}
}

// Readers and writers can run at once.
func TestConcurrentAccess(t *testing.T) {
	s, ctx := testService(t), context.Background()
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 50; i++ {
			_ = s.Set(ctx, "k", strconv.Itoa(i))
		}
	}()
	for i := 0; i < 200; i++ {
		_ = s.Get(ctx, "k", "")
		_ = s.All()
	}
	<-done
	if got := s.Get(ctx, "k", ""); got != "49" {
		t.Errorf("final value = %q, want 49", got)
	}
}
