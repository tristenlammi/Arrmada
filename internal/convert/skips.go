package convert

import (
	"context"
	"database/sql"
	"time"
)

// Skip kinds. These group the Problems list, so they're coarse on purpose — the user cares
// "why won't these convert", not which line of code returned.
const (
	SkipHDRUnsupported = "hdr_unsupported" // this file's HDR can't be carried through a conversion
	SkipHardlinked     = "hardlinked"      // still seeding / hardlinked, so it isn't ours to replace
	SkipNotSmaller     = "not_smaller"     // the encode didn't save enough to be worth keeping
	SkipQualityGate    = "quality_gate"    // the encode couldn't meet the quality threshold
	SkipCancelled      = "cancelled"       // you cancelled it; left alone for a while
	SkipAlreadyTarget  = "already_target"  // nothing to do; not worth recording
	// The test encode put the file just short of the saving rule — close enough that its
	// 60-second sample could be wrong. Tested again later on other scenes (preflight.go)
	// before it's given up on.
	SkipBorderline = "borderline"

	// Conditions that clear on their own (someone frees space, a file reappears). They back
	// off instead of being retried on the very next pick — that used to hot-loop the runner.
	SkipNoScratch   = "no_scratch"   // the transcode folder hasn't room for this file
	SkipLibraryFull = "library_full" // the library disk hasn't room for the converted file
	SkipSourceGone  = "source_gone"  // the library file is missing
	SkipTransient   = "transient"    // any other failure that should clear on its own
	// The converted file's .mkv name is a file another library record points at (a second
	// version, an episode filed under that name), or a file no record owns that has anything
	// in it (someone's own copy). Never moved aside; looked at again later.
	SkipNameTaken = "name_taken"
	// There's nowhere to keep the original: the file is outside every library folder, the
	// folder is inside the app's data folder, or its hold can't be made. Looked at again later.
	SkipNoHold = "no_hold"

	// Retired: originals went to the recycle bin and one that didn't fit under its cap
	// waited here. They now go to the originals hold, so these are cleared at startup; the
	// kind stays so a row an older version wrote still reads.
	SkipBinFull = "bin_full"
)

// backoffSkip reports whether a skip kind waits longer each time it repeats.
func backoffSkip(kind string) bool {
	switch kind {
	case SkipNoScratch, SkipLibraryFull, SkipSourceGone, SkipTransient, SkipNameTaken, SkipNoHold:
		return true
	}
	return false
}

// hardlinkFirstWait is the first wait of a file still linked to the downloads folder. A
// usenet job's folder goes on the seed clean-up pass, every 10 minutes, so a file picked just
// after its import is usually free by then; a torrent that really is seeding pays one extra
// look. It has to outlast a pass that's already due and one more.
const hardlinkFirstWait = 25 * time.Minute

// retryDelay is how long a temporary skip waits before the file is picked again. A seeding
// file is checked once soon, then twice a day; a file you cancelled stays out of the way for a month (or
// until you press "Try again"). A full disk is looked at again after an hour, then six,
// then daily — soon enough to notice space was freed, rarely enough not to flood the log.
// attempts counts this kind in a row, starting at 1.
func retryDelay(kind string, attempts int) time.Duration {
	switch kind {
	case SkipHardlinked:
		if attempts <= 1 {
			return hardlinkFirstWait
		}
		return 12 * time.Hour
	case SkipCancelled:
		return 30 * 24 * time.Hour
	case SkipBinFull, SkipHoldFull:
		return 24 * time.Hour
	case SkipBorderline:
		return borderlineRetest
	}
	if backoffSkip(kind) {
		switch {
		case attempts <= 1:
			return time.Hour
		case attempts == 2:
			return 6 * time.Hour
		}
		return 24 * time.Hour
	}
	return 0
}

// permanentSkip reports whether a skip reason will still hold next time, unchanged.
//
// A Dolby Vision file cannot become AV1-convertible by waiting, and a file that didn't
// shrink won't shrink on a retry. A seeding file, by contrast, stops seeding eventually.
// Only permanent skips are excluded from the reclaimable-space figure — otherwise the
// Overview keeps promising space that will never arrive.
func permanentSkip(kind string) bool {
	switch kind {
	case SkipHDRUnsupported, SkipNotSmaller, SkipQualityGate, SkipReverted:
		return true
	}
	return false
}

// skipStore persists why files were skipped, so the reasons survive a restart and can be
// shown to the user instead of scrolling out of an in-memory job list.
//
// A skip is about a FILE (migration 0355): each row records the path and size the library
// index had for its title when it was written, and it only holds while the index still has
// that file. An upgrade, a remux or any other replacement is judged afresh rather than
// inheriting "not smaller" from the file before it. A row with no file recorded (written
// before 0355 for a title with no indexed file, or a "reverted" skip, which is title-wide
// on purpose) holds for any file, as all of them used to.
//
// A "not smaller" or "quality" verdict a GPU encode reached also records the encoder, and
// holds only while the GPU is in use (gpuOn): the CPU makes smaller files at the same
// quality, so turning the GPU off gives those files their own chance.
type skipStore struct {
	db    *sql.DB
	gpuOn func(context.Context) bool // nil = the GPU is off
}

