package requests

import (
	"fmt"
	"testing"

	"github.com/tristenlammi/arrmada/internal/movies"
)

// Deleting requests with "Also stop looking" unmonitors the films and turns off searching
// for the shows that only the deleted requests held, and leaves everything else as it was,
// saying why: another open request, never approved, not in the library, already off, a book.
func TestDeleteAndStopLooking(t *testing.T) {
	f := newApproveFixture(t, showListing(2, 2))
	ctx := f.ctx
	movieRepo := movies.NewRepo(f.s.repo.db)
	film := func(tmdb int, monitored bool) movies.Movie {
		m, err := movieRepo.Create(ctx, movies.Movie{TMDBID: tmdb, Title: "Film", Year: 2001, Status: "Released", Monitored: monitored})
		if err != nil {
			t.Fatal(err)
		}
		return m
	}
	show := func(tmdb int) int64 {
		sr, err := f.shows().Add(ctx, tmdb, "", true)
		if err != nil {
			t.Fatal(err)
		}
		return sr.ID
	}
	request := func(media string, tmdb int, status string, by int64, seasons ...int) int64 {
		r, err := f.s.repo.Create(ctx, Request{MediaType: media, TMDBID: tmdb, OLKey: "", Title: media, Status: status, RequestedBy: by, Seasons: seasons})
		if err != nil {
			t.Fatal(err)
		}
		return r.ID
	}

	added := film(1, true)     // approved: stopped
	ownerFilm := film(2, true) // only a pending request: never approved, left
	offFilm := film(3, false)  // approved but already unmonitored
	heldShow := show(10)       // approved S1 deleted, S2 still pending from someone else
	freedShow := show(11)      // approved S1 deleted, S2 declined: stopped
	reqAdded := request("movie", 1, StatusApproved, 7)
	reqPending := request("movie", 2, StatusPending, 7)
	reqOff := request("movie", 3, StatusApproved, 7)
	reqGone := request("movie", 4, StatusApproved, 7) // not in the library
	reqHeld := request("series", 10, StatusApproved, 7, 1)
	request("series", 10, StatusPending, 8, 2)
	reqFreed := request("series", 11, StatusApproved, 7, 1)
	declined := request("series", 11, StatusDeclined, 8, 2)
	book, err := f.s.repo.Create(ctx, Request{MediaType: "book", OLKey: "OL1W", Title: "Book", Status: StatusApproved, RequestedBy: 7})
	if err != nil {
		t.Fatal(err)
	}

	results, titles := f.s.DeleteAndStopLooking(ctx, []int64{reqAdded, reqPending, reqOff, reqGone, reqHeld, reqFreed, book.ID, 999})
	for _, r := range results {
		if r.OK != (r.ID != 999) {
			t.Errorf("result %+v", r)
		}
	}
	got := map[string]StoppedTitle{}
	for _, st := range titles {
		key := st.MediaType + ":" + st.OLKey
		if st.MediaType != "book" {
			key = fmt.Sprintf("%s:%d", st.MediaType, st.TMDBID)
		}
		got[key] = st
	}
	if len(titles) != 7 {
		t.Fatalf("titles = %+v, want 7 (the unknown id has none)", titles)
	}
	want := map[string]string{
		"movie:1": "", "movie:2": KeptNotApproved, "movie:3": KeptAlreadyOff, "movie:4": KeptNotInLibrary,
		"series:10": KeptStillRequested, "series:11": "", "book:OL1W": KeptBook,
	}
	for k, reason := range want {
		st, ok := got[k]
		if !ok {
			t.Errorf("%s: missing from %+v", k, titles)
			continue
		}
		if st.Reason != reason || st.Stopped != (reason == "") {
			t.Errorf("%s = %+v, want reason %q", k, st, reason)
		}
	}

	mon := func(id int64) bool {
		m, err := f.s.movies.Get(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		return m.Monitored
	}
	if mon(added.ID) {
		t.Error("the film only the deleted request held is still monitored")
	}
	if !mon(ownerFilm.ID) {
		t.Error("a film no approved request held was unmonitored")
	}
	if mon(offFilm.ID) {
		t.Error("the unmonitored film changed")
	}
	searching := func(id int64) bool {
		sr, err := f.s.series.Get(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		return sr.Monitored
	}
	if !searching(heldShow) {
		t.Error("a show someone else still asks for had its searching turned off")
	}
	if searching(freedShow) {
		t.Error("the show only the deleted request held is still searched")
	}
	// The declined request it left behind isn't touched.
	if _, err := f.s.repo.Get(ctx, declined); err != nil {
		t.Errorf("the declined request not in the delete went: %v", err)
	}
}

// Without stop looking, a bulk delete still leaves the library as it is.
func TestBulkDeleteWithoutStopLookingLeavesLibrary(t *testing.T) {
	f := newApproveFixture(t, showListing(1))
	m, err := movies.NewRepo(f.s.repo.db).Create(f.ctx, movies.Movie{TMDBID: 5, Title: "Film", Status: "Released", Monitored: true})
	if err != nil {
		t.Fatal(err)
	}
	r, err := f.s.repo.Create(f.ctx, Request{MediaType: "movie", TMDBID: 5, Title: "Film", Status: StatusApproved, RequestedBy: 7})
	if err != nil {
		t.Fatal(err)
	}
	if res := f.s.Bulk(f.ctx, BulkDelete, []int64{r.ID}, "", "", 1, "admin"); len(res) != 1 || !res[0].OK {
		t.Fatalf("results = %+v", res)
	}
	if after, _ := f.s.movies.Get(f.ctx, m.ID); !after.Monitored {
		t.Error("a plain bulk delete unmonitored the film")
	}
}
