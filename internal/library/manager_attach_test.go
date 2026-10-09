package library

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/tristenlammi/arrmada/internal/store"
)

// fakeAttach records every attach call and answers from a script: the first len(script)
// calls take their outcome from it, later calls the last entry.
type fakeAttach struct {
	mu     sync.Mutex
	calls  []ImportRecord
	script []attachAnswer
}

type attachAnswer struct {
	outcome AttachOutcome
	err     error
	panic   bool
}

func (f *fakeAttach) fn(_ context.Context, rec ImportRecord) (AttachOutcome, error) {
	f.mu.Lock()
	f.calls = append(f.calls, rec)
	i := len(f.calls) - 1
	if i >= len(f.script) {
		i = len(f.script) - 1
	}
	a := f.script[i]
	f.mu.Unlock()
	if a.panic {
		var m map[string]int
		m["boom"] = 1
	}
	return a.outcome, a.err
}

func (f *fakeAttach) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

// attachManager is a Manager over a fresh store with the fake attach installed. It also
// returns the store's directory, so a test can open a second Manager on the same data
// (a restart).
func attachManager(t *testing.T, lib string, fa *fakeAttach) (*Manager, *sql.DB) {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	m := NewManager(st.DB(), lib, nil, quiet())
	m.SetAttach(fa.fn)
	return m, st.DB()
}

type attachRow struct {
	state, reason string
	attempts      int
	nextAt        int64
	release       string
	year          int
}

func readAttach(t *testing.T, db *sql.DB, hash string) attachRow {
	t.Helper()
	var r attachRow
	if err := db.QueryRow(`SELECT attach_state, attach_error, attach_attempts, attach_next_at, release_name, year
		FROM imports WHERE download_hash = ?`, hash).Scan(&r.state, &r.reason, &r.attempts, &r.nextAt, &r.release, &r.year); err != nil {
		t.Fatalf("read import %s: %v", hash, err)
	}
	return r
}

// makeDue pulls a pending row's retry time into the past, standing in for the wait.
func makeDue(t *testing.T, db *sql.DB, hash string) {
	t.Helper()
	if _, err := db.Exec(`UPDATE imports SET attach_next_at = 0 WHERE download_hash = ?`, hash); err != nil {
		t.Fatal(err)
	}
}

func oneCandidate(t *testing.T, hash, name string) []Candidate {
	t.Helper()
	src := t.TempDir()
	video := filepath.Join(src, name+".mkv")
	writeFile(t, video, 1000)
	return []Candidate{{Hash: hash, Name: name, ContentPath: video}}
}

// An attach that fails is kept pending with its error and a retry time, and the retry
// (once due) attaches it — from the stored row, without importing anything again.
func TestAttachFailureIsRetriedUntilAttached(t *testing.T) {
	ctx := context.Background()
	fa := &fakeAttach{script: []attachAnswer{
		{outcome: AttachRetry, err: errors.New("database is locked")},
		{outcome: Attached},
	}}
	m, db := attachManager(t, t.TempDir(), fa)

	cands := oneCandidate(t, "h1", "Dune.2021.1080p.WEB-DL")
	if n := m.Process(ctx, cands); n != 1 {
		t.Fatalf("imported %d, want 1", n)
	}
	row := readAttach(t, db, "h1")
	if row.state != AttachStatePending || row.attempts != 1 || row.reason != "database is locked" || row.nextAt == 0 {
		t.Fatalf("after a failed attach: %+v", row)
	}
	if row.release != "Dune.2021.1080p.WEB-DL" || row.year != 2021 {
		t.Fatalf("the retry needs the release and year: %+v", row)
	}
	if fa.count() != 1 {
		t.Fatalf("attach calls = %d, want 1", fa.count())
	}

	// Not due yet: nothing happens.
	m.RetryPendingAttach(ctx)
	if fa.count() != 1 {
		t.Fatalf("retried before its time: %d calls", fa.count())
	}

	makeDue(t, db, "h1")
	m.RetryPendingAttach(ctx)
	if got := readAttach(t, db, "h1"); got.state != AttachStateAttached {
		t.Fatalf("after the retry: %+v", got)
	}
	if fa.count() != 2 {
		t.Fatalf("attach calls = %d, want 2", fa.count())
	}
	last := fa.calls[1]
	if last.ReleaseName != "Dune.2021.1080p.WEB-DL" || last.Year != 2021 || last.Hash != "h1" || last.TargetPath == "" {
		t.Fatalf("retry was given %+v", last)
	}

	// Settled: further sweeps leave it alone.
	makeDue(t, db, "h1")
	m.RetryPendingAttach(ctx)
	if fa.count() != 2 {
		t.Fatalf("an attached import was attached again")
	}
}

