package indexer

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/tristenlammi/arrmada/internal/flaresolverr"
)

// Searcher is one indexer implementation: it knows how to search a particular
// kind of source and verify its configuration.
type Searcher interface {
	Search(ctx context.Context, idx Indexer, q SearchQuery) ([]Release, error)
	Test(ctx context.Context, idx Indexer) error
}

// FetchResult is what a Fetcher resolves a search result's download link into:
// either the .torrent file bytes, or a URL/magnet to hand to the download client.
type FetchResult struct {
	File     []byte
	Filename string
	URL      string // magnet: or a direct .torrent/.nzb URL
}

// Fetcher is optionally implemented by searchers whose download links need work
// before a client can use them — auth-gated .torrents (TorrentLeech) or a detail
// page that must be scraped for a magnet (1337x).
type Fetcher interface {
	Fetch(ctx context.Context, idx Indexer, downloadURL string) (FetchResult, error)
}

// Registry maps indexer kinds to their Searcher implementation.
type Registry struct {
	searchers map[Kind]Searcher
	mam       *MAMSearcher
}

// NewRegistry wires the built-in searchers. Torznab and Newznab share one
// protocol client; native trackers register their own. fs (FlareSolverr) may be
// nil.
func NewRegistry(fs *flaresolverr.Client) *Registry {
	tn := NewTorznabSearcher()
	r := &Registry{searchers: map[Kind]Searcher{
		KindTorznab: tn,
		KindNewznab: tn,
	}}
	r.searchers[KindTorrentLeech] = NewTorrentLeechSearcher(fs)
	r.searchers[KindX1337] = NewX1337Searcher(fs)
	r.mam = NewMAMSearcher(nil)
	r.searchers[KindMAM] = r.mam
	return r
}

// SetLogger attaches a logger to the searchers that support request tracing.
func (r *Registry) SetLogger(l *slog.Logger) {
	if tn, ok := r.searchers[KindTorznab].(*TorznabSearcher); ok {
		tn.SetLogger(l)
	}
}

// SetSessionPersister wires the callback used to save a rotated MyAnonaMouse
// mam_id back onto its indexer record.
func (r *Registry) SetSessionPersister(persist func(id int64, session string)) {
	if r.mam != nil {
		r.mam.persist = persist
	}
}

// For returns the searcher for a kind.
func (r *Registry) For(kind Kind) (Searcher, error) {
	s, ok := r.searchers[kind]
	if !ok {
		return nil, fmt.Errorf("no searcher for indexer kind %q", kind)
	}
	return s, nil
}
