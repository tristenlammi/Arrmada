package requests

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tristenlammi/arrmada/internal/automation"
	"github.com/tristenlammi/arrmada/internal/jobs"
	"github.com/tristenlammi/arrmada/internal/movies"
	"github.com/tristenlammi/arrmada/internal/quality"
	"github.com/tristenlammi/arrmada/internal/store"
)

// fakeMovies is a Movies library in memory: Add needs no TMDB.
type fakeMovies struct {
	mu     sync.Mutex
	byTMDB map[int]movies.Movie
	next   int64
}

func newFakeMovies(have ...movies.Movie) *fakeMovies {
	f := &fakeMovies{byTMDB: map[int]movies.Movie{}}
	for _, m := range have {
		f.next++
		m.ID = f.next
		f.byTMDB[m.TMDBID] = m
	}
	return f
}

func (f *fakeMovies) Add(_ context.Context, tmdbID int, profile string, monitored bool) (movies.Movie, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if m, ok := f.byTMDB[tmdbID]; ok {
		return m, movies.ErrExists
	}
	f.next++
	m := movies.Movie{ID: f.next, TMDBID: tmdbID, Title: "film", Monitored: monitored, QualityProfile: profile}
	f.byTMDB[tmdbID] = m
	return m, nil
}

func (f *fakeMovies) Get(_ context.Context, id int64) (movies.Movie, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, m := range f.byTMDB {
		if m.ID == id {
			return m, nil
		}
	}
	return movies.Movie{}, movies.ErrNotFound
}

func (f *fakeMovies) List(context.Context) ([]movies.Movie, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []movies.Movie
	for _, m := range f.byTMDB {
		out = append(out, m)
	}
	return out, nil
}

func (f *fakeMovies) GetByTMDB(_ context.Context, tmdbID int) (movies.Movie, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if m, ok := f.byTMDB[tmdbID]; ok {
		return m, nil
	}
	return movies.Movie{}, movies.ErrNotFound
}

func (f *fakeMovies) SetMonitored(_ context.Context, id int64, monitored bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for k, m := range f.byTMDB {
		if m.ID == id {
			m.Monitored = monitored
			f.byTMDB[k] = m
			return nil
		}
	}
	return movies.ErrNotFound
}

func (f *fakeMovies) AddEvent(context.Context, int64, string, string) {}

func (f *fakeMovies) SearchStatesFor(context.Context, []int64) (map[int64]movies.SearchStamp, error) {
	return map[int64]movies.SearchStamp{}, nil
}

func (f *fakeMovies) ByTMDBIDs(_ context.Context, ids []int) ([]movies.Movie, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []movies.Movie
	for _, id := range ids {
		if m, ok := f.byTMDB[id]; ok {
			out = append(out, m)
		}
	}
	return out, nil
}

// fakeSearcher runs searches through fn; the movie search queue is the real one.
type fakeSearcher struct {
	*automation.Coordinator
	fn func(ctx context.Context, id int64)
}

func (f *fakeSearcher) SearchMovie(ctx context.Context, id int64) (automation.SearchOutcome, error) {
	if f.fn != nil {
		f.fn(ctx, id)
	}
	return automation.SearchOutcome{}, nil
}

func (f *fakeSearcher) SearchSeriesNow(ctx context.Context, id int64) (automation.SearchOutcome, error) {
	return f.SearchMovie(ctx, id)
}

// fakePush records Web Push sends.
type fakePush struct {
	mu    sync.Mutex
	users []int64
}

func (p *fakePush) SendToUserAsync(userID int64, _, _, _ string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.users = append(p.users, userID)
}

func (p *fakePush) sent() []int64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]int64(nil), p.users...)
}

