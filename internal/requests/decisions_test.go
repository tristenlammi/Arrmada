package requests

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// tick gives the service a clock that moves a minute on every reading, so each decision
// has its own time.
func tick(s *Service) {
	at := time.Unix(1_800_000_000, 0)
	s.now = func() time.Time { at = at.Add(time.Minute); return at }
}

// A decline stores its reason and who made it, and the requester's notice says why.
func TestDeclineStoresReasonAndNotifies(t *testing.T) {
	s, _, _ := quietFixture(t)
	tick(s)
	ctx := context.Background()
	req, _, err := s.Create(ctx, Request{MediaType: "movie", TMDBID: 5, Title: "Tron", RequestedBy: 7}, CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Decline(ctx, req.ID, DeclineOptions{Reason: "  Already on Netflix ", DecidedBy: 1, DecidedByName: "admin"}); err != nil {
		t.Fatal(err)
	}
	got, _ := s.repo.Get(ctx, req.ID)
	if got.DeclineReason != "Already on Netflix" || got.DecidedBy != 1 || got.DecidedByName != "admin" || got.DecidedAt == 0 {
		t.Errorf("stored decision = %q by %d/%q at %d", got.DeclineReason, got.DecidedBy, got.DecidedByName, got.DecidedAt)
	}
	inbox, _ := s.repo.listUserNotifications(ctx, 7)
	if len(inbox) != 1 || inbox[0].Body != "Your request for “Tron” was declined: Already on Netflix" {
		t.Errorf("requester inbox = %+v", inbox)
	}
	// Declining it again tells nobody again.
	if err := s.Decline(ctx, req.ID, DeclineOptions{Reason: "still no", DecidedBy: 1}); err != nil {
		t.Fatal(err)
	}
	if refs := inboxRefs(t, s, 7); len(refs) != 1 {
		t.Errorf("a repeat decline notified: %v", refs)
	}
}

// After a re-request, a second decline is a new decision and reaches the requester again.
func TestRepeatDeclineNotifiesAgain(t *testing.T) {
	s, _, _ := quietFixture(t)
	tick(s)
	ctx := context.Background()
	req, _, err := s.Create(ctx, Request{MediaType: "movie", TMDBID: 5, Title: "Tron", RequestedBy: 7}, CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Decline(ctx, req.ID, DeclineOptions{Reason: "No", DecidedBy: 1}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Create(ctx, Request{MediaType: "movie", TMDBID: 5, Title: "Tron", RequestedBy: 7, Note: "pretty please"}, CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := s.Decline(ctx, req.ID, DeclineOptions{Reason: "Still no", DecidedBy: 1}); err != nil {
		t.Fatal(err)
	}
	refs := inboxRefs(t, s, 7)
	if len(refs) != 2 || refs[0] == refs[1] {
		t.Fatalf("requester refs = %v, want two declines", refs)
	}
	for _, r := range refs {
		if !strings.HasPrefix(r, "movie:5:declined:") {
			t.Errorf("ref %q", r)
		}
	}
}

// Asking again for a declined title without a note is refused with the reason and
// changes nothing; with a note it re-opens flagged, keeping the earlier reason for staff.
func TestReRequestNeedsNote(t *testing.T) {
	s, _, _ := quietFixture(t)
	tick(s)
	ctx := context.Background()
	req, _, err := s.Create(ctx, Request{MediaType: "movie", TMDBID: 5, Title: "Tron", RequestedBy: 7, Note: "first"}, CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Decline(ctx, req.ID, DeclineOptions{Reason: "Not for us", DecidedBy: 1}); err != nil {
		t.Fatal(err)
	}
	_, _, err = s.Create(ctx, Request{MediaType: "movie", TMDBID: 5, Title: "Tron", RequestedBy: 8, Note: "   "}, CreateOptions{})
	var needs *NeedsNoteError
	if !errors.As(err, &needs) || needs.DeclineReason != "Not for us" {
		t.Fatalf("without a note: %v, want NeedsNoteError carrying the reason", err)
	}
	if got, _ := s.repo.Get(ctx, req.ID); got.Status != StatusDeclined || got.RequestedBy != 7 {
		t.Fatalf("a refused re-request changed the row: %+v", got)
	}
	again, subscribed, err := s.Create(ctx, Request{MediaType: "movie", TMDBID: 5, Title: "Tron", RequestedBy: 8, RequestedByName: "bob", Note: "It won awards"}, CreateOptions{})
	if err != nil || subscribed {
		t.Fatalf("with a note: subscribed %v, %v", subscribed, err)
	}
	if again.Status != StatusPending || again.ReRequest != 1 || again.Note != "It won awards" || again.DeclineReason != "Not for us" {
		t.Errorf("re-opened = status %q rerequest %d note %q reason %q", again.Status, again.ReRequest, again.Note, again.DeclineReason)
	}
	// An import isn't asked for a note.
	if err := s.Decline(ctx, req.ID, DeclineOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Create(ctx, Request{MediaType: "movie", TMDBID: 5, Title: "Tron", RequestedBy: 9}, CreateOptions{Silent: true}); err != nil {
		t.Errorf("a silent import was refused: %v", err)
	}
}

// Two people asking again for the same declined title at once: the first re-opens it, the
// second (working from the same stale declined row) follows instead of taking it over.
func TestReRequestRaceFollows(t *testing.T) {
	s, _, _ := quietFixture(t)
	ctx := context.Background()
	req, _, err := s.Create(ctx, Request{MediaType: "movie", TMDBID: 5, Title: "Tron", RequestedBy: 7}, CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Decline(ctx, req.ID, DeclineOptions{DecidedBy: 1}); err != nil {
		t.Fatal(err)
	}
	stale, _ := s.repo.Get(ctx, req.ID)
	if _, sub, err := s.attachToExisting(ctx, stale, Request{RequestedBy: 8, Note: "a"}); err != nil || sub {
		t.Fatalf("first: subscribed %v, %v", sub, err)
	}
	if _, sub, err := s.attachToExisting(ctx, stale, Request{RequestedBy: 9, Note: "b"}); err != nil || !sub {
		t.Fatalf("second: subscribed %v, %v; want a follow", sub, err)
	}
	if got, _ := s.repo.Get(ctx, req.ID); got.RequestedBy != 8 || got.ReRequest != 1 {
		t.Errorf("row = owner %d rerequest %d, want 8 and 1", got.RequestedBy, got.ReRequest)
	}
}

// Asking for seasons a declined series request asked for is a re-request too, even when
// it makes a new row: a note is needed, and the new row is flagged with the reason.
func TestSeriesReRequestNeedsNote(t *testing.T) {
	f := newApproveFixture(t, showListing(2, 2, 2))
	tick(f.s)
	declined, _, err := f.s.Create(f.ctx, Request{MediaType: "series", TMDBID: 77, Title: "Show", Seasons: []int{1, 2}, KnownSeasons: []int{1, 2, 3}, RequestedBy: 7}, CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.s.Decline(f.ctx, declined.ID, DeclineOptions{Reason: "Too long", DecidedBy: 1}); err != nil {
		t.Fatal(err)
	}
	// Season 3 alone overlaps nothing declined.
	if _, _, err := f.s.Create(f.ctx, Request{MediaType: "series", TMDBID: 77, Title: "Show", Seasons: []int{3}, KnownSeasons: []int{1, 2, 3}, RequestedBy: 8}, CreateOptions{}); err != nil {
		t.Fatalf("season 3: %v", err)
	}
	var needs *NeedsNoteError
	if _, _, err := f.s.Create(f.ctx, Request{MediaType: "series", TMDBID: 77, Title: "Show", Seasons: []int{2}, KnownSeasons: []int{1, 2, 3}, RequestedBy: 8}, CreateOptions{}); !errors.As(err, &needs) || needs.DeclineReason != "Too long" {
		t.Fatalf("season 2 without a note: %v", err)
	}
	row, _, err := f.s.Create(f.ctx, Request{MediaType: "series", TMDBID: 77, Title: "Show", Seasons: []int{2}, KnownSeasons: []int{1, 2, 3}, RequestedBy: 8, Note: "just S2"}, CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if row.ID == declined.ID || row.ReRequest != 1 || row.DeclineReason != "Too long" {
		t.Errorf("new row = id %d rerequest %d reason %q", row.ID, row.ReRequest, row.DeclineReason)
	}
}

// Approval records who decided and when; the requester's own auto-approve records nobody.
func TestApproveRecordsDecider(t *testing.T) {
	s, _, _ := quietFixture(t)
	tick(s)
	ctx := context.Background()
	req, _, err := s.Create(ctx, Request{MediaType: "movie", TMDBID: 5, Title: "Tron", RequestedBy: 7}, CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Approve(ctx, req.ID, ApproveOptions{DecidedBy: 1, DecidedByName: "admin"})
	if err != nil {
		t.Fatal(err)
	}
	if got.DecidedBy != 1 || got.DecidedByName != "admin" || got.DecidedAt == 0 {
		t.Errorf("approved = by %d/%q at %d", got.DecidedBy, got.DecidedByName, got.DecidedAt)
	}
	auto, _, err := s.Create(ctx, Request{MediaType: "movie", TMDBID: 6, Title: "Up", RequestedBy: 7, RequestedByName: "alice"}, CreateOptions{AutoApprove: true})
	if err != nil {
		t.Fatal(err)
	}
	if auto.DecidedBy != 0 || auto.DecidedByName != "" || auto.DecidedAt == 0 {
		t.Errorf("auto-approved = by %d/%q at %d, want nobody, stamped", auto.DecidedBy, auto.DecidedByName, auto.DecidedAt)
	}
}
