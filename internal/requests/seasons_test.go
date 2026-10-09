package requests

import (
	"context"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
)

// fillSeason puts every episode of a season on disk.
func fillSeason(t *testing.T, f approveFixture, seriesID int64, season, episodes int) {
	t.Helper()
	for e := 1; e <= episodes; e++ {
		if err := f.shows().MarkEpisodeImported(f.ctx, seriesID, season, e, fmt.Sprintf("/tv/show/s%02de%02d.mkv", season, e), 1); err != nil {
			t.Fatal(err)
		}
	}
}

// monitoredBySeason is how many episodes of each season are monitored.
func monitoredBySeason(t *testing.T, f approveFixture, seriesID int64) map[int]int {
	t.Helper()
	sr, err := f.s.series.Get(f.ctx, seriesID)
	if err != nil {
		t.Fatal(err)
	}
	out := map[int]int{}
	for _, sn := range sr.Seasons {
		for _, e := range sn.Episodes {
			if e.Monitored {
				out[sn.SeasonNumber]++
			}
		}
	}
	return out
}

func inbox(t *testing.T, f approveFixture, user int64) []UserNotification {
	t.Helper()
	n, err := f.s.repo.listUserNotifications(f.ctx, user)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func refs(ns []UserNotification) []string {
	var out []string
	for _, n := range ns {
		out = append(out, n.Ref)
	}
	return out
}

// Asking for S4 of a show whose S1-3 are on disk creates a pending request for [4];
// approving it monitors only S4 (S1-3 monitoring untouched) and searches. Asking for S2,
// which is on disk, is refused as already there; a second user asking for [4] follows
// the first request, and a third asking for [4,5] follows [4] and asks for [5].
func TestRequestMoreSeasonsOfAPartlyOwnedShow(t *testing.T) {
	f := newApproveFixture(t, showListing(2, 2, 2, 2, 2))
	sr, err := f.shows().Add(f.ctx, 77, "", false) // a library scan found S1-3
	if err != nil {
		t.Fatal(err)
	}
	for n := 1; n <= 3; n++ {
		fillSeason(t, f, sr.ID, n, 2)
	}
	known := []int{1, 2, 3, 4, 5}

	req, subscribed, err := f.s.Create(f.ctx, Request{MediaType: "series", TMDBID: 77, Title: "Show", Seasons: []int{4}, KnownSeasons: known, RequestedBy: 7, RequestedByName: "alice"}, CreateOptions{})
	if err != nil || subscribed {
		t.Fatalf("create: %v subscribed=%v", err, subscribed)
	}
	if req.Status != StatusPending || !reflect.DeepEqual(req.Seasons, []int{4}) {
		t.Fatalf("request = %+v, want pending [4]", req)
	}
	if _, _, err := f.s.Create(f.ctx, Request{MediaType: "series", TMDBID: 77, Title: "Show", Seasons: []int{2}, KnownSeasons: known, RequestedBy: 8}, CreateOptions{}); err != ErrAlreadyAvailable {
		t.Errorf("asking for a season on disk: err = %v, want ErrAlreadyAvailable", err)
	}
	got, subscribed, err := f.s.Create(f.ctx, Request{MediaType: "series", TMDBID: 77, Title: "Show", Seasons: []int{4}, KnownSeasons: known, RequestedBy: 8, RequestedByName: "bob"}, CreateOptions{})
	if err != nil || !subscribed || got.ID != req.ID {
		t.Fatalf("second ask for [4]: %+v subscribed=%v err=%v", got, subscribed, err)
	}
	more, subscribed, err := f.s.Create(f.ctx, Request{MediaType: "series", TMDBID: 77, Title: "Show", Seasons: []int{4, 5}, KnownSeasons: known, RequestedBy: 9, RequestedByName: "carol"}, CreateOptions{})
	if err != nil || subscribed || !reflect.DeepEqual(more.Seasons, []int{5}) {
		t.Fatalf("ask for [4,5]: %+v subscribed=%v err=%v, want a new request for [5]", more, subscribed, err)
	}
	if subs, _ := f.s.repo.Subscribers(f.ctx, req.ID); len(subs) != 2 {
		t.Errorf("subscribers of [4] = %+v, want bob and carol", subs)
	}

	before := monitoredBySeason(t, f, sr.ID)
	if _, err := f.s.Approve(f.ctx, req.ID, ApproveOptions{}); err != nil {
		t.Fatal(err)
	}
	after := monitoredBySeason(t, f, sr.ID)
	for n := 1; n <= 3; n++ {
		if after[n] != before[n] {
			t.Errorf("S%d monitoring changed: %d -> %d", n, before[n], after[n])
		}
	}
	if after[4] != 2 || after[5] != 0 {
		t.Errorf("monitored episodes = %v, want S4 only", after)
	}
	if s := f.searched("series.search"); len(s) != 1 {
		t.Errorf("searches = %v, want one", s)
	}
	show, _ := f.s.series.Get(f.ctx, sr.ID)
	if show.MonitorNewSeasons {
		t.Error("a season request turned on monitor-new-seasons")
	}
}

// A brand-new show requested for [1,2] has only S1-2 monitored after approval, and new
// seasons aren't monitored.
func TestApproveMonitorsOnlyRequestedSeasons(t *testing.T) {
	f := newApproveFixture(t, showListing(2, 2, 2))
	req, _, err := f.s.Create(f.ctx, Request{MediaType: "series", TMDBID: 77, Title: "Show", Seasons: []int{2, 1}, KnownSeasons: []int{1, 2, 3}, RequestedBy: 7}, CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Approve(f.ctx, req.ID, ApproveOptions{}); err != nil {
		t.Fatal(err)
	}
	sr, err := f.s.series.GetByTMDB(f.ctx, 77)
	if err != nil {
		t.Fatal(err)
	}
	if got := monitoredBySeason(t, f, sr.ID); got[1] != 2 || got[2] != 2 || got[3] != 0 {
		t.Errorf("monitored = %v, want S1-2 only", got)
	}
	if sr.MonitorNewSeasons || !sr.Monitored {
		t.Errorf("gate %v new seasons %v, want on, off", sr.Monitored, sr.MonitorNewSeasons)
	}
	if len(f.searched("series.search")) != 1 {
		t.Errorf("searches = %v", f.jobs.specs)
	}
}

// Staff can approve [1,2] of a [1,2,3] request: the row is rewritten, only S1-2 are
// monitored, and the requester is told S3 wasn't approved. A season the request didn't
// ask for is refused.
func TestApproveTrimsSeasons(t *testing.T) {
	f := newApproveFixture(t, showListing(2, 2, 2, 2))
	req, _, err := f.s.Create(f.ctx, Request{MediaType: "series", TMDBID: 77, Title: "Show", Seasons: []int{1, 2, 3}, KnownSeasons: []int{1, 2, 3, 4}, RequestedBy: 7}, CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Approve(f.ctx, req.ID, ApproveOptions{Seasons: []int{1, 4}}); err != ErrSeasonsNotRequested {
		t.Fatalf("approving an unasked season: err = %v", err)
	}
	got, err := f.s.Approve(f.ctx, req.ID, ApproveOptions{Seasons: []int{2, 1}})
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != StatusApproved || !reflect.DeepEqual(got.Seasons, []int{1, 2}) {
		t.Errorf("approved = %+v, want approved [1,2]", got)
	}
	sr, _ := f.s.series.GetByTMDB(f.ctx, 77)
	if m := monitoredBySeason(t, f, sr.ID); m[1] != 2 || m[2] != 2 || m[3] != 0 || m[4] != 0 {
		t.Errorf("monitored = %v, want S1-2 only", m)
	}
	notes := inbox(t, f, 7)
	if len(notes) != 1 || !strings.Contains(notes[0].Body, "S1–2 of “Show” was approved") || !strings.Contains(notes[0].Body, "Season 3 wasn't approved") {
		t.Errorf("inbox = %+v", notes)
	}
}

// Parallel requests for one show never cover a season twice.
func TestCreateSeriesConcurrent(t *testing.T) {
	f := newApproveFixture(t, showListing(1, 1, 1, 1))
	asks := [][]int{{1, 2}, {2, 3}, {3, 4}, {1, 4}, {2}, {4}, {1, 2, 3, 4}, {3}}
	var wg sync.WaitGroup
	for i, a := range asks {
		wg.Add(1)
		go func(user int64, seasons []int) {
			defer wg.Done()
			if _, _, err := f.s.Create(context.Background(), Request{MediaType: "series", TMDBID: 77, Title: "Show", Seasons: seasons, KnownSeasons: []int{1, 2, 3, 4}, RequestedBy: user}, CreateOptions{}); err != nil {
				t.Errorf("create %v: %v", seasons, err)
			}
		}(int64(i+1), a)
	}
	wg.Wait()
	rows, err := f.s.repo.ListByMedia(f.ctx, "series", 77)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[int]int64{}
	for _, r := range rows {
		if len(r.Seasons) == 0 {
			t.Errorf("request %d is whole-show: %+v", r.ID, r)
		}
		for _, n := range r.Seasons {
			if other, dup := seen[n]; dup {
				t.Errorf("season %d is covered by requests %d and %d", n, other, r.ID)
			}
			seen[n] = r.ID
		}
	}
	if len(seen) != 4 {
		t.Errorf("covered seasons = %v, want all four", seen)
	}
}

// A season-scoped request's notices: 'Season N is ready' for each of several seasons as
// it completes, then one final 'ready' — even with older seasons' gaps — under its own
// references; a whole-show request keeps the reference it always had.
func TestReadyPerSeasonRequest(t *testing.T) {
	f := newApproveFixture(t, showListing(2, 2, 2, 2))
	sr, err := f.shows().Add(f.ctx, 77, "", false)
	if err != nil {
		t.Fatal(err)
	}
	// S1 has a gap the request doesn't care about.
	fillSeason(t, f, sr.ID, 1, 1)
	pair, _, err := f.s.Create(f.ctx, Request{MediaType: "series", TMDBID: 77, Title: "Show", Seasons: []int{3, 4}, KnownSeasons: []int{1, 2, 3, 4}, RequestedBy: 7}, CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Approve(f.ctx, pair.ID, ApproveOptions{}); err != nil {
		t.Fatal(err)
	}
	approvedRef := fmt.Sprintf("series:77:r%d:approved", pair.ID)

	fillSeason(t, f, sr.ID, 3, 2)
	if err := f.s.NotifySeriesReady(f.ctx, sr.ID); err != nil {
		t.Fatal(err)
	}
	want := []string{fmt.Sprintf("series:77:r%d:s3", pair.ID), approvedRef}
	if got := refs(inbox(t, f, 7)); !reflect.DeepEqual(sortedCopy(got), sortedCopy(want)) {
		t.Fatalf("after S3: refs = %v, want %v", got, want)
	}

	fillSeason(t, f, sr.ID, 4, 2)
	if err := f.s.SweepReadyRequests(f.ctx); err != nil {
		t.Fatal(err)
	}
	if err := f.s.NotifySeriesReady(f.ctx, sr.ID); err != nil { // a repeat tells nobody twice
		t.Fatal(err)
	}
	notes := inbox(t, f, 7)
	got := refs(notes)
	if len(got) != 3 || !contains(got, fmt.Sprintf("series:77:r%d", pair.ID)) || contains(got, fmt.Sprintf("series:77:r%d:s4", pair.ID)) {
		t.Errorf("after S4: refs = %v, want the final notice and no separate S4 one", got)
	}
	for _, n := range notes {
		if n.Ref == fmt.Sprintf("series:77:r%d", pair.ID) && !strings.Contains(n.Body, "S3–4 of “Show” is ready") {
			t.Errorf("final body = %q", n.Body)
		}
	}

	// The request now reads available over its own seasons, despite S1's gap.
	list, _, err := f.s.List(f.ctx, ListFilter{})
	if err != nil {
		t.Fatal(err)
	}
	f.s.Track(f.ctx, list, nil, true)
	for _, r := range list {
		if r.ID == pair.ID && (r.Tracking == nil || r.Tracking.Stage != StageAvailable || r.Tracking.Have != 4 || r.Tracking.Total != 4) {
			t.Errorf("tracking = %+v, want available 4/4", r.Tracking)
		}
	}
}

// A whole-show request made before seasons existed tracks and notifies exactly as
// before: the plain "series:<tmdb>" reference, once.
func TestLegacyWholeShowReadyUnchanged(t *testing.T) {
	f := newApproveFixture(t, showListing(2))
	sr, err := f.shows().Add(f.ctx, 77, "", true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.repo.db.Exec(`INSERT INTO requests (media_type, tmdb_id, title, status, requested_by) VALUES ('series', 77, 'Show', 'approved', 7)`); err != nil {
		t.Fatal(err)
	}
	fillSeason(t, f, sr.ID, 1, 1)
	if err := f.s.NotifySeriesReady(f.ctx, sr.ID); err != nil {
		t.Fatal(err)
	}
	if n := inbox(t, f, 7); len(n) != 0 {
		t.Fatalf("told before the show was complete: %+v", n)
	}
	fillSeason(t, f, sr.ID, 1, 2)
	_ = f.s.NotifySeriesReady(f.ctx, sr.ID)
	_ = f.s.SweepReadyRequests(f.ctx)
	if got := refs(inbox(t, f, 7)); !reflect.DeepEqual(got, []string{"series:77"}) {
		t.Errorf("refs = %v, want [series:77]", got)
	}

	// Once delivered it covers nothing: a later ask for the show (files gone, or a new
	// season) is a new request by list, under its own reference.
	if err := f.shows().MarkEpisodeMissing(f.ctx, sr.ID, 1, 2); err != nil {
		t.Fatal(err)
	}
	again, subscribed, err := f.s.Create(f.ctx, Request{MediaType: "series", TMDBID: 77, Title: "Show", KnownSeasons: []int{1}, RequestedBy: 7}, CreateOptions{})
	if err != nil || subscribed || !reflect.DeepEqual(again.Seasons, []int{1}) {
		t.Errorf("ask after delivery: %+v subscribed=%v err=%v, want a new request for [1]", again, subscribed, err)
	}
}

// Asking for the whole show without a catalogue (TMDB down, or an import) keeps a
// whole-show request, as before seasons existed, and a second one follows it.
func TestCreateWholeShowWithoutCatalogue(t *testing.T) {
	f := newApproveFixture(t, showListing(1))
	first, _, err := f.s.Create(f.ctx, Request{MediaType: "series", TMDBID: 77, Title: "Show", RequestedBy: 7}, CreateOptions{})
	if err != nil || first.Seasons != nil {
		t.Fatalf("first = %+v err %v", first, err)
	}
	second, subscribed, err := f.s.Create(f.ctx, Request{MediaType: "series", TMDBID: 77, Title: "Show", RequestedBy: 8}, CreateOptions{})
	if err != nil || !subscribed || second.ID != first.ID {
		t.Fatalf("second = %+v subscribed %v err %v", second, subscribed, err)
	}
	// Movies are still one request per title.
	if _, err := f.s.repo.Create(f.ctx, Request{MediaType: "movie", TMDBID: 5, Title: "M", Status: StatusPending}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.repo.Create(f.ctx, Request{MediaType: "movie", TMDBID: 5, Title: "M", Status: StatusPending}); err != ErrExists {
		t.Errorf("duplicate movie: err = %v, want ErrExists", err)
	}
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

func sortedCopy(xs []string) []string {
	out := append([]string{}, xs...)
	sort.Strings(out)
	return out
}
