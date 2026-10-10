// Package requests implements the Requests module: users ask for a movie or series,
// and on approval it's handed to the Movies/Series module for acquisition.
package requests

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// ErrNotFound is returned when a request id doesn't exist.
var ErrNotFound = errors.New("request not found")

// Status values.
const (
	StatusPending  = "pending"
	StatusApproved = "approved"
	StatusDeclined = "declined"
)

// Request is one user request for a movie, series, or book.
type Request struct {
	ID               int64   `json:"id"`
	MediaType        string  `json:"media_type"`        // "movie" | "series" | "book"
	TMDBID           int     `json:"tmdb_id"`           // movies/series
	OLKey            string  `json:"ol_key,omitempty"`  // books (Open Library work key)
	BookID           int64   `json:"book_id,omitempty"` // books: the library row it became; survives that row's key changing
	Title            string  `json:"title"`
	Author           string  `json:"author,omitempty"`  // books
	Formats          string  `json:"formats,omitempty"` // books: ebook | audiobook | both; "" = from before the choice (formats.go)
	Year             int     `json:"year"`
	PosterURL        string  `json:"poster_url,omitempty"`
	Overview         string  `json:"overview,omitempty"`
	Status           string  `json:"status"`
	QualityProfile   string  `json:"quality_profile,omitempty"`
	RequestedBy      int64   `json:"requested_by"`
	RequestedByName  string  `json:"requested_by_name,omitempty"`
	Note             string  `json:"note,omitempty"`
	Available        bool    `json:"available"`                   // computed at read time, not stored
	DownloadProgress float64 `json:"download_progress,omitempty"` // 0..1 while downloading; computed at read time
	// Tracking is where the request has got to, from request to ready (see Track).
	Tracking *Tracking `json:"tracking,omitempty"`
	// PlexURL opens the title in app.plex.tv once it's delivered and Plex has it. Filled
	// by the HTTP layer from the Plex library index; never stored.
	PlexURL string `json:"plex_url,omitempty"`
	// ReadyAt is when the requester (and followers) were told it's ready, unix seconds;
	// 0 until then. It also sorts approved requests into in progress and ready.
	ReadyAt   int64  `json:"ready_at"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
	// Relation is how the viewer stands to it in their own list: owner or subscriber.
	// Empty in a staff list of everyone's requests.
	Relation string `json:"relation,omitempty"`
	// LibraryID is the library item it became, for staff ("Open in library"). The HTTP
	// layer fills it for staff only; requesters never see library ids.
	LibraryID int64 `json:"library_id,omitempty"`
	// Followers names who else follows it, on the staff detail view only.
	Followers []Follower `json:"followers,omitempty"`

	// The last decision: who made it (staff only — 0 and "" for an auto-approval) and
	// when (unix seconds, 0 while undecided). DeclineReason is what staff told the
	// requester; a re-request keeps the previous one until the next decision.
	DeclineReason string `json:"decline_reason,omitempty"`
	DecidedBy     int64  `json:"decided_by,omitempty"`
	DecidedByName string `json:"decided_by_name,omitempty"`
	DecidedAt     int64  `json:"decided_at,omitempty"`
	// ReRequest counts how often it was asked for again after a decline (> 0: flagged).
	ReRequest int `json:"rerequest,omitempty"`

	// Seasons are the regular seasons a series request asks for, ascending; empty means
	// the whole show (every request made before seasons existed, and "All seasons").
	// On create it is what the caller asked for.
	Seasons []int `json:"seasons,omitempty"`
	// KnownSeasons is, on create only, every season the catalogue lists for the show (the
	// handler fills it from the TMDB detail it fetches anyway). nil means no catalogue:
	// a whole-show ask is then stored whole. Never stored or sent.
	KnownSeasons []int `json:"-"`

	// Filled by enrichAvailability for Track: the library item the request became, and
	// for a series how much of it is on disk.
	libID    int64
	epHave   int
	epTotal  int
	released bool
	// seasonsDone: a season-scoped series request is complete over its own seasons
	// (seasonsProgress). Unused for whole-show requests.
	seasonsDone bool
	// notApproved is the sentence naming the seasons staff trimmed off on approve
	// ("Season 3 wasn't approved."), for the "approved" notice. Never stored.
	notApproved string
	// Searches in a row that found nothing and when the sweep last looked (as stored);
	// books also say when the next one is due (RFC3339).
	searchMisses int
	lastSearchAt string
	nextCheckAt  string
	// Books: for a "both" request with one format here, which one and what's coming.
	partNote string
	// onDiskAt is when a movie or series request was first seen complete while Plex is
	// set up, waiting for Plex to show it (plexready.go); 0 = not waiting. seasonDisk is
	// the same per season for a request of several seasons, as stored.
	onDiskAt   int64
	seasonDisk string
}

// Repo persists requests in SQLite.
type Repo struct{ db *sql.DB }

// NewRepo builds a repository over the given pool.
func NewRepo(db *sql.DB) *Repo { return &Repo{db: db} }

const cols = `id, media_type, tmdb_id, ol_key, title, author, year, poster_url, overview, status,
	quality_profile, requested_by, requested_by_name, note, created_at, updated_at, book_id, formats, seasons, ready_at,
	decline_reason, decided_by, decided_by_name, decided_at, rerequest, on_disk_at, season_disk_at`

func scan(row interface{ Scan(...any) error }) (Request, error) {
	var r Request
	var bookID sql.NullInt64
	var seasons string
	err := row.Scan(&r.ID, &r.MediaType, &r.TMDBID, &r.OLKey, &r.Title, &r.Author, &r.Year, &r.PosterURL, &r.Overview,
		&r.Status, &r.QualityProfile, &r.RequestedBy, &r.RequestedByName, &r.Note, &r.CreatedAt, &r.UpdatedAt, &bookID, &r.Formats, &seasons,
		&r.ReadyAt, &r.DeclineReason, &r.DecidedBy, &r.DecidedByName, &r.DecidedAt, &r.ReRequest, &r.onDiskAt, &r.seasonDisk)
	r.BookID = bookID.Int64
	r.Seasons = decodeSeasons(seasons)
	return r, err
}

// encodeSeasons stores a season list as a JSON array; the whole show (none listed) is an
// empty string.
func encodeSeasons(seasons []int) string {
	if len(seasons) == 0 {
		return ""
	}
	b, err := json.Marshal(seasons)
	if err != nil {
		return ""
	}
	return string(b)
}

// decodeSeasons reads a stored season list; an empty string (or anything unreadable) is
// the whole show.
// Reading an unreadable list as the whole show can only widen what a request covers, never
// drop a season someone asked for.
func decodeSeasons(s string) []int {
	if s == "" {
		return nil
	}
	var out []int
	if json.Unmarshal([]byte(s), &out) != nil || len(out) == 0 {
		return nil
	}
	return out
}

// nullID stores 0 as NULL: the column references books(id), and there is no book 0.
func nullID(id int64) any {
	if id <= 0 {
		return nil
	}
	return id
}

// Create inserts a request. Returns ErrExists (wrapped) on a duplicate media.
func (r *Repo) Create(ctx context.Context, req Request) (Request, error) {
	res, err := r.db.ExecContext(ctx,
		`INSERT INTO requests (media_type, tmdb_id, ol_key, title, author, year, poster_url, overview, status,
			quality_profile, requested_by, requested_by_name, note, book_id, formats, seasons, rerequest, decline_reason)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		req.MediaType, req.TMDBID, req.OLKey, req.Title, req.Author, req.Year, req.PosterURL, req.Overview, req.Status,
		req.QualityProfile, req.RequestedBy, req.RequestedByName, req.Note, nullID(req.BookID), req.Formats, encodeSeasons(req.Seasons),
		req.ReRequest, req.DeclineReason)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return Request{}, ErrExists
		}
		return Request{}, err
	}
	id, _ := res.LastInsertId()
	return r.Get(ctx, id)
}

