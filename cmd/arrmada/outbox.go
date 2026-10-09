package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/tristenlammi/arrmada/internal/audioserver"
	"github.com/tristenlammi/arrmada/internal/convert"
	"github.com/tristenlammi/arrmada/internal/outbox"
	"github.com/tristenlammi/arrmada/internal/requests"
	"github.com/tristenlammi/arrmada/internal/safego"
	"github.com/tristenlammi/arrmada/internal/series"
	"github.com/tristenlammi/arrmada/internal/subtitles"
)

// importConsumers is everything that acts on an import, as outbox consumers. Each runs
// until it succeeds, so each must be safe to run twice: Convert upserts its index,
// Subtitles dedupes its ensure-job per file, the inbox's unique ref stops a second
// "ready", and dropping a cache twice costs a re-read.
type importConsumers struct {
	convert   *convert.Service
	subtitles *subtitles.Service
	requests  *requests.Service
	audio     *audioserver.Server
	grp       *safego.Group // background work a handler starts (the audiobook warm-up)
}

// register adds every consumer. It must run before anything can import — the scheduler's
// sweeps, the HTTP manual-import routes — because Enqueue writes rows only for the
// consumers registered at that moment.
//
// Within a topic the requester's "ready" is registered first: one import's rows are
// written in registration order and run oldest first, one at a time, so the message
// isn't held behind Convert probing a file on a sleeping array.
func (c importConsumers) register(box *outbox.Outbox) {
	box.Register(outbox.TopicMovieImported, "requests.ready", decode(func(ctx context.Context, p outbox.MovieImported) error {
		return c.requests.NotifyMovieReady(ctx, p.MovieID)
	}))
	box.Register(outbox.TopicMovieImported, "convert", decode(func(ctx context.Context, p outbox.MovieImported) error {
		return c.convert.IndexMovie(ctx, p.MovieID) // a movie deleted since is simply forgotten
	}))
	box.Register(outbox.TopicMovieImported, "subtitles", decode(func(ctx context.Context, p outbox.MovieImported) error {
		c.subtitles.OnMovieImported(ctx, p.MovieID)
		return nil
	}))

	// Renames and deletes: both indexes follow the movie's current record — a new path
	// replaces the old one, a movie with no file (or no longer in the library) drops out.
	box.Register(outbox.TopicMovieChanged, "convert", decode(func(ctx context.Context, p outbox.MovieChanged) error {
		return c.convert.IndexMovie(ctx, p.MovieID)
	}))
	box.Register(outbox.TopicMovieChanged, "subtitles", decode(func(ctx context.Context, p outbox.MovieChanged) error {
		return c.subtitles.OnMovieChanged(ctx, p.MovieID)
	}))

	box.Register(outbox.TopicSeriesImported, "requests.ready", decode(func(ctx context.Context, p outbox.SeriesImported) error {
		return c.requests.NotifySeriesReady(ctx, p.SeriesID)
	}))
	box.Register(outbox.TopicSeriesImported, "convert", decode(func(ctx context.Context, p outbox.SeriesImported) error {
		return gone(c.convert.IndexSeries(ctx, p.SeriesID), series.ErrNotFound)
	}))
	box.Register(outbox.TopicSeriesImported, "subtitles", decode(func(ctx context.Context, p outbox.SeriesImported) error {
		eps := make([]series.EpisodeRef, 0, len(p.Episodes))
		for _, e := range p.Episodes {
			eps = append(eps, series.EpisodeRef{Season: e.Season, Episode: e.Episode})
		}
		c.subtitles.OnSeriesImported(ctx, p.SeriesID, eps)
		return nil
	}))

	box.Register(outbox.TopicBookImported, "requests.ready", decode(func(ctx context.Context, p outbox.BookImported) error {
		return c.requests.NotifyBookReady(ctx, p.BookID)
	}))
	box.Register(outbox.TopicBookImported, "audioserver.cache", decode(func(ctx context.Context, _ outbox.BookImported) error {
		// The warm-up probes files and can take a while; it runs in the run group with
		// the app's own context, not this handler's, which ends when the handler returns.
		c.audio.OnBookImported(ctx, func(fn func(ctx context.Context)) {
			c.grp.Go("audiobook server: warm-up", fn)
		})
		return nil
	}))
}

// decode adapts a typed handler to the outbox's JSON one.
func decode[T any](h func(ctx context.Context, p T) error) outbox.Handler {
	return func(ctx context.Context, raw json.RawMessage) error {
		var p T
		if err := json.Unmarshal(raw, &p); err != nil {
			return fmt.Errorf("decode payload: %w", err)
		}
		return h(ctx, p)
	}
}

// gone treats "the item no longer exists" as done: it was deleted after the import, and
// there is nothing left to index. Retrying it twenty times would only fill the log.
func gone(err, notFound error) error {
	if errors.Is(err, notFound) {
		return nil
	}
	return err
}
