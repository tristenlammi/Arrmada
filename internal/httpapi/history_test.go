package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
)

// seedHistory puts one title of each kind in the library and events in all four tables,
// several in the same second (sweeps write in bursts), so ties are exercised.
func seedHistory(t *testing.T, a *api) {
	t.Helper()
	db := a.deps.Store.DB()
	stmts := []string{
		`INSERT INTO movies (id, tmdb_id, title, poster_url) VALUES (1, 10, 'Dune', 'https://image.tmdb.org/t/p/w500/dune.jpg')`,
		`INSERT INTO series (id, tmdb_id, title) VALUES (1, 20, 'Silo')`,
		`INSERT INTO books (id, ol_key, title, cover_url) VALUES (1, 'OL1W', 'Red Rising', 'https://covers.openlibrary.org/b/id/1-L.jpg')`,
		`INSERT INTO artists (id, mbid, name, image_url) VALUES (1, 'mb-1', 'Low', '')`,
		`INSERT INTO movie_events (movie_id, event, detail, created_at) VALUES
			(1, 'grabbed',  'Dune.2021.2160p.WEB-DL', '2026-10-01 10:00:00'),
			(1, 'imported', 'Imported Dune.mkv',      '2026-10-01 11:00:00'),
			(1, 'searched', 'Found nothing new',      '2026-10-02 09:00:00'),
			(1, 'upgraded', 'Upgraded to Dune.mkv',   '2026-10-03 12:00:00')`,
		`INSERT INTO series_events (series_id, event, detail, created_at) VALUES
			(1, 'grabbed',     'Silo.S02E04.1080p',              '2026-10-01 11:00:00'),
			(1, 'imported',    'S02E04',                         '2026-10-01 11:00:00'),
			(1, 'blocklisted', 'Silo.S02E05.CAM',                '2026-10-02 09:00:00'),
			(1, 'seeded',      'Silo.S02E04 — ratio 1.0 reached', '2026-10-03 12:00:00')`,
		`INSERT INTO book_events (book_id, event, detail, created_at) VALUES
			(1, 'grabbed',  'Red.Rising.EPUB', '2026-10-01 11:00:00'),
			(1, 'failed',   'Stalled for 6h',  '2026-10-02 09:00:00'),
			(1, 'added',    'Added to library','2026-10-02 09:30:00')`,
		`INSERT INTO artist_events (artist_id, event, detail, created_at) VALUES
			(1, 'grabbed',  'Low - HEY WHAT (2021) [FLAC]', '2026-10-01 11:00:00'),
			(1, 'imported', 'HEY WHAT',                      '2026-10-03 12:00:00')`,
	}
	for _, q := range stmts {
		if _, err := db.ExecContext(context.Background(), q); err != nil {
			t.Fatalf("seed: %v\n%s", err, q)
		}
	}
}

func feedKeys(evs []historyEvent) []string {
	out := make([]string, len(evs))
	for i, e := range evs {
		out[i] = fmt.Sprintf("%s:%d:%s", e.Kind, e.ID, e.Event)
	}
	return out
}

