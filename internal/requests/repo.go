// Package requests implements the Requests module: users ask for a movie or series,
// and on approval it's handed to the Movies/Series module for acquisition.
package requests

import (
	"context"
	"database/sql"
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
	MediaType        string  `json:"media_type"`       // "movie" | "series" | "book"
	TMDBID           int     `json:"tmdb_id"`          // movies/series
	OLKey            string  `json:"ol_key,omitempty"` // books (Open Library work key)
	Title            string  `json:"title"`
	Author           string  `json:"author,omitempty"` // books
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
	CreatedAt        string  `json:"created_at"`
	UpdatedAt        string  `json:"updated_at"`
}

// Repo persists requests in SQLite.
type Repo struct{ db *sql.DB }

// NewRepo builds a repository over the given pool.
func NewRepo(db *sql.DB) *Repo { return &Repo{db: db} }

const cols = `id, media_type, tmdb_id, ol_key, title, author, year, poster_url, overview, status,
	quality_profile, requested_by, requested_by_name, note, created_at, updated_at`

func scan(row interface{ Scan(...any) error }) (Request, error) {
	var r Request
	err := row.Scan(&r.ID, &r.MediaType, &r.TMDBID, &r.OLKey, &r.Title, &r.Author, &r.Year, &r.PosterURL, &r.Overview,
		&r.Status, &r.QualityProfile, &r.RequestedBy, &r.RequestedByName, &r.Note, &r.CreatedAt, &r.UpdatedAt)
	return r, err
}

// Create inserts a request. Returns ErrExists (wrapped) on a duplicate media.
func (r *Repo) Create(ctx context.Context, req Request) (Request, error) {
	res, err := r.db.ExecContext(ctx,
		`INSERT INTO requests (media_type, tmdb_id, ol_key, title, author, year, poster_url, overview, status,
			quality_profile, requested_by, requested_by_name, note)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		req.MediaType, req.TMDBID, req.OLKey, req.Title, req.Author, req.Year, req.PosterURL, req.Overview, req.Status,
		req.QualityProfile, req.RequestedBy, req.RequestedByName, req.Note)
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

// GetByMedia returns an existing request for the given movie/series, if any.
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