// quietFixture is a Service over a scratch store with an in-memory Movies library, a
// recording push sender and a recording job submitter.
func quietFixture(t *testing.T, have ...movies.Movie) (*Service, *fakePush, *recordJobs) {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	push, rec := &fakePush{}, &recordJobs{}
	s := &Service{
		repo: NewRepo(st.DB()), movies: newFakeMovies(have...), coord: &fakeSearcher{Coordinator: &automation.Coordinator{}},
		quality: quality.NewService(st.DB()), push: push, jobs: rec, log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	return s, push, rec
}

func inboxRefs(t *testing.T, s *Service, uid int64) []string {
	t.Helper()
	inbox, err := s.repo.listUserNotifications(context.Background(), uid)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, n := range inbox {
		out = append(out, n.Ref)
	}
	return out
}

// A user whose requests auto-approve isn't told their own click was approved — no inbox
// row, no push — though the search still starts. Someone following the request still hears.
func TestAutoApproveSendsNoDecisionNotification(t *testing.T) {
	s, push, rec := quietFixture(t)
	ctx := context.Background()

	got, _, err := s.Create(ctx, Request{MediaType: "movie", TMDBID: 603, Title: "The Matrix", RequestedBy: 7, RequestedByName: "alice"},
		CreateOptions{AutoApprove: true})
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != StatusApproved {
		t.Fatalf("status = %q, want approved", got.Status)
	}
	if refs := inboxRefs(t, s, 7); len(refs) != 0 || len(push.sent()) != 0 {
		t.Fatalf("the requester was told about their own auto-approval: inbox %v, push %v", refs, push.sent())
	}
	if len(rec.specs) != 1 || rec.specs[0].Kind != "movie.search" {
		t.Fatalf("searches started = %+v, want one movie.search", rec.specs)
	}

	// An auto-approval of a request someone already follows: they hear, the requester doesn't.
	pending, _, err := s.Create(ctx, Request{MediaType: "movie", TMDBID: 604, Title: "Heat", RequestedBy: 7}, CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.repo.AddSubscriber(ctx, pending.ID, 8, "bob"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Approve(ctx, pending.ID, ApproveOptions{DecidedBy: 7, Auto: true}); err != nil {
		t.Fatal(err)
	}
	if refs := inboxRefs(t, s, 7); len(refs) != 0 {
		t.Errorf("requester inbox = %v, want empty", refs)
	}
	if refs := inboxRefs(t, s, 8); len(refs) != 1 || refs[0] != "movie:604:approved" {
		t.Errorf("follower inbox = %v, want the approval", refs)
	}
}

// Staff approving their own request hear nothing; approving someone else's tells the
// requester exactly once.
func TestStaffApprovingOwnRequestIsSilent(t *testing.T) {
	s, push, _ := quietFixture(t)
	ctx := context.Background()

	own, _, err := s.Create(ctx, Request{MediaType: "movie", TMDBID: 1, Title: "Mine", RequestedBy: 1}, CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Approve(ctx, own.ID, ApproveOptions{DecidedBy: 1, DecidedByName: "admin"}); err != nil {
		t.Fatal(err)
	}
	if refs := inboxRefs(t, s, 1); len(refs) != 0 || len(push.sent()) != 0 {
		t.Fatalf("the admin was told about their own approval: %v / %v", refs, push.sent())
	}

	theirs, _, err := s.Create(ctx, Request{MediaType: "movie", TMDBID: 2, Title: "Theirs", RequestedBy: 7}, CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Approve(ctx, theirs.ID, ApproveOptions{DecidedBy: 1}); err != nil {
		t.Fatal(err)
	}
	if refs := inboxRefs(t, s, 7); len(refs) != 1 || refs[0] != "movie:2:approved" {
		t.Errorf("requester inbox = %v, want one approval", refs)
	}
	if got := push.sent(); len(got) != 1 || got[0] != 7 {
		t.Errorf("pushes = %v, want one to the requester", got)
	}
	if refs := inboxRefs(t, s, 1); len(refs) != 0 {
		t.Errorf("approver inbox = %v, want empty", refs)
	}
}

// An import is silent: nobody's inbox fills with 'approved', no search is queued, and a
// title already on the shelf is stamped ready so the ready sweep never tells anyone.
func TestImportSilentAndPreSeedsReady(t *testing.T) {
	s, push, rec := quietFixture(t, movies.Movie{TMDBID: 603, Title: "The Matrix", HasFile: true})
	ctx := context.Background()
	quiet := CreateOptions{AutoApprove: true, Silent: true, DeferSearch: true}

	have, _, err := s.Create(ctx, Request{MediaType: "movie", TMDBID: 603, Title: "The Matrix", RequestedBy: 7}, quiet)
	if err != nil {
		t.Fatal(err)
	}
	missing, _, err := s.Create(ctx, Request{MediaType: "movie", TMDBID: 604, Title: "Heat", RequestedBy: 7}, quiet)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{have.ID, missing.ID} {
		if err := s.MarkReadyIfAvailable(ctx, id); err != nil {
			t.Fatal(err)
		}
	}
	if got, _ := s.repo.Get(ctx, have.ID); got.ReadyAt == 0 || got.Status != StatusApproved {
		t.Errorf("the title already on the shelf: status %q ready_at %d, want approved and stamped", got.Status, got.ReadyAt)
	}
	if got, _ := s.repo.Get(ctx, missing.ID); got.ReadyAt != 0 {
		t.Errorf("the missing title was stamped ready")
	}
	if err := s.SweepReadyRequests(ctx); err != nil {
		t.Fatal(err)
	}
	if refs := inboxRefs(t, s, 7); len(refs) != 0 || len(push.sent()) != 0 {
		t.Fatalf("an import notified: inbox %v, push %v", refs, push.sent())
	}
	if len(rec.specs) != 0 {
		t.Fatalf("an import queued %d searches; the sweeps should pick them up", len(rec.specs))
	}
}

// DeferSearch adds the title but starts no search.
func TestDeferSearchDoesNotEnqueue(t *testing.T) {
	s, _, rec := quietFixture(t)
	ctx := context.Background()
	req, _, err := s.Create(ctx, Request{MediaType: "movie", TMDBID: 9, Title: "Up", RequestedBy: 7}, CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Approve(ctx, req.ID, ApproveOptions{DeferSearch: true}); err != nil {
		t.Fatal(err)
	}
	if len(rec.specs) != 0 {
		t.Fatalf("submitted %d searches, want none", len(rec.specs))
	}
	if m, err := s.movies.ByTMDBIDs(ctx, []int{9}); err != nil || len(m) != 1 || !m[0].Monitored {
		t.Fatalf("the title wasn't added monitored: %+v %v", m, err)
	}
}

// Approving 20 requests at once never runs more than two of their searches at a time, and
// no approval waits on a search: they queue in the job runner's indexer-search class.
func TestSearchQueueConcurrencyBound(t *testing.T) {
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
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		select {
		case <-release:
		case <-ctx.Done():
		}
		running.Add(-1)
		done.Add(1)
	}
	s.SetJobs(runner)

	start := time.Now()
	for i := 0; i < 20; i++ {
		req, _, err := s.Create(ctx, Request{MediaType: "movie", TMDBID: 100 + i, Title: "film", RequestedBy: 7}, CreateOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.Approve(ctx, req.ID, ApproveOptions{DecidedBy: 1}); err != nil {
			t.Fatal(err)
		}
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("approving took %v: an approval waited on its search", d)
	}
	waitUntil(t, "two searches running", func() bool { return running.Load() == 2 })
	time.Sleep(100 * time.Millisecond) // a third would have started by now
	if p := peak.Load(); p > 2 {
		t.Fatalf("%d searches ran at once, want at most 2", p)
	}
	close(release)
	waitUntil(t, "every search to run", func() bool { return done.Load() == 20 })
	if p := peak.Load(); p > 2 {
		t.Fatalf("%d searches ran at once, want at most 2", p)
	}
}

func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
