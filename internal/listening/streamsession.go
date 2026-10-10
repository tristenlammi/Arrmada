package listening

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// StreamSession looks up an open play session by its id alone. Audiobookshelf 2.22+
// apps (the official app among them) stream from /public/session/{id}/track/{n} without
// a token: the session id — a random UUID handed only to the device that started
// playback — is the key. So only a session that's still open (not closed, not an
// offline upload) and was used within maxIdle counts; anything else is "not found".
func (s *Store) StreamSession(ctx context.Context, id string, maxIdle time.Duration) (Session, error) {
	var sess Session
	err := s.db.QueryRowContext(ctx,
		`SELECT id, user_id, item_key, last_at FROM listen_sessions
		 WHERE id = ? AND closed = 0 AND offline = 0 AND last_at >= ?`,
		id, s.now().Add(-maxIdle).UnixMilli()).Scan(&sess.ID, &sess.UserID, &sess.ItemKey, &sess.LastAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Session{}, ErrSessionNotFound
	}
	return sess, err
}