// A crash between recording an import and attaching it leaves a pending row. A fresh
// Manager (the restarted app) attaches it on its next sweep — with no candidates at all,
// i.e. after the torrent has left the download client — and without importing again.
func TestPendingAttachSurvivesRestart(t *testing.T) {
	ctx := context.Background()
	lib := t.TempDir()
	target := filepath.Join(lib, "Dune (2021)", "Dune (2021).mkv")
	writeFile(t, target, 1000)

	fa := &fakeAttach{script: []attachAnswer{{outcome: Attached}}}
	m, db := attachManager(t, lib, fa)
	if err := m.repo.record(ctx, ImportRecord{
		Hash: "crash1", SourcePath: "/downloads/x.mkv", TargetPath: target, Title: "Dune",
		ReleaseName: "Dune.2021.2160p.WEB-DL", Year: 2021,
	}, AttachStatePending); err != nil {
		t.Fatal(err)
	}

	// The download is gone from the client: Process sees no candidates.
	if n := m.Process(ctx, nil); n != 0 {
		t.Fatalf("imported %d, want 0 — nothing should be re-imported", n)
	}
	if got := readAttach(t, db, "crash1"); got.state != AttachStateAttached {
		t.Fatalf("pending attach after restart: %+v", got)
	}
	if fa.count() != 1 || fa.calls[0].TargetPath != target || fa.calls[0].ReleaseName != "Dune.2021.2160p.WEB-DL" {
		t.Fatalf("attach calls = %+v", fa.calls)
	}
}

// Unmatched and refused are final: the reason is kept and attach isn't called again.
func TestFinalAttachOutcomesAreNotRetried(t *testing.T) {
	for _, tc := range []struct {
		name    string
		answer  attachAnswer
		state   string
		wantMsg string
	}{
		{"unmatched", attachAnswer{outcome: Unmatched, err: errors.New(`no movie in the library matches "Dune (2021)"`)}, AttachStateUnmatched, `no movie in the library matches "Dune (2021)"`},
		{"refused", attachAnswer{outcome: Refused, err: errors.New("import is lower quality than the existing file (720p < 2160p)")}, AttachStateRefused, "import is lower quality than the existing file (720p < 2160p)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			fa := &fakeAttach{script: []attachAnswer{tc.answer}}
			m, db := attachManager(t, t.TempDir(), fa)
			m.Process(ctx, oneCandidate(t, "h", "Dune.2021.720p.WEB-DL"))
			row := readAttach(t, db, "h")
			if row.state != tc.state || row.reason != tc.wantMsg {
				t.Fatalf("row = %+v, want %s with the reason", row, tc.state)
			}
			makeDue(t, db, "h")
			m.RetryPendingAttach(ctx)
			m.Process(ctx, nil)
			if fa.count() != 1 {
				t.Fatalf("attach calls = %d, want 1", fa.count())
			}
		})
	}
}

