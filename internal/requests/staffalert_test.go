package requests

import (
	"context"
	"strings"
	"sync"
	"testing"
)

// emitted records what the "New request" alert was asked to queue.
type emitted struct {
	mu    sync.Mutex
	calls []string // "key|dedupe|title|rerequest"
}

func (e *emitted) emit(_ context.Context, key, dedupe string, data map[string]any) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	again, _ := data["rerequest"].(bool)
	e.calls = append(e.calls, key+"|"+dedupe+"|"+data["title"].(string)+"|"+map[bool]string{true: "again", false: "new"}[again])
	return nil
}

func (e *emitted) got() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.calls...)
}

func staffAlertFixture(t *testing.T, staff []int64, pushedByAlerts map[int64]bool) (*Service, *fakePush, *emitted) {
	t.Helper()
	s, push, _ := quietFixture(t)
	rec := &emitted{}
	s.SetStaffAlerts(StaffAlerts{
		Emit:           rec.emit,
		Staff:          func(context.Context) ([]int64, error) { return staff, nil },
		PushedByAlerts: func(context.Context, string) (map[int64]bool, error) { return pushedByAlerts, nil },
	})
	return s, push, rec
}

// request.created goes out exactly once for a new pending request and once for a declined
// title asked for again — never for a follow, an auto-approval or an import.
func TestCreatePublishesRequestCreated(t *testing.T) {
	s, _, rec := staffAlertFixture(t, nil, nil)
	ctx := context.Background()

	dune, _, err := s.Create(ctx, Request{MediaType: "movie", TMDBID: 438631, Title: "Dune", RequestedBy: 7, RequestedByName: "alice"}, CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	got := rec.got()
	if len(got) != 1 || !strings.HasPrefix(got[0], "request.created|request.created:") || !strings.HasSuffix(got[0], "|Dune|new") {
		t.Fatalf("after create: %v", got)
	}
	// Someone else asking for it follows it: no alert.
	if _, subscribed, err := s.Create(ctx, Request{MediaType: "movie", TMDBID: 438631, Title: "Dune", RequestedBy: 8}, CreateOptions{}); err != nil || !subscribed {
		t.Fatalf("follow: subscribed %v, %v", subscribed, err)
	}
	// Auto-approved and imported requests need nobody's decision.
	if _, _, err := s.Create(ctx, Request{MediaType: "movie", TMDBID: 1, Title: "Auto", RequestedBy: 7}, CreateOptions{AutoApprove: true}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Create(ctx, Request{MediaType: "movie", TMDBID: 2, Title: "Imported", RequestedBy: 7}, CreateOptions{Silent: true}); err != nil {
		t.Fatal(err)
	}
	if got := rec.got(); len(got) != 1 {
		t.Fatalf("a follow, an auto-approval or an import alerted: %v", got)
	}

	// Declined, then asked for again: a second alert.
	if err := s.Decline(ctx, dune.ID, DeclineOptions{DecidedBy: 1}); err != nil {
		t.Fatal(err)
	}
	if _, subscribed, err := s.Create(ctx, Request{MediaType: "movie", TMDBID: 438631, Title: "Dune", RequestedBy: 9, RequestedByName: "cara", Note: "please"}, CreateOptions{}); err != nil || subscribed {
		t.Fatalf("re-request: subscribed %v, %v", subscribed, err)
	}
	if got := rec.got(); len(got) != 2 {
		t.Fatalf("after the re-request: %v, want a second alert", got)
	}
}

// Every enabled manager and admin gets an inbox entry and a Web Push — not the requester,
// and no push for someone whose own alert connection already pushes it. Announcing the
// same ask again adds nothing.
func TestStaffAlertInbox(t *testing.T) {
	s, push, _ := staffAlertFixture(t, []int64{1, 2, 3, 7}, map[int64]bool{3: true})
	ctx := context.Background()
	req, _, err := s.Create(ctx, Request{MediaType: "movie", TMDBID: 603, Title: "The Matrix", Year: 1999, RequestedBy: 7, RequestedByName: "alice", Note: "classic"}, CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, uid := range []int64{1, 2, 3} {
		inbox, err := s.repo.listUserNotifications(ctx, uid)
		if err != nil {
			t.Fatal(err)
		}
		if len(inbox) != 1 || !strings.HasPrefix(inbox[0].Ref, "request:") || inbox[0].Title != "New request" ||
			!strings.Contains(inbox[0].Body, "alice requested The Matrix (1999)") || !strings.Contains(inbox[0].Body, "classic") {
			t.Errorf("staff %d inbox = %+v", uid, inbox)
		}
	}
	if refs := inboxRefs(t, s, 7); len(refs) != 0 {
		t.Errorf("the requester was told about their own request: %v", refs)
	}
	if got := push.sent(); len(got) != 2 || got[0] != 1 || got[1] != 2 {
		t.Errorf("pushes = %v, want users 1 and 2 (3 has a push alert connection)", got)
	}

	// The same ask announced twice (a retry) is one entry and one push each.
	again, _ := s.repo.Get(ctx, req.ID)
	s.alertStaff(ctx, again)
	if refs := inboxRefs(t, s, 1); len(refs) != 1 {
		t.Errorf("inbox after a repeat = %v", refs)
	}
	if got := push.sent(); len(got) != 2 {
		t.Errorf("pushes after a repeat = %v", got)
	}
}
