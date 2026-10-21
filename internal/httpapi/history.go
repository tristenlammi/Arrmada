package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/tristenlammi/arrmada/internal/displaytitle"
	"github.com/tristenlammi/arrmada/internal/library"
	"github.com/tristenlammi/arrmada/internal/store"
)

// History is one log of what Arrmada did to every title — grabs, imports, upgrades,
// failures, blocklists, removals, seed clean-ups — read from the per-title event tables
// (movie_events, series_events, book_events, artist_events) that each detail page already
// shows on its own. The Dashboard's "Recent activity" is the newest slice of the same
// feed. These are acquisition events only: audiobook listening lives elsewhere and never
// appears here.

// Event categories: the free-text event names, folded into a small fixed set the page
// filters on. A name not listed is "other" (added, refreshed, searched, monitored…), left
// out of the default view because it's bookkeeping rather than something that happened
// to a download.
const (
	evGrabbed     = "grabbed"
	evImported    = "imported"
	evUpgraded    = "upgraded"
	evFailed      = "failed"
	evBlocklisted = "blocklisted"
	evRemoved     = "removed"
	evSeeded      = "seeded"
	evDeleted     = "deleted"
	evRenamed     = "renamed"
	evReview      = "review"
	evOther       = "other"
)

// eventCategories maps each event name the services write to its category.
var eventCategories = map[string]string{
	"grabbed":         evGrabbed,
	"handoff":         evGrabbed, // SABnzbd fetched the NZB the indexer blocked Arrmada from
	"imported":        evImported,
	"upgraded":        evUpgraded,
	"failed":          evFailed,
	"delete.failed":   evFailed,
	"merge-failed":    evFailed,
	"blocklisted":     evBlocklisted,
	"rejected":        evBlocklisted, // rejected in review: removed and blocklisted
	"removed":         evRemoved,
	"seeded":          evSeeded,
	"deleted":         evDeleted,
	"file.deleted":    evDeleted,
	"version_removed": evDeleted,
	"renamed":         evRenamed,
	"renumbered":      evRenamed,
	"rename.skipped":  evRenamed,
	"dismissed":       evReview,
}

// eventCategoryOrder is every category, in the order the page lists its filter chips.
var eventCategoryOrder = []string{evGrabbed, evImported, evUpgraded, evFailed, evBlocklisted, evRemoved, evSeeded, evDeleted, evRenamed, evReview, evOther}

// eventCategory is the filter category of an event name.
func eventCategory(event string) string {
	if c, ok := eventCategories[event]; ok {
		return c
	}
	return evOther
}

