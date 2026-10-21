package insights

import (
	"context"
	"fmt"
	"math/rand"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

// checkViewingMarks compares every play's stored viewing_start (migration 0308's triggers)
// with the window function's answer over the same plays, using itemKey and resumeGap — so
// a change to either that the triggers don't follow fails here.
func checkViewingMarks(t *testing.T, s *Service, when string) {
	t.Helper()
	rows, err := s.repo.db.Query(`SELECT id, viewing_start, CASE WHEN prev_stop IS NULL OR started_at - prev_stop > ` + strconv.Itoa(resumeGap) + ` THEN 1 ELSE 0 END
		FROM (SELECT id, viewing_start, started_at,
			LAG(stopped_at) OVER (PARTITION BY user_id, ` + itemKey + ` ORDER BY started_at, id) AS prev_stop
			FROM stream_sessions)`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var bad []string
	for rows.Next() {
		var id, got, want int64
		if err := rows.Scan(&id, &got, &want); err != nil {
			t.Fatal(err)
		}
		if got != want {
			bad = append(bad, fmt.Sprintf("play %d marked %d, want %d", id, got, want))
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(bad) > 0 {
		t.Fatalf("%s: %d wrong marks: %s", when, len(bad), strings.Join(bad[:min(len(bad), 10)], "; "))
	}
}

// groupedPages lists every page of a grouped history through one of the two paths.
func groupedPages(t *testing.T, list func(context.Context, HistoryFilter, func(GroupedRow) error) (int, error), f HistoryFilter) ([]GroupedRow, []int) {
	t.Helper()
	var out []GroupedRow
	var totals []int
	for f.Offset = 0; ; f.Offset += f.Limit {
		var page []GroupedRow
		total, err := list(context.Background(), f, func(g GroupedRow) error { page = append(page, g); return nil })
		if err != nil {
			t.Fatal(err)
		}
		out, totals = append(out, page...), append(totals, total)
		if len(page) == 0 {
			return out, totals
		}
	}
}

// The marked starts give exactly the window function's grouping, through every kind of
// write the app makes: the live recorder's and the importer's inserts (in any order, with
// start-second ties), undoing an import, removing single plays, folding one user into
// another, an import filling in rating keys, and a play's times changing.
func TestViewingMarksFollowEveryWrite(t *testing.T) {
	ctx := context.Background()
	s := newDataTestService(t)
	rng := rand.New(rand.NewSource(20261014))
	const base = int64(1_760_000_000)
	users := []string{"1", "2", "3"}
	runs := []int64{0}
	for range 2 {
		id, err := s.StartImportRun(ctx, 0, 0)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.FinishImportRun(ctx, id, RunDone, 0, ImportCounts{}, ""); err != nil {
			t.Fatal(err)
		}
		runs = append(runs, id)
	}
	insert := func() {
		r := sessionRecord{UserID: users[rng.Intn(len(users))], MediaType: "movie", Title: "T" + strconv.Itoa(rng.Intn(3)),
			StartedAt: base + int64(rng.Intn(60))*1800, ImportRunID: runs[rng.Intn(len(runs))]}
		if rng.Intn(3) > 0 {
			r.RatingKey = strconv.Itoa(100 + rng.Intn(4))
		}
		if rng.Intn(4) == 0 {
			r.MediaType, r.GrandparentTitle, r.ParentIndex, r.MediaIndex = "episode", "Show", 1, 1+rng.Intn(2)
		}
		if rng.Intn(2) == 0 {
			r.SessionKey = "live-" + strconv.Itoa(rng.Int())
		}
		r.StoppedAt = r.StartedAt + int64(rng.Intn(4*3600))
		if _, err := s.repo.insertSession(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	exec := func(q string, args ...any) {
		if _, err := s.repo.db.ExecContext(ctx, q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	for i := 0; i < 300; i++ {
		insert()
	}
	checkViewingMarks(t, s, "after inserts")
	for round := 0; round < 40; round++ {
		switch round % 8 {
		case 0: // undo an import
			run := runs[1+rng.Intn(2)]
			var n int
			if err := s.repo.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM stream_sessions WHERE import_run_id = ? AND session_key = ''`, run).Scan(&n); err != nil {
				t.Fatal(err)
			}
			if _, err := s.RemoveImportRun(ctx, run, n); err != nil {
				t.Fatal(err)
			}
		case 1: // single deletes
			for j := 0; j < 5; j++ {
				exec(`DELETE FROM stream_sessions WHERE id = (SELECT id FROM stream_sessions ORDER BY random() LIMIT 1)`)
			}
		case 2: // fold one user into another
			from, to := users[rng.Intn(3)], users[rng.Intn(3)]
			if _, err := s.repo.repointUser(ctx, from, to); err != nil {
				t.Fatal(err)
			}
		case 3: // an import fills keys in
			exec(`UPDATE stream_sessions SET rating_key = ? WHERE rating_key = '' AND id % 3 = ?`, strconv.Itoa(100+rng.Intn(4)), rng.Intn(3))
		case 4: // times move
			exec(`UPDATE stream_sessions SET stopped_at = started_at + ? WHERE id % 7 = ?`, rng.Intn(5*3600), rng.Intn(7))
			exec(`UPDATE stream_sessions SET started_at = started_at + ? WHERE id % 11 = ?`, (rng.Intn(5)-2)*1800, rng.Intn(11))
		case 5: // titles and types change on keyless plays, which moves them between items
			exec(`UPDATE stream_sessions SET title = ? WHERE rating_key = '' AND id % 5 = ?`, "T"+strconv.Itoa(rng.Intn(3)), rng.Intn(5))
		case 6: // remove imported plays that repeat live ones
			if _, err := s.RemoveImportOverlaps(ctx, -1); err != nil {
				t.Fatal(err)
			}
		default:
			for j := 0; j < 30; j++ {
				insert()
			}
		}
		checkViewingMarks(t, s, fmt.Sprintf("round %d", round))
	}

	// Both paths list the same viewings, page by page, with the same totals.
	for _, f := range []HistoryFilter{{Limit: 7}, {Limit: 7, UserID: "1"}, {Limit: 7, UserID: "2"}, {Limit: 50, UserID: "nobody"}} {
		f.Group = true
		marked, mTotals := groupedPages(t, s.repo.historyGroupedMarked, f)
		window, wTotals := groupedPages(t, s.repo.historyGroupedWindow, f)
		if len(marked) == 0 && f.UserID != "nobody" {
			t.Fatalf("%+v: nothing listed", f)
		}
		if !reflect.DeepEqual(marked, window) || !reflect.DeepEqual(mTotals, wTotals) {
			t.Fatalf("%+v: marked path lists %d viewings (totals %v), the window %d (totals %v)", f, len(marked), mTotals, len(window), wTotals)
		}
	}
}

// The page query finds a viewing's sittings through the item index — which it can only do
// while itemKey is the expression migration 0308 indexed.
func TestViewingStartIndexMatchesItemKey(t *testing.T) {
	s := newDataTestService(t)
	rows, err := s.repo.db.Query(`EXPLAIN QUERY PLAN `+groupedMarkedSQL(s.repo.db, " WHERE s.viewing_start = 1"), 0, 50, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var plan []string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		plan = append(plan, detail)
	}
	joined := strings.Join(plan, "\n")
	if strings.Count(joined, "idx_stream_sessions_item") < 3 || !strings.Contains(joined, "idx_stream_sessions_viewings") {
		t.Fatalf("the grouped page doesn't use the viewing indexes:\n%s", joined)
	}
}
