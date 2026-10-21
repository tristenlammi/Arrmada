package subtitles

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"reflect"
	"testing"

	"github.com/tristenlammi/arrmada/internal/store"
)

// queueDB is a temp database with the migrations applied.
func queueDB(t *testing.T) *sql.DB {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st.DB().(*sql.DB)
}

// bootQueue is a Service as NewService leaves it over db: the saved queue loaded. Calling
// it again on the same db is a restart.
func bootQueue(t *testing.T, db store.Pool) *Service {
	t.Helper()
	s := &Service{
		log:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		wake:   make(chan struct{}, 1),
		jobsDB: newJobStore(db),
	}
	if err := s.loadQueue(context.Background()); err != nil {
		t.Fatal(err)
	}
	return s
}

func popIDs(s *Service) []int64 {
	var ids []int64
	for j := s.pop(); j != nil; j = s.pop() {
		ids = append(ids, j.ID)
	}
	return ids
}

// An update restarts the app mid-queue: the same jobs come back, with the same ids, in the
// same priority order.
func TestQueueSurvivesRestart(t *testing.T) {
	db := queueDB(t)
	a := bootQueue(t, db)
	a.enqueue(&Job{Kind: "movie", MovieID: 1, Title: "swept 1", Priority: PrioSweep})
	a.enqueue(&Job{Kind: "movie", MovieID: 2, Title: "swept 2", Priority: PrioSweep})
	a.enqueue(&Job{Kind: "episode", SeriesID: 7, Season: 1, Episode: 2, Title: "new", Priority: PrioImport, Redo: true})
	a.enqueue(&Job{Kind: "movie", MovieID: 3, Title: "button", Priority: PrioManual})
	want := []int64{3, 4, 1, 2} // import, manual, then the sweep oldest first

	b := bootQueue(t, db)
	if b.Pending() != 4 {
		t.Fatalf("pending after restart = %d, want 4", b.Pending())
	}
	var ep Job
	for _, j := range b.Jobs() {
		if j.Kind == "episode" {
			ep = j
		}
	}
	if ep.SeriesID != 7 || ep.Season != 1 || ep.Episode != 2 || !ep.Redo || ep.Title != "new" || ep.Lane != LaneFast {
		t.Errorf("restored episode job = %+v", ep)
	}
	if got := popIDs(b); !reflect.DeepEqual(got, want) {
		t.Errorf("order after restart = %v, want %v", got, want)
	}
	// The dedupe survives too: queuing a restored file again is the same job.
	c := bootQueue(t, db)
	if j := c.enqueue(&Job{Kind: "movie", MovieID: 2, Title: "swept 2", Priority: PrioSweep}); j.ID != 2 {
		t.Errorf("re-queue after restart made job %d, want the restored job 2", j.ID)
	}
	// New ids carry on from the saved ones instead of starting again at 1.
	if j := c.enqueue(&Job{Kind: "movie", MovieID: 9, Title: "later"}); j.ID != 5 {
		t.Errorf("first id after restart = %d, want 5", j.ID)
	}
}

// A job the restart cut short goes back in line, says why, and keeps its id (a reported
// problem's fix is waiting on that id).
func TestRunningJobRequeuedOnStartup(t *testing.T) {
	db := queueDB(t)
	a := bootQueue(t, db)
	job := a.enqueue(&Job{Kind: "movie", MovieID: 1, Title: "Heat", Priority: PrioManual})
	a.enqueue(&Job{Kind: "movie", MovieID: 2, Title: "Ran", Priority: PrioSweep})
	a.pop()
	a.markRunning(job)

	b := bootQueue(t, db)
	got, ok, err := b.Job(context.Background(), job.ID)
	if err != nil || !ok {
		t.Fatalf("Job(%d) after restart: ok=%v err=%v", job.ID, ok, err)
	}
	if got.State != StateQueued || got.Note != interruptedNote || got.StartedAt != 0 {
		t.Errorf("interrupted job = %+v, want queued with note %q", got, interruptedNote)
	}
	if first := b.pop(); first == nil || first.ID != job.ID {
		t.Errorf("first pop after restart = %+v, want the interrupted manual job", first)
	}
}

// Ensure on a file the sweep queued moves it up a line, and the move is saved: after a
// restart it still runs before the sweep.
func TestPriorityBumpPersists(t *testing.T) {
	db := queueDB(t)
	a := bootQueue(t, db)
	a.enqueue(&Job{Kind: "movie", MovieID: 1, Title: "a", Priority: PrioSweep})
	b2 := a.enqueue(&Job{Kind: "movie", MovieID: 2, Title: "b", Priority: PrioSweep})
	a.enqueue(&Job{Kind: "movie", MovieID: 2, Title: "b", Priority: PrioManual, Redo: true})

	b := bootQueue(t, db)
	first := b.pop()
	if first == nil || first.ID != b2.ID || first.Priority != PrioManual || !first.Redo {
		t.Errorf("first pop after restart = %+v, want job %d bumped to manual with redo", first, b2.ID)
	}
}

