package requests

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/tristenlammi/arrmada/internal/books"
	"github.com/tristenlammi/arrmada/internal/eventbus"
	"github.com/tristenlammi/arrmada/internal/metadata"
	"github.com/tristenlammi/arrmada/internal/quality"
	"github.com/tristenlammi/arrmada/internal/store"
)

// An auto-approved request feeds the admin alert once, naming the title and requester;
// a pending request and a second person asking for the same title don't.
func TestCreatePublishesAutoApproved(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	dune := metadata.BookResult{Key: "OL1W", Title: "Dune", Author: "Frank Herbert"}
	bk := books.NewService(st.DB(), oneBook{book: dune}, log)
	// Already in the library, so approving takes the ErrExists path and starts no search.
	if _, err := books.NewRepo(st.DB()).Create(ctx, books.Book{OLKey: "OL1W", Title: "Dune", Author: "Frank Herbert"}); err != nil {
		t.Fatal(err)
	}
	s := &Service{repo: NewRepo(st.DB()), books: bk, quality: quality.NewService(st.DB()), bus: eventbus.New(log), log: log}
	events, cancel := s.bus.Subscribe("request.auto_approved")
	defer cancel()
	next := func() map[string]any {
		select {
		case ev := <-events:
			return ev.Data.(map[string]any)
		case <-time.After(150 * time.Millisecond):
			return nil
		}
	}

	if _, _, err := s.Create(ctx, Request{MediaType: "book", OLKey: "OL1W", Title: "Dune", Author: "Frank Herbert", Year: 1965, RequestedBy: 7, RequestedByName: "alice"}, true); err != nil {
		t.Fatal(err)
	}
	got := next()
	if got == nil || got["title"] != "Dune" || got["requested_by"] != "alice" || got["year"] != 1965 {
		t.Fatalf("auto-approve event = %v", got)
	}
	if extra := next(); extra != nil {
		t.Fatalf("a second event: %v", extra)
	}
	// Someone else asking for it attaches as a subscriber: no new alert.
	if _, _, err := s.Create(ctx, Request{MediaType: "book", OLKey: "OL1W", Title: "Dune", RequestedBy: 8, RequestedByName: "bob"}, true); err != nil {
		t.Fatal(err)
	}
	// A request left pending isn't auto-approved.
	if _, _, err := s.Create(ctx, Request{MediaType: "book", OLKey: "OL2W", Title: "Emma", RequestedBy: 7, RequestedByName: "alice"}, false); err != nil {
		t.Fatal(err)
	}
	if extra := next(); extra != nil {
		t.Fatalf("unexpected auto-approve event: %v", extra)
	}
}
