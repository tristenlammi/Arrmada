package requests

import (
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/tristenlammi/arrmada/internal/quality"
	"github.com/tristenlammi/arrmada/internal/store"
)

// newTestService builds a Service over a fresh SQLite store. Only repo-level
// paths (create/subscribe/decline/notify) are exercised — the media services
// stay nil, which Create/Decline never touch.
func newTestService(t *testing.T) *Service {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return &Service{repo: NewRepo(st.DB()), quality: quality.NewService(st.DB()), log: slog.Default()}
}

// TestCreateSubscribesDuplicates verifies a second user requesting the same title
// is attached as a subscriber (not rejected), idempotently.
func TestCreateSubscribesDuplicates(t *testing.T) {
	s := newTestService(t)
	ctx := context.Background()

	first, subscribed, err := s.Create(ctx, Request{MediaType: "movie", TMDBID: 42, Title: "Heat", RequestedBy: 7, RequestedByName: "alice"}, false)
	if err != nil || subscribed {
		t.Fatalf("first create: err=%v subscribed=%v", err, subscribed)
	}
	if first.Status != StatusPending {
		t.Fatalf("first create status = %q, want pending", first.Status)
	}

	// A different user requests the same movie → subscribed, same request back.
	got, subscribed, err := s.Create(ctx, Request{MediaType: "movie", TMDBID: 42, Title: "Heat", RequestedBy: 8, RequestedByName: "bob"}, false)
	if err != nil {
		t.Fatalf("duplicate create: %v", err)
	}
	if !subscribed {
		t.Errorf("duplicate create: subscribed = false, want true")
	}
	if got.ID != first.ID || got.RequestedBy != 7 {
		t.Errorf("duplicate create returned id=%d requested_by=%d, want id=%d requested_by=7", got.ID, got.RequestedBy, first.ID)
	}
	subs, _ := s.repo.Subscribers(ctx, first.ID)
	if len(subs) != 1 || subs[0].UserID != 8 || subs[0].UserName != "bob" {
		t.Fatalf("subscribers = %+v, want [{8 bob}]", subs)
	}

	// Repeat by the same user is idempotent.
	if _, subscribed, err = s.Create(ctx, Request{MediaType: "movie", TMDBID: 42, Title: "Heat", RequestedBy: 8, RequestedByName: "bob"}, false); err != nil || !subscribed {
		t.Fatalf("repeat duplicate: err=%v subscribed=%v", err, subscribed)
	}
	if subs, _ = s.repo.Subscribers(ctx, first.ID); len(subs) != 1 {
		t.Errorf("after repeat, subscribers = %d rows, want 1", len(subs))
	}

	// The original requester re-requesting doesn't subscribe themselves.
	if _, subscribed, err = s.Create(ctx, Request{MediaType: "movie", TMDBID: 42, Title: "Heat", RequestedBy: 7, RequestedByName: "alice"}, false); err != nil || !subscribed {
		t.Fatalf("owner repeat: err=%v subscribed=%v", err, subscribed)
	}
	if subs, _ = s.repo.Subscribers(ctx, first.ID); len(subs) != 1 {
		t.Errorf("owner repeat added a self-subscription: %+v", subs)
	}
}

// TestCreateResurrectsDeclined verifies re-requesting a declined title re-opens it
// under the new requester, keeps the old requester subscribed, and preserves the
// original quality profile unless a new one is supplied.
func TestCreateResurrectsDeclined(t *testing.T) {
	s := newTestService(t)
	ctx := context.Background()

	req, _, err := s.Create(ctx, Request{MediaType: "movie", TMDBID: 9, Title: "Dune", QualityProfile: "n/a", RequestedBy: 7, RequestedByName: "alice"}, false)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := s.Decline(ctx, req.ID); err != nil {
		t.Fatalf("decline: %v", err)
	}
	// Decline must not erase the stored profile.
	if got, _ := s.repo.Get(ctx, req.ID); got.QualityProfile != "n/a" {
		t.Fatalf("after decline profile = %q, want preserved n/a", got.QualityProfile)
	}

	// Bob re-requests → back to pending under bob, alice kept as subscriber.
	got, subscribed, err := s.Create(ctx, Request{MediaType: "movie", TMDBID: 9, Title: "Dune", RequestedBy: 8, RequestedByName: "bob"}, false)
	if err != nil {
		t.Fatalf("resurrect: %v", err)
	}
	if subscribed {
		t.Errorf("resurrect reported subscribed = true, want false")
	}
	if got.Status != StatusPending || got.RequestedBy != 8 || got.RequestedByName != "bob" {
		t.Errorf("resurrected = status %q by %d/%q, want pending by 8/bob", got.Status, got.RequestedBy, got.RequestedByName)
	}
	if got.QualityProfile != "n/a" {
		t.Errorf("resurrected profile = %q, want original n/a kept", got.QualityProfile)
	}
	subs, _ := s.repo.Subscribers(ctx, req.ID)
	if len(subs) != 1 || subs[0].UserID != 7 {
		t.Fatalf("subscribers after resurrect = %+v, want previous requester 7", subs)
	}
}