// A finished job keeps its state, note, run and per-language outcomes across a restart,
// and Cancel / Clear queue are saved as cancelled.
func TestFinishedJobsSurviveRestart(t *testing.T) {
	db := queueDB(t)
	a := bootQueue(t, db)
	done := a.enqueue(&Job{Kind: "movie", MovieID: 1, Title: "Heat", Priority: PrioManual})
	gone := a.enqueue(&Job{Kind: "movie", MovieID: 2, Title: "Ran", Priority: PrioSweep})
	cleared := a.enqueue(&Job{Kind: "movie", MovieID: 3, Title: "Alien", Priority: PrioSweep})
	if a.pop() != done {
		t.Fatal("pop did not take the manual job first")
	}
	a.markRunning(done)
	a.update(done, func(j *Job) { j.RunID = "run-1" })
	outs := []Outcome{
		{Lang: "en", Rung: RungOpenSubtitles, Result: ResultBelowScore, Detail: "best scored 40", Score: 40, Reasons: []string{"fps differs"}},
		{Lang: "en", Rung: RungAI, Result: ResultOK, Detail: "AI-generated"},
	}
	a.finishOutcomes(done, StateDone, "en: best scored 40 → AI-generated", outs)
	if err := a.Cancel(gone.ID); err != nil {
		t.Fatal(err)
	}
	a.ClearQueue()

	b := bootQueue(t, db)
	if b.Pending() != 0 {
		t.Errorf("pending after restart = %d, want 0", b.Pending())
	}
	got, ok, _ := b.Job(context.Background(), done.ID)
	if !ok || got.State != StateDone || got.RunID != "run-1" || got.FinishedAt == 0 || !reflect.DeepEqual(got.Outcomes, outs) {
		t.Errorf("finished job after restart = %+v", got)
	}
	for _, id := range []int64{gone.ID, cleared.ID} {
		if j, ok, _ := b.Job(context.Background(), id); !ok || j.State != StateCancelled {
			t.Errorf("job %d after restart = %+v, want cancelled", id, j)
		}
	}
	// Finished jobs are history, not queue: the file can be queued again.
	if j := b.enqueue(&Job{Kind: "movie", MovieID: 1, Title: "Heat"}); j.ID == done.ID {
		t.Error("a finished job blocked re-queuing its file after a restart")
	}
}

// A forced-track repair and a replace job for the same file are separate jobs, and both
// come back from a restart with what they carry.
func TestRepairAndReplaceJobsSurviveRestart(t *testing.T) {
	db := queueDB(t)
	a := bootQueue(t, db)
	repair := a.enqueue(&Job{Kind: "movie", MovieID: 1, Title: "Heat", Mode: ModeRepairForced, Priority: PrioSweep, repairLangs: []string{"en"}})
	replace := a.enqueue(&Job{Kind: "movie", MovieID: 1, Title: "Heat", Priority: PrioManual, retire: []orphanRetire{{Name: "Heat.eng.srt", Lang: "en"}}})
	if repair == replace {
		t.Fatal("the replace job was folded into the repair")
	}
	// A second replace for the same movie joins the queued one, and that is saved too.
	a.enqueue(&Job{Kind: "movie", MovieID: 1, Title: "Heat", Priority: PrioManual, retire: []orphanRetire{{Name: "Heat.srt", Lang: "en"}}})

	b := bootQueue(t, db)
	got := map[int64]Job{}
	for _, j := range b.Jobs() {
		got[j.ID] = j
	}
	if r := got[repair.ID]; r.Mode != ModeRepairForced || !reflect.DeepEqual(r.repairLangs, []string{"en"}) {
		t.Errorf("restored repair job = %+v", r)
	}
	want := []orphanRetire{{Name: "Heat.eng.srt", Lang: "en"}, {Name: "Heat.srt", Lang: "en"}}
	if r := got[replace.ID]; r.Mode != "" || !reflect.DeepEqual(r.retire, want) {
		t.Errorf("restored replace job retires %+v, want %+v", r.retire, want)
	}
	// Still deduped on file plus mode after the restart.
	if j := b.enqueue(&Job{Kind: "movie", MovieID: 1, Title: "Heat", Mode: ModeRepairForced, Priority: PrioSweep}); j.ID != repair.ID {
		t.Errorf("re-queued repair = job %d, want %d", j.ID, repair.ID)
	}
}

