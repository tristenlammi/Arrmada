package requests

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/tristenlammi/arrmada/internal/books"
)

// limitAll gives every requester the same limits and a clock the test moves.
func limitAll(s *Service, l Limits) *time.Time {
	at := time.Unix(1_800_000_000, 0)
	s.now = func() time.Time { return at }
	s.SetQuotaLimits(func(context.Context, int64) (Limits, error) { return l, nil })
	return &at
}

func movie(id int, by int64) Request {
	return Request{MediaType: "movie", TMDBID: id, Title: "film", RequestedBy: by}
}

// Use older than the window doesn't count, and ResetsAt is when the oldest use in the
// window frees up.
func TestQuotaWindow(t *testing.T) {
	s, _, _ := quietFixture(t)
	at := limitAll(s, Limits{Days: 7, Movie: 2})
	ctx := context.Background()
	start := *at
	for i := 1; i <= 2; i++ {
		if _, _, err := s.Create(ctx, movie(i, 7), CreateOptions{}); err != nil {
			t.Fatal(err)
		}
		*at = at.Add(time.Hour)
	}
	_, _, err := s.Create(ctx, movie(3, 7), CreateOptions{})
	var over *ErrQuotaExceeded
	if !errors.As(err, &over) || over.Kind != QuotaMovie || over.Limit != 2 || over.Used != 2 || over.Left() != 0 {
		t.Fatalf("third movie: %v", err)
	}
	if want := start.Add(7 * 24 * time.Hour); !over.ResetsAt.Equal(want) {
		t.Errorf("ResetsAt = %v, want %v (the oldest use plus the window)", over.ResetsAt, want)
	}
	if st, _ := s.Quota(ctx, 7, false); st.Movie.Limit != 2 || st.Movie.Used != 2 || st.Movie.ResetsAt == "" || st.Days != 7 {
		t.Errorf("status = %+v", st)
	}
	// A week after the first: it has left the window.
	*at = start.Add(7*24*time.Hour + time.Minute)
	if _, _, err := s.Create(ctx, movie(3, 7), CreateOptions{}); err != nil {
		t.Fatalf("after the window: %v", err)
	}
	// Someone else's use is theirs.
	if _, _, err := s.Create(ctx, movie(4, 8), CreateOptions{}); err != nil {
		t.Fatalf("another requester: %v", err)
	}
}

