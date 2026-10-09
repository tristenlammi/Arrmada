package requests

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tristenlammi/arrmada/internal/jobs"
)

// A bulk approve reports each request on its own: an unknown id or one that isn't
// pending fails alone, and the rest are approved.
func TestBulkApproveReportsPerItem(t *testing.T) {
	s, _, rec := quietFixture(t)
	ctx := context.Background()
	newReq := func(tmdb int) int64 {
		r, _, err := s.Create(ctx, Request{MediaType: "movie", TMDBID: tmdb, Title: "x", RequestedBy: 7}, CreateOptions{})
		if err != nil {
			t.Fatal(err)
		}
		return r.ID
	}
	p1, p2, declined := newReq(1), newReq(2), newReq(3)
	if err := s.Decline(ctx, declined, DeclineOptions{}); err != nil {
		t.Fatal(err)
	}
	res := s.Bulk(ctx, BulkApprove, []int64{p1, 999, declined, p2}, "", 1, "admin")
	if len(res) != 4 {
		t.Fatalf("results = %+v", res)
	}
	want := []bool{true, false, false, true}
	for i, r := range res {
		if r.OK != want[i] || (!r.OK && r.Error == "") {
			t.Errorf("result %d = %+v, want ok %v", i, r, want[i])
		}
	}
	for _, id := range []int64{p1, p2} {
		if got, _ := s.repo.Get(ctx, id); got.Status != StatusApproved {
			t.Errorf("request %d: %s, want approved", id, got.Status)
		}
	}
	if got, _ := s.repo.Get(ctx, declined); got.Status != StatusDeclined {
		t.Errorf("a declined request was approved by a bulk action")
	}
	if len(rec.specs) != 2 {
		t.Errorf("searches started = %d, want 2", len(rec.specs))
	}
	// A bulk decline tells each requester once.
	p3 := newReq(4)
	if res := s.Bulk(ctx, BulkDecline, []int64{p3}, "", 1, "admin"); !res[0].OK {
		t.Fatalf("decline: %+v", res)
	}
	if refs := inboxRefs(t, s, 7); len(refs) != 4 { // three approved/declined above, plus this
		t.Errorf("requester inbox = %v", refs)
	}
}

// Bulk approvals queue their searches in the job runner: never more than two at a time.
func TestBulkUsesSearchQueue(t *testing.T) {
	s, _, _ := quietFixture(t)
	ctx := context.Background()
	runner, err := jobs.New(ctx, s.repo.db, s.log, nil)
	if err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{})
	t.Cleanup(func() { runner.Shutdown(5 * time.Second) })
	var running, peak, done atomic.Int32
	s.coord.(*fakeSearcher).fn = func(ctx context.Context, _ int64) {
		n := running.Add(1)
		for p := peak.Load(); n > p && !peak.CompareAndSwap(p, n); p = peak.Load() {
		}
		select {
		case <-release:
		case <-ctx.Done():
		}
		running.Add(-1)
		done.Add(1)
	}
	s.SetJobs(runner)
	var ids []int64
	for i := 0; i < 10; i++ {
		r, _, err := s.Create(ctx, Request{MediaType: "movie", TMDBID: 200 + i, Title: "x", RequestedBy: 7}, CreateOptions{})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, r.ID)
	}
	for _, r := range s.Bulk(ctx, BulkApprove, ids, "", 1, "admin") {
		if !r.OK {
			t.Fatalf("approve %d: %s", r.ID, r.Error)
		}
	}
	waitUntil(t, "two searches running", func() bool { return running.Load() == 2 })
	time.Sleep(100 * time.Millisecond)
	close(release)
	waitUntil(t, "every search", func() bool { return done.Load() == 10 })
	if p := peak.Load(); p > 2 {
		t.Fatalf("%d searches at once, want at most 2", p)
	}
}