// eventsOf is every event name in the given categories, sorted.
func eventsOf(cats map[string]bool) []string {
	var out []string
	for name, c := range eventCategories {
		if cats[c] {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// eventBranch is one media type's event table, joined to its titles (alias i) for the name
// and poster. title is the SQL for the name shown — a film's or show's display title — and
// alt the canonical one a search also matches ("" when they're the same). rank orders the
// kinds within the same second, so paging is stable.
type eventBranch struct {
	kind, table, fk, items, title, alt, poster string
	rank                                       int
}

var eventBranches = []eventBranch{
	{kind: "movie", table: "movie_events", fk: "movie_id", items: "movies", title: displaytitle.SQL("i"), alt: "i.title", poster: "poster_url", rank: 4},
	{kind: "series", table: "series_events", fk: "series_id", items: "series", title: displaytitle.SQL("i"), alt: "i.title", poster: "poster_url", rank: 3},
	{kind: "book", table: "book_events", fk: "book_id", items: "books", title: "i.title", poster: "cover_url", rank: 2},
	{kind: "music", table: "artist_events", fk: "artist_id", items: "artists", title: "i.name", poster: "image_url", rank: 1},
}

func branchFor(kind string) (eventBranch, bool) {
	for _, b := range eventBranches {
		if b.kind == kind {
			return b, true
		}
	}
	return eventBranch{}, false
}

// eventsCursor is where a page of the feed ends: the last event's time, kind and row id.
// The next page is everything strictly older in (created_at, rank, id) order.
type eventsCursor struct {
	AtMS int64
	Kind string
	ID   int64
}

func (c eventsCursor) String() string {
	return strconv.FormatInt(c.AtMS, 10) + ":" + c.Kind + ":" + strconv.FormatInt(c.ID, 10)
}

func parseEventsCursor(s string) (eventsCursor, error) {
	parts := strings.Split(s, ":")
	if len(parts) != 3 {
		return eventsCursor{}, fmt.Errorf("before must be <at_ms>:<kind>:<id>")
	}
	at, err1 := strconv.ParseInt(parts[0], 10, 64)
	id, err2 := strconv.ParseInt(parts[2], 10, 64)
	if _, ok := branchFor(parts[1]); err1 != nil || err2 != nil || !ok || at <= 0 {
		return eventsCursor{}, fmt.Errorf("before must be <at_ms>:<kind>:<id>")
	}
	return eventsCursor{AtMS: at, Kind: parts[1], ID: id}, nil
}

// eventsFilter narrows the feed. Zero values mean "everything".
type eventsFilter struct {
	Kinds []string // movie, series, book, music
	// Categories to include (eventCategory values). Empty means every event, "other"
	// included; the History endpoint fills in its own default.
	Categories []string
	Q          string // matches the title or the event's detail
	Before     *eventsCursor
	Limit      int
}

// historyEvent is one row of the feed.
type historyEvent struct {
	Kind      string `json:"kind"`    // movie | series | book | music
	ItemID    int64  `json:"item_id"` // the movie, series, book or artist
	Title     string `json:"title"`
	PosterURL string `json:"poster_url,omitempty"`
	Event     string `json:"event"`
	Category  string `json:"category"`
	Detail    string `json:"detail"`
	AtMS      int64  `json:"at_ms"`
	// ID is the event's row id within its kind's table; with kind it keys the row.
	ID int64 `json:"id"`
	// Network is "usenet" when the line is about a release grabbed from Usenet.
	Network string `json:"network,omitempty"`
}

func (e historyEvent) cursor() eventsCursor {
	return eventsCursor{AtMS: e.AtMS, Kind: e.Kind, ID: e.ID}
}

// sqliteStamp is how CURRENT_TIMESTAMP stores a time, which the cursor compares against.
const sqliteStamp = "2006-01-02 15:04:05"

// eventsFeed reads the newest events matching f across the four event tables.
func eventsFeed(ctx context.Context, db store.Pool, f eventsFilter) ([]historyEvent, error) {
	q, args := eventsFeedSQL(db, f)
	if q == "" {
		return []historyEvent{}, nil
	}
	rows, err := db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []historyEvent{}
	for rows.Next() {
		var ev historyEvent
		var rank int
		var at string
		if err := rows.Scan(&ev.Kind, &rank, &ev.ID, &ev.ItemID, &ev.Title, &ev.PosterURL, &ev.Event, &ev.Detail, &at); err != nil {
			return nil, err
		}
		ev.Category = eventCategory(ev.Event)
		ev.AtMS = parseEventStamp(at).UnixMilli()
		out = append(out, ev)
	}
	return out, rows.Err()
}

// eventsFeedSQL builds the feed's query; "" when nothing can match. Each branch applies
// the filters, the keyset and the limit itself, so it walks its own created_at index and
// stops early; the outer query merges at most Limit rows from each.
func eventsFeedSQL(db store.Execer, f eventsFilter) (string, []any) {
	limit := f.Limit
	if limit <= 0 {
		limit = 50
	}
	kinds := map[string]bool{}
	for _, k := range f.Kinds {
		kinds[k] = true
	}
	var eventCond string
	var eventArgs []any
	if len(f.Categories) > 0 {
		cats := map[string]bool{}
		for _, c := range f.Categories {
			cats[c] = true
		}
		var ors []string
		if names := eventsOf(cats); len(names) > 0 {
			ors = append(ors, "e.event IN ("+placeholders(len(names))+")")
			for _, n := range names {
				eventArgs = append(eventArgs, n)
			}
		}
		if cats[evOther] {
			known := eventsOf(allCategories())
			ors = append(ors, "e.event NOT IN ("+placeholders(len(known))+")")
			for _, n := range known {
				eventArgs = append(eventArgs, n)
			}
		}
		if len(ors) == 0 {
			return "", nil // only unknown categories asked for
		}
		eventCond = "(" + strings.Join(ors, " OR ") + ")"
	}
	like := ""
	if q := strings.TrimSpace(f.Q); q != "" {
		like = "%" + escapeLike(q) + "%"
	}

	var parts []string
	var args []any
	for _, b := range eventBranches {
		if len(kinds) > 0 && !kinds[b.kind] {
			continue
		}
		var conds []string
		var bargs []any
		if eventCond != "" {
			conds = append(conds, eventCond)
			bargs = append(bargs, eventArgs...)
		}
		if like != "" {
			// The shown title, the canonical one (a search by either finds it) and the line.
			alt, n := "", 2
			if b.alt != "" {
				alt, n = ` OR `+store.LikeASCII(db, b.alt, "?", true), 3
			}
			conds = append(conds, `(`+store.LikeASCII(db, b.title, "?", true)+alt+` OR `+store.LikeASCII(db, "e.detail", "?", true)+`)`)
			for range n {
				bargs = append(bargs, like)
			}
		}
		if c := f.Before; c != nil {
			at := time.UnixMilli(c.AtMS).UTC().Format(sqliteStamp)
			cr, _ := branchFor(c.Kind)
			switch {
			case b.rank < cr.rank: // later in the same second
				conds = append(conds, "e.created_at <= ?")
				bargs = append(bargs, at)
			case b.rank == cr.rank:
				conds = append(conds, "(e.created_at < ? OR (e.created_at = ? AND e.id < ?))")
				bargs = append(bargs, at, at, c.ID)
			default:
				conds = append(conds, "e.created_at < ?")
				bargs = append(bargs, at)
			}
		}
		where := ""
		if len(conds) > 0 {
			where = " WHERE " + strings.Join(conds, " AND ")
		}
		parts = append(parts, `SELECT * FROM (SELECT '`+b.kind+`' AS kind, `+strconv.Itoa(b.rank)+` AS krank, e.id AS eid,
		i.id AS item_id, `+b.title+` AS title, i.`+b.poster+` AS poster, e.event AS event, e.detail AS detail,
		CAST(e.created_at AS TEXT) AS at
		  FROM `+b.table+` e JOIN `+b.items+` i ON i.id = e.`+b.fk+where+`
		 ORDER BY e.created_at DESC, e.id DESC LIMIT ?)`)
		args = append(args, bargs...)
		args = append(args, limit)
	}
	if len(parts) == 0 {
		return "", nil
	}
	q := strings.Join(parts, "\nUNION ALL\n") + "\nORDER BY at DESC, krank DESC, eid DESC LIMIT ?"
	return q, append(args, limit)
}

func allCategories() map[string]bool {
	out := map[string]bool{}
	for _, c := range eventCategoryOrder {
		out[c] = true
	}
	return out
}

func placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

// escapeLike makes a search box's text literal inside a LIKE pattern (ESCAPE '\').
func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

// parseEventStamp reads a created_at as stored: CURRENT_TIMESTAMP's UTC text, or an RFC
// 3339 time. An unreadable one reads as the zero time.
func parseEventStamp(s string) time.Time {
	for _, layout := range []string{sqliteStamp, time.RFC3339Nano, "2006-01-02 15:04:05.999999999-07:00", "2006-01-02T15:04:05Z"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	return time.Time{}
}

// handleHistory is the History tab: GET /api/v1/history?kind=&event=&q=&before=&limit=.
// kind and event take comma-separated lists; event=all includes the bookkeeping events
// ("other") the default leaves out. Pages with next_before until it comes back empty.
// ?view=imports is the old list of imported files (with where each went), kept for one
// release.
func (a *api) handleHistory(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if q.Get("view") == "imports" {
		a.handleImportsList(w, r)
		return
	}
	f := eventsFilter{Limit: 50, Q: q.Get("q")}
	if len(f.Q) > 200 {
		a.writeError(w, http.StatusBadRequest, "search is too long")
		return
	}
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 200 {
			a.writeError(w, http.StatusBadRequest, "limit must be 1–200")
			return
		}
		f.Limit = n
	}
	for _, k := range splitList(q.Get("kind")) {
		if _, ok := branchFor(k); !ok {
			a.writeError(w, http.StatusBadRequest, "unknown kind "+strconv.Quote(k))
			return
		}
		f.Kinds = append(f.Kinds, k)
	}
	switch ev := splitList(q.Get("event")); {
	case len(ev) == 1 && ev[0] == "all":
		// every event, bookkeeping included
	case len(ev) == 0:
		for _, c := range eventCategoryOrder {
			if c != evOther {
				f.Categories = append(f.Categories, c)
			}
		}
	default:
		known := allCategories()
		for _, c := range ev {
			if !known[c] {
				a.writeError(w, http.StatusBadRequest, "unknown event "+strconv.Quote(c))
				return
			}
			f.Categories = append(f.Categories, c)
		}
	}
	if v := q.Get("before"); v != "" {
		c, err := parseEventsCursor(v)
		if err != nil {
			a.writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		f.Before = &c
	}
	if a.deps.Store == nil {
		a.writeJSON(w, http.StatusOK, map[string]any{"events": []historyEvent{}, "next_before": ""})
		return
	}
	events, err := eventsFeed(r.Context(), a.deps.Store.DB(), f)
	if err != nil {
		a.deps.Log.Warn("history: event feed unreadable", "err", err)
		a.writeError(w, http.StatusInternalServerError, "could not read history")
		return
	}
	next := ""
	if len(events) == f.Limit {
		next = events[len(events)-1].cursor().String()
	}
	// Which lines are about a usenet release, for the page's Usenet chip. Decoration: an
	// unreadable grabs table leaves the lines unmarked.
	if a.deps.Automation != nil && len(events) > 0 {
		if u, err := a.deps.Automation.UsenetReleases(r.Context()); err == nil {
			for i := range events {
				if u.Mentions(events[i].Kind, events[i].ItemID, events[i].Detail) {
					events[i].Network = "usenet"
				}
			}
		}
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"events": events, "next_before": next})
}

// handleImportsList is the old History: the files imported most recently and where each
// went. Rows with no file (a dismissed review, a pack that placed nothing, a series or
// book import recorded only so it isn't imported twice) aren't imports and are left out.
func (a *api) handleImportsList(w http.ResponseWriter, r *http.Request) {
	recs, err := a.deps.Library.Recent(r.Context(), 100)
	if err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not read history")
		return
	}
	if recs == nil {
		recs = []library.ImportRecord{}
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"imports": recs})
}

// splitList reads a comma-separated query value, trimmed, without empties.
func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