// fileVerdictHolds is the SQL condition under which a skip or failure row of table still
// applies: no file recorded, or the index still has exactly that file.
func fileVerdictHolds(table string) string {
	return `(` + table + `.file_path = '' OR EXISTS (SELECT 1 FROM convert_library l
	          WHERE l.path = ` + table + `.file_path AND l.size_bytes = ` + table + `.file_size))`
}

// holds is the full condition for a skip row: its file, and — for a GPU's verdict — the GPU.
func (st *skipStore) holds(ctx context.Context) (string, []any) {
	gpu := st.gpuOn != nil && st.gpuOn(ctx)
	return fileVerdictHolds("convert_skips") + ` AND (convert_skips.encoder IN ('', 'cpu') OR ?)`, []any{gpu}
}

// indexedFile is the file the library index has for an item key now, or "" and 0.
func indexedFile(ctx context.Context, db *sql.DB, key string) (string, int64) {
	b := Blocked{Key: key}
	parseItemKey(&b)
	var row *sql.Row
	switch b.Kind {
	case "movie":
		row = db.QueryRowContext(ctx, `SELECT path, size_bytes FROM convert_library
			WHERE media_type = 'movie' AND movie_id = ? AND version_id = ? LIMIT 1`, b.MovieID, b.VersionID)
	case "episode":
		row = db.QueryRowContext(ctx, `SELECT path, size_bytes FROM convert_library
			WHERE media_type = 'episode' AND series_id = ? AND season = ? AND episode = ? LIMIT 1`, b.SeriesID, b.Season, b.Episode)
	default:
		return "", 0
	}
	var path string
	var size int64
	if row.Scan(&path, &size) != nil {
		return "", 0
	}
	return path, size
}

// encoderVerdict reports whether a skip kind is a verdict on what an encoder produced, and
// so belongs to that encoder.
func encoderVerdict(kind string) bool {
	return kind == SkipNotSmaller || kind == SkipQualityGate || kind == SkipBorderline
}

// Skipped is one skipped file, for the Problems list.
type Skipped struct {
	Key       string `json:"key"`
	Kind      string `json:"kind"`
	Reason    string `json:"reason"`
	Permanent bool   `json:"permanent"`
	UpdatedAt string `json:"updated_at"`
	// When a temporary skip is next tried (unix seconds; 0 = no wait), and how many times
	// in a row it has hit this same kind.
	RetryAfter int64 `json:"retry_after"`
	Attempts   int   `json:"attempts"`

	// Resolved for display.
	MediaKind string `json:"media_kind"`
	MovieID   int64  `json:"movie_id,omitempty"`
	VersionID int64  `json:"version_id,omitempty"`
	SeriesID  int64  `json:"series_id,omitempty"`
	Season    int    `json:"season"`
	Episode   int    `json:"episode"`
	Title     string `json:"title"`
}

// record stores why an item was skipped and returns how many times in a row it has now hit
// this kind (1 for a new or different kind), which sets how long a backing-off skip waits.
// Only one job runs per item at a time, so the read and the write can't interleave.
func (st *skipStore) record(ctx context.Context, key, kind, reason string) int {
	return st.recordAt(ctx, key, kind, reason, 0)
}

// recordAt is record with the retry time given (unix seconds) instead of worked out from
// the kind; 0 works it out as record does.
func (st *skipStore) recordAt(ctx context.Context, key, kind, reason string, retryAt int64) int {
	return st.recordEnc(ctx, key, kind, reason, retryAt, "")
}

// recordEnc is recordAt for a verdict an encoder reached: encoder is its kind ("cpu",
// "vaapi", …), kept for the "not smaller" and quality verdicts (encoderVerdict) so a GPU's
// holds only while the GPU is in use. The row is tied to the file the index has for the
// item now; a "reverted" skip is title-wide (see migration 0355).
func (st *skipStore) recordEnc(ctx context.Context, key, kind, reason string, retryAt int64, encoder string) int {
	if key == "" || kind == "" || kind == SkipAlreadyTarget {
		return 0 // "already the target codec" isn't a problem, it's success
	}
	perm := 0
	if permanentSkip(kind) {
		perm = 1
	}
	path, size := "", int64(0)
	if kind != SkipReverted {
		path, size = indexedFile(ctx, st.db, key)
	}
	if !encoderVerdict(kind) {
		encoder = ""
	}
	attempts := 1
	var prevKind, prevPath string
	var prevAttempts int
	var prevSize int64
	if st.db.QueryRowContext(ctx, `SELECT kind, attempts, file_path, file_size FROM convert_skips WHERE item_key = ?`, key).
		Scan(&prevKind, &prevAttempts, &prevPath, &prevSize) == nil && prevKind == kind && prevPath == path && prevSize == size {
		attempts = prevAttempts + 1
	}
	retry := retryAt
	if d := retryDelay(kind, attempts); retry == 0 && d > 0 {
		retry = time.Now().Add(d).Unix()
	}
	_, _ = st.db.ExecContext(ctx,
		`INSERT INTO convert_skips (item_key, kind, reason, permanent, retry_after, attempts, file_path, file_size, encoder, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, datetime('now'))
		 ON CONFLICT(item_key) DO UPDATE SET
		   kind = excluded.kind, reason = excluded.reason, permanent = excluded.permanent,
		   retry_after = excluded.retry_after, attempts = excluded.attempts, file_path = excluded.file_path,
		   file_size = excluded.file_size, encoder = excluded.encoder, updated_at = datetime('now')`,
		key, kind, reason, perm, retry, attempts, path, size, encoder)
	return attempts
}

