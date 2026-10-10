package main

import (
	"context"
	"time"

	"github.com/tristenlammi/arrmada/internal/insights"
	"github.com/tristenlammi/arrmada/internal/requests"
)

// plexReadyLocator is the Plex library index (insights) as the Requests module asks it
// "is this title in Plex yet?" before saying a request is ready. Memory only: it never
// calls Plex.
type plexReadyLocator struct{ insights *insights.Service }

var _ requests.PlexLocator = plexReadyLocator{}

func (p plexReadyLocator) Configured(ctx context.Context) bool { return p.insights.Configured(ctx) }

func (p plexReadyLocator) Find(ctx context.Context, media string, ids requests.PlexIDs) (string, bool) {
	x := insights.ExternalIDs{TMDB: ids.TMDB, TVDB: ids.TVDB, IMDB: ids.IMDB}
	if _, ok := p.insights.Locate(ctx, media, x); !ok {
		return "", false
	}
	return p.insights.WatchURL(ctx, media, x), true
}

func (p plexReadyLocator) IndexBuiltAt() time.Time { return p.insights.PlexIndexBuiltAt() }

func (p plexReadyLocator) Refresh(after time.Duration) { p.insights.PlexIndexStale(after) }
