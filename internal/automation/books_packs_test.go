package automation

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/tristenlammi/arrmada/internal/books"
	"github.com/tristenlammi/arrmada/internal/eventbus"
	"github.com/tristenlammi/arrmada/internal/library"
	"github.com/tristenlammi/arrmada/internal/metadata"
	"github.com/tristenlammi/arrmada/internal/store"
)

func packTestCoord(t *testing.T) (*Coordinator, *books.Service, *store.Store, context.Context) {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	svc := books.NewService(st.DB(), nil, slog.Default())
	c := &Coordinator{books: svc, db: st.DB(), log: slog.Default(), bus: eventbus.New(slog.Default()),
		imp: library.NewImporter(t.TempDir(), slog.Default())}
	return c, svc, st, context.Background()
}

func insertBookGrab(t *testing.T, st *store.Store, bookID, versionID int64, title, hash string) {
	t.Helper()
	if _, err := st.DB().Exec(`INSERT INTO grabs (movie_id, version_id, title, indexer, quality_profile, media_type, info_hash)
		VALUES (?, ?, ?, 'MAM', '', 'book', ?)`, bookID, versionID, title, hash); err != nil {
		t.Fatal(err)
	}
}

// A download grabbed for Fire & Blood that holds only A Game of Thrones: the other book
// is filed, and the download counts as done — it used to report "nothing here", so the
// sweep re-filed A Game of Thrones every 30 seconds and the review's Import failed.
func TestPackHoldingOnlyOtherBooksIsFinished(t *testing.T) {
	c, svc, st, ctx := packTestCoord(t)
	added, _ := svc.AddWorks(ctx, []metadata.BookResult{
		{Key: "hc:1", Title: "Fire & Blood", Author: "George R.R. Martin"},
		{Key: "hc:2", Title: "A Game of Thrones", Author: "George R.R. Martin"},
	}, "", true)
	if len(added) != 2 {
		t.Fatal("books not added")
	}
	fire, got := added[0], added[1]
	dl := filepath.Join(t.TempDir(), "George R R Martin - Fire & Blood (HBO Tie-in Edition)")
	for _, n := range []string{"01.m4b", "02.m4b"} {
		p := filepath.Join(dl, "A Game of Thrones", n)
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte("audio"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	insertBookGrab(t, st, fire.ID, 0, "George R R Martin - Fire & Blood (HBO Tie-in Edition) [M4B]", "abc123")

	imported, hadFiles := c.importBookContent(ctx, fire, dl, "abc123", filepath.Base(dl))
	if !imported || !hadFiles {
		t.Fatalf("imported=%v hadFiles=%v, want both true — the download is finished with", imported, hadFiles)
	}
	if b, _ := svc.Get(ctx, got.ID); b.Audiobook == nil {
		t.Error("A Game of Thrones should have received the audiobook")
	}
	if b, _ := svc.Get(ctx, fire.ID); b.Audiobook != nil {
		t.Error("Fire & Blood must not be marked as having an audiobook")
	}
	var status string
	_ = st.DB().QueryRow(`SELECT status FROM grabs WHERE movie_id = ? AND media_type = 'book'`, fire.ID).Scan(&status)
	if status != "imported" {
		t.Errorf("grab status = %q, want imported so seeding rules and the pending guard let go", status)
	}
	var blocked int
	_ = st.DB().QueryRow(`SELECT COUNT(*) FROM blocklist WHERE movie_id = ? AND media_type = 'book'`, fire.ID).Scan(&blocked)
	if blocked != 1 {
		t.Errorf("blocklist rows for Fire & Blood = %d, want 1 so the same torrent isn't grabbed again", blocked)
	}
}

// Grabbing an already-imported torrent again for a version with no file must let the
// import through; once the version has a file, or the grab is done, it must not.
func TestRegrabbedForVersion(t *testing.T) {
	c, svc, st, ctx := packTestCoord(t)
	added, _ := svc.AddWorks(ctx, []metadata.BookResult{{Key: "hc:1", Title: "Dungeon Crawler Carl", Author: "Matt Dinniman"}}, "", true)
	b := added[0]
	v, err := svc.AddAudioVersion(ctx, b.ID, "Narrator", nil, true)
	if err != nil {
		t.Fatal(err)
	}
	if c.regrabbedForVersion(ctx, "HASH1") {
		t.Fatal("no grab yet — nothing to let through")
	}
	insertBookGrab(t, st, b.ID, 0, "Dungeon Crawler Carl [M4B]", "hash1") // the first, standard grab
	if c.regrabbedForVersion(ctx, "HASH1") {
		t.Fatal("a standard grab must not reopen an imported torrent")
	}
	insertBookGrab(t, st, b.ID, v.ID, "Dungeon Crawler Carl [M4B]", "hash1") // grabbed again as Narrator
	if !c.regrabbedForVersion(ctx, "HASH1") {
		t.Fatal("re-grab for an empty version must be let through")
	}
	_ = svc.SetAudioVersionFile(ctx, v.ID, "/x/Dungeon Crawler Carl (Narrator).m4b", "M4B", 1, 1)
	if c.regrabbedForVersion(ctx, "HASH1") {
		t.Error("the version has its file now — don't import again")
	}
	_ = svc.ClearAudioVersionFile(ctx, v.ID)
	_, _ = st.DB().Exec(`UPDATE grabs SET status = 'imported'`)
	if c.regrabbedForVersion(ctx, "HASH1") {
		t.Error("a grab already imported must not reopen the torrent")
	}
}