// Withdrawing or having a request declined gives its unit back at once; trimming a series
// request on approval gives back the seasons not approved.
func TestQuotaRefundOnWithdrawDeclineAndTrim(t *testing.T) {
	s, _, _ := quietFixture(t)
	limitAll(s, Limits{Days: 7, Movie: 1})
	ctx := context.Background()
	first, _, err := s.Create(ctx, movie(1, 7), CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Create(ctx, movie(2, 7), CreateOptions{}); err == nil {
		t.Fatal("over the limit")
	}
	if err := s.Delete(ctx, first.ID); err != nil {
		t.Fatal(err)
	}
	second, _, err := s.Create(ctx, movie(2, 7), CreateOptions{})
	if err != nil {
		t.Fatalf("after withdrawing: %v", err)
	}
	if err := s.Decline(ctx, second.ID, DeclineOptions{DecidedBy: 1}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Create(ctx, movie(3, 7), CreateOptions{}); err != nil {
		t.Fatalf("after a decline: %v", err)
	}
	// Asking again for the declined one is a new ask: over the limit now.
	if _, _, err := s.Create(ctx, Request{MediaType: "movie", TMDBID: 2, Title: "film", RequestedBy: 7, Note: "please"}, CreateOptions{}); err == nil {
		t.Error("a re-request went over the limit")
	}

	f := newApproveFixture(t, showListing(2, 2, 2, 2))
	f.s.SetQuotaLimits(func(context.Context, int64) (Limits, error) { return Limits{Days: 7, Season: 4}, nil })
	show, _, err := f.s.Create(f.ctx, Request{MediaType: "series", TMDBID: 77, Title: "Show", Seasons: []int{1, 2, 3}, KnownSeasons: []int{1, 2, 3, 4}, RequestedBy: 7}, CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var over *ErrQuotaExceeded
	if _, _, err := f.s.Create(f.ctx, Request{MediaType: "series", TMDBID: 78, Title: "Other", Seasons: []int{1, 2}, KnownSeasons: []int{1, 2}, RequestedBy: 7}, CreateOptions{}); !errors.As(err, &over) || over.Left() != 1 || over.Asked != 2 {
		t.Fatalf("two seasons with one left: %v", err)
	}
	if _, err := f.s.Approve(f.ctx, show.ID, ApproveOptions{Seasons: []int{1}, DecidedBy: 1}); err != nil {
		t.Fatal(err)
	}
	if st, _ := f.s.Quota(f.ctx, 7, false); st.Season.Used != 1 {
		t.Errorf("after trimming to one season: used %d, want 1", st.Season.Used)
	}
}

// Staff, imports and followers are never charged; a whole-show ask counts every known
// season.
func TestQuotaExemptStaffImportAndSubscribe(t *testing.T) {
	s, _, _ := quietFixture(t)
	limitAll(s, Limits{Days: 7, Movie: 1})
	ctx := context.Background()
	for i := 1; i <= 3; i++ {
		if _, _, err := s.Create(ctx, movie(i, 1), CreateOptions{QuotaExempt: true}); err != nil {
			t.Fatalf("staff: %v", err)
		}
		if _, _, err := s.Create(ctx, movie(10+i, 7), CreateOptions{Silent: true}); err != nil {
			t.Fatalf("import: %v", err)
		}
	}
	if _, _, err := s.Create(ctx, movie(20, 7), CreateOptions{}); err != nil {
		t.Fatalf("the requester's first: %v", err)
	}
	// Following requests that exist (staff's) uses nothing.
	for i := 1; i <= 3; i++ {
		if _, subscribed, err := s.Create(ctx, movie(i, 7), CreateOptions{}); err != nil || !subscribed {
			t.Fatalf("following %d: %v %v", i, subscribed, err)
		}
	}
	if st, _ := s.Quota(ctx, 7, false); st.Movie.Used != 1 {
		t.Errorf("used = %d, want only the one they asked for", st.Movie.Used)
	}
	if st, _ := s.Quota(ctx, 1, true); st.Movie.Limit != 0 {
		t.Errorf("staff status = %+v, want unlimited", st)
	}

	f := newApproveFixture(t, showListing(2, 2, 2))
	f.s.SetQuotaLimits(func(context.Context, int64) (Limits, error) { return Limits{Days: 7, Season: 2}, nil })
	var over *ErrQuotaExceeded
	if _, _, err := f.s.Create(f.ctx, Request{MediaType: "series", TMDBID: 77, Title: "Show", KnownSeasons: []int{0, 1, 2, 3}, RequestedBy: 7}, CreateOptions{}); !errors.As(err, &over) || over.Asked != 3 {
		t.Fatalf("the whole show with two left: %v, want 3 seasons asked", err)
	}
}

// A limited requester joining an approved book request with the other format widens it
// (a download nobody approved), so it counts as a book; joining a pending one is free.
func TestQuotaBookWideningCounts(t *testing.T) {
	s, repo, _, ctx := bookLinkFixture(t, duneCatalogue())
	searchSpy(s)
	s.SetQuotaLimits(func(context.Context, int64) (Limits, error) { return Limits{Days: 7, Book: 1}, nil })
	b, err := repo.Create(ctx, books.Book{OLKey: "OL1W", Title: "Dune", Author: "Frank Herbert", QualityProfile: presetRef(t, s, "Ebook")})
	if err != nil {
		t.Fatal(err)
	}
	giveEbook(t, repo, ctx, b.ID)
	// Staff ask for the ebook (approved, exempt); a pending Emma by someone else.
	if _, _, err := s.Create(ctx, Request{MediaType: "book", OLKey: "OL1W", Title: "Dune", Author: "Frank Herbert", RequestedBy: 1, Formats: FormatsEbook}, CreateOptions{AutoApprove: true, QuotaExempt: true}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Create(ctx, Request{MediaType: "book", OLKey: "OL2W", Title: "Emma", Author: "Jane Austen", RequestedBy: 1, Formats: FormatsEbook}, CreateOptions{QuotaExempt: true}); err != nil {
		t.Fatal(err)
	}
	// Joining the pending one in the other format: free (staff still decide).
	if _, _, err := s.Create(ctx, Request{MediaType: "book", OLKey: "OL2W", Title: "Emma", Author: "Jane Austen", RequestedBy: 7, Formats: FormatsAudiobook}, CreateOptions{}); err != nil {
		t.Fatalf("joining a pending request: %v", err)
	}
	if _, _, err := s.Create(ctx, Request{MediaType: "book", OLKey: "OL1W", Title: "Dune", Author: "Frank Herbert", RequestedBy: 7, Formats: FormatsAudiobook}, CreateOptions{}); err != nil {
		t.Fatalf("widening: %v", err)
	}
	if st, _ := s.Quota(ctx, 7, false); st.Book.Used != 1 {
		t.Errorf("used = %d after widening an approved book, want 1", st.Book.Used)
	}
}