// TestCreateValidatesProfile verifies an unknown quality profile is rejected up front.
func TestCreateValidatesProfile(t *testing.T) {
	s := newTestService(t)
	ctx := context.Background()

	if _, _, err := s.Create(ctx, Request{MediaType: "movie", TMDBID: 1, Title: "X", QualityProfile: "custom:999", RequestedBy: 7}, false); err != ErrUnknownProfile {
		t.Fatalf("unknown profile: err = %v, want ErrUnknownProfile", err)
	}
	// "n/a" is the accepted no-profile marker; empty is always fine.
	if _, _, err := s.Create(ctx, Request{MediaType: "movie", TMDBID: 1, Title: "X", QualityProfile: "n/a", RequestedBy: 7}, false); err != nil {
		t.Fatalf("n/a profile rejected: %v", err)
	}
}

// TestDeclineNotifiesAllParties verifies the requester and every subscriber get the
// declined inbox entry, deduped from any future availability notification by ref.
func TestDeclineNotifiesAllParties(t *testing.T) {
	s := newTestService(t)
	ctx := context.Background()

	req, _, err := s.Create(ctx, Request{MediaType: "movie", TMDBID: 5, Title: "Tron", RequestedBy: 7, RequestedByName: "alice"}, false)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, _, err := s.Create(ctx, Request{MediaType: "movie", TMDBID: 5, Title: "Tron", RequestedBy: 8, RequestedByName: "bob"}, false); err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	if err := s.Decline(ctx, req.ID); err != nil {
		t.Fatalf("decline: %v", err)
	}
	for _, uid := range []int64{7, 8} {
		inbox, err := s.repo.listUserNotifications(ctx, uid)
		if err != nil || len(inbox) != 1 {
			t.Fatalf("user %d inbox = %d items (err %v), want 1", uid, len(inbox), err)
		}
		if !strings.Contains(inbox[0].Body, "declined") || !strings.Contains(inbox[0].Body, "Tron") {
			t.Errorf("user %d body = %q, want declined + title", uid, inbox[0].Body)
		}
		if inbox[0].Ref != "movie:5:declined" {
			t.Errorf("user %d ref = %q, want movie:5:declined", uid, inbox[0].Ref)
		}
	}
}

// TestDeleteRemovesSubscribers verifies withdrawing a request also drops its
// subscriber rows.
func TestDeleteRemovesSubscribers(t *testing.T) {
	s := newTestService(t)
	ctx := context.Background()

	req, _, err := s.Create(ctx, Request{MediaType: "movie", TMDBID: 3, Title: "Up", RequestedBy: 7}, false)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, _, err := s.Create(ctx, Request{MediaType: "movie", TMDBID: 3, Title: "Up", RequestedBy: 8, RequestedByName: "bob"}, false); err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	if err := s.Delete(ctx, req.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if subs, _ := s.repo.Subscribers(ctx, req.ID); len(subs) != 0 {
		t.Errorf("subscribers after delete = %+v, want none", subs)
	}
}

// TestReadyNotificationFansOut verifies the availability notification reaches the
// requester and subscribers, and stays deduped on repeat imports.
func TestReadyNotificationFansOut(t *testing.T) {
	s := newTestService(t)
	ctx := context.Background()

	req, _, err := s.Create(ctx, Request{MediaType: "movie", TMDBID: 11, Title: "Alien", RequestedBy: 7}, false)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := s.repo.AddSubscriber(ctx, req.ID, 8, "bob"); err != nil {
		t.Fatalf("add subscriber: %v", err)
	}

	s.notifyRequester(ctx, "movie", 11, "")
	s.notifyRequester(ctx, "movie", 11, "") // repeat import must not double-notify
	for _, uid := range []int64{7, 8} {
		inbox, _ := s.repo.listUserNotifications(ctx, uid)
		if len(inbox) != 1 {
			t.Fatalf("user %d inbox = %d items, want 1", uid, len(inbox))
		}
		if inbox[0].Ref != "movie:11" {
			t.Errorf("user %d ref = %q, want movie:11", uid, inbox[0].Ref)
		}
	}
}
