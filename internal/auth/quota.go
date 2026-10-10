package auth

import (
	"context"
	"database/sql"
	"errors"
)

// UserQuota is one person's own request limits: -1 follows the global limit, 0 is
// unlimited, n is n per window. The global limits and the window live in settings.
type UserQuota struct {
	Movies  int `json:"movies"`
	Seasons int `json:"seasons"`
	Books   int `json:"books"`
}

// DefaultQuota follows the global limits for everything.
var DefaultQuota = UserQuota{Movies: -1, Seasons: -1, Books: -1}

// Quota reads a user's own limits. A user without a row (the local-dev bypass) follows
// the global ones.
func (s *Service) Quota(ctx context.Context, id int64) (UserQuota, error) {
	var q UserQuota
	err := s.db.QueryRowContext(ctx, `SELECT quota_movies, quota_seasons, quota_books FROM users WHERE id = ?`, id).
		Scan(&q.Movies, &q.Seasons, &q.Books)
	if errors.Is(err, sql.ErrNoRows) {
		return DefaultQuota, nil
	}
	return q, err
}

// SetQuota changes a user's own limits (anything below -1 is read as -1).
func (s *Service) SetQuota(ctx context.Context, id int64, q UserQuota) error {
	res, err := s.db.ExecContext(ctx, `UPDATE users SET quota_movies = ?, quota_seasons = ?, quota_books = ? WHERE id = ?`,
		max(q.Movies, -1), max(q.Seasons, -1), max(q.Books, -1), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// Quotas reads every user's own limits, by id (for the users list).
func (s *Service) Quotas(ctx context.Context) (map[int64]UserQuota, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, quota_movies, quota_seasons, quota_books FROM users`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]UserQuota{}
	for rows.Next() {
		var id int64
		var q UserQuota
		if err := rows.Scan(&id, &q.Movies, &q.Seasons, &q.Books); err != nil {
			return nil, err
		}
		out[id] = q
	}
	return out, rows.Err()
}
