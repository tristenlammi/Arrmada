package automation

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"testing"

	"github.com/tristenlammi/arrmada/internal/download"
	"github.com/tristenlammi/arrmada/internal/eventbus"
	"github.com/tristenlammi/arrmada/internal/indexer"
	"github.com/tristenlammi/arrmada/internal/library"
	"github.com/tristenlammi/arrmada/internal/metadata"
	"github.com/tristenlammi/arrmada/internal/music"
	"github.com/tristenlammi/arrmada/internal/quality"
	"github.com/tristenlammi/arrmada/internal/store"
)

// fakeMusicProvider stands in for MusicBrainz with canned artists, albums and track
// listings. An album missing from tracks has no listing, like a pre-announced record.
type fakeMusicProvider struct {
	mu      sync.Mutex
	artists map[string]metadata.ArtistDetails // artist MBID → details
	albums  map[string][]metadata.AlbumResult // artist MBID → release groups
	tracks  map[string][]metadata.TrackResult // release-group MBID → listing
	listing int                               // AlbumTracks calls made
}

func (f *fakeMusicProvider) Available() bool { return true }

func (f *fakeMusicProvider) SearchArtists(context.Context, string) ([]metadata.ArtistResult, error) {
	return nil, nil
}

func (f *fakeMusicProvider) GetArtist(_ context.Context, mbid string) (*metadata.ArtistDetails, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	d, ok := f.artists[mbid]
	if !ok {
		return nil, fmt.Errorf("no artist %s", mbid)
	}
	return &d, nil
}

func (f *fakeMusicProvider) ArtistAlbums(_ context.Context, mbid string) ([]metadata.AlbumResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.albums[mbid], nil
}

func (f *fakeMusicProvider) AlbumTracks(_ context.Context, rg string) ([]metadata.TrackResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.listing++
	return f.tracks[rg], nil
}

// musicHarness is a coordinator wired for the music path over a temp DB. The indexers and
// the download client are replaced by the musicSearchFn/musicQueueFn/musicGrabFn seams;
// searches are counted and recorded by query text.
type musicHarness struct {
	c        *Coordinator
	svc      *music.Service
	st       *store.Store
	fake     *fakeMusicProvider
	ctx      context.Context
	queries  []string
	releases func(q indexer.SearchQuery) (indexer.SearchResult, error) // nil = nothing found
	grabs    []string
}

func musicTestCoord(t *testing.T) *musicHarness {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	fake := &fakeMusicProvider{
		artists: map[string]metadata.ArtistDetails{},
		albums:  map[string][]metadata.AlbumResult{},
		tracks:  map[string][]metadata.TrackResult{},
	}
	svc := music.NewService(st.DB(), fake, slog.Default())
	c := &Coordinator{
		music: svc, quality: quality.NewService(st.DB()), db: st.DB(), log: slog.Default(),
		bus:      eventbus.New(slog.Default()),
		indexers: indexer.NewService(st.DB(), slog.Default(), nil), // empty: seed rules fall back
		imp:      library.NewImporter(t.TempDir(), slog.Default()),
		// Real free space of a temp dir, so only an absurdly large release trips the guard.
		downloadsDir: t.TempDir(),
	}
	h := &musicHarness{c: c, svc: svc, st: st, fake: fake, ctx: context.Background()}
	c.musicSearchFn = func(_ context.Context, q indexer.SearchQuery) (indexer.SearchResult, error) {
		h.queries = append(h.queries, q.Text)
		if h.releases == nil {
			return indexer.SearchResult{}, nil
		}
		return h.releases(q)
	}
	c.musicQueueFn = func(context.Context) ([]download.Item, error) { return nil, nil }
	c.musicGrabFn = func(_ context.Context, _, _, title, _ string) (string, error) {
		h.grabs = append(h.grabs, title)
		return fmt.Sprintf("%040d", len(h.grabs)), nil
	}
	return h
}

// addArtist adds a monitored artist whose albums are titled as given. Every album gets a
// two-track listing unless its title is in noListing. date, when set, is every album's
// release date.
func (h *musicHarness) addArtist(t *testing.T, name, profile string, titles []string, noListing map[string]bool, date string) music.Artist {
	t.Helper()
	mbid := "artist-" + name
	h.fake.mu.Lock()
	h.fake.artists[mbid] = metadata.ArtistDetails{ArtistResult: metadata.ArtistResult{MBID: mbid, Name: name}}
	var rgs []metadata.AlbumResult
	for i, title := range titles {
		rg := fmt.Sprintf("%s-rg-%d", mbid, i)
		rgs = append(rgs, metadata.AlbumResult{MBID: rg, Title: title, Type: "Album", ReleaseDate: date})
		if !noListing[title] {
			h.fake.tracks[rg] = []metadata.TrackResult{
				{Title: "One", Disc: 1, Number: 1}, {Title: "Two", Disc: 1, Number: 2},
			}
		}
	}
	h.fake.albums[mbid] = rgs
	h.fake.mu.Unlock()
	a, err := h.svc.AddArtist(h.ctx, mbid, profile, true)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// album reads one album's stored row (with its backoff state) by title.
func (h *musicHarness) album(t *testing.T, artistID int64, title string) music.Album {
	t.Helper()
	albums, err := h.svc.Albums(h.ctx, artistID)
	if err != nil {
		t.Fatal(err)
	}
	for _, al := range albums {
		if al.Title == title {
			return al
		}
	}
	t.Fatalf("no album %q", title)
	return music.Album{}
}

// rel is a release on the "Test" indexer.
func rel(title string) indexer.Release {
	return indexer.Release{Title: title, Indexer: "Test", DownloadURL: "magnet:?xt=" + title, SizeBytes: 300 << 20, Seeders: 10}
}

// found answers every search with the given releases.
func found(rels ...indexer.Release) func(indexer.SearchQuery) (indexer.SearchResult, error) {
	return func(indexer.SearchQuery) (indexer.SearchResult, error) {
		return indexer.SearchResult{Releases: rels}, nil
	}
}
