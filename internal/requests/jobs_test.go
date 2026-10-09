package requests

import (
	"context"
	"fmt"
	"testing"

	"github.com/tristenlammi/arrmada/internal/automation"
	"github.com/tristenlammi/arrmada/internal/books"
	"github.com/tristenlammi/arrmada/internal/jobs"
	"github.com/tristenlammi/arrmada/internal/metadata"
)

// recordJobs is a jobs.Submitter that keeps what it was given.
type recordJobs struct{ specs []jobs.Spec }

func (r *recordJobs) Submit(_ context.Context, s jobs.Spec) (int64, bool, error) {
	r.specs = append(r.specs, s)
	return int64(len(r.specs)), false, nil
}

// Approving a request starts its search as a job named for the title and the request, so
// a Search click on the same book while it runs joins it instead of searching twice.
func TestApproveSubmitsASearchJob(t *testing.T) {
	s, repo, _, ctx := bookLinkFixture(t, catalogue{byKey: map[string]metadata.BookResult{
		"OL1W": {Key: "OL1W", Title: "Dune", Author: "Frank Herbert"},
	}})
	rec := &recordJobs{}
	s.SetJobs(rec)
	var searched []int64
	s.searchBook = func(_ context.Context, id int64) (automation.SearchOutcome, error) {
		searched = append(searched, id)
		return automation.SearchOutcome{Reason: automation.ReasonAlreadySearching}, automation.ErrAlreadySearching
	}
	held, err := repo.Create(ctx, books.Book{OLKey: "hc:9", Title: "Dune", Author: "Herbert, Frank"})
	if err != nil {
		t.Fatal(err)
	}
	req, err := s.repo.Create(ctx, Request{MediaType: "book", OLKey: "OL1W", Title: "Dune", Author: "Frank Herbert", Status: StatusPending, RequestedBy: 7})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Approve(ctx, req.ID, ApproveOptions{}); err != nil {
		t.Fatal(err)
	}
	if len(rec.specs) != 1 {
		t.Fatalf("submitted %d jobs, want 1", len(rec.specs))
	}
	sp := rec.specs[0]
	if sp.Kind != "book.search" || sp.Target != fmt.Sprintf("book:%d", held.ID) || sp.Trigger != fmt.Sprintf("request:%d", req.ID) ||
		sp.Class != jobs.ClassIndexerSearch || sp.Timeout == 0 {
		t.Fatalf("spec = %+v", sp)
	}
	// Running it searches the book; finding it already being searched is a success.
	if _, err := sp.Fn(context.Background(), nil); err != nil {
		t.Fatalf("job fn: %v", err)
	}
	if len(searched) != 1 || searched[0] != held.ID {
		t.Fatalf("searched = %v", searched)
	}
}
