package insights

import (
	"context"

	"github.com/tristenlammi/arrmada/internal/plex"
)

// PlexClient is a client for the Plex server as configured right now, for the other
// modules that talk to it (the library scanner). It's built fresh each call, so a changed
// URL or token applies at once.
func (s *Service) PlexClient(ctx context.Context) *plex.Client {
	return s.client(ctx)
}

// Configured reports whether a Plex server URL and token are saved. Monitoring being
// switched off doesn't matter here: scans and links work without it.
func (s *Service) Configured(ctx context.Context) bool {
	return s.configured(ctx)
}
