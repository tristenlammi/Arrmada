package automation

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/tristenlammi/arrmada/internal/connstatus"
	"github.com/tristenlammi/arrmada/internal/indexer"
	"github.com/tristenlammi/arrmada/internal/movies"
	"github.com/tristenlammi/arrmada/internal/quality"
	"github.com/tristenlammi/arrmada/internal/store"
)

// Search attempts: every movie, series, book and album search leaves one row in
// search_attempts saying what it found and why nothing was taken, so "why isn't it
// downloading?" has an answer outside the log. Recording is purely observational — it
// counts what the search already decided and never changes what is ranked or grabbed.

// What started a search. Carried on the context (WithSearchTrigger) instead of threaded
// through every function; a search with none set records TriggerOther.
const (
	TriggerSweep   = "sweep"
	TriggerRSS     = "rss"
	TriggerManual  = "manual"
	TriggerRequest = "request"
	TriggerAdd     = "add"
	TriggerUpgrade = "upgrade"
	TriggerStall   = "stall"
	TriggerReplace = "replace"
	TriggerOther   = "other"
)

type searchTriggerKey struct{}

// WithSearchTrigger says what started the searches made under ctx.
func WithSearchTrigger(ctx context.Context, trigger string) context.Context {
	return context.WithValue(ctx, searchTriggerKey{}, trigger)
}

// WithDefaultSearchTrigger is WithSearchTrigger unless ctx already says what started it:
// the Search button's job wrapper says "manual", but an add or a profile change that runs
// the same job has said so first.
func WithDefaultSearchTrigger(ctx context.Context, trigger string) context.Context {
	if _, ok := ctx.Value(searchTriggerKey{}).(string); ok {
		return ctx
	}
	return WithSearchTrigger(ctx, trigger)
}

func searchTrigger(ctx context.Context) string {
	if t, ok := ctx.Value(searchTriggerKey{}).(string); ok && t != "" {
		return t
	}
	return TriggerOther
}

// What a stored attempt's outcome column holds: the search's result in one word, for the
// Wanted view to group by. SearchOutcome.Reason is the finer reason under it.
const (
	OutcomeGrabbed         = "grabbed"
	OutcomeNothingFound    = "nothing_found"
	OutcomeNoneSuitable    = "none_suitable"
	OutcomeIndexersFailed  = "indexers_failed"
	OutcomeSkippedInFlight = "skipped_in_flight"
	OutcomeError           = "error"
)

// Why one release wasn't taken, besides the quality profile's own reject codes
// (quality.Reject*). Stored in reasons_json; never rename one.
const (
	DropWrongTitle  = "wrong_title"  // for a different film, show, book or album
	DropBlocklisted = "blocklisted"  // blocklisted for this title
	DropPending     = "pending"      // already grabbed and still downloading
	DropOutOfScope  = "out_of_scope" // the right title, but other episodes, another edition or version
	DropNotTorrent  = "not_torrent"  // a usenet release, which no download client takes
	DropNotWanted   = "not_wanted"   // a book format the profile doesn't want
	// The two "taken" states, never counted as reasons.
	classEligible = "eligible"
	classGrabbed  = "grabbed"
	// A profile rejection with no code (shouldn't happen; kept so it still counts).
	rejectedUncoded = "rejected"
)

// classRank orders what can happen to one release, so a release seen by several passes of
// one search (a series' broad and per-season queries, or a movie judged under two
// versions' profiles) is counted once, by the furthest it got.
func classRank(code string) int {
	switch code {
	case classGrabbed:
		return 5
	case DropPending:
		return 4
	case classEligible:
		return 3
	case DropWrongTitle, DropBlocklisted, DropOutOfScope, DropNotTorrent:
		return 1
	}
	return 2 // a profile rejection
}

