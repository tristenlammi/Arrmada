package movies

import (
	"context"
	"fmt"

	"github.com/tristenlammi/arrmada/internal/outbox"
	"github.com/tristenlammi/arrmada/internal/store"
)

// Every change to a movie's files is announced from here, the service, so it doesn't
// matter which path made it (the import sweep, a manual import, Review, a rename, a
// delete, a rescan). Each change goes out two ways:
//
//   - an outbox row (movie.imported / movie.changed), written in the same transaction as
//     the change where there is one, for work that must happen: Convert's and Subtitles'
//     indexes, the requester's "ready" message. A crash or a busy moment can't lose it.
//   - a bus event (movie.downloaded / movie.renamed / movie.file_deleted / movie.deleted)
//     carrying the affected paths, for the UI and admin alerts. 'id' stays an int64.

// SetOutbox installs where the service writes durable side effects (nil = none, which
// tests without consumers use).
func (s *Service) SetOutbox(o outbox.Enqueuer) { s.outbox = o }

// movieKey collapses repeats for one movie while a row waits: handlers re-read the movie,
// so the newest row says everything the older ones did.
func movieKey(id int64) string { return fmt.Sprintf("movie:%d", id) }

// enqueue writes an outbox row through q (the caller's transaction, or the pool).
func (s *Service) enqueue(ctx context.Context, q store.Execer, topic string, payload any, key string) error {
	if s.outbox == nil {
		return nil
	}
	return s.outbox.Enqueue(ctx, q, topic, payload, key)
}

// enqueueChange writes a movie.changed row outside any transaction, for the paths whose
// writes aren't transactional (rescan, Convert's repoint, a half-finished delete). A
// failure is logged: the nightly Convert sweep and the 6-hourly Subtitles pass still
// catch up.
func (s *Service) enqueueChange(ctx context.Context, ch outbox.MovieChanged) {
	if err := s.enqueue(ctx, s.repo.db, outbox.TopicMovieChanged, ch, movieKey(ch.MovieID)); err != nil {
		s.log.Warn("movies: couldn't queue the index update for a file change — the nightly sweep will catch it",
			"movie_id", ch.MovieID, "change", ch.Change, "err", err)
	}
}

// publish sends a bus event for the UI and admin alerts.
func (s *Service) publish(topic string, data map[string]any) {
	if s.bus != nil {
		s.bus.Publish(topic, data)
	}
}

// setDefaultFileIn is setDefaultFile inside a transaction, with the media info read
// beforehand (mediaJSONOf; "" clears the cache, so an old file's facts never describe the
// new one — the detail page reads it again in the background).
func setDefaultFileIn(ctx context.Context, r *Repo, id int64, path, media string) error {
	if err := r.SetFile(ctx, id, path); err != nil {
		return err
	}
	return r.SetMediaInfo(ctx, id, media)
}