// Rows from before attach states existed default to 'attached' and are never touched; a
// pending row whose file has vanished becomes 'gone' without calling attach.
func TestLegacyRowsUntouchedAndMissingTargetIsGone(t *testing.T) {
	ctx := context.Background()
	lib := t.TempDir()
	fa := &fakeAttach{script: []attachAnswer{{outcome: Attached}}}
	m, db := attachManager(t, lib, fa)

	legacy := filepath.Join(lib, "Old (1999)", "Old (1999).mkv")
	writeFile(t, legacy, 10)
	// The insert an older version made: no attach columns at all.
	if _, err := db.Exec(`INSERT INTO imports (download_hash, source_path, target_path, title, size_bytes)
		VALUES ('legacy', '', ?, 'Old', 10)`, legacy); err != nil {
		t.Fatal(err)
	}
	if err := m.repo.record(ctx, ImportRecord{
		Hash: "vanished", TargetPath: filepath.Join(lib, "Gone (2020)", "Gone (2020).mkv"), Title: "Gone", Year: 2020,
	}, AttachStatePending); err != nil {
		t.Fatal(err)
	}

	m.RetryPendingAttach(ctx)

	if fa.count() != 0 {
		t.Fatalf("attach called %d times, want 0", fa.count())
	}
	if got := readAttach(t, db, "legacy"); got.state != AttachStateAttached {
		t.Fatalf("legacy row = %+v", got)
	}
	if got := readAttach(t, db, "vanished"); got.state != AttachStateGone {
		t.Fatalf("vanished row = %+v", got)
	}
}

// A panic inside the attach is that import's failure — retried later — not the sweep's.
func TestPanickingAttachStaysPending(t *testing.T) {
	ctx := context.Background()
	fa := &fakeAttach{script: []attachAnswer{{panic: true}, {outcome: Attached}}}
	m, db := attachManager(t, t.TempDir(), fa)

	if n := m.Process(ctx, oneCandidate(t, "p1", "Dune.2021.1080p.WEB-DL")); n != 1 {
		t.Fatalf("imported %d, want 1", n)
	}
	if row := readAttach(t, db, "p1"); row.state != AttachStatePending || row.attempts != 1 {
		t.Fatalf("after a panicking attach: %+v", row)
	}
	makeDue(t, db, "p1")
	m.RetryPendingAttach(ctx)
	if row := readAttach(t, db, "p1"); row.state != AttachStateAttached {
		t.Fatalf("after the retry: %+v", row)
	}
}

// Without an attach function (nothing will ever attach), imports are recorded as
// attached — never left pending forever.
func TestNoAttachRecordsAttached(t *testing.T) {
	ctx := context.Background()
	m := newTestManager(t, t.TempDir(), nil)
	if n := m.Process(ctx, oneCandidate(t, "n1", "Dune.2021.1080p.WEB-DL")); n != 1 {
		t.Fatalf("imported %d, want 1", n)
	}
	var state string
	if err := m.repo.db.QueryRow(`SELECT attach_state FROM imports WHERE download_hash = 'n1'`).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != AttachStateAttached {
		t.Fatalf("state = %q", state)
	}
}

// MarkRemovedByTarget flags the import so the still-seeding torrent isn't re-imported.
func TestMarkRemovedByTargetStopsReimport(t *testing.T) {
	ctx := context.Background()
	fa := &fakeAttach{script: []attachAnswer{{outcome: Attached}}}
	m, _ := attachManager(t, t.TempDir(), fa)
	cands := oneCandidate(t, "d1", "Dune.2021.1080p.WEB-DL")
	if n := m.Process(ctx, cands); n != 1 {
		t.Fatalf("imported %d, want 1", n)
	}
	target, _, err := m.repo.targetFor(ctx, "d1")
	if err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(target)
	if err := m.MarkRemovedByTarget(ctx, target); err != nil {
		t.Fatal(err)
	}
	if n := m.Process(ctx, cands); n != 0 {
		t.Fatalf("a deliberately deleted file was imported back (%d)", n)
	}
}
