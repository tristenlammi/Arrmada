package requests

import (
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/tristenlammi/arrmada/internal/store"
)

// TestNotifyRequester verifies an import matches back to its requester, lands in their inbox,
// and doesn't double-notify on a repeat import (dedupe via the unique ref).
func TestNotifyRequester(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	s := &Service{repo: NewRepo(st.DB()), log: slog.Default()}
	ctx := context.Background()

	// User 7 requested Dune (movie, tmdb 123).
	if _, err := s.repo.Create(ctx, Request{MediaType: "movie", TMDBID: 123, Title: "Dune", Status: StatusApproved, RequestedBy: 7}); err != nil {
		t.Fatalf("create request: %v", err)
	}

	// The movie imports → notify the requester.
	s.notifyRequester(ctx, "movie", 123, "")
	inbox, err := s.repo.listUserNotifications(ctx, 7)
	if err != nil {
		t.Fatalf("list inbox: %v", err)
	}
	if len(inbox) != 1 {
		t.Fatalf("want 1 inbox item, got %d", len(inbox))
	}
	if !strings.Contains(inbox[0].Body, "Dune") {
		t.Errorf("body = %q, want it to mention Dune", inbox[0].Body)
	}
	if inbox[0].Read {
		t.Errorf("new notification should be unread")
	}

	// A repeat import of the same movie must NOT double-notify.
	s.notifyRequester(ctx, "movie", 123, "")
	inbox, _ = s.repo.listUserNotifications(ctx, 7)
	if len(inbox) != 1 {
		t.Fatalf("dedupe failed: got %d items", len(inbox))
	}

	// Unrelated user has nothing.
	if n, _ := s.repo.unreadCount(ctx, 99); n != 0 {
		t.Errorf("unrelated user unread = %d, want 0", n)
	}

	// Mark read clears the unread count.
	if err := s.repo.markAllRead(ctx, 7); err != nil {
		t.Fatalf("mark read: %v", err)
	}
	if n, _ := s.repo.unreadCount(ctx, 7); n != 0 {
		t.Errorf("after mark-all-read unread = %d, want 0", n)
	}
}

// TestUserApprise round-trips the per-user Apprise URL.
func TestUserApprise(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	s := &Service{repo: NewRepo(st.DB()), log: slog.Default()}
	ctx := context.Background()

	// Seed a user row (apprise_url lives on users).
	if _, err := st.DB().ExecContext(ctx, `INSERT INTO users (id, username, role, password_hash) VALUES (7, 'bob', 'requester', 'x')`); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if err := s.repo.setUserApprise(ctx, 7, "ntfy://mytopic"); err != nil {
		t.Fatalf("set apprise: %v", err)
	}
	got, err := s.repo.getUserApprise(ctx, 7)
	if err != nil {
		t.Fatalf("get apprise: %v", err)
	}
	if got != "ntfy://mytopic" {
		t.Errorf("apprise = %q, want ntfy://mytopic", got)
	}
}
