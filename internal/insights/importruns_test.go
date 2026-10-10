package insights

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// Undo removes one run's plays and nothing else: not another run's, not an older import's,
// not a live recording — and not when the confirmed count is stale.
func TestImportRunUndoDeletesOnlyThatRun(t *testing.T) {
	ctx := context.Background()
	s := newTestService(t)
	run1, err := s.StartImportRun(ctx, 11, 0)
	if err != nil {
		t.Fatal(err)
	}
	run2, _ := s.StartImportRun(ctx, 12, 0)

	c := s.ImportHistory(ctx, []ImportedSession{
		{UserID: 7, RatingKey: "1", Title: "A", StartedAt: 1000, StoppedAt: 2000},
		{UserID: 7, RatingKey: "2", Title: "B", StartedAt: 3000, StoppedAt: 4000},
	}, ImportOptions{RunID: run1})
	if c.Imported != 2 {
		t.Fatalf("run 1 counts = %+v", c)
	}
	s.ImportHistory(ctx, []ImportedSession{{UserID: 7, RatingKey: "3", Title: "C", StartedAt: 5000, StoppedAt: 6000}}, ImportOptions{RunID: run2})
	s.ImportHistory(ctx, []ImportedSession{{UserID: 7, RatingKey: "4", Title: "Old", StartedAt: 7000, StoppedAt: 8000}}, ImportOptions{})
	live := seedLive(t, s, "42", "7", "1", "A", 50_000, 53_000)
	var run1Row int64
	if err := s.repo.db.QueryRow(`SELECT id FROM stream_sessions WHERE import_run_id = ? LIMIT 1`, run1).Scan(&run1Row); err != nil {
		t.Fatal(err)
	}
	_ = s.repo.insertBufferEvent(ctx, run1Row, 1500, 0, 1000, "", "")
	_ = s.repo.insertBufferEvent(ctx, live, 51_000, 0, 1000, "", "")

	if _, err := s.RemoveImportRun(ctx, run1, 2); !errors.Is(err, ErrRunRunning) {
		t.Fatalf("undoing a running import: err = %v, want ErrRunRunning", err)
	}
	for _, id := range []int64{run1, run2} {
		if err := s.FinishImportRun(ctx, id, RunDone, 2, ImportCounts{Imported: 2}, ""); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.RemoveImportRun(ctx, run1, 3); !errors.Is(err, ErrRunRowsChanged) {
		t.Fatalf("stale count: err = %v, want ErrRunRowsChanged", err)
	}
	if countRows(t, s, "1=1") != 5 {
		t.Fatal("a refused undo deleted rows")
	}

	removed, err := s.RemoveImportRun(ctx, run1, 2)
	if err != nil || removed != 2 {
		t.Fatalf("removed %d, err %v; want 2", removed, err)
	}
	if n := countRows(t, s, "import_run_id = "+itoa(run1)); n != 0 {
		t.Errorf("%d of run 1's plays survived", n)
	}
	if countRows(t, s, "import_run_id = "+itoa(run2)) != 1 || countRows(t, s, "title = 'Old'") != 1 || countRows(t, s, "id = "+itoa(live)) != 1 {
		t.Error("undo touched plays that weren't run 1's")
	}
	var events int
	_ = s.repo.db.QueryRow(`SELECT COUNT(*) FROM buffer_events`).Scan(&events)
	if events != 1 {
		t.Errorf("buffer events = %d, want only the live play's left", events)
	}
	r, err := s.ImportRun(ctx, run1)
	if err != nil || r.Rows != 0 || r.RemovedRows != 2 || r.RemovedAt == 0 {
		t.Errorf("run after undo = %+v (err %v)", r, err)
	}
}

func TestInterruptedRunsMarkedOnBoot(t *testing.T) {
	ctx := context.Background()
	s := newTestService(t)
	running, _ := s.StartImportRun(ctx, 1, 0)
	done, _ := s.StartImportRun(ctx, 2, 0)
	if err := s.FinishImportRun(ctx, done, RunDone, 10, ImportCounts{Imported: 10}, ""); err != nil {
		t.Fatal(err)
	}
	if n, err := s.MarkInterruptedImports(ctx); err != nil || n != 1 {
		t.Fatalf("marked %d, err %v; want 1", n, err)
	}
	if r, _ := s.ImportRun(ctx, running); r.Status != RunInterrupted || r.Error == "" || r.FinishedAt == 0 {
		t.Errorf("left-running run = %+v, want interrupted with a reason", r)
	}
	if r, _ := s.ImportRun(ctx, done); r.Status != RunDone {
		t.Errorf("finished run = %+v, want left alone", r)
	}
	// Progress written after the run was closed (a straggling page) doesn't reopen it.
	if err := s.UpdateImportRun(ctx, running, 5, ImportCounts{Imported: 5}); err != nil {
		t.Fatal(err)
	}
	if r, _ := s.ImportRun(ctx, running); r.Status != RunInterrupted || r.Imported != 0 {
		t.Errorf("a late progress write changed a closed run: %+v", r)
	}
}

func TestTautulliKeyMasked(t *testing.T) {
	ctx := context.Background()
	s := newTestService(t)
	const secret = "s3cr3t-tautulli-key"
	if err := s.SaveTautulli(ctx, "http://tautulli:8181", secret); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(s.TautulliConfig(ctx))
	if strings.Contains(string(b), secret) || !strings.Contains(string(b), `"api_key_set":true`) {
		t.Errorf("config = %s, want the key masked as api_key_set", b)
	}
	// Saving again without a key keeps the saved one.
	if err := s.SaveTautulli(ctx, "http://tautulli:8181", ""); err != nil {
		t.Fatal(err)
	}
	if _, key := s.TautulliCredentials(ctx); key != secret {
		t.Errorf("key after a keyless save = %q, want it kept", key)
	}
}