// ErrExists is returned when the same media has already been requested.
var ErrExists = errors.New("already requested")

// Get returns one request by id.
func (r *Repo) Get(ctx context.Context, id int64) (Request, error) {
	row := r.db.QueryRowContext(ctx, `SELECT `+cols+` FROM requests WHERE id = ?`, id)
	req, err := scan(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Request{}, ErrNotFound
	}
	return req, err
}

// ListByMedia returns every request for one movie or series, oldest first. A series can
// have several (one per ask for more seasons).
func (r *Repo) ListByMedia(ctx context.Context, mediaType string, tmdbID int) ([]Request, error) {
	return r.query(ctx, `SELECT `+cols+` FROM requests WHERE media_type = ? AND tmdb_id = ? ORDER BY id`, mediaType, tmdbID)
}

// SetSeasons rewrites the seasons a series request stands for (staff trimming it on
// approve).
func (r *Repo) SetSeasons(ctx context.Context, id int64, seasons []int) error {
	res, err := r.db.ExecContext(ctx, `UPDATE requests SET seasons = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`, encodeSeasons(seasons), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// GetByMedia returns an existing request for the given movie, if any. A series can have
// several requests: use ListByMedia.
func (r *Repo) GetByMedia(ctx context.Context, mediaType string, tmdbID int) (Request, bool) {
	row := r.db.QueryRowContext(ctx, `SELECT `+cols+` FROM requests WHERE media_type = ? AND tmdb_id = ?`, mediaType, tmdbID)
	req, err := scan(row)
	if err != nil {
		return Request{}, false
	}
	return req, true
}

// GetByBook returns an existing request for the given Open Library work, if any.
func (r *Repo) GetByBook(ctx context.Context, olKey string) (Request, bool) {
	row := r.db.QueryRowContext(ctx, `SELECT `+cols+` FROM requests WHERE media_type = 'book' AND ol_key = ?`, olKey)
	req, err := scan(row)
	if err != nil {
		return Request{}, false
	}
	return req, true
}

// SetBookID links a book request to the library row it became.
func (r *Repo) SetBookID(ctx context.Context, id, bookID int64) error {
	_, err := r.db.ExecContext(ctx, `UPDATE requests SET book_id = ? WHERE id = ?`, nullID(bookID), id)
	return err
}

// ListByBookID returns the book requests linked to one library row (oldest first).
func (r *Repo) ListByBookID(ctx context.Context, bookID int64) ([]Request, error) {
	return r.query(ctx, `SELECT `+cols+` FROM requests WHERE media_type = 'book' AND book_id = ? ORDER BY id`, bookID)
}

// SetFormats records a book request's format choice and the profile that goes with it
// (an empty profile leaves the stored one alone). A change of formats un-stamps ready_at:
// the request has to be judged ready again over what it asks for now (an audiobook added
// to a delivered ebook request is still on its way). Each format's message has its own
// inbox reference, so nobody hears about one twice when it is stamped again.
func (r *Repo) SetFormats(ctx context.Context, id int64, formats, profile string) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE requests
		    SET ready_at = CASE WHEN formats = ? THEN ready_at ELSE 0 END,
		        formats = ?,
		        quality_profile = CASE WHEN ? = '' THEN quality_profile ELSE ? END,
		        updated_at = CURRENT_TIMESTAMP
		  WHERE id = ?`,
		formats, formats, profile, profile, id)
	return err
}

// ListForBook returns the book requests for one library row, oldest first: those linked
// to it, and those not linked to any row yet that were made under one of its keys
// (keys holds every catalogue key the book has had).
func (r *Repo) ListForBook(ctx context.Context, bookID int64, keys []string) ([]Request, error) {
	q := `SELECT ` + cols + ` FROM requests WHERE media_type = 'book' AND (book_id = ?`
	args := []any{bookID}
	if len(keys) > 0 {
		q += ` OR (book_id IS NULL AND ol_key IN (?` + strings.Repeat(`, ?`, len(keys)-1) + `))`
		for _, k := range keys {
			args = append(args, k)
		}
	}
	return r.query(ctx, q+`) ORDER BY id`, args...)
}

// bookRequests returns every book request, oldest first.
func (r *Repo) bookRequests(ctx context.Context) ([]Request, error) {
	return r.query(ctx, `SELECT `+cols+` FROM requests WHERE media_type = 'book' ORDER BY id`)
}

// unlinkedBookRequests returns the book requests not yet linked to a library row.
func (r *Repo) unlinkedBookRequests(ctx context.Context) ([]Request, error) {
	return r.query(ctx, `SELECT `+cols+` FROM requests WHERE media_type = 'book' AND book_id IS NULL ORDER BY id`)
}

func (r *Repo) query(ctx context.Context, q string, args ...any) ([]Request, error) {
	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Request
	for rows.Next() {
		req, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, req)
	}
	return out, rows.Err()
}

// ListAwaitingReady returns the approved requests whose requester hasn't been told it's
// ready yet: the ready sweep's work list.
func (r *Repo) ListAwaitingReady(ctx context.Context) ([]Request, error) {
	return r.query(ctx, `SELECT `+cols+` FROM requests WHERE status = ? AND ready_at = 0 ORDER BY id`, StatusApproved)
}

// MarkReady records when the requester was told a request is ready. Only the first time
// counts: a request already stamped keeps its stamp.
func (r *Repo) MarkReady(ctx context.Context, id, at int64) error {
	_, err := r.db.ExecContext(ctx, `UPDATE requests SET ready_at = ? WHERE id = ? AND ready_at = 0`, at, id)
	return err
}

// MarkOnDisk records when a request was first seen complete while waiting for Plex. The
// first stamp stands, so the grace period counts from then however often it is asked.
// It answers the stamp in force.
func (r *Repo) MarkOnDisk(ctx context.Context, id, at int64) (int64, error) {
	if _, err := r.db.ExecContext(ctx, `UPDATE requests SET on_disk_at = ? WHERE id = ? AND on_disk_at = 0`, at, id); err != nil {
		return 0, err
	}
	var got int64
	err := r.db.QueryRowContext(ctx, `SELECT on_disk_at FROM requests WHERE id = ?`, id).Scan(&got)
	if errors.Is(err, sql.ErrNoRows) {
		return at, nil // withdrawn meanwhile: nothing left to wait for
	}
	return got, err
}

// ClearOnDisk forgets that a request was complete (its file went again before anyone was
// told), so the wait starts afresh when it is complete once more.
func (r *Repo) ClearOnDisk(ctx context.Context, id int64) error {
	_, err := r.db.ExecContext(ctx, `UPDATE requests SET on_disk_at = 0 WHERE id = ? AND ready_at = 0`, id)
	return err
}

// seasonDiskJSON is season_disk_at as a JSON object SQLite's json functions can work on
// (an empty or unreadable value is none).
const seasonDiskJSON = `(CASE WHEN json_valid(season_disk_at) THEN season_disk_at ELSE '{}' END)`

// seasonTold marks a season whose notice has gone out, so it waits for nothing more.
const seasonTold = -1

// MarkSeasonOnDisk is MarkOnDisk for one season of a request for several: the first
// stamp for that season stands. It answers every season's stamp (seasonTold for those
// already announced).
func (r *Repo) MarkSeasonOnDisk(ctx context.Context, id int64, season int, at int64) (map[int]int64, error) {
	key := fmt.Sprintf("$.s%d", season)
	if _, err := r.db.ExecContext(ctx,
		`UPDATE requests SET season_disk_at = json_set(`+seasonDiskJSON+`, ?, ?)
		  WHERE id = ? AND json_extract(`+seasonDiskJSON+`, ?) IS NULL`,
		key, at, id, key); err != nil {
		return nil, err
	}
	return r.seasonDisk(ctx, id, season, at)
}

// MarkSeasonTold records that a season's notice went out: it leaves the Plex check's list.
func (r *Repo) MarkSeasonTold(ctx context.Context, id int64, season int) error {
	_, err := r.db.ExecContext(ctx, `UPDATE requests SET season_disk_at = json_set(`+seasonDiskJSON+`, ?, ?) WHERE id = ?`,
		fmt.Sprintf("$.s%d", season), seasonTold, id)
	return err
}

// ClearSeasonOnDisk forgets a season's wait (its files went again before anyone was told).
// A season already announced keeps its mark.
func (r *Repo) ClearSeasonOnDisk(ctx context.Context, id int64, season int) error {
	key := fmt.Sprintf("$.s%d", season)
	_, err := r.db.ExecContext(ctx,
		`UPDATE requests SET season_disk_at = json_remove(`+seasonDiskJSON+`, ?)
		  WHERE id = ? AND json_extract(`+seasonDiskJSON+`, ?) > 0`, key, id, key)
	return err
}

func (r *Repo) seasonDisk(ctx context.Context, id int64, season int, at int64) (map[int]int64, error) {
	var raw string
	err := r.db.QueryRowContext(ctx, `SELECT season_disk_at FROM requests WHERE id = ?`, id).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return map[int]int64{season: at}, nil // withdrawn meanwhile
	}
	if err != nil {
		return nil, err
	}
	return decodeSeasonDisk(raw), nil
}

// decodeSeasonDisk reads season_disk_at ({"s2": unix, "s3": -1, …}); an empty or
// unreadable value is no stamps, which only means a season's wait starts now.
func decodeSeasonDisk(raw string) map[int]int64 {
	out := map[int]int64{}
	if raw == "" {
		return out
	}
	var m map[string]int64
	if json.Unmarshal([]byte(raw), &m) != nil {
		return out
	}
	for k, v := range m {
		var n int
		if _, err := fmt.Sscanf(k, "s%d", &n); err == nil && n > 0 && (v > 0 || v == seasonTold) {
			out[n] = v
		}
	}
	return out
}

// ListPlexWaiting returns the approved requests not told yet that are waiting for Plex
// (the request, or one of its seasons not yet announced, seen complete): the Plex
// check's work list.
func (r *Repo) ListPlexWaiting(ctx context.Context) ([]Request, error) {
	return r.query(ctx, `SELECT `+cols+` FROM requests
		WHERE status = ? AND ready_at = 0
		  AND (on_disk_at > 0 OR (season_disk_at != '' AND EXISTS (SELECT 1 FROM json_each(`+seasonDiskJSON+`) WHERE value > 0)))
		ORDER BY id`, StatusApproved)
}

// Decision is who decided a request, when, and (declines) what the requester is told.
type Decision struct {
	By     int64
	ByName string
	At     int64 // unix seconds
	Reason string
}

// Decide records a decision: the status, who made it and when, and the decline reason (an
// approval clears it). A non-empty profile also updates the stored quality profile; an
// empty one leaves it alone (so a decline doesn't erase the profile the requester picked).
func (r *Repo) Decide(ctx context.Context, id int64, status, profile string, d Decision) error {
	res, err := r.db.ExecContext(ctx,
		`UPDATE requests
		    SET status = ?,
		        quality_profile = CASE WHEN ? = '' THEN quality_profile ELSE ? END,
		        decided_by = ?, decided_by_name = ?, decided_at = ?, decline_reason = ?,
		        updated_at = CURRENT_TIMESTAMP
		  WHERE id = ?`,
		status, profile, profile, d.By, d.ByName, d.At, d.Reason, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// Resurrect re-opens a declined request under a new requester: status back to pending,
// requested_by swapped to the caller, their note in place of the old one, and flagged as
// asked for again (the decline reason stays, for staff, until the next decision). A
// non-empty profile replaces the stored one; empty keeps the original choice.
func (r *Repo) Resurrect(ctx context.Context, id, userID int64, userName, profile, note string) error {
	res, err := r.db.ExecContext(ctx,
		`UPDATE requests
		    SET status = ?,
		        requested_by = ?,
		        requested_by_name = ?,
		        quality_profile = CASE WHEN ? = '' THEN quality_profile ELSE ? END,
		        note = ?,
		        rerequest = rerequest + 1,
		        updated_at = CURRENT_TIMESTAMP
		  WHERE id = ? AND status = ?`,
		StatusPending, userID, userName, profile, profile, note, id, StatusDeclined)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return errNotDeclined
	}
	return nil
}

// errNotDeclined: Resurrect found the request no longer declined (someone else asked for
// it again a moment earlier) or gone.
var errNotDeclined = errors.New("request is not declined")

// Delete removes a request (and its subscriber rows).
func (r *Repo) Delete(ctx context.Context, id int64) error {
	res, err := r.db.ExecContext(ctx, `DELETE FROM requests WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	_, _ = r.db.ExecContext(ctx, `DELETE FROM request_subscribers WHERE request_id = ?`, id)
	return nil
}

// Subscriber is one extra user attached to a request (beyond the requester).
type Subscriber struct {
	UserID   int64
	UserName string
}

// AddSubscriber attaches a user to a request. Idempotent (unique request_id+user_id).
func (r *Repo) AddSubscriber(ctx context.Context, requestID, userID int64, userName string) error {
	_, err := r.db.ExecContext(ctx,
		`INSERT OR IGNORE INTO request_subscribers (request_id, user_id, user_name) VALUES (?, ?, ?)`,
		requestID, userID, userName)
	return err
}

// RemoveSubscriber detaches a user from a request (used when a subscriber becomes
// the requester on a re-request, so they aren't listed twice).
func (r *Repo) RemoveSubscriber(ctx context.Context, requestID, userID int64) error {
	_, err := r.db.ExecContext(ctx,
		`DELETE FROM request_subscribers WHERE request_id = ? AND user_id = ?`, requestID, userID)
	return err
}

// Unsubscribe detaches a user who follows a request. removed is false when they didn't
// follow it (an owner has no subscription to drop).
func (r *Repo) Unsubscribe(ctx context.Context, requestID, userID int64) (removed bool, err error) {
	res, err := r.db.ExecContext(ctx,
		`DELETE FROM request_subscribers WHERE request_id = ? AND user_id = ?`, requestID, userID)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// Follower is someone following a request, as staff see them on its detail view.
type Follower struct {
	Name string `json:"name"`
}

// MediaKeysForUser is the movies and shows a user asked for or follows, as
// "movie:<tmdb>" / "series:<tmdb>" keys. Declined requests don't count: the user was
// told no, so it isn't theirs to look forward to.
func (r *Repo) MediaKeysForUser(ctx context.Context, userID int64) (map[string]bool, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT media_type, tmdb_id FROM requests
		WHERE media_type IN ('movie', 'series') AND tmdb_id > 0 AND status <> ?
		  AND (requested_by = ? OR id IN (SELECT request_id FROM request_subscribers WHERE user_id = ?))`,
		StatusDeclined, userID, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var media string
		var tmdb int
		if err := rows.Scan(&media, &tmdb); err != nil {
			return nil, err
		}
		out[MediaKey(media, tmdb)] = true
	}
	return out, rows.Err()
}

// MediaKey is the key MediaKeysForUser returns for a title: "movie:603", "series:1399".
func MediaKey(mediaType string, tmdbID int) string {
	return fmt.Sprintf("%s:%d", mediaType, tmdbID)
}

// Subscribers lists the extra users attached to a request.
func (r *Repo) Subscribers(ctx context.Context, requestID int64) ([]Subscriber, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT user_id, user_name FROM request_subscribers WHERE request_id = ? ORDER BY created_at`, requestID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Subscriber
	for rows.Next() {
		var s Subscriber
		if err := rows.Scan(&s.UserID, &s.UserName); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}
