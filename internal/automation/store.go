package automation

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"unicode"

	"github.com/tristenlammi/arrmada/internal/download"
	"github.com/tristenlammi/arrmada/internal/indexer"
	"github.com/tristenlammi/arrmada/internal/parser"
)

// BlockEntry is a blocklisted release for a movie.
type BlockEntry struct {
	ID        int64  `json:"id"`
	MovieID   int64  `json:"movie_id"`
	Title     string `json:"title"`
	Indexer   string `json:"indexer,omitempty"`
	Reason    string `json:"reason,omitempty"`
	CreatedAt string `json:"created_at"`
	// Network is "usenet" for a release grabbed from Usenet, absent otherwise.
	Network string `json:"network,omitempty"`
}

// grab is a recorded automatic grab, tracked for stall detection and seed
// cleanup. The seed policy is snapshotted here so cleanup doesn't depend on the
// originating indexer still existing.
type grab struct {
	ID           int64
	MovieID      int64
	VersionID    int64
	Title        string
	Indexer      string
	Profile      string
	StallMinutes int
	GrabbedAt    string
	SeedEnabled  bool
	SeedRatio    float64
	SeedHours    int
	MediaType    string // "movie" | "series" | "book" | "music"
	InfoHash     string // the torrent's real identity; "" for rows predating migration 0062
	Status       string // one of the grabStatus* words (grabstatus.go)
	// ReplacesPath is the file the grab's movie track held when it was grabbed (an
	// upgrade, a re-grab, a manual grab over a file); "" for a grab for a missing file and
	// for rows predating migration 0155. See movieHasFileFor.
	ReplacesPath string
	// Transport is how its download travels (grabs.transport, migration 0320): a usenet job
	// is judged by its own rules — it never seeds, never stalls for want of peers, and a
	// failure comes with SABnzbd's reason (usenetlife.go).
	Transport download.Transport
	// Dups are other 'grabbed' rows for the same download of the same title, folded onto
	// this one by the stall check (collapseDuplicateGrabs) so they get one decision between
	// them. Never read from the table.
	Dups []grab
}

// usenet reports whether the grab's download is a usenet job: recorded as one, or keyed
// as one (a key always says what it is, whatever an older row's column holds).
func (g grab) usenet() bool {
	return g.Transport == download.TransportUsenet || download.KeyTransport(g.InfoHash) == download.TransportUsenet
}

// uploadedTransportKey carries, on a grab's context, the transport of the file someone
// uploaded for it (withUploadedTransport).
type uploadedTransportKey struct{}

// withUploadedTransport marks ctx as recording the grab of an uploaded file of transport t.
func withUploadedTransport(ctx context.Context, t download.Transport) context.Context {
	return context.WithValue(ctx, uploadedTransportKey{}, t)
}

// uploadedTransport is the transport withUploadedTransport put on ctx, "" for none.
func uploadedTransport(ctx context.Context) download.Transport {
	t, _ := ctx.Value(uploadedTransportKey{}).(download.Transport)
	return t
}

// ids is the grab's row id followed by those of the duplicates folded onto it.
func (g grab) ids() []int64 {
	out := make([]int64, 0, 1+len(g.Dups))
	out = append(out, g.ID)
	for _, d := range g.Dups {
		out = append(out, d.ID)
	}
	return out
}

// sameDownload reports whether two grab rows record the same torrent: by info hash when
// both have one, else by release name (rows predating migration 0062 have no hash).
func sameDownload(a, b grab) bool {
	if a.InfoHash != "" && b.InfoHash != "" {
		return strings.EqualFold(a.InfoHash, b.InfoHash)
	}
	return normRelease(a.Title) == normRelease(b.Title)
}

// grabCols is the column list scanGrab reads, in its order.
const grabCols = `id, movie_id, version_id, title, indexer, quality_profile, stall_minutes, grabbed_at, seed_enabled, seed_ratio, seed_hours, media_type, info_hash, status, replaces_path, transport`

