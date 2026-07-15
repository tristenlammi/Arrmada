// Package indexer is Arrmada's search backbone (the Prowlarr-class layer). It
// supports the standard protocols — Torznab (torrent) and Newznab (usenet) — and
// native private-tracker integrations (e.g. TorrentLeech), all behind one
// Searcher interface so every media module searches through one aggregated API.
package indexer

import "context"

// Kind is the indexer implementation. torznab/newznab speak the standard
// protocols; other kinds (e.g. torrentleech) are native site integrations.
type Kind string

const (
	KindTorznab      Kind = "torznab"      // standard torrent protocol
	KindNewznab      Kind = "newznab"      // standard usenet protocol
	KindTorrentLeech Kind = "torrentleech" // native TorrentLeech integration
	KindX1337        Kind = "1337x"        // native 1337x (public) integration
	KindMAM          Kind = "myanonamouse" // native MyAnonaMouse integration (books)
)

// Transport is the download transport an indexer's releases use.
type Transport string

const (
	TransportTorrent Transport = "torrent"
	TransportUsenet  Transport = "usenet"
)

// Indexer is a configured search source.
type Indexer struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	Kind Kind   `json:"kind"`

	// Protocol endpoints (torznab/newznab).
	URL    string `json:"url,omitempty"`
	APIKey string `json:"-"` // secret

	// Credentials for native login-based trackers (e.g. TorrentLeech).
	Username string `json:"username,omitempty"`
	Password string `json:"-"` // secret

	Categories []int   `json:"categories,omitempty"`
	// MediaTypes scopes which searches use this indexer (movie | series | book | music).
	// Empty = used for everything (backward compatible).
	MediaTypes  []string `json:"media_types,omitempty"`
	Priority    int      `json:"priority"`     // 1 (highest) .. 50 (lowest)
	MinSeeders  int      `json:"min_seeders"`  // drop torrent results below this (0 = off)
	SeedEnabled bool     `json:"seed_enabled"` // false = delete once imported (no seeding)
	SeedRatio   float64  `json:"seed_ratio"`   // remove after this ratio (0 = no target)
	SeedHours   int      `json:"seed_hours"`   // remove after this many hours (0 = no limit)
	Enabled     bool     `json:"enabled"`
}

// Media types an indexer can be scoped to.
const (
	MediaMovie  = "movie"
	MediaSeries = "series"
	MediaBook   = "book"
	MediaMusic  = "music"
)

// Serves reports whether this indexer should be queried for the given media type. An indexer with
// no media types set serves everything; an empty query type also matches (e.g. a manual test search).
func (i Indexer) Serves(mediaType string) bool {
	if mediaType == "" || len(i.MediaTypes) == 0 {
		return true
	}
	for _, t := range i.MediaTypes {
		if t == mediaType {
			return true
		}
	}
	return false
}

// Transport reports whether this indexer yields torrents or usenet.
func (i Indexer) Transport() Transport {
	if i.Kind == KindNewznab {
		return TransportUsenet
	}
	return TransportTorrent
}

// SearchQuery is a normalized search request across indexers.
type SearchQuery struct {
	Text       string
	Categories []int
	MediaType  string // movie | series | book | music — restricts to indexers that serve it
	Limit      int
}

// Recenter is an optional capability: fetch the newest releases (an RSS-style
// feed) with no search term. RSS sync polls this to catch new uploads promptly
// and gently, instead of firing a title search per wanted movie.
type Recenter interface {
	Recent(ctx context.Context, idx Indexer, limit int) ([]Release, error)
}
