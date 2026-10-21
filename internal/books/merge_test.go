package books

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"testing"

	"github.com/tristenlammi/arrmada/internal/listening"
	"github.com/tristenlammi/arrmada/internal/store"
)

// Adding a book that is already there under another catalogue's key is refused with
// the existing row, not added twice.
func TestFindDuplicateAcrossCatalogues(t *testing.T) {
	repo, ctx := historyRepo(t)
	s := &Service{repo: repo, log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	existing, _ := repo.Create(ctx, Book{OLKey: "OL1W", Title: "The Hobbit", Author: "J.R.R. Tolkien"})
	if b, ok := s.findDuplicate(ctx, "Hobbit: 75th Anniversary Edition", "Tolkien, J. R. R."); !ok || b.ID != existing.ID {
		t.Errorf("duplicate not found: ok=%v id=%d", ok, b.ID)
	}
	if _, ok := s.findDuplicate(ctx, "The Hobbit", ""); ok {
		t.Error("a title with no author matched a book that has one")
	}
	if _, ok := s.findDuplicate(ctx, "The Silmarillion", "J.R.R. Tolkien"); ok {
		t.Error("a different title matched")
	}
}

// mergeFixture is a library with a listener, wired for merges with the real listening
// store, and a recorder for the announcement.
type mergeFixture struct {
	t         *testing.T
	ctx       context.Context
	db        *sql.DB
	s         *Service
	listen    *listening.Store
	uid       int64
	announced []int64
}

func newMergeFixture(t *testing.T) *mergeFixture {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	db := st.DB().(*sql.DB)
	res, err := db.Exec(`INSERT INTO users (username, password_hash, role) VALUES ('listener', 'x', 'requester')`)
	if err != nil {
		t.Fatal(err)
	}
	uid, _ := res.LastInsertId()
	f := &mergeFixture{t: t, ctx: context.Background(), db: db, uid: uid, listen: listening.NewStore(db),
		s: NewService(db, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))}
	f.s.SetMergeHooks(f.listen, func(ctx context.Context, q store.Execer, keepID int64) error {
		// Inside the transaction: the dropped row is already gone.
		var n int
		if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM book_events WHERE book_id = ? AND event = 'merged'`, keepID).Scan(&n); err != nil || n == 0 {
			t.Errorf("announced before the merge was written (n=%d, err=%v)", n, err)
		}
		f.announced = append(f.announced, keepID)
		return nil
	})
	return f
}

func (f *mergeFixture) book(key, title string) Book {
	f.t.Helper()
	b, err := f.s.repo.Create(f.ctx, Book{OLKey: key, Title: title, Author: "Brandon Sanderson", QualityProfile: "p-" + key})
	if err != nil {
		f.t.Fatal(err)
	}
	return b
}

func (f *mergeFixture) edition(id int64, kind, path string) {
	f.t.Helper()
	if err := f.s.MarkImported(f.ctx, id, kind, path, "X", 1000, 2); err != nil {
		f.t.Fatal(err)
	}
}

func (f *mergeFixture) version(bookID int64, label, path string) AudioVersion {
	f.t.Helper()
	v, err := f.s.AddAudioVersion(f.ctx, bookID, label, []string{"graphicaudio"}, true)
	if err != nil {
		f.t.Fatal(err)
	}
	if path != "" {
		if err := f.s.SetAudioVersionFile(f.ctx, v.ID, path, "M4B", 5000, 1); err != nil {
			f.t.Fatal(err)
		}
	}
	return v
}

func (f *mergeFixture) exec(q string, args ...any) {
	f.t.Helper()
	if _, err := f.db.Exec(q, args...); err != nil {
		f.t.Fatal(err)
	}
}

func (f *mergeFixture) count(q string, args ...any) int {
	f.t.Helper()
	var n int
	if err := f.db.QueryRow(q, args...).Scan(&n); err != nil {
		f.t.Fatal(err)
	}
	return n
}

// references seeds a request, a former key, a grab, a blocklist row, a review and a
// search attempt on the book.
func (f *mergeFixture) references(bookID int64, key string) {
	f.exec(`INSERT INTO requests (media_type, ol_key, title, requested_by, book_id) VALUES ('book', ?, 'The Way of Kings', ?, ?)`,
		key, f.uid, bookID)
	if err := f.s.AddKey(f.ctx, "OL555W", bookID, ""); err != nil {
		f.t.Fatal(err)
	}
	f.exec(`INSERT INTO grabs (movie_id, title, indexer, media_type, info_hash) VALUES (?, 'Way.of.Kings.EPUB', 'mam', 'book', 'abc')`, bookID)
	f.exec(`INSERT INTO blocklist (movie_id, norm_title, title, media_type) VALUES (?, 'wok', 'Way of Kings [PDF]', 'book')`, bookID)
	f.exec(`INSERT INTO import_reviews (hash, name, media_type, expected_id, reason, reason_code) VALUES ('h1', 'Way of Kings', 'book', ?, 'x', 'mismatch')`, bookID)
	f.exec(`INSERT INTO search_attempts (media_type, media_id, started_at, returned, wrong_title, outcome, top_reason, reasons_json)
		VALUES ('book', ?, 1000, 3, 0, 'none_suitable', 'quality', '{}')`, bookID)
}

// Both rows have an ebook and an audiobook: the dropped audiobook becomes a "Merged copy"
// version, the dropped ebook stays on disk and is reported, versions, places, bookmarks,
// requests, keys, events, grabs, blocklist, reviews and search history all move, and the
// dropped row is gone.
func TestMergeLossless(t *testing.T) {
	f := newMergeFixture(t)
	ctx := f.ctx
	keep := f.book("OL1W", "The Way of Kings")
	drop := f.book("hc:99", "The Way of Kings")
	f.edition(keep.ID, KindEbook, "/lib/e/keep.epub")
	f.edition(keep.ID, KindAudiobook, "/lib/a/keep")
	f.edition(drop.ID, KindEbook, "/lib/e/drop.epub")
	f.edition(drop.ID, KindAudiobook, "/lib/a/drop")
	kv := f.version(keep.ID, "Full cast", "/lib/a/keep-fc")
	dv := f.version(drop.ID, "Full cast", "/lib/a/drop-fc")
	f.exec(`UPDATE books SET description = 'A storm.', series_name = 'The Stormlight Archive', series_position = 1, monitored = 1 WHERE id = ?`, drop.ID)
	f.exec(`UPDATE books SET monitored = 0 WHERE id = ?`, keep.ID)
	f.references(drop.ID, "hc:99")
	f.s.repo.AddEvent(ctx, drop.ID, "grabbed", "Grabbed something")

	dropMain, dropVer := listening.ItemKey(drop.ID, 0), listening.ItemKey(drop.ID, dv.ID)
	if _, err := f.listen.SetProgress(ctx, f.uid, dropMain, 3600, 36000, nil, "Pixel"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.listen.SetProgress(ctx, f.uid, dropVer, 120, 600, nil, "Pixel"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.listen.AddBookmark(ctx, f.uid, dropMain, 60, "mark"); err != nil {
		t.Fatal(err)
	}

	res, err := f.s.Merge(ctx, keep.ID, drop.ID, MergeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if res.MovedEbook || res.MovedAudiobook || res.MergedCopyID == 0 || res.Versions != 1 {
		t.Fatalf("result = %+v", res)
	}
	if len(res.LeftFiles) != 1 || res.LeftFiles[0] != "/lib/e/drop.epub" {
		t.Fatalf("left files = %v", res.LeftFiles)
	}
	for _, name := range []string{"requests", "grabs", "blocklist", "reviews", "search_attempts"} {
		if res.Moved[name] != 1 {
			t.Errorf("moved %s = %d, want 1", name, res.Moved[name])
		}
	}

	if _, err := f.s.Get(ctx, drop.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("dropped row still there: %v", err)
	}
	got, err := f.s.Get(ctx, keep.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Ebook.Path != "/lib/e/keep.epub" || got.Audiobook.Path != "/lib/a/keep" {
		t.Fatalf("kept editions changed: %+v %+v", got.Ebook, got.Audiobook)
	}
	if !got.Monitored || got.Description != "A storm." || got.SeriesName != "The Stormlight Archive" || got.SeriesPosition != 1 {
		t.Fatalf("carried over: monitored=%v desc=%q series=%q #%v", got.Monitored, got.Description, got.SeriesName, got.SeriesPosition)
	}
	if got.QualityProfile != "p-OL1W" {
		t.Fatalf("kept profile changed to %q", got.QualityProfile)
	}
	byID := map[int64]AudioVersion{}
	for _, v := range got.AudioVersions {
		byID[v.ID] = v
	}
	if len(byID) != 3 || byID[kv.ID].Label != "Full cast" {
		t.Fatalf("versions = %+v", got.AudioVersions)
	}
	mc := byID[res.MergedCopyID]
	if mc.Label != MergedCopyLabel || mc.Monitored || mc.File == nil || mc.File.Path != "/lib/a/drop" || len(mc.Terms) != 0 || mc.File.FileCount != 2 {
		t.Fatalf("merged copy = %+v", mc)
	}
	if moved := byID[dv.ID]; moved.Label != "Full cast 2" || moved.File == nil || moved.File.Path != "/lib/a/drop-fc" {
		t.Fatalf("moved version = %+v", moved)
	}

	// Listening followed the items.
	if p, ok, _ := f.listen.Progress(ctx, f.uid, listening.ItemKey(keep.ID, res.MergedCopyID)); !ok || p.Position != 3600 {
		t.Fatalf("place on the merged copy = %+v %v", p, ok)
	}
	if p, ok, _ := f.listen.Progress(ctx, f.uid, listening.ItemKey(keep.ID, dv.ID)); !ok || p.Position != 120 {
		t.Fatalf("place on the moved version = %+v %v", p, ok)
	}
	if bms, _ := f.listen.Bookmarks(ctx, f.uid, listening.ItemKey(keep.ID, res.MergedCopyID)); len(bms) != 1 {
		t.Fatalf("bookmarks = %+v", bms)
	}
	if got := f.listen.ResolveItem(ctx, dropMain); got != listening.ItemKey(keep.ID, res.MergedCopyID) {
		t.Fatalf("redirect for %s = %s", dropMain, got)
	}
	if res.Listening.Progress != 2 || res.Listening.Bookmarks != 1 {
		t.Fatalf("listening counts = %+v", res.Listening)
	}

	// Everything that named the dropped row names the kept one.
	if n := f.count(`SELECT COUNT(*) FROM requests WHERE book_id = ?`, keep.ID); n != 1 {
		t.Fatalf("requests on keep = %d", n)
	}
	for _, k := range []string{"hc:99", "OL555W", "OL1W"} {
		if id, ok := f.s.BookIDForKey(ctx, k); !ok || id != keep.ID {
			t.Errorf("key %s → %d %v", k, id, ok)
		}
	}
	for q, want := range map[string]int{
		`SELECT COUNT(*) FROM grabs WHERE media_type = 'book' AND movie_id = ?`:             1,
		`SELECT COUNT(*) FROM blocklist WHERE media_type = 'book' AND movie_id = ?`:         1,
		`SELECT COUNT(*) FROM import_reviews WHERE media_type = 'book' AND expected_id = ?`: 1,
		`SELECT COUNT(*) FROM search_attempts WHERE media_type = 'book' AND media_id = ?`:   1,
		`SELECT COUNT(*) FROM book_events WHERE book_id = ? AND event = 'grabbed'`:          1,
		`SELECT COUNT(*) FROM book_events WHERE book_id = ? AND event = 'merged'`:           1,
	} {
		if n := f.count(q, keep.ID); n != want {
			t.Errorf("%s = %d, want %d", q, n, want)
		}
	}
	events, _ := f.s.Events(ctx, keep.ID, 1)
	if len(events) != 1 || !strings.Contains(events[0].Detail, "/lib/e/drop.epub") || !strings.Contains(events[0].Detail, "hc:99") {
		t.Fatalf("merged event = %+v", events)
	}
	if len(f.announced) != 1 || f.announced[0] != keep.ID {
		t.Fatalf("announced %v", f.announced)
	}
}

// When the kept book lacks an edition the dropped one has, the edition moves onto it —
// and the places in the dropped audiobook become the kept book's, the newer one winning.
func TestMergeMovesMissingEditions(t *testing.T) {
	f := newMergeFixture(t)
	ctx := f.ctx
	keep := f.book("OL1W", "Elantris")
	drop := f.book("hc:7", "Elantris")
	f.edition(drop.ID, KindEbook, "/lib/e/elantris.epub")
	f.edition(drop.ID, KindAudiobook, "/lib/a/elantris")
	keepMain := listening.ItemKey(keep.ID, 0)
	if _, err := f.listen.SetProgress(ctx, f.uid, keepMain, 50, 36000, nil, "Old"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.listen.SetProgress(ctx, f.uid, listening.ItemKey(drop.ID, 0), 7000, 36000, nil, "New"); err != nil {
		t.Fatal(err)
	}
	// The second place is written after the first, so it's newer.
	f.exec(`UPDATE listen_progress SET updated_at = updated_at + 1000 WHERE item_key = ?`, listening.ItemKey(drop.ID, 0))

	res, err := f.s.Merge(ctx, keep.ID, drop.ID, MergeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !res.MovedEbook || !res.MovedAudiobook || res.MergedCopyID != 0 || len(res.LeftFiles) != 0 {
		t.Fatalf("result = %+v", res)
	}
	got, _ := f.s.Get(ctx, keep.ID)
	if got.Ebook == nil || got.Ebook.Path != "/lib/e/elantris.epub" || got.Audiobook == nil || got.Audiobook.Path != "/lib/a/elantris" ||
		got.Audiobook.FileCount != 2 || !got.HasFile {
		t.Fatalf("kept book = %+v", got)
	}
	if p, _, _ := f.listen.Progress(ctx, f.uid, keepMain); p.Position != 7000 || p.Device != "New" {
		t.Fatalf("place = %+v, want the newer one", p)
	}
}

// The release day carries over: the kept book's when it has one, else the dropped one's.
// [BOOK-18]
func TestMergeCarriesReleaseDate(t *testing.T) {
	f := newMergeFixture(t)
	ctx := f.ctx
	keep := f.book("OL1W", "Warbreaker")
	drop := f.book("hc:8", "Warbreaker")
	f.exec(`UPDATE books SET release_date = '2027-03-12' WHERE id = ?`, drop.ID)
	if _, err := f.s.Merge(ctx, keep.ID, drop.ID, MergeOptions{}); err != nil {
		t.Fatal(err)
	}
	if got, _ := f.s.Get(ctx, keep.ID); got.ReleaseDate != "2027-03-12" {
		t.Errorf("kept book's release date = %q, want the dropped one's", got.ReleaseDate)
	}

	keep2 := f.book("OL2W", "Elantris")
	drop2 := f.book("hc:9", "Elantris")
	f.exec(`UPDATE books SET release_date = '2026-01-01' WHERE id = ?`, keep2.ID)
	f.exec(`UPDATE books SET release_date = '2027-05-05' WHERE id = ?`, drop2.ID)
	if _, err := f.s.Merge(ctx, keep2.ID, drop2.ID, MergeOptions{}); err != nil {
		t.Fatal(err)
	}
	if got, _ := f.s.Get(ctx, keep2.ID); got.ReleaseDate != "2026-01-01" {
		t.Errorf("kept book's own date = %q, want it kept", got.ReleaseDate)
	}
}

// A failure part-way through — here the listening move — leaves both books, every
// version and every listening row exactly as they were.
func TestMergeRollsBackOnFailure(t *testing.T) {
	f := newMergeFixture(t)
	ctx := f.ctx
	keep := f.book("OL1W", "Warbreaker")
	drop := f.book("hc:8", "Warbreaker")
	f.edition(keep.ID, KindAudiobook, "/lib/a/keep")
	f.edition(drop.ID, KindAudiobook, "/lib/a/drop")
	f.edition(drop.ID, KindEbook, "/lib/e/drop.epub")
	dv := f.version(drop.ID, "Dramatized", "/lib/a/drop-d")
	f.references(drop.ID, "hc:8")
	if _, err := f.listen.SetProgress(ctx, f.uid, listening.ItemKey(drop.ID, 0), 3600, 36000, nil, "Pixel"); err != nil {
		t.Fatal(err)
	}
	snapshot := func() string {
		var b strings.Builder
		for _, q := range []string{
			`SELECT COUNT(*) FROM books`,
			`SELECT COUNT(*) FROM book_audio_versions WHERE book_id = ` + itoa(drop.ID),
			`SELECT COUNT(*) FROM book_audio_versions WHERE book_id = ` + itoa(keep.ID),
			`SELECT COUNT(*) FROM requests WHERE book_id = ` + itoa(drop.ID),
			`SELECT COUNT(*) FROM book_keys WHERE book_id = ` + itoa(drop.ID),
			`SELECT COUNT(*) FROM grabs WHERE movie_id = ` + itoa(drop.ID),
			`SELECT COUNT(*) FROM listen_progress WHERE item_key = 'b` + itoa(drop.ID) + `'`,
			`SELECT COUNT(*) FROM listen_history WHERE item_key = 'b` + itoa(drop.ID) + `'`,
			`SELECT COUNT(*) FROM listen_item_redirects`,
			`SELECT COUNT(*) FROM books WHERE id = ` + itoa(keep.ID) + ` AND ebook_path = ''`,
		} {
			b.WriteString(itoa(int64(f.count(q))) + ",")
		}
		return b.String()
	}
	want := snapshot()

	boom := errors.New("boom")
	f.s.SetMergeHooks(failingRekeyer{f.listen, boom}, nil)
	if _, err := f.s.Merge(ctx, keep.ID, drop.ID, MergeOptions{}); !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	if got := snapshot(); got != want {
		t.Fatalf("after a failed merge: %s, want %s", got, want)
	}
	if v, err := f.s.GetAudioVersion(ctx, drop.ID, dv.ID); err != nil || v.Label != "Dramatized" {
		t.Fatalf("drop's version = %+v %v", v, err)
	}
}

// failingRekeyer re-keys for real and then fails, as a statement late in a merge might.
type failingRekeyer struct {
	real *listening.Store
	err  error
}

func (r failingRekeyer) HoldReports() func() { return r.real.HoldReports() }
func (r failingRekeyer) RekeyItemsTx(ctx context.Context, tx *store.Transaction, moves map[string]string) (listening.RekeyCounts, error) {
	if _, err := r.real.RekeyItemsTx(ctx, tx, moves); err != nil {
		return listening.RekeyCounts{}, err
	}
	return listening.RekeyCounts{}, r.err
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

// A book can't be merged into itself, and nothing merges without the listening store.
func TestMergeRefusals(t *testing.T) {
	f := newMergeFixture(t)
	a := f.book("OL1W", "Mistborn")
	b := f.book("hc:1", "Mistborn")
	if _, err := f.s.Merge(f.ctx, a.ID, a.ID, MergeOptions{}); !errors.Is(err, ErrMergeSame) {
		t.Fatalf("self merge: %v", err)
	}
	if _, err := f.s.Merge(f.ctx, a.ID, 9999, MergeOptions{}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing drop: %v", err)
	}
	f.s.SetMergeHooks(nil, nil)
	if _, err := f.s.Merge(f.ctx, a.ID, b.ID, MergeOptions{}); !errors.Is(err, ErrMergeUnavailable) {
		t.Fatalf("unwired merge: %v", err)
	}
	if n := f.count(`SELECT COUNT(*) FROM books`); n != 2 {
		t.Fatalf("%d books left", n)
	}
}

// An edition only the dropped book's profile wanted is noted on the timeline.
func TestMergeNotesWantedEdition(t *testing.T) {
	f := newMergeFixture(t)
	keep := f.book("OL1W", "Skyward")
	drop := f.book("hc:2", "Skyward")
	editions := func(_ context.Context, profile string) (bool, bool) {
		return true, profile == "p-hc:2" // the dropped book wanted the audiobook too
	}
	res, err := f.s.Merge(f.ctx, keep.ID, drop.ID, MergeOptions{Editions: editions})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Note, "the audiobook") {
		t.Fatalf("note = %q", res.Note)
	}
	events, _ := f.s.Events(f.ctx, keep.ID, 1)
	if len(events) != 1 || !strings.Contains(events[0].Detail, "the audiobook") {
		t.Fatalf("event = %+v", events)
	}
}
