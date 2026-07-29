package books

import (
	"context"
	"testing"

	"github.com/tristenlammi/arrmada/internal/store"
)

func historyRepo(t *testing.T) (*Repo, context.Context) {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return NewRepo(st.DB()), context.Background()
}

// A book's timeline is newest-first and scoped to that book.
func TestBookEvents(t *testing.T) {
	repo, ctx := historyRepo(t)
	a, err := repo.Create(ctx, Book{OLKey: "OL1W", Title: "Dune", Author: "Frank Herbert"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := repo.Create(ctx, Book{OLKey: "OL2W", Title: "Neuromancer", Author: "William Gibson"})
	if err != nil {
		t.Fatal(err)
	}

	repo.AddEvent(ctx, a.ID, "added", "Added to library")
	repo.AddEvent(ctx, a.ID, "grabbed", "Grabbed the ebook edition")
	repo.AddEvent(ctx, b.ID, "added", "Other book")

	evs, err := repo.Events(ctx, a.ID, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 2 {
		t.Fatalf("want 2 events for this book only, got %d: %+v", len(evs), evs)
	}
	if evs[0].Event != "grabbed" {
		t.Errorf("newest event should come first, got %q", evs[0].Event)
	}
	if evs[0].CreatedAt == "" {
		t.Error("events must carry a timestamp")
	}

	// Deleting the book cascades its history away.
	if err := repo.Delete(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	if evs, _ := repo.Events(ctx, a.ID, 100); len(evs) != 0 {
		t.Errorf("history should cascade-delete with the book, got %d", len(evs))
	}
}

// Re-matching swaps the metadata identity but must keep what the library owns: the files on
// disk, monitoring and the quality profile.
func TestRematchKeepsFilesAndSettings(t *testing.T) {
	repo, ctx := historyRepo(t)
	b, err := repo.Create(ctx, Book{
		OLKey: "OL_WRONG", Title: "Wrong Book", Author: "Nobody",
		Monitored: true, QualityProfile: "custom:3",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.SetEdition(ctx, b.ID, KindEbook, "/books/wrong.epub", "EPUB", 1234, 1); err != nil {
		t.Fatal(err)
	}

	if err := repo.Rematch(ctx, b.ID, Book{
		OLKey: "OL_RIGHT", Title: "The Right Book", Author: "Real Author", Year: 1965,
		Description: "correct", Subjects: []string{"sci-fi"},
	}); err != nil {
		t.Fatal(err)
	}

	got, err := repo.Get(ctx, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.OLKey != "OL_RIGHT" || got.Title != "The Right Book" || got.Year != 1965 {
		t.Errorf("identity not replaced: %+v", got)
	}
	if got.Ebook == nil || got.Ebook.Path != "/books/wrong.epub" {
		t.Error("the file already on disk must survive a re-match")
	}
	if !got.Monitored || got.QualityProfile != "custom:3" {
		t.Errorf("monitoring and profile are the user's, not the provider's: %+v", got)
	}
}

// Re-matching onto a work that is already a separate row must report the conflict rather
// than corrupt either row — merging the two is the user's decision.
func TestRematchOntoExistingWorkConflicts(t *testing.T) {
	repo, ctx := historyRepo(t)
	a, _ := repo.Create(ctx, Book{OLKey: "OL_A", Title: "Book A"})
	if _, err := repo.Create(ctx, Book{OLKey: "OL_B", Title: "Book B"}); err != nil {
		t.Fatal(err)
	}
	err := repo.Rematch(ctx, a.ID, Book{OLKey: "OL_B", Title: "Book B"})
	if err != ErrExists {
		t.Errorf("want ErrExists when the target work is already in the library, got %v", err)
	}
}