// The table keeps the newest 500 finished jobs and every queued one.
func TestHistoryPrunedTo500(t *testing.T) {
	db := queueDB(t)
	s := bootQueue(t, db)
	var batch []*Job
	for i := int64(1); i <= jobHistoryKeep+20; i++ {
		batch = append(batch, &Job{Kind: "movie", MovieID: i, Title: "m"})
	}
	s.enqueueJobs("test", batch)
	for j := s.pop(); j != nil; j = s.pop() {
		s.finish(j, StateSkipped, "nothing to do")
	}
	waiting := s.enqueue(&Job{Kind: "movie", MovieID: 9999, Title: "still queued"})
	s.PruneJobs(context.Background())

	var finished, queued int
	if err := db.QueryRow(`SELECT COUNT(*) FROM subtitle_jobs WHERE state = 'skipped'`).Scan(&finished); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM subtitle_jobs WHERE state = 'queued'`).Scan(&queued); err != nil {
		t.Fatal(err)
	}
	if finished != jobHistoryKeep || queued != 1 {
		t.Errorf("after prune: %d finished, %d queued; want %d and 1", finished, queued, jobHistoryKeep)
	}
	// The oldest went, the newest stayed.
	if _, ok, _ := s.jobsDB.get(context.Background(), 1); ok {
		t.Error("the oldest finished job survived the prune")
	}
	if _, ok, _ := s.jobsDB.get(context.Background(), int64(jobHistoryKeep+20)); !ok {
		t.Error("the newest finished job was pruned")
	}
	// Pruned ids are never handed out again.
	b := bootQueue(t, db)
	if j := b.enqueue(&Job{Kind: "movie", MovieID: 1, Title: "again"}); j.ID <= waiting.ID {
		t.Errorf("new job id %d reuses an old one (last was %d)", j.ID, waiting.ID)
	}
}

// JobHistory pages the saved history newest first, filtered by state.
func TestJobHistoryPages(t *testing.T) {
	db := queueDB(t)
	s := bootQueue(t, db)
	for i := int64(1); i <= 6; i++ {
		j := s.enqueue(&Job{Kind: "movie", MovieID: i, Title: "m"})
		s.pop()
		state := StateDone
		if i%2 == 0 {
			state = StateFailed
		}
		s.finish(j, state, "")
	}
	live := s.enqueue(&Job{Kind: "movie", MovieID: 7, Title: "m"})
	ctx := context.Background()
	page, err := s.JobHistory(ctx, JobQuery{Limit: 3})
	if err != nil || len(page) != 3 || page[0].ID != live.ID || page[2].ID != 5 {
		t.Fatalf("first page = %v (%v), want ids 7, 6, 5", ids(page), err)
	}
	page, _ = s.JobHistory(ctx, JobQuery{Before: 5, Limit: 3})
	if got := ids(page); !reflect.DeepEqual(got, []int64{4, 3, 2}) {
		t.Errorf("second page = %v, want [4 3 2]", got)
	}
	page, _ = s.JobHistory(ctx, JobQuery{State: StateFailed})
	if got := ids(page); !reflect.DeepEqual(got, []int64{6, 4, 2}) {
		t.Errorf("failed only = %v, want [6 4 2]", got)
	}
	// A live job reads from memory, with its stage.
	s.pop()
	s.markRunning(live)
	s.update(live, func(j *Job) { j.Stage = "searching OpenSubtitles" })
	page, _ = s.JobHistory(ctx, JobQuery{Limit: 1})
	if len(page) != 1 || page[0].Stage != "searching OpenSubtitles" {
		t.Errorf("live row = %+v, want the in-memory stage", page)
	}
}

func ids(js []Job) []int64 {
	var out []int64
	for _, j := range js {
		out = append(out, j.ID)
	}
	return out
}

// The sweep queues its whole batch in one go and it is all saved.
func TestSweepQueueIsSaved(t *testing.T) {
	s, _ := sweepFixture(t)
	s.jobsDB = newJobStore(s.ledger.db)
	if err := s.loadQueue(context.Background()); err != nil {
		t.Fatal(err)
	}
	n, err := s.SweepMissing(context.Background(), "movies")
	if err != nil || n != 2 {
		t.Fatalf("sweep queued %d (%v), want 2", n, err)
	}
	b := bootQueue(t, s.ledger.db)
	if b.Pending() != 2 {
		t.Errorf("pending after restart = %d, want the sweep's 2", b.Pending())
	}
	// A second sweep over the same files adds nothing.
	if _, err := s.SweepMissing(context.Background(), "movies"); err != nil {
		t.Fatal(err)
	}
	var rows int
	if err := s.ledger.db.QueryRow(`SELECT COUNT(*) FROM subtitle_jobs`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 2 {
		t.Errorf("%d rows after a second sweep, want 2", rows)
	}
}