// addBlock blocklists a release for a movie.
func (c *Coordinator) addBlock(ctx context.Context, movieID int64, title, indexer, downloadURL, reason string) error {
	_, err := c.db.ExecContext(ctx,
		`INSERT INTO blocklist (movie_id, norm_title, title, indexer, download_url, reason)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		movieID, normTitle(title), title, indexer, downloadURL, reason)
	return err
}

// listBlocks returns a movie's blocklisted releases (newest first).
func (c *Coordinator) listBlocks(ctx context.Context, movieID int64) ([]BlockEntry, error) {
	rows, err := c.db.QueryContext(ctx,
		`SELECT id, movie_id, title, indexer, reason, created_at FROM blocklist
		 WHERE movie_id = ? AND media_type = 'movie' ORDER BY id DESC`, movieID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []BlockEntry
	for rows.Next() {
		var b BlockEntry
		if err := rows.Scan(&b.ID, &b.MovieID, &b.Title, &b.Indexer, &b.Reason, &b.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// listBlocksSeries returns a series' blocklisted releases (newest first).
func (c *Coordinator) listBlocksSeries(ctx context.Context, seriesID int64) ([]BlockEntry, error) {
	rows, err := c.db.QueryContext(ctx,
		`SELECT id, movie_id, title, indexer, reason, created_at FROM blocklist
		 WHERE movie_id = ? AND media_type = 'series' ORDER BY id DESC`, seriesID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []BlockEntry
	for rows.Next() {
		var b BlockEntry
		if err := rows.Scan(&b.ID, &b.MovieID, &b.Title, &b.Indexer, &b.Reason, &b.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// removeBlock un-blocklists an entry.
func (c *Coordinator) removeBlock(ctx context.Context, id int64) error {
	_, err := c.db.ExecContext(ctx, `DELETE FROM blocklist WHERE id = ?`, id)
	return err
}

// blockedSet returns the normalized titles blocklisted for a movie.
func (c *Coordinator) blockedSet(ctx context.Context, movieID int64) (map[string]bool, error) {
	return c.blockedSetOf(ctx, movieID, "movie")
}

// blockedSetSeries returns the normalized titles blocklisted for a series.
func (c *Coordinator) blockedSetSeries(ctx context.Context, seriesID int64) (map[string]bool, error) {
	return c.blockedSetOf(ctx, seriesID, "series")
}

// blockedSetOf reads the blocklist for one library item. It fails closed: an unreadable
// blocklist is an error, never an empty set, because an empty set would make every known
// fake grabbable again. Callers skip the grab for this round instead.
func (c *Coordinator) blockedSetOf(ctx context.Context, id int64, mediaType string) (map[string]bool, error) {
	set := map[string]bool{}
	// Global rows (media_type='global') apply to every library item: a fake/malware
	// release detected on one show must not stay grabbable for everything else it
	// happens to match.
	rows, err := c.db.QueryContext(ctx,
		`SELECT norm_title FROM blocklist WHERE (movie_id = ? AND media_type = ?) OR media_type = 'global'`,
		id, mediaType)
	if err != nil {
		return nil, fmt.Errorf("blocklist unreadable: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			return nil, fmt.Errorf("blocklist unreadable: %w", err)
		}
		set[t] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("blocklist unreadable: %w", err)
	}
	return set, nil
}

// pendingTitlesOf reads the normalized titles of an item's grabs still in flight (see
// pendingTitleWhere). Like the blocklist it fails closed: without it the next sweep could
// grab the same release a second time.
func (c *Coordinator) pendingTitlesOf(ctx context.Context, id int64, mediaType string) (map[string]bool, error) {
	rows, err := c.db.QueryContext(ctx,
		`SELECT title FROM grabs
		 WHERE movie_id = ? AND media_type = ? AND `+c.pendingTitleWhere(), id, mediaType)
	if err != nil {
		return nil, fmt.Errorf("pending grabs unreadable: %w", err)
	}
	defer rows.Close()
	set := map[string]bool{}
	for rows.Next() {
		var title string
		if err := rows.Scan(&title); err != nil {
			return nil, fmt.Errorf("pending grabs unreadable: %w", err)
		}
		set[normTitle(title)] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("pending grabs unreadable: %w", err)
	}
	return set, nil
}

// skipUnreadable logs that title isn't being grabbed this round because the blocklist or
// the pending-grab list couldn't be read. The next sweep tries again.
func (c *Coordinator) skipUnreadable(title string, err error) {
	c.log.Warn("automation: blocklist unreadable — not grabbing "+title+" this round", "title", title, "err", err)
}

// addBlockSeries blocklists a release for a series (stall fail-over), so a re-search
// won't pick the same stalled release again.
func (c *Coordinator) addBlockSeries(ctx context.Context, seriesID int64, title, indexer, reason string) {
	_, err := c.db.ExecContext(ctx,
		`INSERT INTO blocklist (movie_id, norm_title, title, indexer, download_url, reason, media_type)
		 VALUES (?, ?, ?, ?, '', ?, 'series')`,
		seriesID, normTitle(title), title, indexer, reason)
	if err != nil {
		c.log.Warn("series: blocklist failed", "err", err)
	}
}

// addBlockBook blocklists a release for a book (stall fail-over).
func (c *Coordinator) addBlockBook(ctx context.Context, bookID int64, title, indexer, reason string) {
	_, err := c.db.ExecContext(ctx,
		`INSERT INTO blocklist (movie_id, norm_title, title, indexer, download_url, reason, media_type)
		 VALUES (?, ?, ?, ?, '', ?, 'book')`,
		bookID, normTitle(title), title, indexer, reason)
	if err != nil {
		c.log.Warn("book: blocklist failed", "err", err)
	}
}

// blockedSetBook returns the normalized titles blocklisted for a book.
func (c *Coordinator) blockedSetBook(ctx context.Context, bookID int64) (map[string]bool, error) {
	return c.blockedSetOf(ctx, bookID, "book")
}

// addBlockGlobal blocklists a release for EVERY library item — used when the release
// itself is the problem (executables and no video: a fake or malware), not its fit for
// one particular movie or show. blockedSetOf folds these rows into every lookup.
func (c *Coordinator) addBlockGlobal(ctx context.Context, title, indexer, reason string) {
	_, err := c.db.ExecContext(ctx,
		`INSERT INTO blocklist (movie_id, norm_title, title, indexer, download_url, reason, media_type)
		 VALUES (0, ?, ?, ?, '', ?, 'global')`,
		normTitle(title), title, indexer, reason)
	if err != nil {
		c.log.Warn("automation: global blocklist failed", "err", err)
	}
}

// recordGrab logs an automatic grab for stall tracking + seed cleanup, capturing
// the originating indexer's seed policy so cleanup survives the indexer being
// removed or renamed later.
func (c *Coordinator) recordGrab(ctx context.Context, movieID, versionID int64, title, indexer, profile string, stallMinutes int, infoHash string) {
	c.recordGrabReplacing(ctx, movieID, versionID, title, indexer, profile, stallMinutes, infoHash, "")
}

// recordGrabReplacing is recordGrab for a grab meant to replace the file its movie track
// holds now (replacesPath): an upgrade, a re-grab, a manual grab over an existing file.
// The stall check then waits for that track to hold a different file before calling the
// grab imported, instead of taking the old file for the new one.
func (c *Coordinator) recordGrabReplacing(ctx context.Context, movieID, versionID int64, title, indexer, profile string, stallMinutes int, infoHash, replacesPath string) {
	defer c.grabsChanged()
	notesFrom(ctx).grabbed(title) // the search attempt this grab came from, if any
	c.noteHashless("movie", title, infoHash)
	transport, seedEnabled, seedRatio, seedHours := c.grabPolicy(ctx, indexer, infoHash)
	decidedBy, decidedMode := pickOf(ctx, title) // what decided the pick (pickkey.go)
	_, err := c.db.ExecContext(ctx,
		`INSERT INTO grabs (movie_id, version_id, title, indexer, quality_profile, stall_minutes, seed_enabled, seed_ratio, seed_hours, info_hash, replaces_path, decided_by, decided_mode, transport)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		movieID, versionID, title, indexer, profile, stallMinutes, boolToInt(seedEnabled), seedRatio, seedHours, infoHash, replacesPath, decidedBy, decidedMode, transport)
	if err != nil {
		c.log.Warn("automation: record grab failed", "err", err)
		return
	}
	c.noteHandoff(ctx, "movie", movieID, title, infoHash)
	c.grabbed(ctx, "movie", movieID)
}

