package notify

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

// drainNow starts every due row and waits for the sends to finish.
func drainNow(t *testing.T, s *Service) {
	t.Helper()
	w := newWorker()
	if err := s.startDue(context.Background(), w); err != nil {
		t.Fatal(err)
	}
	w.wg.Wait()
}

type clock struct{ t time.Time }

func (c *clock) now() time.Time          { return c.t }
func (c *clock) advance(d time.Duration) { c.t = c.t.Add(d) }
func useClock(s *Service, c *clock)      { s.now = c.now }
func newClock() *clock                   { return &clock{t: time.Unix(1_800_000_000, 0)} }
func sub(events ...string) []string      { return events }
func msg(body string) Message            { return Message{Title: "T", Body: body} }
func (r *recorder) count() int           { return len(r.got()) }
func row(t *testing.T, s *Service, id int64) (status string, attempts int, next int64, lastErr string) {
	t.Helper()
	if err := s.db.QueryRow(`SELECT status, attempts, next_attempt_at, last_error FROM notification_deliveries WHERE id = ?`, id).Scan(&status, &attempts, &next, &lastErr); err != nil {
		t.Fatal(err)
	}
	return
}

func mustCreate(t *testing.T, s *Service, c Connection) Connection {
	t.Helper()
	got, err := s.Create(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// A failing send is retried after 1 and then 5 minutes, and recorded sent when it
// finally goes through.
func TestQueueRetriesWithBackoff(t *testing.T) {
	s, rec, _ := newTestService(t)
	clk := newClock()
	useClock(s, clk)
	ctx := context.Background()
	mustCreate(t, s, Connection{Name: "a", URL: "ntfy://a", Events: sub("release.grabbed"), Enabled: true})
	failures := 2
	s.SetTransport(func(ctx context.Context, url, title, body string) error {
		_ = rec.send(ctx, url, title, body)
		if failures > 0 {
			failures--
			return errors.New("503 from ntfy")
		}
		return nil
	})
	if n, err := s.Dispatch(ctx, "release.grabbed", msg("Dune")); err != nil || n != 1 {
		t.Fatalf("Dispatch = %d, %v", n, err)
	}

	drainNow(t, s)
	status, attempts, next, lastErr := row(t, s, 1)
	if status != StatusQueued || attempts != 1 || next != clk.t.Add(time.Minute).Unix() || lastErr != "503 from ntfy" {
		t.Fatalf("after 1st failure: %s %d %d %q", status, attempts, next, lastErr)
	}
	drainNow(t, s) // not due yet
	if rec.count() != 1 {
		t.Fatalf("retried before it was due: %d sends", rec.count())
	}
	clk.advance(time.Minute)
	drainNow(t, s)
	if status, attempts, next, _ = row(t, s, 1); status != StatusQueued || attempts != 2 || next != clk.t.Add(5*time.Minute).Unix() {
		t.Fatalf("after 2nd failure: %s %d %d", status, attempts, next)
	}
	clk.advance(5 * time.Minute)
	drainNow(t, s)
	if status, attempts, _, lastErr = row(t, s, 1); status != StatusSent || attempts != 3 || lastErr != "" {
		t.Fatalf("after success: %s %d %q", status, attempts, lastErr)
	}
	states, err := s.DeliveryStates(ctx)
	if err != nil || states[1].Status != StatusSent || states[1].LastSentAt != clk.t.Unix() {
		t.Fatalf("states = %+v, %v", states, err)
	}
}

func TestQueueMarksFailedAfterMax(t *testing.T) {
	s, rec, _ := newTestService(t)
	clk := newClock()
	useClock(s, clk)
	mustCreate(t, s, Connection{Name: "a", URL: "ntfy://dead", Events: sub("release.grabbed"), Enabled: true})
	rec.fail["ntfy://dead"] = true
	if _, err := s.Dispatch(context.Background(), "release.grabbed", msg("Dune")); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 6; i++ {
		drainNow(t, s)
		clk.advance(time.Hour)
	}
	status, attempts, _, lastErr := row(t, s, 1)
	if status != StatusFailed || attempts != maxAttempts || rec.count() != maxAttempts {
		t.Fatalf("got %s after %d attempts (%d sends), want failed after %d", status, attempts, rec.count(), maxAttempts)
	}
	if lastErr == "" {
		t.Error("the failure reason should be kept")
	}
	states, _ := s.DeliveryStates(context.Background())
	if states[1].Status != StatusFailed || states[1].Error == "" {
		t.Errorf("state = %+v, want failed with the reason", states[1])
	}
}

// Rows queued before a restart, and one a crash left "sending", go out from a fresh worker.
func TestQueueSurvivesRestart(t *testing.T) {
	s, rec, _ := newTestService(t)
	ctx := context.Background()
	mustCreate(t, s, Connection{Name: "a", URL: "ntfy://a", Events: sub("release.grabbed"), Enabled: true})
	if _, err := s.Dispatch(ctx, "release.grabbed", msg("one")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Dispatch(ctx, "release.grabbed", msg("two")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE notification_deliveries SET status = 'sending' WHERE id = 2`); err != nil {
		t.Fatal(err)
	}
	// A new Service over the same database, as after a restart.
	fresh := NewService(s.db, s.bus, s.log)
	fresh.SetTransport(rec.send)
	fresh.poll = 10 * time.Millisecond
	rctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { fresh.RunWorker(rctx); close(done) }()
	waitFor(t, "both deliveries", func() bool { return rec.count() == 2 })
	cancel()
	<-done
	for _, id := range []int64{1, 2} {
		if status, _, _, _ := row(t, s, id); status != StatusSent {
			t.Errorf("row %d = %s, want sent", id, status)
		}
	}
}

// A connection whose endpoint hangs holds up only its own alerts; another connection's
// alert goes straight through, and the hung one never has two sends at once.
func TestSlowConnectionDoesNotBlockOthers(t *testing.T) {
	s, _, _ := newTestService(t)
	ctx := context.Background()
	mustCreate(t, s, Connection{Name: "slow", URL: "ntfy://slow", Events: sub("release.grabbed"), Enabled: true})
	mustCreate(t, s, Connection{Name: "fast", URL: "ntfy://fast", Events: sub("release.grabbed"), Enabled: true})
	release := make(chan struct{})
	var mu sync.Mutex
	inSlow, maxInSlow := 0, 0
	fastSent := make(chan struct{}, 10)
	s.SetTransport(func(ctx context.Context, url, _, _ string) error {
		if url == "ntfy://fast" {
			fastSent <- struct{}{}
			return nil
		}
		mu.Lock()
		inSlow++
		if inSlow > maxInSlow {
			maxInSlow = inSlow
		}
		mu.Unlock()
		<-release
		mu.Lock()
		inSlow--
		mu.Unlock()
		return nil
	})
	s.poll = 10 * time.Millisecond
	rctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { s.RunWorker(rctx); close(done) }()
	for i := 0; i < 3; i++ {
		if _, err := s.Dispatch(ctx, "release.grabbed", msg("x")); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 3; i++ {
		select {
		case <-fastSent:
		case <-time.After(3 * time.Second):
			t.Fatalf("the fast connection got %d of 3 while the slow one hung", i)
		}
	}
	close(release)
	waitFor(t, "the slow connection to drain", func() bool {
		var n int
		_ = s.db.QueryRow(`SELECT COUNT(*) FROM notification_deliveries WHERE status = 'sent'`).Scan(&n)
		return n == 6
	})
	cancel()
	<-done
	if maxInSlow != 1 {
		t.Errorf("the slow connection had %d sends at once, want 1", maxInSlow)
	}
}

func TestDeletedConnectionCascades(t *testing.T) {
	s, _, _ := newTestService(t)
	ctx := context.Background()
	c := mustCreate(t, s, Connection{Name: "a", URL: "ntfy://a", Events: sub("release.grabbed"), Enabled: true})
	if _, err := s.Dispatch(ctx, "release.grabbed", msg("x")); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(ctx, c.ID); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM notification_deliveries`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("deliveries left: %d (%v)", n, err)
	}
}

// A dedupe key queues once per connection however often it's dispatched; unsubscribed
// and disabled connections get nothing.
func TestDispatchOnceDedupes(t *testing.T) {
	s, rec, _ := newTestService(t)
	ctx := context.Background()
	mustCreate(t, s, Connection{Name: "a", URL: "ntfy://a", Events: sub("release.grabbed"), Enabled: true})
	mustCreate(t, s, Connection{Name: "b", URL: "ntfy://b", Events: sub("release.grabbed"), Enabled: true})
	mustCreate(t, s, Connection{Name: "off", URL: "ntfy://off", Events: sub("release.grabbed"), Enabled: false})
	mustCreate(t, s, Connection{Name: "other", URL: "ntfy://other", Events: sub("plex.buffering"), Enabled: true})
	for i := 0; i < 3; i++ {
		n, err := s.DispatchOnce(ctx, "release.grabbed", "grab:42", msg("Dune"))
		if err != nil {
			t.Fatal(err)
		}
		if want := map[bool]int{true: 2, false: 0}[i == 0]; n != want {
			t.Fatalf("dispatch %d queued %d, want %d", i, n, want)
		}
	}
	drainNow(t, s)
	if rec.count() != 2 {
		t.Fatalf("sends = %v, want one each to a and b", rec.got())
	}
	if n, _ := s.DispatchOnce(ctx, "release.grabbed", "grab:42", msg("Dune")); n != 0 {
		t.Errorf("re-dispatch after sending queued %d", n)
	}
}

// Switching a connection off fails what it still had waiting, with a reason.
func TestDisablingFailsQueued(t *testing.T) {
	s, rec, _ := newTestService(t)
	ctx := context.Background()
	c := mustCreate(t, s, Connection{Name: "a", URL: "ntfy://a", Events: sub("release.grabbed"), Enabled: true})
	if _, err := s.Dispatch(ctx, "release.grabbed", msg("x")); err != nil {
		t.Fatal(err)
	}
	c.Enabled = false
	if err := s.Update(ctx, c.ID, c); err != nil {
		t.Fatal(err)
	}
	drainNow(t, s)
	status, _, _, lastErr := row(t, s, 1)
	if status != StatusFailed || lastErr != "connection disabled" || rec.count() != 0 {
		t.Fatalf("row = %s %q, sends %d", status, lastErr, rec.count())
	}
}

// Test answers synchronously and lands in the saved connection's log.
func TestTestRecordsDelivery(t *testing.T) {
	s, rec, _ := newTestService(t)
	ctx := context.Background()
	c := mustCreate(t, s, Connection{Name: "a", URL: "ntfy://a", Enabled: true})
	if err := s.Test(ctx, c); err != nil {
		t.Fatal(err)
	}
	rec.fail["ntfy://a"] = true
	if err := s.Test(ctx, c); err == nil {
		t.Fatal("want the failure back")
	}
	log, err := s.Deliveries(ctx, c.ID, 10)
	if err != nil || len(log) != 2 || log[0].Status != StatusFailed || log[1].Status != StatusSent || log[0].EventKey != "test" {
		t.Fatalf("log = %+v, %v", log, err)
	}
	// An unsaved connection's Test has nowhere to be recorded.
	if err := s.Test(ctx, Connection{URL: "ntfy://b"}); err != nil {
		t.Fatal(err)
	}
}

type fakePusher struct {
	mu   sync.Mutex
	sent []string
	err  error
}

func (f *fakePusher) SendToUserResult(_ context.Context, uid int64, title, body, url string) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, fmt.Sprintf("%d|%s|%s|%s", uid, title, body, url))
	if f.err != nil {
		return 0, f.err
	}
	return 1, nil
}

// A push connection's alerts go to its user's devices through the Pusher, with the
// message's link — never through apprise — and stop once the user isn't staff.
func TestDeliverWebPushRoutesToPusher(t *testing.T) {
	s, rec, _ := newTestService(t)
	ctx := context.Background()
	p := &fakePusher{}
	staff := map[int64]bool{7: true}
	s.SetPusher(p, func(_ context.Context, uid int64) bool { return staff[uid] })
	mustCreate(t, s, Connection{Name: "My phone", Kind: KindWebPush, Config: PushConfigFor(7), Events: sub("release.grabbed", "plex.buffering"), Enabled: true})

	if _, err := s.Dispatch(ctx, "release.grabbed", Message{Title: "Grabbed", Body: "🎬 Dune", Link: "/downloads"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Dispatch(ctx, "plex.buffering", Message{Title: "Buffering", Body: "x"}); err != nil {
		t.Fatal(err)
	}
	drainNow(t, s)
	drainNow(t, s) // one at a time per connection
	if got := fmt.Sprint(p.sent); got != "[7|Grabbed|🎬 Dune|/downloads 7|Buffering|x|/]" {
		t.Fatalf("pushes = %s", got)
	}
	if rec.count() != 0 {
		t.Fatalf("apprise was called: %v", rec.got())
	}

	// Demoted: the next alert fails with a reason instead of reaching their phone.
	staff[7] = false
	if _, err := s.Dispatch(ctx, "release.grabbed", Message{Title: "Grabbed", Body: "again"}); err != nil {
		t.Fatal(err)
	}
	drainNow(t, s)
	if len(p.sent) != 2 {
		t.Fatalf("pushed to a user who isn't staff: %v", p.sent)
	}
	if _, attempts, _, lastErr := row(t, s, 3); attempts != 1 || lastErr == "" {
		t.Errorf("row 3: attempts %d, error %q", attempts, lastErr)
	}
}

func TestPruneDeliveries(t *testing.T) {
	s, _, _ := newTestService(t)
	clk := newClock()
	useClock(s, clk)
	ctx := context.Background()
	c := mustCreate(t, s, Connection{Name: "a", URL: "ntfy://a", Events: sub("release.grabbed"), Enabled: true})
	if err := s.Test(ctx, c); err != nil {
		t.Fatal(err)
	}
	clk.advance(31 * 24 * time.Hour)
	if _, err := s.Dispatch(ctx, "release.grabbed", msg("new")); err != nil {
		t.Fatal(err)
	}
	n, err := s.PruneDeliveries(ctx, HistoryKeep)
	if err != nil || n != 1 {
		t.Fatalf("pruned %d, %v; want the old test row only", n, err)
	}
}
