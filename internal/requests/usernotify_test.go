package requests

import (
	"context"
	"errors"
	"log/slog"
	"net"
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

// fixedResolver maps every host to one address.
type fixedResolver map[string]string

func (r fixedResolver) LookupIPAddr(_ context.Context, host string) ([]net.IPAddr, error) {
	ip, ok := r[host]
	if !ok {
		return nil, errors.New("no such host")
	}
	return []net.IPAddr{{IP: net.ParseIP(ip)}}, nil
}

// A requester's saved URL that points at the local network is skipped at send time —
// no Apprise call — while the inbox row still lands. A staff member's same URL, and a
// requester's public one, still push.
func TestNotifyPartiesSkipsInternalApprise(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	var sent []string
	s := &Service{repo: NewRepo(st.DB()), log: slog.Default()}
	s.userApprise.resolver = fixedResolver{"internal.lan": "192.168.1.5", "push.example.com": "93.184.216.34"}
	s.userApprise.send = func(_ context.Context, url, _, _ string) error { sent = append(sent, url); return nil }
	s.SetStaffLookup(func(_ context.Context, uid int64) bool { return uid == 8 })
	ctx := context.Background()

	for _, u := range []struct {
		id   int64
		name string
		url  string
	}{{7, "kid", "gotify://internal.lan/token"}, {8, "mgr", "gotify://internal.lan/token"}, {9, "aunt", "gotify://push.example.com/token"}} {
		if _, err := st.DB().ExecContext(ctx, `INSERT INTO users (id, username, role, password_hash, apprise_url) VALUES (?, ?, 'requester', 'x', ?)`, u.id, u.name, u.url); err != nil {
			t.Fatalf("seed user: %v", err)
		}
	}
	for i, uid := range []int64{7, 8, 9} {
		req := Request{ID: int64(i + 1), MediaType: "movie", TMDBID: 100 + i, Title: "Dune", RequestedBy: uid}
		if err := s.notifyParties(ctx, req, "Your request is ready", "“Dune” is ready to watch.", requestRef(req), "request-ready"); err != nil {
			t.Fatalf("notify %d: %v", uid, err)
		}
		if n, _ := s.repo.unreadCount(ctx, uid); n != 1 {
			t.Errorf("user %d inbox = %d, want 1", uid, n)
		}
	}
	if len(sent) != 2 || sent[0] != "gotify://internal.lan/token" || sent[1] != "gotify://push.example.com/token" {
		t.Fatalf("apprise sends = %v, want only the staff member's and the public one", sent)
	}

	set, hint, blocked, err := s.AppriseStatus(ctx, 7, false)
	if err != nil || !set || blocked == "" || strings.Contains(hint, "token") {
		t.Errorf("status for the blocked URL = %v %q %q %v, want set with a reason and no token", set, hint, blocked, err)
	}
}
