package listening

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/tristenlammi/arrmada/internal/store"
)

func rekey(t *testing.T, s *Store, db *sql.DB, moves map[string]string) RekeyCounts {
	t.Helper()
	var c RekeyCounts
	release := s.HoldReports()
	defer release()
	err := store.WithTx(context.Background(), db, func(tx *store.Transaction) error {
		var err error
		c, err = s.RekeyItemsTx(context.Background(), tx, moves)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func count(t *testing.T, db *sql.DB, q string, args ...any) int {
	t.Helper()
	var n int
	if err := db.QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func secondUser(t *testing.T, db *sql.DB) int64 {
	t.Helper()
	res, err := db.Exec(`INSERT INTO users (username, password_hash, role) VALUES ('other', 'x', 'requester')`)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	return id
}

// A merge moves a user's place, finished flag, bookmarks, sessions and timeline from the
// dropped book's items to the kept book's, and leaves a redirect behind for each.
func TestRekeyMovesEverything(t *testing.T) {
	s, db, uid, clock := testStore(t)
	ctx := context.Background()
	fin := true
	if _, err := s.SetProgress(ctx, uid, "b5", 3600, 36000, nil, "Pixel"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetProgress(ctx, uid, "b5v2", 500, 600, &fin, "Pixel"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddBookmark(ctx, uid, "b5", 120, "the good bit"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddBookmark(ctx, uid, "b5v2", 30, "intro"); err != nil {
		t.Fatal(err)
	}
	sess, _, err := s.OpenSession(ctx, uid, "b5", "dev", "Pixel", "Lissen")
	if err != nil {
		t.Fatal(err)
	}
	hist := count(t, db, `SELECT COUNT(*) FROM listen_history WHERE item_key IN ('b5', 'b5v2')`)

	c := rekey(t, s, db, map[string]string{"b5": "b9", "b5v2": "b9v2"})
	if c.Progress != 2 || c.Conflicts != 0 || c.Bookmarks != 2 || c.Sessions != 1 || c.History != hist || c.Redirects != 2 {
		t.Fatalf("counts = %+v (history before %d)", c, hist)
	}
	p, ok, _ := s.Progress(ctx, uid, "b9")
	if !ok || p.Position != 3600 {
		t.Fatalf("b9 place = %+v %v", p, ok)
	}
	v, ok, _ := s.Progress(ctx, uid, "b9v2")
	if !ok || !v.Finished {
		t.Fatalf("b9v2 place = %+v %v", v, ok)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM listen_progress WHERE item_key LIKE 'b5%'`); n != 0 {
		t.Fatalf("%d places left on the old keys", n)
	}
	if bms, _ := s.Bookmarks(ctx, uid, "b9"); len(bms) != 1 || bms[0].Title != "the good bit" {
		t.Fatalf("b9 bookmarks = %+v", bms)
	}
	if bms, _ := s.Bookmarks(ctx, uid, "b5"); len(bms) != 0 {
		t.Fatalf("bookmarks left on b5: %+v", bms)
	}
	got, err := s.GetSession(ctx, uid, sess.ID)
	if err != nil || got.ItemKey != "b9" {
		t.Fatalf("session = %+v %v", got, err)
	}
	if h, _ := s.History(ctx, uid, "b9"); len(h) == 0 {
		t.Fatal("no timeline on b9")
	}
	// The open session keeps syncing — onto the new key.
	*clock = clock.Add(30e9)
	if _, err := s.Sync(ctx, uid, sess.ID, at(3620), 20, 36000, false); err != nil {
		t.Fatal(err)
	}
	if p, _, _ := s.Progress(ctx, uid, "b9"); p.Position != 3620 {
		t.Fatalf("sync after the move: b9 at %v", p.Position)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM listen_progress WHERE item_key = 'b5'`); n != 0 {
		t.Fatal("the sync wrote under the old key")
	}
	if got := s.ResolveItem(ctx, "b5"); got != "b9" {
		t.Fatalf("ResolveItem(b5) = %q", got)
	}
	if got := s.ResolveItem(ctx, "b7"); got != "b7" {
		t.Fatalf("ResolveItem(b7) = %q", got)
	}
	// The redirect table says where an item went, never who listened to it.
	rows, err := db.Query(`SELECT name FROM pragma_table_info('listen_item_redirects')`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var col string
		_ = rows.Scan(&col)
		if col != "old_key" && col != "new_key" && col != "created_at" {
			t.Fatalf("listen_item_redirects has a %q column", col)
		}
	}
}

// When a user has a place on both keys the newer one wins whole — its held jump (the
// pending_* fields) included — and the other stays on the timeline to be put back.
func TestRekeyConflictNewerWins(t *testing.T) {
	s, db, uid, clock := testStore(t)
	ctx := context.Background()
	other := secondUser(t, db)

	// uid: the kept book's place is newer.
	if _, err := s.SetProgress(ctx, uid, "b5", 100, 36000, nil, "Old phone"); err != nil {
		t.Fatal(err)
	}
	// other: the dropped book's place is newer, and has a held jump back.
	if _, err := s.SetProgress(ctx, other, "b9", 200, 36000, nil, "Tablet"); err != nil {
		t.Fatal(err)
	}
	*clock = clock.Add(60e9)
	if _, err := s.SetProgress(ctx, uid, "b9", 900, 36000, nil, "New phone"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetProgress(ctx, other, "b5", 5000, 36000, nil, "Phone"); err != nil {
		t.Fatal(err)
	}
	*clock = clock.Add(60e9)
	sess, _, err := s.OpenSession(ctx, other, "b5", "dev", "Phone", "Lissen")
	if err != nil {
		t.Fatal(err)
	}
	*clock = clock.Add(10e9)
	if d, err := s.Sync(ctx, other, sess.ID, at(1000), 5, 36000, false); err != nil || d.Progress.PendingPosition == nil {
		t.Fatalf("expected a held jump back: %+v %v", d, err)
	}
	before, _, _ := s.Progress(ctx, other, "b5")

	c := rekey(t, s, db, map[string]string{"b5": "b9"})
	if c.Conflicts != 2 || c.Progress != 0 {
		t.Fatalf("counts = %+v", c)
	}
	if p, _, _ := s.Progress(ctx, uid, "b9"); p.Position != 900 || p.Device != "New phone" {
		t.Fatalf("uid kept %+v, want the newer b9 place", p)
	}
	p, _, _ := s.Progress(ctx, other, "b9")
	if p.Position != 5000 || p.PendingPosition == nil || *p.PendingPosition != *before.PendingPosition ||
		p.PendingSession != before.PendingSession || p.PendingAt != before.PendingAt || p.UpdatedAt != before.UpdatedAt {
		t.Fatalf("other kept %+v, want the newer b5 place with its hold %+v", p, before)
	}
	// The losing places are on the timeline as the place before the merge.
	for _, tc := range []struct {
		user int64
		pos  float64
	}{{uid, 100}, {other, 200}} {
		h, _ := s.History(ctx, tc.user, "b9")
		found := false
		for _, e := range h {
			if e.Reason == "merge" && e.Kind == KindBefore && e.Position == tc.pos {
				found = true
			}
		}
		if !found {
			t.Fatalf("user %d: the losing place %v isn't on the timeline: %+v", tc.user, tc.pos, h)
		}
	}
	// The hold still resolves the way it would have: carrying on from it proves it.
	*clock = clock.Add(40e9)
	if _, err := s.Sync(ctx, other, sess.ID, at(1035), 35, 36000, false); err != nil {
		t.Fatal(err)
	}
	if p, _, _ := s.Progress(ctx, other, "b9"); p.Position != 1035 || p.PendingPosition != nil {
		t.Fatalf("held jump after the merge: %+v", p)
	}
}

// A live place beats one an app removed, even an older live one.
func TestRekeyLivePlaceBeatsRemoved(t *testing.T) {
	s, db, uid, clock := testStore(t)
	ctx := context.Background()
	if _, err := s.SetProgress(ctx, uid, "b9", 300, 36000, nil, "Phone"); err != nil {
		t.Fatal(err)
	}
	*clock = clock.Add(60e9)
	if _, err := s.SetProgress(ctx, uid, "b5", 700, 36000, nil, "Phone"); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteProgress(ctx, uid, "b5", "Lissen"); err != nil {
		t.Fatal(err)
	}
	rekey(t, s, db, map[string]string{"b5": "b9"})
	if p, ok, _ := s.Progress(ctx, uid, "b9"); !ok || p.Position != 300 {
		t.Fatalf("b9 = %+v %v, want the live place", p, ok)
	}
}

// Moving a timeline onto a key that has one trims it as every write does, per kind.
func TestRekeyTrimsHistory(t *testing.T) {
	s, db, uid, _ := testStore(t)
	for _, key := range []string{"b5", "b9"} {
		for i := 0; i < 40; i++ {
			if _, err := db.Exec(`INSERT INTO listen_history (user_id, item_key, position, at, kind) VALUES (?, ?, ?, ?, 'applied')`,
				uid, key, i, i); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`INSERT INTO listen_history (user_id, item_key, position, at, kind) VALUES (?, ?, ?, ?, 'rejected')`,
				uid, key, i, i); err != nil {
				t.Fatal(err)
			}
		}
	}
	rekey(t, s, db, map[string]string{"b5": "b9"})
	if n := count(t, db, `SELECT COUNT(*) FROM listen_history WHERE item_key = 'b9' AND kind = 'applied'`); n != historyKeep {
		t.Fatalf("%d applied rows, want %d", n, historyKeep)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM listen_history WHERE item_key = 'b9' AND kind = 'rejected'`); n != historyKeepOther {
		t.Fatalf("%d rejected rows, want %d", n, historyKeepOther)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM listen_history WHERE item_key = 'b5'`); n != 0 {
		t.Fatalf("%d rows left on b5", n)
	}
}

// A bookmark at the same second on both keys keeps the new key's; the rest move.
func TestRekeyBookmarkCollision(t *testing.T) {
	s, db, uid, _ := testStore(t)
	ctx := context.Background()
	_, _ = s.AddBookmark(ctx, uid, "b5", 60, "old")
	_, _ = s.AddBookmark(ctx, uid, "b5", 90, "only on b5")
	_, _ = s.AddBookmark(ctx, uid, "b9", 60, "kept")
	if c := rekey(t, s, db, map[string]string{"b5": "b9"}); c.Bookmarks != 1 {
		t.Fatalf("moved %d bookmarks, want 1", c.Bookmarks)
	}
	bms, _ := s.Bookmarks(ctx, uid, "b9")
	if len(bms) != 2 || bms[0].Title != "kept" || bms[1].Title != "only on b5" {
		t.Fatalf("b9 bookmarks = %+v", bms)
	}
}

// A redirect chain collapses: b5 → b9, then b9 → b12, leaves b5 pointing at b12.
func TestRekeyCollapsesChains(t *testing.T) {
	s, db, _, _ := testStore(t)
	ctx := context.Background()
	rekey(t, s, db, map[string]string{"b5": "b9v7"})
	rekey(t, s, db, map[string]string{"b9v7": "b12v7", "b9": "b12"})
	for old, want := range map[string]string{"b5": "b12v7", "b9v7": "b12v7", "b9": "b12"} {
		if got := s.ResolveItem(ctx, old); got != want {
			t.Fatalf("ResolveItem(%s) = %q, want %q", old, got, want)
		}
	}
}

// A failure anywhere in the transaction leaves every row where it was.
func TestRekeyRollsBack(t *testing.T) {
	s, db, uid, _ := testStore(t)
	ctx := context.Background()
	if _, err := s.SetProgress(ctx, uid, "b5", 3600, 36000, nil, "Pixel"); err != nil {
		t.Fatal(err)
	}
	_, _ = s.AddBookmark(ctx, uid, "b5", 60, "mark")
	if _, _, err := s.OpenSession(ctx, uid, "b5", "dev", "Pixel", "Lissen"); err != nil {
		t.Fatal(err)
	}
	snapshot := func() [4]int {
		return [4]int{
			count(t, db, `SELECT COUNT(*) FROM listen_progress WHERE item_key = 'b5'`),
			count(t, db, `SELECT COUNT(*) FROM listen_history WHERE item_key = 'b5'`),
			count(t, db, `SELECT COUNT(*) FROM listen_sessions WHERE item_key = 'b5'`),
			count(t, db, `SELECT COUNT(*) FROM listen_bookmarks WHERE item_key = 'b5'`),
		}
	}
	want := snapshot()

	// A statement failing part-way: the bookmarks step can't find its table.
	err := store.WithTx(ctx, db, func(tx *store.Transaction) error {
		if _, err := tx.Exec(`ALTER TABLE listen_bookmarks RENAME TO listen_bookmarks_gone`); err != nil {
			return err
		}
		_, err := s.RekeyItemsTx(ctx, tx, map[string]string{"b5": "b9"})
		return err
	})
	if err == nil {
		t.Fatal("re-key with a missing table succeeded")
	}
	// The caller failing after the re-key (the rest of a merge).
	boom := errors.New("boom")
	err = store.WithTx(ctx, db, func(tx *store.Transaction) error {
		if _, err := s.RekeyItemsTx(ctx, tx, map[string]string{"b5": "b9"}); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	if got := snapshot(); got != want {
		t.Fatalf("after rollback: %v, want %v", got, want)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM listen_item_redirects`); n != 0 {
		t.Fatalf("%d redirects survived the rollback", n)
	}
}

// Bad moves are refused before anything is written.
func TestRekeyRejectsBadMoves(t *testing.T) {
	s, db, _, _ := testStore(t)
	ctx := context.Background()
	for _, moves := range []map[string]string{{"b5": "b5"}, {"": "b9"}, {"b5": ""}, {"b5": "b9", "b9": "b12"}} {
		err := store.WithTx(ctx, db, func(tx *store.Transaction) error {
			_, err := s.RekeyItemsTx(ctx, tx, moves)
			return err
		})
		if err == nil {
			t.Fatalf("moves %v accepted", moves)
		}
	}
}