func TestEventsFeedMergesAllFourKindsNewestFirst(t *testing.T) {
	a := dashAPI(t)
	seedHistory(t, a)
	evs, err := eventsFeed(context.Background(), a.deps.Store.DB(), eventsFilter{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 13 {
		t.Fatalf("got %d events, want all 13: %v", len(evs), feedKeys(evs))
	}
	for i := 1; i < len(evs); i++ {
		if evs[i].AtMS > evs[i-1].AtMS {
			t.Fatalf("not newest first at %d: %v", i, feedKeys(evs))
		}
	}
	kinds := map[string]historyEvent{}
	for _, e := range evs {
		kinds[e.Kind] = e
	}
	for kind, title := range map[string]string{"movie": "Dune", "series": "Silo", "book": "Red Rising", "music": "Low"} {
		if kinds[kind].Title != title || kinds[kind].ItemID != 1 {
			t.Errorf("%s resolved to %+v, want %q — the join is wrong", kind, kinds[kind], title)
		}
	}
	if kinds["movie"].PosterURL == "" || kinds["book"].PosterURL == "" {
		t.Errorf("posters missing: %+v / %+v", kinds["movie"], kinds["book"])
	}
	// The same second: movie before series before book before music, newest row first.
	at := evs[0].AtMS
	if evs[0].Kind != "movie" || evs[1].Kind != "series" || evs[2].Kind != "music" || evs[1].AtMS != at || evs[2].AtMS != at {
		t.Errorf("ties out of order: %v", feedKeys(evs[:3]))
	}
	if got := evs[0].Category; got != evUpgraded {
		t.Errorf("upgraded categorised as %q", got)
	}
}

func TestEventsFeedFilters(t *testing.T) {
	a := dashAPI(t)
	seedHistory(t, a)
	db := a.deps.Store.DB()
	ctx := context.Background()

	evs, err := eventsFeed(ctx, db, eventsFilter{Kinds: []string{"series"}, Categories: []string{evImported}, Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 1 || evs[0].Kind != "series" || evs[0].Event != "imported" {
		t.Errorf("Series + Imported = %v, want only the series import", feedKeys(evs))
	}

	evs, _ = eventsFeed(ctx, db, eventsFilter{Q: "dune", Limit: 50})
	for _, e := range evs {
		if e.Title != "Dune" {
			t.Errorf("q=dune matched %+v", e)
		}
	}
	if len(evs) != 4 {
		t.Errorf("q=dune found %d, want Dune's 4 events", len(evs))
	}
	// The detail (a release name) matches too, and LIKE wildcards are literal.
	if evs, _ = eventsFeed(ctx, db, eventsFilter{Q: "CAM", Limit: 50}); len(evs) != 1 || evs[0].Event != "blocklisted" {
		t.Errorf("q=CAM = %v, want the blocklisted release", feedKeys(evs))
	}
	if evs, _ = eventsFeed(ctx, db, eventsFilter{Q: "%", Limit: 50}); len(evs) != 0 {
		t.Errorf("q=%% matched %v; a wildcard must be literal", feedKeys(evs))
	}

	// "other" is the bookkeeping the default view leaves out: searched and added.
	evs, _ = eventsFeed(ctx, db, eventsFilter{Categories: []string{evOther}, Limit: 50})
	if strings.Join(feedKeys(evs), ",") != "book:3:added,movie:3:searched" {
		t.Errorf("other = %v", feedKeys(evs))
	}
}

func TestEventsFeedPagesWithoutGapsOrOverlap(t *testing.T) {
	a := dashAPI(t)
	seedHistory(t, a)
	db := a.deps.Store.DB()
	ctx := context.Background()
	all, err := eventsFeed(ctx, db, eventsFilter{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	for _, size := range []int{1, 2, 3, 5} {
		var got []historyEvent
		var before *eventsCursor
		for page := 0; page < 50; page++ {
			evs, err := eventsFeed(ctx, db, eventsFilter{Limit: size, Before: before})
			if err != nil {
				t.Fatal(err)
			}
			got = append(got, evs...)
			if len(evs) < size {
				break
			}
			c := evs[len(evs)-1].cursor()
			// Through the wire format, as the page sends it back.
			parsed, err := parseEventsCursor(c.String())
			if err != nil {
				t.Fatal(err)
			}
			before = &parsed
		}
		if strings.Join(feedKeys(got), ",") != strings.Join(feedKeys(all), ",") {
			t.Errorf("pages of %d:\n got %v\nwant %v", size, feedKeys(got), feedKeys(all))
		}
	}
}

// Each branch reads its own created_at index, so a long history isn't sorted per request.
func TestEventsFeedUsesTheCreatedIndexes(t *testing.T) {
	a := dashAPI(t)
	seedHistory(t, a)
	plan := explainFeed(t, a, eventsFilter{Limit: 50, Categories: []string{evGrabbed, evImported}})
	for _, idx := range []string{"idx_movie_events_created", "idx_series_events_created", "idx_book_events_created", "idx_artist_events_created"} {
		if !strings.Contains(plan, idx) {
			t.Errorf("plan doesn't use %s:\n%s", idx, plan)
		}
	}
}

// explainFeed is EXPLAIN QUERY PLAN over the SQL eventsFeed runs for f.
func explainFeed(t *testing.T, a *api, f eventsFilter) string {
	t.Helper()
	q, args := eventsFeedSQL(a.deps.Store.DB(), f)
	rows, err := a.deps.Store.DB().QueryContext(context.Background(), "EXPLAIN QUERY PLAN "+q, args...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var b strings.Builder
	cols, _ := rows.Columns()
	vals := make([]any, len(cols))
	ptrs := make([]any, len(cols))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	for rows.Next() {
		if err := rows.Scan(ptrs...); err != nil {
			t.Fatal(err)
		}
		fmt.Fprintln(&b, vals...)
	}
	return b.String()
}

func TestHistoryEndpoint(t *testing.T) {
	a := dashAPI(t)
	seedHistory(t, a)
	get := func(q string) (int, map[string]json.RawMessage) {
		rec := httptest.NewRecorder()
		a.handleHistory(rec, httptest.NewRequest("GET", "/api/v1/history"+q, nil))
		var out map[string]json.RawMessage
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}
	events := func(raw json.RawMessage) []historyEvent {
		var evs []historyEvent
		if err := json.Unmarshal(raw, &evs); err != nil {
			t.Fatal(err)
		}
		return evs
	}

	// The default leaves the bookkeeping out; event=all brings it back.
	code, out := get("")
	if code != 200 || len(events(out["events"])) != 11 {
		t.Fatalf("default: %d %s", code, out["events"])
	}
	if _, out = get("?event=all"); len(events(out["events"])) != 13 {
		t.Errorf("event=all: %s", out["events"])
	}
	if _, out = get("?kind=series&event=imported"); len(events(out["events"])) != 1 {
		t.Errorf("series imports: %s", out["events"])
	}
	// A full page says where the next starts; the last one says nothing.
	_, out = get("?limit=4")
	var next string
	_ = json.Unmarshal(out["next_before"], &next)
	if next == "" {
		t.Fatal("a full page must carry next_before")
	}
	_, out = get("?limit=50&before=" + next)
	if n := len(events(out["events"])); n != 7 {
		t.Errorf("the rest after 4 = %d, want 7", n)
	}
	_ = json.Unmarshal(out["next_before"], &next)
	if next != "" {
		t.Errorf("last page next_before = %q", next)
	}
	for _, bad := range []string{"?kind=audiobook", "?event=listened", "?before=12", "?before=1:podcast:2", "?limit=0", "?limit=500"} {
		if code, _ := get(bad); code != 400 {
			t.Errorf("%s answered %d, want 400", bad, code)
		}
	}
}
