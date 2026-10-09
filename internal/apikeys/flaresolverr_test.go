package apikeys

import (
	"context"
	"testing"
)

// The FlareSolverr URL is a catalogue entry like a key: the saved value beats
// ARRMADA_FLARESOLVERR_URL, the env var is the fallback, and its status shows the URL in
// full (it isn't a secret) with where it came from.
func TestFlareSolverrURLPrecedence(t *testing.T) {
	ctx := context.Background()
	s := NewStore(memStore{})
	t.Setenv("ARRMADA_FLARESOLVERR_URL", "http://arrmada-flaresolverr:8191")
	if got := s.Value(ctx, "flaresolverr"); got != "http://arrmada-flaresolverr:8191" {
		t.Fatalf("env fallback = %q", got)
	}
	if err := s.Set(ctx, "flaresolverr", "http://other:8191"); err != nil {
		t.Fatal(err)
	}
	if got := s.Func("flaresolverr")(); got != "http://other:8191" {
		t.Fatalf("the saved URL should win, got %q", got)
	}
	for _, st := range s.Status(ctx) {
		if st.ID != "flaresolverr" {
			continue
		}
		if !st.Testable || st.Secret || st.Source != "settings" || st.Hint != "http://other:8191" || st.EnvHint != "http://arrmada-flaresolverr:8191" {
			t.Fatalf("status = %+v", st)
		}
		return
	}
	t.Fatal("flaresolverr is missing from the catalogue")
}