// grabPolicy is what a new grab records about its download: the transport, and the seed
// policy that goes with it.
//
// The transport comes from the download key (key.go) — a "sab-…" key is a usenet job, a
// hash a torrent — and, for a grab recorded without a key, from the indexer it came from.
// A usenet download never seeds, so it gets no seed goal whatever the indexer says; that
// includes the manual paths, which would otherwise default to seeding (seedRules).
func (c *Coordinator) grabPolicy(ctx context.Context, indexerName, key string) (transport download.Transport, seedEnabled bool, seedRatio float64, seedHours int) {
	transport = download.KeyTransport(key)
	if transport == "" {
		// An upload whose client didn't say the job's key: what was uploaded says what it
		// is. "manual" is no indexer, and reading it as one recorded an .nzb as a torrent —
		// which seeds by default, and which the stall check would judge as one.
		transport = uploadedTransport(ctx)
	}
	if transport == "" {
		transport = c.indexerTransport(ctx, indexerName)
	}
	if transport == download.TransportUsenet {
		return transport, false, 0, 0
	}
	seedEnabled, seedRatio, seedHours = c.seedRules(ctx, indexerName)
	return transport, seedEnabled, seedRatio, seedHours
}

// seedRules returns the seed policy of the named indexer.
//
// An unknown indexer must NOT mean "don't seed". A manual torrent upload is recorded
// with indexer "manual", and an indexer can be renamed or removed after a grab — in
// both cases the lookup misses. Returning seed-disabled made cleanup delete the torrent
// AND its data the moment it imported, which is a hit-and-run on whatever private
// tracker the file actually came from. Default to seeding for the standard window; the
// user can still stop or delete it from the Downloads page.
func (c *Coordinator) seedRules(ctx context.Context, name string) (enabled bool, ratio float64, hours int) {
	idxs, err := c.indexers.List(ctx)
	if err != nil {
		return true, 0, indexer.DefaultSeedHours
	}
	for _, ix := range idxs {
		if ix.Name == name {
			return ix.SeedEnabled, ix.SeedRatio, ix.SeedHours
		}
	}
	return true, 0, indexer.DefaultSeedHours
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// markGrabManual flags the most recent grab for this info hash as the user's own choice,
// so the import gate imports it whatever it scores against the current file.
//
// Set after recording rather than as a column on every record path: only two callers ever
// want it, and threading a bool through every grab recorder for their sake would put the
// question in a dozen places that have no opinion on it.
//
// It covers the whole release, which is what movies and books want and what an uploaded
// series torrent means. A series grab made for one season or episode uses markGrab.
func (c *Coordinator) markGrabManual(ctx context.Context, infoHash string) {
	c.markGrab(ctx, infoHash, true, WholeShow)
}

// markGrab records, on the most recent grab for this info hash, whether the user chose the
// release and which part of the show it was chosen for. The import gate is skipped only for
// episodes inside that scope.
func (c *Coordinator) markGrab(ctx context.Context, infoHash string, manual bool, scope GrabScope) {
	defer c.grabsChanged()
	if infoHash == "" {
		return
	}
	if _, err := c.db.ExecContext(ctx,
		`UPDATE grabs SET manual = ?, scope = ?
		  WHERE id = (SELECT id FROM grabs WHERE lower(info_hash) = lower(?) ORDER BY id DESC LIMIT 1)`,
		boolToInt(manual), scope.String(), infoHash); err != nil {
		c.log.Warn("automation: could not record the grab's say-so and scope", "err", err)
	}
}

// grabForce reads which episodes of this download skip the import gate: none for an
// automatic grab, the recorded scope for one the user chose. Target carries the recorded
// scope either way.
// Matched on info hash only: a name can be rewritten by the client, and getting this
// wrong in the permissive direction would let an automatic grab overwrite a better file.
func (c *Coordinator) grabForce(ctx context.Context, infoHash string) forceRule {
	if infoHash == "" {
		return forceRule{}
	}
	var manual int
	var scope string
	if err := c.db.QueryRowContext(ctx,
		`SELECT manual, scope FROM grabs WHERE lower(info_hash) = lower(?) ORDER BY id DESC LIMIT 1`,
		infoHash).Scan(&manual, &scope); err != nil {
		return forceRule{}
	}
	sc, ok := parseGrabScope(scope)
	if !ok {
		// Unreadable scope: gate everything. Too strict only costs a re-pick; too
		// permissive is the library-wide overwrite this column exists to stop.
		c.log.Warn("automation: unreadable grab scope — applying the import gate", "scope", scope)
		return forceRule{}
	}
	if manual == 0 {
		return forceRule{Target: sc}
	}
	return forceRule{On: true, Scope: sc, Target: sc}
}

// pendingGrabs returns grabs still downloading as far as we know — the ones the stall
// check judges. A grab held in Review is complete and isn't one of them.
func (c *Coordinator) pendingGrabs(ctx context.Context) ([]grab, error) {
	return c.grabsWhere(ctx, stallWatchWhere)
}

// grabsWhere reads every grab matching one of the status sets in grabstatus.go, oldest first.
func (c *Coordinator) grabsWhere(ctx context.Context, where string, args ...any) ([]grab, error) {
	rows, err := c.db.QueryContext(ctx, `SELECT `+grabCols+` FROM grabs WHERE `+where+` ORDER BY id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []grab
	for rows.Next() {
		g, err := scanGrab(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// pendingGrabTitles returns the normalized titles of a movie's still-pending grabs (downloaded /
// in-flight but not yet imported or failed). Used to stop the same release being grabbed again on
// the next sweep — a belt-and-suspenders guard against re-grab loops when the in-client name-match
// (inQueue) can't recognize a download.
func (c *Coordinator) pendingGrabTitles(ctx context.Context, movieID int64) (map[string]bool, error) {
	// Bounded to a day, matching pendingSeriesGrabTitles: a grab stuck 'grabbed' forever
	// (torrent removed by hand, stall timeout unset) must not block re-grabbing that
	// release permanently.
	return c.pendingTitlesOf(ctx, movieID, "movie")
}

// pendingSeriesGrabTitles returns the normalized titles of a series' still-pending grabs
// (grabbed but not yet imported or failed).
//
// The series path relied solely on seriesDownloading(), which reads the download client's
// queue and matches on the torrent's category plus its parsed title. That misses whenever
// the client hasn't applied the category, the queue read fails, or the torrent's name
// doesn't parse back to the show — and the next sweep then grabs the identical release
// again. Observed with Taskmaster: S16, S17 and S21 each grabbed twice, twelve minutes
// apart. Movies have had this DB-backed guard for exactly this reason.
func (c *Coordinator) pendingSeriesGrabTitles(ctx context.Context, seriesID int64) (map[string]bool, error) {
	// Bounded to a day. Stall fail-over normally clears a dead grab, but it only runs
	// when the profile sets a stall timeout — with none set, an unbounded guard would
	// blocklist the release by accident and permanently, which is worse than the
	// duplicate it prevents. After 24h, re-grabbing is the right call.
	return c.pendingTitlesOf(ctx, seriesID, "series")
}

// setGrabStatus moves one grab to status (a grabStatus* word).
func (c *Coordinator) setGrabStatus(ctx context.Context, id int64, status string) {
	defer c.grabsChanged()
	_, _ = c.db.ExecContext(ctx, `UPDATE grabs SET status = ? WHERE id = ?`, status, id)
}

// closeGrab moves grab g and every duplicate folded onto it to status, so a decision about
// one download settles all of its rows at once. A row left behind would be judged again on
// the next check: a second blocklist line, a second search.
func (c *Coordinator) closeGrab(ctx context.Context, g grab, status string) {
	c.setGrabStatuses(ctx, g.ids(), status)
}

// setGrabStatuses moves the grabs with these ids to status in one statement.
func (c *Coordinator) setGrabStatuses(ctx context.Context, ids []int64, status string) {
	if len(ids) == 0 {
		return
	}
	defer c.grabsChanged()
	args := make([]any, 0, 1+len(ids))
	args = append(args, status)
	for _, id := range ids {
		args = append(args, id)
	}
	marks := strings.TrimSuffix(strings.Repeat("?, ", len(ids)), ", ")
	if _, err := c.db.ExecContext(ctx, `UPDATE grabs SET status = ? WHERE id IN (`+marks+`)`, args...); err != nil {
		c.log.Warn("automation: couldn't record the grab's status", "status", status, "err", err)
	}
}

// closeStaleTwins moves the other 'grabbed' rows recorded for g's download of g's title to
// status. They are the same release grabbed again on a later day, while the first grab never
// closed (an older build re-grabbed a release whose import it couldn't match every day the
// pending guard ran out). Left 'grabbed' once their torrent has left the client, each one
// reads as a download that vanished and is failed over on its own.
func (c *Coordinator) closeStaleTwins(ctx context.Context, g grab, status string) {
	twins, err := c.grabsWhere(ctx, stallWatchWhere+` AND media_type = ? AND movie_id = ? AND version_id = ? AND id != ?`,
		g.MediaType, g.MovieID, g.VersionID, g.ID)
	if err != nil {
		c.log.Warn("automation: couldn't read the grab's duplicates", "release", g.Title, "err", err)
		return
	}
	var ids []int64
	for _, t := range twins {
		if sameDownload(g, t) {
			ids = append(ids, t.ID)
		}
	}
	c.setGrabStatuses(ctx, ids, status)
}

// seedCleanupGrabs returns grabs whose torrent may leave the client at its seed goal:
// imported ones, and dismissed ones (removed with their files kept). Never a grab still
// downloading or held in Review — nothing has landed from those yet.
func (c *Coordinator) seedCleanupGrabs(ctx context.Context) ([]grab, error) {
	return c.grabsWhere(ctx, seedCleanupWhere)
}

// liveGrabs returns every grab still in play — downloading, held, or imported-and-seeding.
//
// Distinct from seedCleanupGrabs on purpose: seed CLEANUP must only consider grabs whose
// data has landed (never remove a torrent before then), but the Seeding tab wants the
// rule for anything currently in the client. Using the imported-only set there made a
// torrent that had finished downloading but not yet imported read "Not managed by
// Arrmada — no seed rule", even though its rule was recorded at grab time.
func (c *Coordinator) liveGrabs(ctx context.Context) ([]grab, error) {
	return c.grabsWhere(ctx, liveWhere)
}

// scanGrab reads a grab row selected with grabCols.
func scanGrab(row interface{ Scan(...any) error }) (grab, error) {
	var g grab
	var seedEnabled int
	err := row.Scan(&g.ID, &g.MovieID, &g.VersionID, &g.Title, &g.Indexer, &g.Profile,
		&g.StallMinutes, &g.GrabbedAt, &seedEnabled, &g.SeedRatio, &g.SeedHours, &g.MediaType, &g.InfoHash, &g.Status, &g.ReplacesPath, &g.Transport)
	g.SeedEnabled = seedEnabled != 0
	return g, err
}

// setGrabStatusByHash closes out the grab a download came from, wherever that download
// was decided (Review, a manual import, an import sweep). It moves every in-flight
// (grabbed or held) row whose info hash is hash; with no such row it falls back to
// hashless in-flight rows of mediaType whose normalized title is name — rows predating
// migration 0062 and unparseable torrents. Returns the rows it moved.
//
// A grab already closed out is left alone: an imported grab mustn't be dragged back to
// 'held', and a seeded one is history.
func (c *Coordinator) setGrabStatusByHash(ctx context.Context, hash, name, mediaType, to string) []grab {
	defer c.grabsChanged()
	var moved []grab
	if hash != "" {
		hits, err := c.grabsWhere(ctx, inFlightWhere+` AND info_hash != '' AND lower(info_hash) = lower(?)`, hash)
		if err != nil {
			c.log.Warn("automation: couldn't read the grab to close out", "hash", hash, "err", err)
			return nil
		}
		moved = hits
	}
	if len(moved) == 0 && name != "" {
		where, args := inFlightWhere+` AND info_hash = ''`, []any{}
		if mediaType != "" {
			where += ` AND media_type = ?`
			args = append(args, mediaType)
		}
		hits, err := c.grabsWhere(ctx, where, args...)
		if err != nil {
			c.log.Warn("automation: couldn't read the grab to close out", "release", name, "err", err)
			return nil
		}
		want := normRelease(name)
		for _, g := range hits {
			if normRelease(g.Title) == want {
				moved = append(moved, g)
			}
		}
	}
	for i := range moved {
		if moved[i].Status == to {
			continue
		}
		if _, err := c.db.ExecContext(ctx, `UPDATE grabs SET status = ? WHERE id = ?`, to, moved[i].ID); err != nil {
			c.log.Warn("automation: couldn't record the grab's status", "status", to, "err", err)
			continue
		}
		moved[i].Status = to
	}
	return moved
}

// markGrabImportedForMovie flips the ONE grab this import came from to imported, the
// same way markSeriesGrabImported does.
//
// It used to flip EVERY pending grab for the movie, which marked sibling version
// downloads (e.g. a 4K track still in flight while the 1080p landed) as imported
// before their data existed. Seed cleanup only considers imported grabs and removes
// finished ones WITH their data, so the sibling was deleted the moment it completed
// and the version re-grabbed — the same grab → delete → re-grab loop the series path
// fixed. A grab that matches no release name here is left pending; DetectStalled
// flips it via movieHasFileFor once its own version gains a file.
func (c *Coordinator) markGrabImportedForMovie(ctx context.Context, movieID int64, releaseName string) {
	defer c.grabsChanged()
	rows, err := c.db.QueryContext(ctx,
		`SELECT id, title FROM grabs WHERE movie_id = ? AND media_type = 'movie' AND `+inFlightWhere, movieID)
	if err != nil {
		return
	}
	want := normRelease(releaseName)
	var ids []int64
	for rows.Next() {
		var id int64
		var title string
		if rows.Scan(&id, &title) == nil && normRelease(title) == want {
			ids = append(ids, id)
		}
	}
	rows.Close() // close before writing — SQLite won't take a write while a read is open
	for _, id := range ids {
		if _, err := c.db.ExecContext(ctx, `UPDATE grabs SET status = ? WHERE id = ?`, grabStatusImported, id); err != nil {
			c.log.Warn("automation: mark grab imported failed", "err", err)
		}
	}
}

// markMovieGrabImportedByHash flips the in-flight grab a movie download came from to
// imported, found by its info hash — the identity that survives the trip through the
// client. Reports whether a row matched; rows without a hash need the name fallback
// (markGrabImportedForMovie), which misses prettified tracker titles.
func (c *Coordinator) markMovieGrabImportedByHash(ctx context.Context, movieID int64, infoHash string) bool {
	defer c.grabsChanged()
	if infoHash == "" {
		return false
	}
	res, err := c.db.ExecContext(ctx,
		`UPDATE grabs SET status = ? WHERE movie_id = ? AND media_type = 'movie' AND info_hash != ''
		   AND lower(info_hash) = lower(?) AND `+inFlightWhere, grabStatusImported, movieID, infoHash)
	if err != nil {
		c.log.Warn("automation: mark grab imported failed", "err", err)
		return false
	}
	n, _ := res.RowsAffected()
	return n > 0
}

// markSeriesGrabImported flips the ONE grab this download came from to imported.
//
// It used to flip EVERY pending grab for the series, which marked sibling torrents as
// imported before their data had landed. Seed cleanup only considers imported grabs, so
// it would then remove those torrents WITH their data (the default seed policy deletes
// data), the episodes stayed missing, and the next sweep re-grabbed them — a real
// grab → delete → re-grab loop.
// It matches by info hash first — the torrent's real identity — and only falls back to
// the normalized name for rows without one (pre-migration-0062, unparseable torrents).
// Name matching alone silently failed for whole trackers whose listing titles are
// prettified renderings of the torrent name ("DD+" vs "EAC3"), leaving the grab
// 'grabbed' forever: seed cleanup never removed the torrent, and the pending-grab
// guard blocked legitimate re-grabs for a day.
// markSeriesGrabFailed flips a series grab to 'failed'. Used when its download is thrown
// away — blocklisted as junk, say — so the pending-grab guard stops treating the release
// as still in flight. Left at 'grabbed', the sweep answers "already grabbed and still
// importing" for a full day about a torrent that no longer exists and can never import,
// and the show can't grab an alternate for the whole of it.
func (c *Coordinator) markSeriesGrabFailed(ctx context.Context, seriesID int64, infoHash, releaseName string) {
	c.setSeriesGrabStatus(ctx, seriesID, infoHash, releaseName, grabStatusFailed)
}

func (c *Coordinator) markSeriesGrabImported(ctx context.Context, seriesID int64, infoHash, releaseName string) {
	c.setSeriesGrabStatus(ctx, seriesID, infoHash, releaseName, grabStatusImported)
}

// setSeriesGrabStatus finds the grab a download came from — by info hash first, since a
// name survives the round trip through the client only by luck — and sets its status.
func (c *Coordinator) setSeriesGrabStatus(ctx context.Context, seriesID int64, infoHash, releaseName, status string) {
	c.setItemGrabStatus(ctx, "series", seriesID, infoHash, releaseName, status)
}

// setItemGrabStatus is setSeriesGrabStatus for any media type: the in-flight grabs of one
// library item, matched by info hash first and by normalized release name only when no
// hash matches, are set to status.
func (c *Coordinator) setItemGrabStatus(ctx context.Context, mediaType string, itemID int64, infoHash, releaseName, status string) {
	defer c.grabsChanged()
	rows, err := c.db.QueryContext(ctx,
		`SELECT id, title, info_hash FROM grabs WHERE movie_id = ? AND media_type = ? AND `+inFlightWhere, itemID, mediaType)
	if err != nil {
		return
	}
	wantHash := strings.ToLower(infoHash)
	want := normRelease(releaseName)
	var byHash, byName []int64
	for rows.Next() {
		var id int64
		var title, hash string
		if rows.Scan(&id, &title, &hash) != nil {
			continue
		}
		if wantHash != "" && hash != "" && strings.ToLower(hash) == wantHash {
			byHash = append(byHash, id)
		} else if normRelease(title) == want {
			byName = append(byName, id)
		}
	}
	ids := byHash
	if len(ids) == 0 {
		ids = byName
	}
	rows.Close() // close before writing — SQLite won't take a write while a read is open
	for _, id := range ids {
		if _, err := c.db.ExecContext(ctx, `UPDATE grabs SET status = ? WHERE id = ?`, status, id); err != nil {
			c.log.Warn("automation: mark grab status failed", "kind", mediaType, "status", status, "err", err)
		}
	}
}

// grabForHash returns the newest grab recorded for a torrent's info hash, whatever its
// status. sql.ErrNoRows when there is none.
func (c *Coordinator) grabForHash(ctx context.Context, hash string) (grab, error) {
	if hash == "" {
		return grab{}, sql.ErrNoRows
	}
	return scanGrab(c.db.QueryRowContext(ctx,
		`SELECT `+grabCols+` FROM grabs WHERE info_hash != '' AND lower(info_hash) = lower(?) ORDER BY id DESC LIMIT 1`, hash))
}

// GrabRef is which movie and track a download was grabbed for.
type GrabRef struct {
	ID, MovieID int64
	// VersionID is the track the grab was made for (0 = the default track).
	VersionID int64
	// ReplacesPath is the file the grab was meant to replace ("" when it fills a missing
	// track): an upgrade, a re-grab, a manual grab over an existing file.
	ReplacesPath string
}

// MovieGrabForHash resolves which movie and track a download was grabbed for, by torrent
// info hash — the one identity that survives the round trip through the download client
// unchanged. Any grab status counts: by import time the row may already be seeded.
func (c *Coordinator) MovieGrabForHash(ctx context.Context, hash string) (GrabRef, bool) {
	if hash == "" {
		return GrabRef{}, false
	}
	var g GrabRef
	err := c.db.QueryRowContext(ctx,
		`SELECT id, movie_id, version_id, replaces_path FROM grabs
		 WHERE media_type = 'movie' AND info_hash != '' AND lower(info_hash) = lower(?)
		 ORDER BY id DESC LIMIT 1`, hash).Scan(&g.ID, &g.MovieID, &g.VersionID, &g.ReplacesPath)
	return g, err == nil
}

// normTitle normalizes a release title for blocklist/matching comparisons. Accents
// are folded first so "Pokémon" and "Pokemon" compare equal.
func normTitle(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(parser.FoldAccents(s)) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// addBlockMusic blocklists a release for one album.
func (c *Coordinator) addBlockMusic(ctx context.Context, albumID int64, title, indexer, reason string) {
	_, err := c.db.ExecContext(ctx,
		`INSERT INTO blocklist (movie_id, norm_title, title, indexer, download_url, reason, media_type)
		 VALUES (?, ?, ?, ?, '', ?, 'music')`,
		albumID, normTitle(title), title, indexer, reason)
	if err != nil {
		c.log.Warn("music: blocklist failed", "err", err)
	}
}

// blockedSetMusic returns the normalized titles blocklisted for an album.
func (c *Coordinator) blockedSetMusic(ctx context.Context, albumID int64) (map[string]bool, error) {
	return c.blockedSetOf(ctx, albumID, "music")
}

// addBlockMusicArtist blocklists a discography release for an artist. A discography is
// grabbed against the artist (mediaMusicArtist), so its block is kept there too: keyed on
// the album id column, it blocked it for whichever album shared the artist's number.
func (c *Coordinator) addBlockMusicArtist(ctx context.Context, artistID int64, title, indexer, reason string) {
	_, err := c.db.ExecContext(ctx,
		`INSERT INTO blocklist (movie_id, norm_title, title, indexer, download_url, reason, media_type)
		 VALUES (?, ?, ?, ?, '', ?, ?)`,
		artistID, normTitle(title), title, indexer, reason, mediaMusicArtist)
	if err != nil {
		c.log.Warn("music: blocklist failed", "err", err)
	}
}

// blockedSetMusicArtist returns the normalized discography titles blocklisted for an artist.
func (c *Coordinator) blockedSetMusicArtist(ctx context.Context, artistID int64) (map[string]bool, error) {
	return c.blockedSetOf(ctx, artistID, mediaMusicArtist)
}

// setMusicGrabStatus finds the grab a music download came from and sets its status: an
// album grab ("music", album id) or a discography (mediaMusicArtist, artist id). By info
// hash first, then by release name, like setSeriesGrabStatus.
func (c *Coordinator) setMusicGrabStatus(ctx context.Context, mediaType string, id int64, infoHash, releaseName, status string) {
	c.setItemGrabStatus(ctx, mediaType, id, infoHash, releaseName, status)
}

// setGrabInfoHash records the torrent's real identity on a grab that was recorded
// without one. Guarded to empty rows in SQL as well as in the caller: this is what seed
// removal decisions key on, and overwriting a good hash would point a rule at the wrong
// torrent.
func (c *Coordinator) setGrabInfoHash(ctx context.Context, id int64, hash string) error {
	defer c.grabsChanged()
	_, err := c.db.ExecContext(ctx,
		`UPDATE grabs SET info_hash = ? WHERE id = ? AND (info_hash IS NULL OR info_hash = '')`,
		hash, id)
	return err
}
