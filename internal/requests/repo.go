// Package requests implements the Requests module: users ask for a movie or series,
// and on approval it's handed to the Movies/Series module for acquisition.
package requests

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
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
	Tracking  *Tracking `json:"tracking,omitempty"`
	CreatedAt string    `json:"created_at"`
	UpdatedAt string    `json:"updated_at"`

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
	// Books: searches in a row that found nothing, and when the next one is due (RFC3339).
	searchMisses int
	nextCheckAt  string
	// Books: for a "both" request with one format here, which one and what's coming.
	partNote string
}

// Repo persists requests in SQLite.
type Repo struct{ db *sql.DB }

// NewRepo builds a repository over the given pool.
func NewRepo(db *sql.DB) *Repo { return &Repo{db: db} }

const cols = `id, media_type, tmdb_id, ol_key, title, author, year, poster_url, overview, status,
	quality_profile, requested_by, requested_by_name, note, created_at, updated_at, book_id, formats, seasons`

func scan(row interface{ Scan(...any) error }) (Request, error) {
	var r Request
	var bookID sql.NullInt64
	var seasons string
	err := row.Scan(&r.ID, &r.MediaType, &r.TMDBID, &r.OLKey, &r.Title, &r.Author, &r.Year, &r.PosterURL, &r.Overview,
		&r.Status, &r.QualityProfile, &r.RequestedBy, &r.RequestedByName, &r.Note, &r.CreatedAt, &r.UpdatedAt, &bookID, &r.Formats, &seasons)
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
			quality_profile, requested_by, requested_by_name, note, book_id, formats, seasons)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		req.MediaType, req.TMDBID, req.OLKey, req.Title, req.Author, req.Year, req.PosterURL, req.Overview, req.Status,
		req.QualityProfile, req.RequestedBy, req.RequestedByName, req.Note, nullID(req.BookID), req.Formats, encodeSeasons(req.Seasons))
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
// (an empty profile leaves the stored one alone).
func (r *Repo) SetFormats(ctx context.Context, id int64, formats, profile string) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE requests
		    SET formats = ?,
		        quality_profile = CASE WHEN ? = '' THEN quality_profile ELSE ? END,
		        updated_at = CURRENT_TIMESTAMP
		  WHERE id = ?`,
		formats, profile, profile, id)
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

// List returns requests (newest first), optionally filtered by status and/or the
// requesting user (requestedBy = 0 means all users).
func (r *Repo) List(ctx context.Context, status string, requestedBy int64) ([]Request, error) {
	q := `SELECT ` + cols + ` FROM requests`
	var where []string
	var args []any
	if status != "" {
		where = append(where, "status = ?")
		args = append(args, status)
	}
	if requestedBy != 0 {
		where = append(where, "requested_by = ?")
		args = append(args, requestedBy)
	}
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	q += ` ORDER BY id DESC`
	return r.query(ctx, q, args...)
}

// SetStatus updates a request's status. A non-empty profile also updates the
// stored quality profile; an empty profile leaves it alone (so a decline doesn't
// erase the profile the requester picked).
func (r *Repo) SetStatus(ctx context.Context, id int64, status, profile string) error {
	res, err := r.db.ExecContext(ctx,
		`UPDATE requests
		    SET status = ?,
		        quality_profile = CASE WHEN ? = '' THEN quality_profile ELSE ? END,
		        updated_at = CURRENT_TIMESTAMP
		  WHERE id = ?`,
		status, profile, profile, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// Resurrect re-opens a declined request under a new requester: status back to
// pending, requested_by swapped to the caller. A non-empty profile replaces the
// stored one; empty keeps the original choice.
func (r *Repo) Resurrect(ctx context.Context, id, userID int64, userName, profile string) error {
	res, err := r.db.ExecContext(ctx,
		`UPDATE requests
		    SET status = ?,
		        requested_by = ?,
		        requested_by_name = ?,
		        quality_profile = CASE WHEN ? = '' THEN quality_profile ELSE ? END,
		        updated_at = CURRENT_TIMESTAMP
		  WHERE id = ?`,
		StatusPending, userID, userName, profile, profile, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

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
