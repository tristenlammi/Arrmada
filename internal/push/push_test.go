package push

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/tristenlammi/arrmada/internal/settings"
	"github.com/tristenlammi/arrmada/internal/store"
)

func newTestService(t *testing.T) *Service {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return New(st.DB(), settings.NewService(st.DB()), slog.New(slog.NewTextHandler(io.Discard, nil)))
}

// PublicKey generates once and then returns the same persisted key.
func TestPublicKeyStable(t *testing.T) {
	s := newTestService(t)
	ctx := context.Background()
	k1, err := s.PublicKey(ctx)
	if err != nil || k1 == "" {
		t.Fatalf("first key: %q err=%v", k1, err)
	}
	k2, err := s.PublicKey(ctx)
	if err != nil || k2 != k1 {
		t.Fatalf("key not stable: %q vs %q err=%v", k1, k2, err)
	}
}

// Subscriptions are keyed by endpoint: re-subscribing re-points the row (shared
// device → last sign-in wins), and unsubscribe is scoped to the owning user.
func TestSubscribeSemantics(t *testing.T) {
	s := newTestService(t)
	ctx := context.Background()

	if err := s.Subscribe(ctx, 1, "https://push.example/abc", "p", "a"); err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	if !s.HasSubscription(ctx, 1, "https://push.example/abc") {
		t.Fatal("subscription not recorded")
	}

	// Same endpoint, different user → row re-points to user 2.
	if err := s.Subscribe(ctx, 2, "https://push.example/abc", "p2", "a2"); err != nil {
		t.Fatalf("re-subscribe: %v", err)
	}
	if s.HasSubscription(ctx, 1, "https://push.example/abc") {
		t.Fatal("old user still owns the endpoint")
	}
	if !s.HasSubscription(ctx, 2, "https://push.example/abc") {
		t.Fatal("new user doesn't own the endpoint")
	}

	// User 1 cannot remove user 2's subscription.
	if err := s.Unsubscribe(ctx, 1, "https://push.example/abc"); err != nil {
		t.Fatalf("unsubscribe: %v", err)
	}
	if !s.HasSubscription(ctx, 2, "https://push.example/abc") {
		t.Fatal("cross-user unsubscribe removed the row")
	}
	if err := s.Unsubscribe(ctx, 2, "https://push.example/abc"); err != nil {
		t.Fatalf("owner unsubscribe: %v", err)
	}
	if s.HasSubscription(ctx, 2, "https://push.example/abc") {
		t.Fatal("owner unsubscribe didn't remove the row")
	}
}

// A user with no device subscribed is told so, rather than "sent to nobody" passing as
// a success in an alert's delivery log.
func TestSendToUserResultNoDevices(t *testing.T) {
	s := newTestService(t)
	if n, err := s.SendToUserResult(context.Background(), 1, "t", "b", "/"); n != 0 || !errors.Is(err, ErrNoDevices) {
		t.Fatalf("got %d, %v; want ErrNoDevices", n, err)
	}
}

// A 'ready' push carries the title's Watch on Plex page next to the app address, and a
// push without one says nothing about Plex.
func TestPayloadIncludesPlexURL(t *testing.T) {
	b, err := Message{Title: "Your request is ready", Body: "“Dune” is ready to watch on Plex.", URL: "/discover/movie/603",
		PlexURL: "https://app.plex.tv/desktop/#!/server/m1/details?key=%2Flibrary%2Fmetadata%2F101"}.payload()
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]string
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if got["url"] != "/discover/movie/603" || got["plex_url"] != "https://app.plex.tv/desktop/#!/server/m1/details?key=%2Flibrary%2Fmetadata%2F101" {
		t.Fatalf("payload = %s", b)
	}
	plain, _ := Message{Title: "Request approved", Body: "b", URL: "/requests?id=4"}.payload()
	if strings.Contains(string(plain), "plex_url") {
		t.Errorf("a push without a Plex link carries one: %s", plain)
	}
}