// consider notes releases the search looked at.
func (n *searchNotes) consider(releases []indexer.Release) {
	if n == nil {
		return
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.seen == nil {
		n.seen = map[string]bool{}
	}
	for _, rel := range releases {
		n.seen[rel.Title] = true
	}
}

// mark records what happened to one release: a Drop* or quality.Reject* code, or
// eligible/grabbed. A release keeps the furthest state any pass gave it.
func (n *searchNotes) mark(title, code string) {
	if n == nil || title == "" {
		return
	}
	if code == "" {
		code = rejectedUncoded
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.class == nil {
		n.class, n.examples, n.seen = map[string]string{}, map[string]string{}, orMap(n.seen)
	}
	n.seen[title] = true
	if cur, ok := n.class[title]; ok && classRank(cur) >= classRank(code) {
		return
	}
	n.class[title] = code
	if _, ok := n.examples[code]; !ok {
		n.examples[code] = title
	}
}

func orMap(m map[string]bool) map[string]bool {
	if m == nil {
		return map[string]bool{}
	}
	return m
}

// dropped marks with code every release in before that a filter left out of after — the
// releases a title check, blocklist or pending guard just threw away.
func (n *searchNotes) dropped(before, after []indexer.Release, code string) {
	if n == nil || len(before) == len(after) {
		return
	}
	kept := make(map[string]bool, len(after))
	for _, rel := range after {
		kept[rel.Title] = true
	}
	for _, rel := range before {
		if !kept[rel.Title] {
			n.mark(rel.Title, code)
		}
	}
}

// decided records the quality engine's verdict on each candidate: its RejectCode, or
// eligible. The codes come from the engine itself (quality.Evaluate), never re-derived.
func (n *searchNotes) decided(d quality.Decision) {
	if n == nil {
		return
	}
	for _, ev := range d.Rejected {
		n.mark(ev.Candidate.Name, ev.RejectCode)
	}
	for _, ev := range d.Eligible {
		n.mark(ev.Candidate.Name, classEligible)
	}
}

// grabbed records a release sent to the download client. Every record*Grab calls it, so
// whichever path grabbed, the attempt knows.
func (n *searchNotes) grabbed(title string) {
	if n == nil || title == "" {
		return
	}
	n.mu.Lock()
	n.took = append(n.took, title)
	n.mu.Unlock()
	n.mark(title, classGrabbed)
}

// tally is the counts a collector adds up to.
type tally struct {
	returned, wrongTitle, blocklisted, pending, outOfScope, rejected, eligible int
	reasons                                                                    map[string]int
	topReason, example                                                         string
	grabbed                                                                    []string
}

func (n *searchNotes) tally() tally {
	var t tally
	if n == nil {
		return t
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	t.returned = len(n.seen)
	t.grabbed = append([]string(nil), n.took...)
	for _, code := range n.class {
		switch code {
		case classGrabbed, classEligible, DropPending:
			t.eligible++ // a pending release passed the profile too
		case DropWrongTitle:
			t.wrongTitle++
		case DropBlocklisted:
			t.blocklisted++
		case DropOutOfScope:
			t.outOfScope++
		case DropNotTorrent, DropNotWanted:
		default:
			t.rejected++
		}
		if code == DropPending {
			t.pending++
		}
		if code == classGrabbed || code == classEligible {
			continue
		}
		if t.reasons == nil {
			t.reasons = map[string]int{}
		}
		t.reasons[code]++
	}
	t.topReason = topReason(t.reasons)
	t.example = n.examples[t.topReason]
	return t
}

// topReason is the most common reason, ties broken by name so it's stable.
func topReason(reasons map[string]int) string {
	best, bestN := "", 0
	for code, n := range reasons {
		if n > bestN || (n == bestN && code < best) {
			best, bestN = code, n
		}
	}
	return best
}

// absorb fills in what the collector saw that the search's own outcome didn't count. The
// counts the outcome already carries (the Search button's returned/matching/usable) are
// left as they are: they're what the message says.
func (o *SearchOutcome) absorb(n *searchNotes) {
	if n == nil {
		return
	}
	t := n.tally()
	if !o.Searched && n.ran && o.Reason == "" {
		o.Searched = true // an outcome built from scratch (a search that only returns an error)
	}
	if o.Returned == 0 {
		o.Returned = t.returned
	}
	if o.Matching == 0 && o.Usable == 0 {
		o.Matching = max(0, o.Returned-t.wrongTitle)
		o.Usable = max(0, o.Matching-t.blocklisted)
	}
	if len(o.GrabbedTitles) == 0 && len(t.grabbed) > 0 {
		o.GrabbedTitles = t.grabbed
		if o.Grabbed == 0 {
			o.Grabbed = len(t.grabbed)
		}
	}
	o.WrongTitle, o.Blocklisted, o.Pending, o.OutOfScope = t.wrongTitle, t.blocklisted, t.pending, t.outOfScope
	o.Rejected, o.Eligible = t.rejected, t.eligible
	o.Reasons = t.reasons
	if o.Example == "" {
		o.Example = t.example
	}
	if o.TopReason == "" {
		o.TopReason = t.topReason
	}
	if errs := n.errorMap(); len(errs) > 0 {
		o.IndexerErrors = errs
	}
}

// Kind is the outcome in one word (Outcome*), given the error the search returned.
func (o SearchOutcome) Kind(err error) string {
	switch {
	case o.Grabbed > 0:
		return OutcomeGrabbed
	case o.Reason == ReasonAlreadyDownloading:
		return OutcomeSkippedInFlight
	case o.Reason == ReasonIndexersPaused || o.Reason == ReasonIndexersFailed || o.Reason == ReasonNoIndexers || indexer.IsOutage(err):
		return OutcomeIndexersFailed
	case err != nil:
		return OutcomeError
	case o.Returned == 0:
		return OutcomeNothingFound
	}
	return OutcomeNoneSuitable
}

// maxAttemptsPerTitle is how many attempts each title keeps; the insert prunes the rest.
const maxAttemptsPerTitle = 20

// ScopeUpgrade is the scope an upgrade search is stored under: the title has its file,
// so its attempts are left out of the "why is it missing?" summaries.
const ScopeUpgrade = "upgrade"

// ScopeAlbum is the scope an album search is stored under.
const ScopeAlbum = "album"

// recordRSSGrab stores a grab the RSS feed made as an attempt. The feed only records what
// it grabs: the same uploads come round every cycle for hours, and a row each time saying
// they still don't fit would bury the real searches.
func (c *Coordinator) recordRSSGrab(ctx context.Context, mediaType string, id int64, scope, title string) {
	out := SearchOutcome{Searched: true, Returned: 1, Matching: 1, Usable: 1, Grabbed: 1, GrabbedTitles: []string{title}, Reason: ReasonGrabbed, Eligible: 1}
	c.recordAttempt(WithDefaultSearchTrigger(ctx, TriggerRSS), nil, mediaType, id, scope, &out, nil)
}

// replaceRecorded runs a stall fail-over's replacement search as a recorded attempt. The
// target's kind (movie, series, book, music) is the attempt's media type.
func (c *Coordinator) replaceRecorded(ctx context.Context, t stallTarget, exclude map[string]bool) (repl string, err error) {
	ctx, notes := newSearchNotes(WithSearchTrigger(ctx, TriggerStall))
	defer func() {
		var out SearchOutcome
		c.recordAttempt(ctx, notes, t.kind, t.id, "", &out, err)
	}()
	return t.replace(ctx, exclude)
}

// recordAttempt stores one search's outcome and tells staff pages it finished. It folds
// what the collector n saw into out first, so the caller's outcome carries the counts too.
//
// Nothing is stored for a search that never ran — nothing wanted, or the title already
// being searched — except a skip because the title is already downloading, which is the
// answer to "why didn't Search do anything?". A failure to store is logged, never
// returned: the search itself happened either way.
func (c *Coordinator) recordAttempt(ctx context.Context, n *searchNotes, mediaType string, id int64, scope string, out *SearchOutcome, err error) {
	out.absorb(n)
	if out.Reason == "" {
		if err != nil {
			out.noteSearchErr(err)
		} else if out.Searched {
			out.settle()
		}
	}
	switch {
	case errors.Is(err, ErrAlreadySearching), out.Reason == ReasonAlreadySearching:
		return
	case out.Reason == ReasonAlreadyDownloading:
	case indexer.IsOutage(err):
		// Nobody could answer: recorded, so the outage is visible on the title.
	case !out.Searched:
		return // nothing wanted, or it failed before any indexer was asked
	}
	if c.db == nil || id == 0 {
		return
	}
	trigger := searchTrigger(ctx)
	kind := out.Kind(err)
	started := time.Now()
	if n != nil && !n.started.IsZero() {
		started = n.started
	}
	errText := ""
	if err != nil {
		errText = connstatus.Redact(err.Error())
	}
	reasons, _ := json.Marshal(nonNilCounts(out.Reasons))
	titles, _ := json.Marshal(nonNilStrings(out.GrabbedTitles))
	ixErrs, _ := json.Marshal(nonNilErrors(out.IndexerErrors))

	// The title's previous attempt under this scope, to tell a repeat from news.
	var prev struct {
		outcome, example, topReason string
		returned                    int
	}
	_ = c.db.QueryRowContext(ctx, `SELECT outcome, example, top_reason, returned FROM search_attempts
		WHERE media_type = ? AND media_id = ? AND scope = ? ORDER BY id DESC LIMIT 1`,
		mediaType, id, scope).Scan(&prev.outcome, &prev.example, &prev.topReason, &prev.returned)
	// A sweep that keeps finding the title still downloading would otherwise fill its 20
	// rows with the same skip every few minutes and push out the searches that said
	// something. One row stands for the whole stretch.
	if kind == OutcomeSkippedInFlight && prev.outcome == OutcomeSkippedInFlight && prev.example == out.Example {
		return
	}
	repeat := prev.outcome == kind && prev.topReason == out.TopReason && prev.returned == out.Returned

	// Stored even when the job that ran the search is timing out: the search happened.
	wctx := context.WithoutCancel(ctx)
	var attemptID int64
	werr := store.WithTx(wctx, c.db, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(wctx, `INSERT INTO search_attempts
			(media_type, media_id, scope, triggered_by, started_at, duration_ms, returned, wrong_title,
			 blocklisted, pending, out_of_scope, rejected, eligible, grabbed, reasons_json, top_reason,
			 example, grabbed_titles, indexer_errors, outcome, reason, error)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			mediaType, id, scope, trigger, started.UnixMilli(), time.Since(started).Milliseconds(),
			out.Returned, out.WrongTitle, out.Blocklisted, out.Pending, out.OutOfScope, out.Rejected,
			out.Eligible, out.Grabbed, string(reasons), out.TopReason, out.Example, string(titles),
			string(ixErrs), kind, out.Reason, errText)
		if err != nil {
			return err
		}
		attemptID, _ = res.LastInsertId()
		_, err = tx.ExecContext(wctx, `DELETE FROM search_attempts
			WHERE media_type = ? AND media_id = ? AND id NOT IN (
				SELECT id FROM search_attempts WHERE media_type = ? AND media_id = ?
				ORDER BY id DESC LIMIT ?)`, mediaType, id, mediaType, id, maxAttemptsPerTitle)
		return err
	})
	if werr != nil {
		c.log.Warn("search: couldn't record the attempt", "media", mediaType, "id", id, "err", werr)
		return
	}
	out.AttemptID = attemptID
	if mediaType == AttemptMovie {
		c.movieSearchedEvent(wctx, id, trigger, kind, *out, err, repeat)
	}
	if c.bus != nil {
		// Ids and counts only: staff pages refetch what they show.
		c.bus.Publish("search.finished", map[string]any{
			"media_type": mediaType, "media_id": id, "scope": scope, "attempt_id": attemptID,
			"outcome": kind, "grabbed": out.Grabbed,
		})
	}
}

// movieSearchedEvent puts a search on the movie's History, so "why hasn't this downloaded?"
// is answered on the page. A search someone started always gets a line; a sweep only
// when its result differs from the last one, or the 5-minute sweeps would bury the
// history in identical rows. A grab already has its own "grabbed" line, and an upgrade
// search that found nothing better is the normal case, not news.
func (c *Coordinator) movieSearchedEvent(ctx context.Context, id int64, trigger, kind string, out SearchOutcome, err error, repeat bool) {
	// A stall fail-over writes its own history lines ("still waiting", "replaced with").
	if c.movies == nil || kind == OutcomeGrabbed || trigger == TriggerUpgrade || trigger == TriggerStall {
		return
	}
	if trigger == TriggerSweep && repeat {
		return
	}
	c.movies.AddEvent(ctx, id, "searched", out.OutcomeLine(kind, err))
}

// OutcomeLine is a stored-or-fresh outcome as one line for a history entry or a toast:
// "41 releases · 29 for other titles, 12 over your bitrate ceiling · nothing grabbed",
// "Every indexer failed: TorrentLeech, 1337x", "Already downloading <release>".
func (o SearchOutcome) OutcomeLine(kind string, err error) string {
	switch kind {
	case OutcomeIndexersFailed:
		names := make([]string, 0, len(o.IndexerErrors))
		for name := range o.IndexerErrors {
			names = append(names, name)
		}
		sort.Strings(names)
		if len(names) == 0 {
			return o.Message("title")
		}
		return "Every indexer failed: " + strings.Join(names, ", ")
	case OutcomeSkippedInFlight:
		return o.Message("title")
	case OutcomeNothingFound:
		return "No releases found"
	case OutcomeError:
		if err != nil {
			return "Search failed: " + connstatus.Redact(err.Error())
		}
	}
	return o.Summary()
}

func nonNilCounts(m map[string]int) map[string]int {
	if m == nil {
		return map[string]int{}
	}
	return m
}

func nonNilErrors(m map[string]string) map[string]string {
	if m == nil {
		return map[string]string{}
	}
	return m
}

func nonNilStrings(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// --- reading attempts back ---

// Attempt is one stored search attempt, as GET /api/v1/searches returns it.
type Attempt struct {
	ID            int64             `json:"id"`
	MediaType     string            `json:"media_type"`
	MediaID       int64             `json:"media_id"`
	Scope         string            `json:"scope"`
	Trigger       string            `json:"trigger"`
	StartedAt     int64             `json:"started_at"` // unix ms
	DurationMs    int64             `json:"duration_ms"`
	Returned      int               `json:"returned"`
	WrongTitle    int               `json:"wrong_title"`
	Blocklisted   int               `json:"blocklisted"`
	Pending       int               `json:"pending"`
	OutOfScope    int               `json:"out_of_scope"`
	Rejected      int               `json:"rejected"`
	Eligible      int               `json:"eligible"`
	Grabbed       int               `json:"grabbed"`
	Reasons       map[string]int    `json:"reasons"`
	TopReason     string            `json:"top_reason"`
	Example       string            `json:"example"`
	GrabbedTitles []string          `json:"grabbed_titles"`
	IndexerErrors map[string]string `json:"indexer_errors"`
	Outcome       string            `json:"outcome"`
	Reason        string            `json:"reason"`
	Error         string            `json:"error,omitempty"`
}

const attemptCols = `id, media_type, media_id, scope, triggered_by, started_at, duration_ms, returned,
	wrong_title, blocklisted, pending, out_of_scope, rejected, eligible, grabbed, reasons_json,
	top_reason, example, grabbed_titles, indexer_errors, outcome, reason, error`

func scanAttempt(sc interface{ Scan(...any) error }) (Attempt, error) {
	var a Attempt
	var reasons, titles, ixErrs string
	err := sc.Scan(&a.ID, &a.MediaType, &a.MediaID, &a.Scope, &a.Trigger, &a.StartedAt, &a.DurationMs,
		&a.Returned, &a.WrongTitle, &a.Blocklisted, &a.Pending, &a.OutOfScope, &a.Rejected, &a.Eligible,
		&a.Grabbed, &reasons, &a.TopReason, &a.Example, &titles, &ixErrs, &a.Outcome, &a.Reason, &a.Error)
	if err != nil {
		return a, err
	}
	_ = json.Unmarshal([]byte(reasons), &a.Reasons)
	_ = json.Unmarshal([]byte(titles), &a.GrabbedTitles)
	_ = json.Unmarshal([]byte(ixErrs), &a.IndexerErrors)
	a.Reasons, a.GrabbedTitles, a.IndexerErrors = nonNilCounts(a.Reasons), nonNilStrings(a.GrabbedTitles), nonNilErrors(a.IndexerErrors)
	return a, nil
}

// Search attempt media types: the same words grabs and the blocklist use.
const (
	AttemptMovie  = "movie"
	AttemptSeries = "series"
	AttemptBook   = "book"
	AttemptMusic  = "music"
)

// ValidAttemptKind reports whether kind is a media type attempts are stored under.
func ValidAttemptKind(kind string) bool {
	switch kind {
	case AttemptMovie, AttemptSeries, AttemptBook, AttemptMusic:
		return true
	}
	return false
}

// SearchAttempts lists a title's stored attempts, newest first: those started at or after
// sinceMs (unix ms; 0 = all), at most limit (1-20).
func (c *Coordinator) SearchAttempts(ctx context.Context, kind string, id, sinceMs int64, limit int) ([]Attempt, error) {
	if limit <= 0 || limit > maxAttemptsPerTitle {
		limit = maxAttemptsPerTitle
	}
	rows, err := c.db.QueryContext(ctx, `SELECT `+attemptCols+` FROM search_attempts
		WHERE media_type = ? AND media_id = ? AND started_at >= ? ORDER BY id DESC LIMIT ?`,
		kind, id, sinceMs, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Attempt{}
	for rows.Next() {
		a, err := scanAttempt(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// AttemptSummary is the "why isn't it downloading?" line for one title: its latest search
// and how the searches since its last grab went. The Wanted view, the detail pages and the
// series page read this rather than the raw rows.
type AttemptSummary struct {
	Latest Attempt `json:"latest"`
	// EmptyTries counts the newest attempts in a row that ran and grabbed nothing
	// (nothing_found / none_suitable). An outage, an in-flight skip or an error neither
	// counts nor breaks the run; a grab ends it.
	EmptyTries int `json:"empty_tries"`
	// MainReason is the reject code most often on top over those empty tries ("" when
	// there were none): the one answer to "why does it keep finding nothing usable?".
	MainReason string `json:"main_reason,omitempty"`
}

// LatestAttempts summarises the stored attempts for each of ids. Upgrade searches
// (scope "upgrade") are left out: a summary answers why something missing hasn't been
// fetched. Titles never searched are absent from the map.
func (c *Coordinator) LatestAttempts(ctx context.Context, kind string, ids []int64) (map[int64]AttemptSummary, error) {
	out := map[int64]AttemptSummary{}
	if len(ids) == 0 {
		return out, nil
	}
	// In chunks, to stay well under SQLite's bound-parameter limit.
	const chunk = 500
	for start := 0; start < len(ids); start += chunk {
		part := ids[start:min(start+chunk, len(ids))]
		args := make([]any, 0, len(part)+1)
		args = append(args, kind)
		for _, id := range part {
			args = append(args, id)
		}
		rows, err := c.db.QueryContext(ctx, `SELECT `+attemptCols+` FROM search_attempts
			WHERE media_type = ? AND scope != '`+ScopeUpgrade+`' AND media_id IN (?`+strings.Repeat(",?", len(part)-1)+`)
			ORDER BY media_id, id DESC`, args...)
		if err != nil {
			return nil, err
		}
		type run struct {
			sum     AttemptSummary
			done    bool // the empty run has ended
			reasons map[string]int
		}
		runs := map[int64]*run{}
		for rows.Next() {
			a, err := scanAttempt(rows)
			if err != nil {
				rows.Close()
				return nil, err
			}
			r := runs[a.MediaID]
			if r == nil {
				r = &run{sum: AttemptSummary{Latest: a}, reasons: map[string]int{}}
				runs[a.MediaID] = r
			}
			if r.done {
				continue
			}
			switch a.Outcome {
			case OutcomeGrabbed:
				r.done = true
			case OutcomeNothingFound, OutcomeNoneSuitable:
				r.sum.EmptyTries++
				if a.TopReason != "" {
					r.reasons[a.TopReason]++
				}
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
		for id, r := range runs {
			r.sum.MainReason = topReason(r.reasons)
			out[id] = r.sum
		}
	}
	return out, nil
}

// SearchState is what a movie's page says about searching for it: the sweep's own
// backoff state, when it will next look, and how the stored attempts went.
type SearchState struct {
	LastSearchAt string `json:"last_search_at,omitempty"` // RFC 3339; the sweep stamps it on a miss or a grab
	SearchMisses int    `json:"search_misses"`            // sweeps in a row that grabbed nothing
	// NextSearchAt (RFC 3339) is when the missing-sweep will next search it; "now" means
	// on its next run. Absent when no automatic search is coming: not monitored, nothing
	// missing, or not yet at its minimum availability.
	NextSearchAt string          `json:"next_search_at,omitempty"`
	LastSearch   *AttemptSummary `json:"last_search,omitempty"`
}

// MovieSearchState reads a movie's search state for its detail page. Database reads only.
func (c *Coordinator) MovieSearchState(ctx context.Context, m movies.Movie) SearchState {
	var st SearchState
	if c == nil || c.movies == nil || c.db == nil {
		return st
	}
	lastAt, misses := c.movies.SearchState(ctx, m.ID)
	last := parseTime(lastAt)
	st.SearchMisses = misses
	if !last.IsZero() {
		st.LastSearchAt = last.Format(time.RFC3339)
	}
	if m.Monitored && c.movies.IsAvailable(m) && len(c.missingVersions(ctx, m.ID)) > 0 {
		next := time.Now().UTC()
		if t := NextSearchAt(last, misses); t.After(next) {
			next = t
		}
		st.NextSearchAt = next.Format(time.RFC3339)
	}
	if sums, err := c.LatestAttempts(ctx, AttemptMovie, []int64{m.ID}); err == nil {
		if s, ok := sums[m.ID]; ok {
			st.LastSearch = &s
		}
	}
	return st
}

// NextSearchAt is when the missing-sweep will next search a movie or series that has
// grabbed nothing for misses sweeps in a row, given when it last searched (zero time: as
// soon as the sweep comes round). Books keep their own ladder (books.NextSearch).
func NextSearchAt(lastSearch time.Time, misses int) time.Time {
	if lastSearch.IsZero() {
		return time.Time{}
	}
	return lastSearch.Add(searchBackoff(misses))
}

// ReasonLabel is a reject or drop code in words, for a summary line: "over your bitrate
// ceiling". Unknown codes come back as themselves.
func ReasonLabel(code string) string {
	if l, ok := reasonLabels[code]; ok {
		return l
	}
	return code
}

var reasonLabels = map[string]string{
	DropWrongTitle:                "for other titles",
	DropBlocklisted:               "blocklisted",
	DropPending:                   "already downloading",
	DropOutOfScope:                "for other episodes or editions",
	DropNotTorrent:                "usenet only",
	DropNotWanted:                 "a format you don't want",
	rejectedUncoded:               "rejected by your profile",
	quality.RejectResolution:      "not a resolution you want",
	quality.RejectSourceFloor:     "below your minimum source",
	quality.RejectPrerelease:      "cam or screener copies",
	quality.RejectSourceCeiling:   "above your maximum source",
	quality.RejectBitrateCeiling:  "over your bitrate ceiling",
	quality.RejectSeeders:         "too few seeders",
	quality.RejectTerm:            "containing a rejected term",
	quality.RejectMissingRequired: "missing a required format",
	quality.RejectMinFormatScore:  "below your minimum format score",
}

// Summary is an outcome as one line for a log or a history entry: "37 releases · 22 for
// other titles, 15 over your bitrate ceiling · nothing grabbed".
func (o SearchOutcome) Summary() string {
	if o.Grabbed > 0 {
		return o.Message("title")
	}
	type kv struct {
		code string
		n    int
	}
	var parts []kv
	for code, n := range o.Reasons {
		parts = append(parts, kv{code, n})
	}
	sort.Slice(parts, func(i, j int) bool {
		if parts[i].n != parts[j].n {
			return parts[i].n > parts[j].n
		}
		return parts[i].code < parts[j].code
	})
	line := fmt.Sprintf("%d %s", o.Returned, pluralize(o.Returned, "release"))
	if len(parts) > 0 {
		words := make([]string, 0, len(parts))
		for _, p := range parts {
			words = append(words, fmt.Sprintf("%d %s", p.n, ReasonLabel(p.code)))
		}
		line += " · " + strings.Join(words, ", ")
	}
	return line + " · nothing grabbed"
}
