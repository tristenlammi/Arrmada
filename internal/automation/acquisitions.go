package automation

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/tristenlammi/arrmada/internal/books"
	"github.com/tristenlammi/arrmada/internal/download"
	"github.com/tristenlammi/arrmada/internal/parser"
	"github.com/tristenlammi/arrmada/internal/series"
	"github.com/tristenlammi/arrmada/internal/store"
)

// The acquisition record: each grab row, plus what the download client last said about
// its torrent (migration 0152). It answers "is something already downloading for this?"
// and "how far along is it?" by info hash, instead of every caller re-parsing torrent
// names against titles — which re-grabbed a film whose torrent was named differently and
// showed no progress for it. The lifecycle stays in status (grabstatus.go); phase is only
// the client's view, and there is no second state machine.

// Acquisition phases written by the reconciler, besides the client phases Item.Phase()
// reports (queued, metadata, downloading, stalled, checking, moving, allocating, paused,
// seeding, error).
const (
	phaseComplete = "complete" // every byte it was told to fetch is on disk
	phaseMissing  = "missing"  // gone from a complete read of the client
)

// missingGrace is how long a torrent gone from the client still counts as in flight. A
// torrent the user removed by hand shouldn't hold its title forever, but a client that
// briefly lists without it (a restart, a re-add) mustn't let a sweep grab it again.
const missingGrace = 30 * time.Minute

// Acquisition is one grab and where its download has got to.
type Acquisition struct {
	GrabID    int64  `json:"grab_id"`
	MediaType string `json:"media_type"` // movie | series | book | music
	// ItemID is the movie, series, book or album the grab is for (grabs.movie_id).
	ItemID    int64   `json:"item_id"`
	VersionID int64   `json:"version_id,omitempty"` // movie version / audiobook version; 0 = none named
	Title     string  `json:"title"`                // the release grabbed, as listed
	InfoHash  string  `json:"info_hash,omitempty"`  // "" for a legacy row (see legacyMatchByName)
	Status    string  `json:"status"`               // a grabStatus* word
	Phase     string  `json:"phase"`                // the client's view; "" until first seen
	Progress  float64 `json:"progress"`
	// Scope is what the grab was for: v<version> (movie), S03 / S03E05 / S01-S04 /
	// complete / abs (series), ebook / audiobook / v<id> (book), album (music).
	Scope       string    `json:"scope,omitempty"`
	ClientID    int64     `json:"client_id,omitempty"`
	Profile     string    `json:"quality_profile,omitempty"` // as resolved when grabbed
	GrabbedAt   string    `json:"grabbed_at"`
	ProgressAt  time.Time `json:"progress_at,omitzero"` // the stall clock: last forward progress
	LastSeenAt  time.Time `json:"last_seen_at,omitzero"`
	CompletedAt time.Time `json:"completed_at,omitzero"`
	UpdatedAt   time.Time `json:"updated_at,omitzero"`
	LastError   string    `json:"last_error,omitempty"`
}

// acqCols is the column list scanAcq reads, in its order.
const acqCols = `id, media_type, movie_id, version_id, title, info_hash, status, phase, progress,
	acq_scope, client_id, quality_profile, grabbed_at, progress_at, last_seen_at, completed_at,
	updated_at, last_error`

func scanAcq(row interface{ Scan(...any) error }) (Acquisition, error) {
	var a Acquisition
	var progressAt, seenAt, doneAt, updAt int64
	err := row.Scan(&a.GrabID, &a.MediaType, &a.ItemID, &a.VersionID, &a.Title, &a.InfoHash, &a.Status,
		&a.Phase, &a.Progress, &a.Scope, &a.ClientID, &a.Profile, &a.GrabbedAt, &progressAt, &seenAt,
		&doneAt, &updAt, &a.LastError)
	a.ProgressAt, a.LastSeenAt, a.CompletedAt, a.UpdatedAt = msTime(progressAt), msTime(seenAt), msTime(doneAt), msTime(updAt)
	if a.Scope == "" {
		a.Scope = derivedScope(a)
	}
	return a, err
}

func msTime(ms int64) time.Time {
	if ms <= 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms)
}

