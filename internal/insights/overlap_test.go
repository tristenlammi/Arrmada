package insights

import (
	"context"
	"errors"
	"strconv"
	"testing"
)

// live and imported rows for the overlap tests. A live row carries Plex's session key; an
// imported one has none (that's how every query tells them apart).
func seedLive(t *testing.T, s *Service, key, uid, rk, title string, start, stop int64) int64 {
	t.Helper()
	id, err := s.repo.insertSession(context.Background(), sessionRecord{
		SessionKey: key, UserID: uid, UserName: "u" + uid, RatingKey: rk, MediaType: "movie", Title: title,
		StartedAt: start, StoppedAt: stop,
	})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func seedImported(t *testing.T, s *Service, r sessionRecord) int64 {
	t.Helper()
	r.SessionKey = ""
	if r.UserName == "" {
		r.UserName = "u" + r.UserID
	}
	if r.MediaType == "" {
		r.MediaType = "movie"
	}
	id, err := s.repo.insertSession(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func countRows(t *testing.T, s *Service, where string) int {
	t.Helper()
	var n int
	if err := s.repo.db.QueryRow(`SELECT COUNT(*) FROM stream_sessions WHERE ` + where).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func totalWatched(t *testing.T, s *Service) (plays int, secs int64) {
	t.Helper()
	if err := s.repo.db.QueryRow(`SELECT COUNT(*), COALESCE(`+watchedSum+`,0) FROM stream_sessions`).Scan(&plays, &secs); err != nil {
		t.Fatal(err)
	}
	return plays, secs
}

func TestImportSkipsOverlapWithLive(t *testing.T) {
	ctx := context.Background()
	s := newDataTestService(t)
	seedLive(t, s, "42", "7", "100", "Dune", 1000, 4000)
	plays, secs := totalWatched(t, s)

	row := ImportedSession{UserID: 7, RatingKey: "100", Title: "Dune", MediaType: "movie", StartedAt: 1003, StoppedAt: 3990, WatchedMS: 2987 * 1000}
	c := s.ImportHistory(ctx, []ImportedSession{row}, ImportOptions{})
	if c.Overlap != 1 || c.Imported != 0 {
		t.Fatalf("counts = %+v, want overlap=1 imported=0", c)
	}
	if p, w := totalWatched(t, s); p != plays || w != secs {
		t.Errorf("totals moved from %d plays/%ds to %d/%ds", plays, secs, p, w)
	}
	// Re-running changes nothing either.
	if c := s.ImportHistory(ctx, []ImportedSession{row}, ImportOptions{}); c.Imported != 0 {
		t.Errorf("re-run imported %d rows", c.Imported)
	}
}

// A rebuilt Plex database hands out new rating keys; the same episode by the same person at
// the same time is still the same play.
func TestImportOverlapByTitleIdentity(t *testing.T) {
	ctx := context.Background()
	s := newDataTestService(t)
	if _, err := s.repo.insertSession(ctx, sessionRecord{
		SessionKey: "9", UserID: "7", RatingKey: "100", MediaType: "episode", Title: "Pilot",
		GrandparentTitle: "Show", ParentIndex: 1, MediaIndex: 1, StartedAt: 1000, StoppedAt: 3000,
	}); err != nil {
		t.Fatal(err)
	}
	same := ImportedSession{UserID: 7, RatingKey: "555", MediaType: "episode", Title: "Pilot", GrandparentTitle: "Show",
		ParentIndex: 1, MediaIndex: 1, StartedAt: 1010, StoppedAt: 2990}
	other := same
	other.Title, other.MediaIndex = "Second", 2
	c := s.ImportHistory(ctx, []ImportedSession{same, other}, ImportOptions{})
	if c.Overlap != 1 || c.Imported != 1 {
		t.Fatalf("counts = %+v, want overlap=1 (same episode) imported=1 (a different one)", c)
	}
}

// Plays the live recorder never saw must still come in — including a replay of the same
// title straight after a play it did see, which touches that play for a few seconds of
// clock skew between the two recorders.
func TestImportKeepsNonOverlapping(t *testing.T) {
	ctx := context.Background()
	s := newDataTestService(t)
	// Live: play A of item 100 by user 7, its last sighting 10s past the replay's start.
	seedLive(t, s, "42", "7", "100", "Dune", 1003, 4010)

	rows := []ImportedSession{
		// Tautulli's copy of play A: a duplicate.
		{UserID: 7, RatingKey: "100", Title: "Dune", StartedAt: 1000, StoppedAt: 4005, WatchedMS: 3005 * 1000},
		// The back-to-back replay (Arrmada was restarting then): a real play.
		{UserID: 7, RatingKey: "100", Title: "Dune", StartedAt: 4000, StoppedAt: 7000, WatchedMS: 3000 * 1000},
		// While monitoring was off: a real play.
		{UserID: 7, RatingKey: "100", Title: "Dune", StartedAt: 50_000, StoppedAt: 53_000},
		// Someone else watching the same thing at the same time: a real play.
		{UserID: 8, RatingKey: "100", Title: "Dune", StartedAt: 1000, StoppedAt: 4005},
		// The same person on another screen, another title: a real play.
		{UserID: 7, RatingKey: "200", Title: "Arrival", StartedAt: 1000, StoppedAt: 4005},
	}
	c := s.ImportHistory(ctx, rows, ImportOptions{})
	if c.Overlap != 1 || c.Imported != 4 {
		t.Fatalf("counts = %+v, want overlap=1 imported=4", c)
	}
	if c := s.ImportHistory(ctx, rows, ImportOptions{}); c.Imported != 0 || c.Duplicate != 4 || c.Overlap != 1 {
		t.Errorf("re-run counts = %+v, want nothing imported", c)
	}
}

func TestImportCountsFailedInserts(t *testing.T) {
	ctx := context.Background()
	s := newDataTestService(t)
	if _, err := s.repo.db.Exec(`CREATE TRIGGER boom BEFORE INSERT ON stream_sessions WHEN NEW.title = 'Boom'
		BEGIN SELECT RAISE(ABORT, 'forced failure'); END`); err != nil {
		t.Fatal(err)
	}
	c := s.ImportHistory(ctx, []ImportedSession{
		{UserID: 7, RatingKey: "1", Title: "Boom", StartedAt: 1000, StoppedAt: 2000},
		{UserID: 7, RatingKey: "2", Title: "Fine", StartedAt: 1000, StoppedAt: 2000},
	}, ImportOptions{})
	if c.Failed != 1 || c.Imported != 1 || c.FirstError == "" {
		t.Fatalf("counts = %+v, want failed=1 imported=1 with the error kept", c)
	}
	if c.Processed() != 2 {
		t.Errorf("processed = %d, want every row in a bucket", c.Processed())
	}
}

func TestImportCutoff(t *testing.T) {
	ctx := context.Background()
	s := newDataTestService(t)
	c := s.ImportHistory(ctx, []ImportedSession{
		{UserID: 7, RatingKey: "1", Title: "Old", StartedAt: 1000, StoppedAt: 2000},
		{UserID: 7, RatingKey: "2", Title: "New", StartedAt: 5000, StoppedAt: 6000},
	}, ImportOptions{Before: 5000})
	if c.Imported != 1 || c.AfterCutoff != 1 {
		t.Fatalf("counts = %+v, want imported=1 after_cutoff=1", c)
	}
}

// The repair deletes exactly the imported rows that repeat a live play, with their buffer
// events, and nothing else: never a live row, never a real play the live recorder missed.
func TestRemoveImportOverlaps(t *testing.T) {
	ctx := context.Background()
	s := newDataTestService(t)
	liveA := seedLive(t, s, "42", "7", "100", "Dune", 1003, 4010)
	liveB := seedLive(t, s, "43", "7", "300", "Heat", 20_000, 26_000)
	_ = s.repo.insertBufferEvent(ctx, liveA, 2000, 0, 5000, "network", "")

	// True duplicates (imported before the importer skipped them).
	dupA := seedImported(t, s, sessionRecord{UserID: "7", RatingKey: "100", Title: "Dune", StartedAt: 1000, StoppedAt: 4005, WatchedMS: 3005 * 1000})
	_ = s.repo.insertBufferEvent(ctx, dupA, 2000, 0, 1000, "", "")
	// A pre-grouping=0 Tautulli row: a whole evening's sittings of Heat in one row, its
	// first 100 minutes watched. The live recorder saw that sitting.
	dupB := seedImported(t, s, sessionRecord{UserID: "7", RatingKey: "300", Title: "Heat", StartedAt: 19_990,
		StoppedAt: 19_990 + 34*3600, WatchedMS: 6010 * 1000})

	// Real plays that only exist as imports.
	keep := []int64{
		seedImported(t, s, sessionRecord{UserID: "7", RatingKey: "100", Title: "Dune", StartedAt: 4000, StoppedAt: 7000, WatchedMS: 3000 * 1000}), // back-to-back replay
		seedImported(t, s, sessionRecord{UserID: "7", RatingKey: "100", Title: "Dune", StartedAt: 90_000, StoppedAt: 93_000}),                     // monitoring off
		seedImported(t, s, sessionRecord{UserID: "8", RatingKey: "100", Title: "Dune", StartedAt: 1000, StoppedAt: 4005}),                         // another person
		// A grouped row whose live-recorded sitting came long after the part it watched.
		seedImported(t, s, sessionRecord{UserID: "7", RatingKey: "300", Title: "Heat", StartedAt: 10_000, StoppedAt: 30_000, WatchedMS: 600 * 1000}),
	}

	n, first, err := s.ImportOverlaps(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 || first != 1003 {
		t.Fatalf("overlaps = %d (first live %d), want 2 (first live 1003)", n, first)
	}
	plays, secs := totalWatched(t, s)
	var dupSecs int64
	for _, id := range []int64{dupA, dupB} {
		var w int64
		if err := s.repo.db.QueryRow(`SELECT `+watchedExpr+` FROM stream_sessions WHERE id = ?`, id).Scan(&w); err != nil {
			t.Fatal(err)
		}
		dupSecs += w
	}

	// A stale confirmation deletes nothing.
	if _, err := s.RemoveImportOverlaps(ctx, 5); !errors.Is(err, ErrOverlapsChanged) {
		t.Fatalf("stale count: err = %v, want ErrOverlapsChanged", err)
	}
	if p, _ := totalWatched(t, s); p != plays {
		t.Fatalf("a refused repair deleted %d rows", plays-p)
	}

	removed, err := s.RemoveImportOverlaps(ctx, 2)
	if err != nil || removed != 2 {
		t.Fatalf("removed %d, err %v; want 2", removed, err)
	}
	for _, id := range []int64{dupA, dupB} {
		if countRows(t, s, "id = "+itoa(id)) != 0 {
			t.Errorf("duplicate %d survived", id)
		}
	}
	for _, id := range append([]int64{liveA, liveB}, keep...) {
		if countRows(t, s, "id = "+itoa(id)) != 1 {
			t.Errorf("row %d was deleted but is a real play", id)
		}
	}
	var events int
	_ = s.repo.db.QueryRow(`SELECT COUNT(*) FROM buffer_events WHERE session_id = ?`, dupA).Scan(&events)
	if events != 0 {
		t.Errorf("the duplicate's buffer events survived")
	}
	_ = s.repo.db.QueryRow(`SELECT COUNT(*) FROM buffer_events WHERE session_id = ?`, liveA).Scan(&events)
	if events != 1 {
		t.Errorf("the live play's buffer event was deleted")
	}
	if p, w := totalWatched(t, s); p != plays-2 || w != secs-dupSecs {
		t.Errorf("totals %d plays/%ds, want %d/%ds (dropped by exactly the duplicates)", p, w, plays-2, secs-dupSecs)
	}
	if n, _, _ := s.ImportOverlaps(ctx); n != 0 {
		t.Errorf("%d overlaps left after the repair", n)
	}
}

// The import's single-row check (Go window end) and the repair (SQL window end) must agree.
func TestImportedEndMatchesSQL(t *testing.T) {
	s := newDataTestService(t)
	cases := []sessionRecord{
		{StartedAt: 1000, StoppedAt: 5000},
		{StartedAt: 1000, StoppedAt: 5000, WatchedMS: 1500, PausedMS: 999},
		{StartedAt: 1000, StoppedAt: 1000 + 34*3600, WatchedMS: 34 * 60 * 1000, PausedMS: 60_500},
		{StartedAt: 1000, StoppedAt: 2000, WatchedMS: 9_999_000},
	}
	for _, r := range cases {
		id := seedImported(t, s, r)
		var got int64
		if err := s.repo.db.QueryRow(`SELECT `+importedEndSQL("i")+` FROM stream_sessions i WHERE id = ?`, id).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if want := importedEnd(r.StartedAt, r.StoppedAt, r.WatchedMS, r.PausedMS); got != want {
			t.Errorf("%+v: SQL end %d, Go end %d", r, got, want)
		}
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }
