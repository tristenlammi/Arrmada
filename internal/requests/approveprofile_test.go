package requests

import (
	"context"
	"io"
	"log/slog"
	"strconv"
	"testing"

	"github.com/tristenlammi/arrmada/internal/books"
	"github.com/tristenlammi/arrmada/internal/metadata"
	"github.com/tristenlammi/arrmada/internal/quality"
	"github.com/tristenlammi/arrmada/internal/store"
)

// oneBook is a catalogue that knows a single book. Only GetBook is ever called on the
// approve path; the embedded interface stays nil.
type oneBook struct {
	metadata.BookProvider
	book metadata.BookResult
}

func (o oneBook) GetBook(context.Context, string) (*metadata.BookDetails, error) {
	return &metadata.BookDetails{BookResult: o.book}, nil
}

// A request whose stored profile has since been deleted is approved onto the default
// profile, while an approver who explicitly picks an unknown profile is still refused.
func TestApproveDanglingStoredProfileUsesDefault(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	q := quality.NewService(st.DB())
	sp, err := q.Create(ctx, quality.StoredProfile{MediaType: quality.MediaBook, Name: "Ebooks", FormatScores: map[string]int{"EPUB": 40}})
	if err != nil {
		t.Fatal(err)
	}
	def := "custom:" + strconv.FormatInt(sp.ID, 10)
	if err := q.SetDefaultProfile(ctx, quality.MediaBook, def); err != nil {
		t.Fatal(err)
	}

	dune := metadata.BookResult{Key: "OL1W", Title: "Dune", Author: "Frank Herbert"}
	bk := books.NewService(st.DB(), oneBook{book: dune}, log)
	// Already in the library, so the approve takes the ErrExists path and starts no search.
	if _, err := books.NewRepo(st.DB()).Create(ctx, books.Book{OLKey: "OL1W", Title: "Dune", Author: "Frank Herbert", QualityProfile: def}); err != nil {
		t.Fatal(err)
	}
	s := &Service{repo: NewRepo(st.DB()), books: bk, quality: q, log: log}
	req, err := s.repo.Create(ctx, Request{MediaType: "book", OLKey: "OL1W", Title: "Dune", Author: "Frank Herbert",
		Status: StatusPending, QualityProfile: "custom:999", RequestedBy: 7, RequestedByName: "alice"})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := s.Approve(ctx, req.ID, "custom:998"); err != ErrUnknownProfile {
		t.Fatalf("explicit unknown profile: err = %v, want ErrUnknownProfile", err)
	}
	got, err := s.Approve(ctx, req.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != StatusApproved || got.QualityProfile != def {
		t.Errorf("approved = status %q profile %q, want approved on the default %q", got.Status, got.QualityProfile, def)
	}
}