// derivedScope is the scope of a row that doesn't carry one: every movie grab (its version
// is a column of its own, so the record path needn't write it), music, and rows from
// before migration 0152.
func derivedScope(a Acquisition) string {
	switch a.MediaType {
	case "movie":
		return "v" + strconv.FormatInt(a.VersionID, 10)
	case "music":
		return "album"
	case "book":
		if a.VersionID > 0 {
			return "v" + strconv.FormatInt(a.VersionID, 10)
		}
		return books.EditionOf(detectBookFormat(a.Title))
	}
	// A series row without one is worked out where the series is at hand (alias seasons
	// and anime numbering need it): see seriesInFlightScope.
	return ""
}

// acquisitionsWhere reads the acquisitions matching a status set and more, oldest first.
func (c *Coordinator) acquisitionsWhere(ctx context.Context, where string, args ...any) ([]Acquisition, error) {
	rows, err := c.db.QueryContext(ctx, `SELECT `+acqCols+` FROM grabs WHERE `+where+` ORDER BY id`, args...)
	if err != nil {
		return nil, fmt.Errorf("acquisitions unreadable: %w", err)
	}
	defer rows.Close()
	var out []Acquisition
	for rows.Next() {
		a, err := scanAcq(rows)
		if err != nil {
			return nil, fmt.Errorf("acquisitions unreadable: %w", err)
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("acquisitions unreadable: %w", err)
	}
	return out, nil
}

// activeWhere is the in-flight set (grabbed or held in Review), less torrents that have
// been gone from the client for longer than missingGrace. args: the cutoff (unix ms).
const activeWhere = inFlightWhere + ` AND NOT (phase = '` + phaseMissing + `' AND updated_at < ?)`

func (c *Coordinator) missingCutoff() int64 { return c.clock().Add(-missingGrace).UnixMilli() }

// Active is everything in flight for one library item: grabbed and not yet imported or
// closed out. It fails closed — callers deciding whether to grab must skip the item on an
// error, never treat it as "nothing in flight".
func (c *Coordinator) Active(ctx context.Context, mediaType string, itemID int64) ([]Acquisition, error) {
	return c.acquisitionsWhere(ctx, activeWhere+` AND media_type = ? AND movie_id = ?`,
		c.missingCutoff(), mediaType, itemID)
}

// ActiveForVersion is what's in flight for one version of a movie. A grab that named no
// version (an interactive pick, an uploaded torrent) counts for every version: it's
// unknown which it will fill, and grabbing on top of it is the duplicate this guards.
func (c *Coordinator) ActiveForVersion(ctx context.Context, movieID, versionID int64) ([]Acquisition, error) {
	all, err := c.Active(ctx, "movie", movieID)
	if err != nil {
		return nil, err
	}
	var out []Acquisition
	for _, a := range all {
		if a.VersionID == versionID || a.VersionID == 0 {
			out = append(out, a)
		}
	}
	return out, nil
}

// ActiveByItem is every in-flight acquisition of one media type, keyed by item id — one
// query for a list page or feed (the library list joins downloads through this).
func (c *Coordinator) ActiveByItem(ctx context.Context, mediaType string) (map[int64][]Acquisition, error) {
	all, err := c.acquisitionsWhere(ctx, activeWhere+` AND media_type = ?`, c.missingCutoff(), mediaType)
	if err != nil {
		return nil, err
	}
	out := make(map[int64][]Acquisition, len(all))
	for _, a := range all {
		out[a.ItemID] = append(out[a.ItemID], a)
	}
	return out, nil
}

// ByHash is the newest acquisition for a torrent, whatever its status. ok is false when
// no grab recorded that hash.
func (c *Coordinator) ByHash(ctx context.Context, hash string) (Acquisition, bool, error) {
	if hash == "" {
		return Acquisition{}, false, nil
	}
	a, err := scanAcq(c.db.QueryRowContext(ctx,
		`SELECT `+acqCols+` FROM grabs WHERE info_hash != '' AND lower(info_hash) = lower(?) ORDER BY id DESC LIMIT 1`, hash))
	if err == sql.ErrNoRows {
		return Acquisition{}, false, nil
	}
	if err != nil {
		return Acquisition{}, false, err
	}
	return a, true, nil
}

// LiveByHash is every grab whose torrent may still be in the client (downloading, held,
// imported or dismissed and seeding), keyed by lowercased info hash — what a page joins
// the queue to. A hash grabbed twice keeps its newest row. Legacy rows without a hash
// come back separately, for legacyMatchByName.
func (c *Coordinator) LiveByHash(ctx context.Context) (byHash map[string]Acquisition, legacy []Acquisition, err error) {
	all, err := c.acquisitionsWhere(ctx, liveWhere)
	if err != nil {
		return nil, nil, err
	}
	byHash = make(map[string]Acquisition, len(all))
	for _, a := range all {
		if a.InfoHash == "" {
			legacy = append(legacy, a)
			continue
		}
		byHash[strings.ToLower(a.InfoHash)] = a // ORDER BY id: the newest wins
	}
	return byHash, legacy, nil
}

// legacyMatchByName pairs a hashless grab with its torrent by release name.
//
// TODO: delete once no grab row is left without an info hash. Every grab since migration
// 0062 records one, and the reconciler adopts the hash for the older rows it can pair
// (AdoptTorrentHashes), so this only serves the stragglers.
func legacyMatchByName(legacy []Acquisition, it download.Item) (Acquisition, bool) {
	want := normRelease(it.Name)
	for _, a := range legacy {
		if a.InfoHash == "" && normRelease(a.Title) == want {
			return a, true
		}
	}
	return Acquisition{}, false
}

// acqPhase is the phase the record keeps for a torrent the client listed.
func acqPhase(it download.Item) string {
	if it.Complete() {
		return phaseComplete
	}
	return it.Phase()
}

// reconcileRow is the part of a grab row the reconciler compares against the client.
type reconcileRow struct {
	id                                     int64
	hash, phase                            string
	progress                               float64
	progressAt, seenAt, completedAt, updAt int64
	clientID                               int64
}

// reconcileRewriteAfter is how stale last_seen_at may get before an unchanged row is
// written anyway, so "last seen" stays roughly true without a write every 30 seconds.
const reconcileRewriteAfter = 5 * time.Minute

// ReconcileAcquisitions brings each in-flight grab's record up to date with the shared
// queue snapshot: its phase, progress, client and when it was last seen; 'missing' when a
// complete read of the client no longer lists it; completed_at when it finishes. Legacy
// hashless grabs get their hash adopted first (AdoptTorrentHashes).
//
// It writes only rows that changed — a new phase, progress moved by a percent or more, a
// new client, or last_seen_at older than five minutes — in one short transaction, so the
// 30-second tick doesn't contend with the import sweeps for SQLite's write lock.
// Returns how many rows it wrote. A client that can't be read is not this task's failure:
// it skips the tick, and the Downloads page and health check say the client is down.
func (c *Coordinator) ReconcileAcquisitions(ctx context.Context) (int, error) {
	if c.downloads == nil || c.db == nil {
		return 0, nil
	}
	snap, err := c.downloads.Snapshot(ctx)
	if err != nil {
		return 0, nil
	}
	c.AdoptTorrentHashes(ctx, snap.Items)

	rows, err := c.db.QueryContext(ctx, `SELECT id, info_hash, phase, progress, progress_at, last_seen_at,
		completed_at, updated_at, client_id FROM grabs WHERE `+inFlightWhere+` AND info_hash != ''`)
	if err != nil {
		return 0, err
	}
	var recs []reconcileRow
	for rows.Next() {
		var r reconcileRow
		if err := rows.Scan(&r.id, &r.hash, &r.phase, &r.progress, &r.progressAt, &r.seenAt, &r.completedAt, &r.updAt, &r.clientID); err != nil {
			rows.Close()
			return 0, err
		}
		recs = append(recs, r)
	}
	rows.Close() // close before writing — SQLite won't take a write while a read is open
	if err := rows.Err(); err != nil {
		return 0, err
	}

	byHash := make(map[string]download.Item, len(snap.Items))
	for _, it := range snap.Items {
		byHash[strings.ToLower(it.Hash)] = it
	}
	now := c.clock().UnixMilli()
	var changed []reconcileRow
	for _, r := range recs {
		it, found := byHash[strings.ToLower(r.hash)]
		if !found {
			// Absence from a partial read says nothing: that client's torrents are all
			// missing from it.
			if snap.Complete && r.phase != phaseMissing {
				r.phase, r.updAt = phaseMissing, now
				changed = append(changed, r)
			}
			continue
		}
		next := r
		next.phase, next.clientID, next.seenAt = acqPhase(it), it.ClientID, now
		if it.Complete() && r.completedAt == 0 {
			next.completedAt = now
		}
		moved := math.Abs(it.Progress-r.progress) >= 0.01
		if moved {
			next.progress = it.Progress
			if it.Progress > r.progress {
				next.progressAt = now // forward progress restarts the stall clock
			}
		}
		if r.progressAt == 0 {
			next.progressAt = now // first sight starts it
		}
		write := next.phase != r.phase || moved || next.clientID != r.clientID ||
			next.completedAt != r.completedAt || next.progressAt != r.progressAt ||
			time.Duration(now-r.seenAt)*time.Millisecond >= reconcileRewriteAfter
		if !write {
			continue
		}
		next.updAt = now
		changed = append(changed, next)
	}
	if len(changed) == 0 {
		return 0, nil
	}
	err = store.WithTx(ctx, c.db, func(tx *sql.Tx) error {
		for _, r := range changed {
			if _, err := tx.ExecContext(ctx, `UPDATE grabs SET phase = ?, progress = ?, progress_at = ?,
				last_seen_at = ?, completed_at = ?, updated_at = ?, client_id = ? WHERE id = ?`,
				r.phase, r.progress, r.progressAt, r.seenAt, r.completedAt, r.updAt, r.clientID, r.id); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return len(changed), nil
}

// noteImportError records why a download couldn't be imported on its grab, by hash, so
// the record says it rather than only the log.
func (c *Coordinator) noteImportError(ctx context.Context, hash, reason string) {
	if hash == "" || c.db == nil {
		return
	}
	if _, err := c.db.ExecContext(ctx,
		`UPDATE grabs SET last_error = ?, updated_at = ? WHERE info_hash != '' AND lower(info_hash) = lower(?) AND `+inFlightWhere,
		reason, c.clock().UnixMilli(), hash); err != nil && c.log != nil {
		c.log.Warn("automation: couldn't record the import error on its grab", "hash", hash, "err", err)
	}
}

// noteHashless says so when a grab is recorded without an info hash. Such a grab can only
// be paired with its torrent by name (legacyMatchByName), which a prettified listing title
// defeats; logging it keeps that visible.
func (c *Coordinator) noteHashless(kind, title, infoHash string) {
	if infoHash == "" && c.log != nil {
		c.log.Info("automation: grab recorded without an info hash — it can only be matched by name",
			"kind", kind, "release", title)
	}
}

// --- the stall clock ----------------------------------------------------------------

// The stall clock is the grab row's progress_at: when its download last moved forward.
// It used to live in memory, so a restart reset every clock and a torrent idle for five
// hours got a fresh six-hour window. Reads fail safe — an unreadable clock never condemns
// a download.

// noProgressFor reports whether grab id's download has made no progress for at least
// window. Each call updates the clock: any forward progress restarts it. The first
// observation of a grab always returns false — a genuinely dead download simply waits one
// extra window, which is far cheaper than condemning a live one.
func (c *Coordinator) noProgressFor(ctx context.Context, id int64, progress float64, window time.Duration) bool {
	if c.db == nil {
		return false
	}
	var stored float64
	var at int64
	if err := c.db.QueryRowContext(ctx, `SELECT progress, progress_at FROM grabs WHERE id = ?`, id).Scan(&stored, &at); err != nil {
		return false
	}
	now := c.clock()
	if at == 0 || progress > stored {
		c.setStallClock(ctx, id, progress, now)
		return false
	}
	return now.Sub(time.UnixMilli(at)) >= window
}

// holdStallClock restarts a grab's stall clock WITHOUT counting it as progress, so time
// spent in a state that cannot progress doesn't accumulate toward the stall window. The
// grab resumes being judged the moment the torrent is running again.
func (c *Coordinator) holdStallClock(ctx context.Context, id int64, progress float64) {
	c.setStallClock(ctx, id, progress, c.clock())
}

func (c *Coordinator) setStallClock(ctx context.Context, id int64, progress float64, at time.Time) {
	if c.db == nil {
		return
	}
	if _, err := c.db.ExecContext(ctx, `UPDATE grabs SET progress = ?, progress_at = ?, updated_at = ? WHERE id = ?`,
		progress, at.UnixMilli(), at.UnixMilli(), id); err != nil && c.log != nil {
		c.log.Warn("automation: couldn't save a download's stall clock", "grab", id, "err", err)
	}
}

// markStillWaiting records that grab id came up with no replacement just now.
func (c *Coordinator) markStillWaiting(ctx context.Context, id int64) {
	if c.db == nil {
		return
	}
	if _, err := c.db.ExecContext(ctx, `UPDATE grabs SET still_waiting_at = ? WHERE id = ?`, c.clock().UnixMilli(), id); err != nil && c.log != nil {
		c.log.Warn("automation: couldn't save when a stalled download last found no replacement", "grab", id, "err", err)
	}
}

// waitingOut reports whether grab id came up with no replacement less than a window ago.
// Unreadable reads as waiting: the next attempt is only a window away, and an attempt
// costs a search.
func (c *Coordinator) waitingOut(ctx context.Context, id int64, window time.Duration) bool {
	if c.db == nil {
		return false
	}
	var at int64
	if err := c.db.QueryRowContext(ctx, `SELECT still_waiting_at FROM grabs WHERE id = ?`, id).Scan(&at); err != nil {
		return true
	}
	return at > 0 && c.clock().Sub(time.UnixMilli(at)) < window
}

// stallClocks is each pending grab's stall clock, for the Downloads page. Grabs never
// observed are left out.
func (c *Coordinator) stallClocks(ctx context.Context) map[int64]time.Time {
	out := map[int64]time.Time{}
	rows, err := c.db.QueryContext(ctx, `SELECT id, progress_at FROM grabs WHERE `+stallWatchWhere+` AND progress_at > 0`)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var id, at int64
		if rows.Scan(&id, &at) == nil {
			out[id] = time.UnixMilli(at)
		}
	}
	return out
}

// --- scope ---------------------------------------------------------------------------

// seriesAcqScope is what a series release covers, as stored in grabs.acq_scope:
// "S03" (a season pack), "S03E05" (an episode), "S01-S04" (several seasons), "complete",
// or "abs" when which season it lands in takes the series' numbering to work out (an
// absolute-numbered anime release, an anime cour numbered as its own season, or a name
// with no numbering at all). s may be empty, which skips the alias and anime rules.
func seriesAcqScope(name string, s series.Series) string {
	if sn, ok := aliasSeasonOf(name, s); ok {
		// The alias' numbers are read inside one season of the series, whatever season
		// the release itself claims.
		return fmt.Sprintf("S%02d", sn)
	}
	p := parser.Parse(name)
	switch {
	case p.Complete:
		return "complete"
	case len(p.Seasons) > 1:
		lo, hi := p.Seasons[0], p.Seasons[0]
		for _, sn := range p.Seasons {
			lo, hi = min(lo, sn), max(hi, sn)
		}
		return fmt.Sprintf("S%02d-S%02d", lo, hi)
	case p.Season > 0 && s.IsAnime() && len(s.Seasons) > 0 && !hasSeason(s, p.Season):
		return "abs"
	case p.Season > 0 || p.SeasonExplicit:
		if len(p.Episodes) > 0 {
			return fmt.Sprintf("S%02dE%02d", p.Season, p.Episodes[0])
		}
		return fmt.Sprintf("S%02d", p.Season)
	}
	return "abs"
}

// scopeSeason reads a series scope for the in-flight check: the one season it holds, or
// whole=true when it covers more than a season can say (several seasons, the complete
// show, an absolute-numbered release, or anything unreadable). An episode holds its
// season, as a pack does: the sweeps search and skip by season.
func scopeSeason(scope string) (season int, whole bool) {
	m := reGrabScope.FindStringSubmatch(scope)
	if m == nil {
		return 0, true
	}
	season, _ = strconv.Atoi(m[1])
	return season, false
}

// recordAcqScope stamps what a just-recorded grab was for onto its row (the newest one
// for its hash, or the newest hashless row of that title).
func (c *Coordinator) recordAcqScope(ctx context.Context, mediaType string, itemID int64, title, infoHash, scope string) {
	if scope == "" || c.db == nil {
		return
	}
	_, err := c.db.ExecContext(ctx,
		`UPDATE grabs SET acq_scope = ?, updated_at = ?
		  WHERE id = (SELECT id FROM grabs WHERE media_type = ? AND movie_id = ? AND title = ? AND info_hash = ?
		              ORDER BY id DESC LIMIT 1)`,
		scope, c.clock().UnixMilli(), mediaType, itemID, title, infoHash)
	if err != nil && c.log != nil {
		c.log.Warn("automation: couldn't record what the grab was for", "release", title, "err", err)
	}
}
