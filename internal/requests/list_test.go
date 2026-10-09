package requests

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/tristenlammi/arrmada/internal/automation"
	"github.com/tristenlammi/arrmada/internal/download"
)

// mk stores a request straight in the table and returns its id.
func mk(t *testing.T, s *Service, tmdb int, status string, by int64) int64 {
	t.Helper()
	rq, err := s.repo.Create(context.Background(), Request{MediaType: "movie", TMDBID: tmdb, Title: fmt.Sprintf("t%d", tmdb), Status: status, RequestedBy: by})
	if err != nil {
		t.Fatal(err)
	}
	return rq.ID
}

func titles(reqs []Request) []string {
	out := []string{}
	for _, r := range reqs {
		out = append(out, r.Title)
	}
	return out
}

// Each section is its own predicate and order; counts cover every section; paging pages.
func TestListSections(t *testing.T) {
	s := newTestService(t)
	ctx := context.Background()
	db := s.repo.db
	p1, p2 := mk(t, s, 1, StatusPending, 7), mk(t, s, 2, StatusPending, 7)
	if _, err := db.Exec(`UPDATE requests SET created_at = '2020-01-01 00:00:00' WHERE id = ?`, p2); err != nil {
		t.Fatal(err)
	}
	a1, a2 := mk(t, s, 3, StatusApproved, 7), mk(t, s, 4, StatusApproved, 8)
	if _, err := db.Exec(`UPDATE requests SET updated_at = '2020-01-01 00:00:00' WHERE id = ?`, a1); err != nil {
		t.Fatal(err)
	}
	r1, r2 := mk(t, s, 5, StatusApproved, 7), mk(t, s, 6, StatusApproved, 7)
	_ = s.repo.MarkReady(ctx, r1, 100)
	_ = s.repo.MarkReady(ctx, r2, 200)
	d1 := mk(t, s, 7, StatusDeclined, 8)

	cases := []struct {
		f    ListFilter
		want []int64
	}{
		{ListFilter{Section: SectionNeedsApproval}, []int64{p2, p1}}, // oldest first
		{ListFilter{Section: SectionInProgress}, []int64{a2, a1}},    // latest change first
		{ListFilter{Section: SectionReady}, []int64{r2, r1}},         // latest ready first
		{ListFilter{Section: SectionDeclined}, []int64{d1}},
		{ListFilter{}, []int64{d1, r2, r1, a2, a1, p2, p1}},  // everything, newest first
		{ListFilter{Status: StatusPending}, []int64{p2, p1}}, // legacy status filter
		{ListFilter{Section: SectionNeedsApproval, Limit: 1, Offset: 1}, []int64{p1}},
		{ListFilter{Section: SectionInProgress, UserID: 8}, []int64{a2}},
		{ListFilter{Query: "t5"}, []int64{r1}},
	}
	for _, c := range cases {
		got, total, err := s.repo.List(ctx, c.f)
		if err != nil {
			t.Fatal(err)
		}
		var ids []int64
		for _, r := range got {
			ids = append(ids, r.ID)
		}
		if fmt.Sprint(ids) != fmt.Sprint(c.want) {
			t.Errorf("%+v: ids %v, want %v", c.f, ids, c.want)
		}
		if c.f.Limit == 0 && total != len(c.want) {
			t.Errorf("%+v: total %d, want %d", c.f, total, len(c.want))
		}
		if c.f.Limit > 0 && total != 2 {
			t.Errorf("%+v: total %d, want 2 (all pages)", c.f, total)
		}
	}
	got, err := s.repo.Counts(ctx, ListFilter{Section: SectionReady, Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if got != (Counts{NeedsApproval: 2, InProgress: 2, Ready: 2, Declined: 1}) {
		t.Errorf("counts = %+v", got)
	}
	// A search term is literal: % and _ don't match everything.
	if got, _, _ := s.repo.List(ctx, ListFilter{Query: "%"}); len(got) != 0 {
		t.Errorf("a %% search matched %v", titles(got))
	}
	// Ready within a window.
	if _, err := db.Exec(`UPDATE requests SET ready_at = ? WHERE id = ?`, time.Now().Unix(), r2); err != nil {
		t.Fatal(err)
	}
	if got, _, _ := s.repo.List(ctx, ListFilter{Section: SectionReady, ReadyWithinDays: 14}); len(got) != 1 || got[0].ID != r2 {
		t.Errorf("ready within 14 days = %v", titles(got))
	}
}

// A user's list holds their own requests and the ones they follow, each with its
// relation; the section filter still applies.
func TestListIncludesSubscriptions(t *testing.T) {
	s := newTestService(t)
	ctx := context.Background()
	mine := mk(t, s, 1, StatusPending, 8)
	followed := mk(t, s, 2, StatusPending, 7)
	followedApproved := mk(t, s, 3, StatusApproved, 7)
	mk(t, s, 4, StatusPending, 7) // not bob's business
	for _, id := range []int64{followed, followedApproved} {
		if err := s.repo.AddSubscriber(ctx, id, 8, "bob"); err != nil {
			t.Fatal(err)
		}
	}
	got, total, err := s.List(ctx, ListFilter{Section: SectionNeedsApproval, UserID: 8, IncludeJoined: true})
	if err != nil {
		t.Fatal(err)
	}
	rel := map[int64]string{}
	for _, r := range got {
		rel[r.ID] = r.Relation
	}
	if total != 2 || rel[mine] != RelationOwner || rel[followed] != RelationSubscriber {
		t.Errorf("bob's pending = %v (total %d)", rel, total)
	}
	c, err := s.Counts(ctx, ListFilter{UserID: 8, IncludeJoined: true})
	if err != nil {
		t.Fatal(err)
	}
	if c != (Counts{NeedsApproval: 2, InProgress: 1}) {
		t.Errorf("bob's counts = %+v", c)
	}
	// Own only, as My Books asks.
	if got, _, _ := s.List(ctx, ListFilter{UserID: 8}); len(got) != 1 || got[0].ID != mine {
		t.Errorf("own only = %v", titles(got))
	}
}

// Stopping following drops only your own subscription; not following is ErrNotFound.
func TestUnsubscribeOnlySelf(t *testing.T) {
	s := newTestService(t)
	ctx := context.Background()
	id := mk(t, s, 1, StatusPending, 7)
	for _, u := range []int64{8, 9} {
		if err := s.repo.AddSubscriber(ctx, id, u, "x"); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Unsubscribe(ctx, id, 8); err != nil {
		t.Fatal(err)
	}
	if err := s.Unsubscribe(ctx, id, 8); !errors.Is(err, ErrNotFound) {
		t.Errorf("unsubscribing twice: %v, want ErrNotFound", err)
	}
	if err := s.Unsubscribe(ctx, id, 7); !errors.Is(err, ErrNotFound) {
		t.Errorf("the owner unsubscribing: %v, want ErrNotFound", err)
	}
	subs, _ := s.repo.Subscribers(ctx, id)
	if len(subs) != 1 || subs[0].UserID != 9 {
		t.Errorf("subscribers = %+v, want 9 untouched", subs)
	}
	// Bob no longer hears about it.
	if err := s.Decline(ctx, id, DeclineOptions{}); err != nil {
		t.Fatal(err)
	}
	if inbox, _ := s.repo.listUserNotifications(ctx, 8); len(inbox) != 0 {
		t.Errorf("an unfollowed request still notified: %+v", inbox)
	}
}

// A requester sees a request by id only as its owner or a follower; staff see anyone's,
// with its followers.
func TestGetRequestScope(t *testing.T) {
	s := newTestService(t)
	ctx := context.Background()
	id := mk(t, s, 1, StatusPending, 7)
	if err := s.repo.AddSubscriber(ctx, id, 8, "bob"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Detail(ctx, id, 9, false); !errors.Is(err, ErrNotFound) {
		t.Errorf("a stranger: %v, want ErrNotFound", err)
	}
	if r, err := s.Detail(ctx, id, 8, false); err != nil || r.Relation != RelationSubscriber || len(r.Followers) != 0 {
		t.Errorf("the follower: %+v %v", r, err)
	}
	if r, err := s.Detail(ctx, id, 7, false); err != nil || r.Relation != RelationOwner {
		t.Errorf("the owner: %+v %v", r, err)
	}
	if r, err := s.Detail(ctx, id, 1, true); err != nil || len(r.Followers) != 1 || r.Followers[0].Name != "bob" {
		t.Errorf("staff: %+v %v", r, err)
	}
	if _, err := s.Detail(ctx, 999, 1, true); !errors.Is(err, ErrNotFound) {
		t.Errorf("a missing request: %v", err)
	}
}

// countingRecord counts how often the acquisition record is read.
type countingRecord struct {
	searcher
	reads int
}

func (c *countingRecord) ActiveByItem(ctx context.Context, mediaType string) (map[int64][]automation.Acquisition, error) {
	c.reads++
	return c.searcher.ActiveByItem(ctx, mediaType)
}

// Tracking a page reads the record once per media type, however many requests it holds,
// and finds the same stages as reading it per request did.
func TestTrackBatchedGrabs(t *testing.T) {
	s := newTestService(t)
	ctx := context.Background()
	rec := &countingRecord{searcher: s.coord}
	s.coord = rec
	old := time.Now().Add(-3 * time.Hour).UTC().Format("2006-01-02 15:04:05")
	var reqs []Request
	var queue []download.Item
	for i := int64(1); i <= 5; i++ {
		hash := fmt.Sprintf("H%d", i)
		if _, err := s.repo.db.Exec(`INSERT INTO grabs (movie_id, title, status, media_type, info_hash, grabbed_at) VALUES (?, 'x', 'grabbed', 'movie', ?, ?)`, i, hash, old); err != nil {
			t.Fatal(err)
		}
		queue = append(queue, download.Item{Hash: hash, State: "downloading", Progress: 0.5, SizeBytes: 100, DownloadedBytes: 50, DownSpeed: 1})
		reqs = append(reqs, Request{Title: hash, Status: StatusApproved, MediaType: "movie", libID: i, released: true})
	}
	reqs = append(reqs, Request{Title: "waiting", Status: StatusPending, MediaType: "series", libID: 9})
	s.Track(ctx, reqs, queue, true)
	if rec.reads != 1 {
		t.Errorf("the record was read %d times, want once", rec.reads)
	}
	for _, r := range reqs[:5] {
		if r.Tracking.Stage != StageDownloading || r.Tracking.Progress != 0.5 {
			t.Errorf("%s: %+v", r.Title, r.Tracking)
		}
	}
}

// When the download client can't be read, the record's own last view stands in: a
// download it saw running is still on its way, one it saw finish is importing.
func TestTrackReadsTheRecordWhenTheQueueIsUnknown(t *testing.T) {
	s := newTestService(t)
	ctx := context.Background()
	old := time.Now().Add(-3 * time.Hour).UTC().Format("2006-01-02 15:04:05")
	for _, g := range []struct {
		id    int64
		phase string
	}{{1, "downloading"}, {2, "complete"}, {3, "missing"}} {
		if _, err := s.repo.db.Exec(`INSERT INTO grabs (movie_id, title, status, media_type, info_hash, grabbed_at, phase, updated_at)
			VALUES (?, 'x', 'grabbed', 'movie', ?, ?, ?, ?)`, g.id, fmt.Sprintf("H%d", g.id), old, g.phase, time.Now().UnixMilli()); err != nil {
			t.Fatal(err)
		}
	}
	reqs := []Request{
		{Title: "running", Status: StatusApproved, MediaType: "movie", libID: 1, released: true},
		{Title: "finished", Status: StatusApproved, MediaType: "movie", libID: 2, released: true},
		{Title: "gone", Status: StatusApproved, MediaType: "movie", libID: 3, released: true},
	}
	s.Track(ctx, reqs, nil, false)
	want := []string{StageQueued, StageImporting, StageSearching}
	for i, r := range reqs {
		if r.Tracking.Stage != want[i] {
			t.Errorf("%s: stage %s, want %s", r.Title, r.Tracking.Stage, want[i])
		}
	}
	// With the queue known, a torrent it doesn't list (and grabbed long ago) isn't in flight.
	s.Track(ctx, reqs, nil, true)
	for _, r := range reqs {
		if r.Tracking.Stage != StageSearching {
			t.Errorf("queue known, %s: stage %s, want searching", r.Title, r.Tracking.Stage)
		}
	}
}

// The download client is read only when something on the page could be downloading.
func TestListSkipsQueueWhenNothingInFlight(t *testing.T) {
	cases := []struct {
		name string
		rq   Request
		need bool
	}{
		{"pending", Request{Status: StatusPending, MediaType: "movie", libID: 1}, false},
		{"declined", Request{Status: StatusDeclined, MediaType: "movie", libID: 1}, false},
		{"approved, not added", Request{Status: StatusApproved, MediaType: "movie"}, false},
		{"approved and told ready", Request{Status: StatusApproved, MediaType: "movie", libID: 1, ReadyAt: 5}, false},
		{"approved, on its way", Request{Status: StatusApproved, MediaType: "movie", libID: 1}, true},
		{"a ready series keeps getting episodes", Request{Status: StatusApproved, MediaType: "series", libID: 1, ReadyAt: 5}, true},
	}
	for _, c := range cases {
		if got := NeedsQueue([]Request{c.rq}); got != c.need {
			t.Errorf("%s: NeedsQueue = %v, want %v", c.name, got, c.need)
		}
	}
}