// attemptsOf is how many times in a row an item has hit kind on its current file (0 when
// its skip is another kind, or about another file).
func (st *skipStore) attemptsOf(ctx context.Context, key, kind string) int {
	cond, args := st.holds(ctx)
	var n int
	_ = st.db.QueryRowContext(ctx, `SELECT attempts FROM convert_skips WHERE item_key = ? AND kind = ? AND `+cond,
		append([]any{key, kind}, args...)...).Scan(&n)
	return n
}

// clear forgets an item's skip — called when it converts successfully, or when the user
// asks for it to be retried.
func (st *skipStore) clear(ctx context.Context, key string) {
	_, _ = st.db.ExecContext(ctx, `DELETE FROM convert_skips WHERE item_key = ?`, key)
}

func (st *skipStore) clearAll(ctx context.Context) error {
	_, err := st.db.ExecContext(ctx, `DELETE FROM convert_skips`)
	return err
}

// clearKind forgets every skip of one kind, reporting how many there were.
func (st *skipStore) clearKind(ctx context.Context, kind string) int64 {
	res, err := st.db.ExecContext(ctx, `DELETE FROM convert_skips WHERE kind = ?`, kind)
	if err != nil {
		return 0
	}
	n, _ := res.RowsAffected()
	return n
}

// keysOfKind returns the items currently skipped for one reason.
func (st *skipStore) keysOfKind(ctx context.Context, kind string) map[string]bool {
	out := map[string]bool{}
	cond, args := st.holds(ctx)
	rows, err := st.db.QueryContext(ctx, `SELECT item_key FROM convert_skips WHERE kind = ? AND `+cond, append([]any{kind}, args...)...)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var k string
		if rows.Scan(&k) == nil {
			out[k] = true
		}
	}
	return out
}

// permanentKeys returns the items whose skip won't resolve on its own, so their space can
// be left out of the reclaimable total.
func (st *skipStore) permanentKeys(ctx context.Context) map[string]bool {
	out := map[string]bool{}
	cond, args := st.holds(ctx)
	rows, err := st.db.QueryContext(ctx, `SELECT item_key FROM convert_skips WHERE permanent = 1 AND `+cond, args...)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var k string
		if rows.Scan(&k) == nil {
			out[k] = true
		}
	}
	return out
}

// waitingKeys returns the items the runner must not pick right now: permanent skips, and
// temporary ones whose retry time hasn't come.
func (st *skipStore) waitingKeys(ctx context.Context) map[string]bool {
	out := map[string]bool{}
	cond, args := st.holds(ctx)
	rows, err := st.db.QueryContext(ctx,
		`SELECT item_key FROM convert_skips WHERE (permanent = 1 OR retry_after > ?) AND `+cond, append([]any{time.Now().Unix()}, args...)...)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var k string
		if rows.Scan(&k) == nil {
			out[k] = true
		}
	}
	return out
}

func (st *skipStore) list(ctx context.Context) ([]Skipped, error) {
	// Only the skips that still hold: one about a file since replaced is history.
	cond, args := st.holds(ctx)
	rows, err := st.db.QueryContext(ctx,
		`SELECT item_key, kind, reason, permanent, updated_at, retry_after, attempts FROM convert_skips
		 WHERE `+cond+`
		 ORDER BY permanent DESC, kind, updated_at DESC`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Skipped{}
	for rows.Next() {
		var s Skipped
		var perm int
		if err := rows.Scan(&s.Key, &s.Kind, &s.Reason, &perm, &s.UpdatedAt, &s.RetryAfter, &s.Attempts); err != nil {
			return nil, err
		}
		s.Permanent = perm == 1
		var b Blocked
		b.Key = s.Key
		parseItemKey(&b)
		s.MediaKind, s.MovieID, s.VersionID, s.SeriesID, s.Season, s.Episode = b.Kind, b.MovieID, b.VersionID, b.SeriesID, b.Season, b.Episode
		out = append(out, s)
	}
	return out, rows.Err()
}
