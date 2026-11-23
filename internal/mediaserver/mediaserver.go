// Package mediaserver is what Arrmada's features need from "a media server" without caring
// which one: the server kinds, the ids a title is known by, an indexed title, a live
// stream, a library, the Server interface every connected server answers, and a Registry
// over all of them. Types only — nothing here calls a server; internal/insights (Plex)
// and internal/jellyfinsvc (Jellyfin) implement Server.
package mediaserver

import (
	"context"
	"sync/atomic"
	"time"
)

// Kind names a media server product.
type Kind string

// The servers Arrmada can connect to, in the order they're listed and asked.
const (
	Plex     Kind = "plex"
	Jellyfin Kind = "jellyfin"
)

// Label is the product name as people write it. The Jellyfin slot names itself through
// SetJellyfinLabel ("Emby" when it's connected to an Emby server).
func (k Kind) Label() string {
	switch k {
	case Plex, "":
		return "Plex"
	case Jellyfin:
		if fn := jellyfinLabel.Load(); fn != nil {
			if v := (*fn)(); v != "" {
				return v
			}
		}
		return "Jellyfin"
	}
	return string(k)
}

var jellyfinLabel atomic.Pointer[func() string]

// SetJellyfinLabel sets where the Jellyfin slot's name comes from: the Jellyfin connection
// says "Emby" when the server it's set up for is Emby (Emby uses the Jellyfin slot: its
// settings, recorded plays and linked accounts). nil, or a func answering "", means
// "Jellyfin". Call it once at startup.
func SetJellyfinLabel(fn func() string) {
	if fn == nil {
		jellyfinLabel.Store(nil)
		return
	}
	jellyfinLabel.Store(&fn)
}

// order is where a kind sits in every list: Plex first, then Jellyfin.
func (k Kind) order() int {
	switch k {
	case Plex:
		return 0
	case Jellyfin:
		return 1
	}
	return 2
}

// ExternalIDs are what a title is known by outside Arrmada. Zero / "" = unknown.
type ExternalIDs struct {
	TMDB int
	TVDB int
	IMDB string
	// Folder is the title's folder as Arrmada sees it (a movie's folder, a show's folder).
	// A server that matched no outside id (Jellyfin lists each file of an ungrouped
	// multi-version folder as its own movie, with no ids) can still be found by it.
	// Servers that only index by id ignore it.
	Folder string
}

// Item is one title in a server's library index.
type Item struct {
	Server Kind
	// ID is the server's own key for the title: Plex's rating key, Jellyfin's item id.
	ID      string
	Media   string // movie | series
	Title   string
	Year    int
	IDs     ExternalIDs
	AddedAt time.Time
}

// RecentTMDB is one title a server added recently, by TMDB id: what Discover's "Recently
// added" row can show without saying anything about the server itself (no library names,
// paths or users).
type RecentTMDB struct {
	Media   string // movie | series
	TMDB    int
	AddedAt int64 // unix seconds
}

// Library is one library (Plex section, Jellyfin virtual folder) and its folders.
type Library struct {
	Key   string `json:"key"`
	Title string `json:"title"`
	Type  string `json:"type"` // movie | show | artist | photo
	Count int64  `json:"-"`    // filled by SectionTotal on demand
	// Locations are the library's folders as the server sees them (its own paths, which
	// usually differ from Arrmada's container paths). Partial scans name a folder under
	// one of these.
	Locations []string `json:"locations,omitempty"`
}

// Server is one connected media server as the rest of Arrmada uses it. Every lookup
// answers from memory (an index the server keeps fresh in the background), so a slow or
// down server never holds up a page.
type Server interface {
	Kind() Kind
	// Name is the server's own name ("Basement"), "" when it hasn't said yet.
	Name(ctx context.Context) string
	// Configured: an address and a credential are saved.
	Configured(ctx context.Context) bool
	// IndexReady: Configured and the library index has been built at least once.
	IndexReady(ctx context.Context) bool
	// IndexBuiltAt is the last successful index build (zero = never since start).
	IndexBuiltAt() time.Time
	// IndexStale asks for a rebuild after the delay (the server needs a moment to add
	// what it was just told about). A burst of asks costs one rebuild.
	IndexStale(after time.Duration)
	// OnIndexRebuilt registers fn to run after each successful rebuild.
	OnIndexRebuilt(fn func())
	// Locate finds a title (media "movie" or "series") in the index.
	Locate(ctx context.Context, media string, ids ExternalIDs) (Item, bool)
	// WatchURL is the link that opens the title in the server's own app, "" when the
	// server doesn't have it (yet). It never carries a credential.
	WatchURL(ctx context.Context, media string, ids ExternalIDs) string
	// RecentlyAddedTMDB is what the server added lately, by TMDB id, newest first.
	RecentlyAddedTMDB(ctx context.Context, limit int) ([]RecentTMDB, error)
}

// WatchLink is one server's link to a title.
type WatchLink struct {
	Server Kind   `json:"server"`
	Label  string `json:"label"` // "Plex", "Jellyfin"
	URL    string `json:"url"`
}

// Home is the server a person watches on, from the accounts they've linked: Jellyfin for
// someone linked to Jellyfin and not to Plex, Plex for everyone else (Plex stays first
// when they're linked to both, or to neither).
func Home(plexLinked, jellyfinLinked bool) Kind {
	if jellyfinLinked && !plexLinked {
		return Jellyfin
	}
	return Plex
}

// LeadWith is links with home's link first and the rest in their order, so each person
// sees their own server's Watch button first. links is not changed.
func LeadWith(links []WatchLink, home Kind) []WatchLink {
	if len(links) < 2 {
		return links
	}
	out := make([]WatchLink, 0, len(links))
	for _, l := range links {
		if l.Server == home {
			out = append(out, l)
		}
	}
	for _, l := range links {
		if l.Server != home {
			out = append(out, l)
		}
	}
	return out
}

// Registry is every media server Arrmada knows how to use, Plex first, then Jellyfin.
// A server that isn't set up is still in it; the methods skip it. The zero value and a
// nil *Registry are empty.
type Registry struct {
	servers []Server
}

// NewRegistry builds a registry over the given servers (nil ones are skipped), ordered
// Plex first, then Jellyfin, whatever order they're passed in.
func NewRegistry(servers ...Server) *Registry {
	r := &Registry{}
	for _, s := range servers {
		if s != nil {
			r.servers = append(r.servers, s)
		}
	}
	// A stable insertion sort: there are two of them.
	for i := 1; i < len(r.servers); i++ {
		for j := i; j > 0 && r.servers[j].Kind().order() < r.servers[j-1].Kind().order(); j-- {
			r.servers[j], r.servers[j-1] = r.servers[j-1], r.servers[j]
		}
	}
	return r
}

// Servers lists every server, configured or not, in order.
func (r *Registry) Servers() []Server {
	if r == nil {
		return nil
	}
	return append([]Server(nil), r.servers...)
}

// Get is the server of one kind (nil when there's none).
func (r *Registry) Get(k Kind) Server {
	if r == nil {
		return nil
	}
	for _, s := range r.servers {
		if s.Kind() == k {
			return s
		}
	}
	return nil
}

// ConfiguredServers lists the servers that are set up, in order.
func (r *Registry) ConfiguredServers(ctx context.Context) []Server {
	if r == nil {
		return nil
	}
	var out []Server
	for _, s := range r.servers {
		if s.Configured(ctx) {
			out = append(out, s)
		}
	}
	return out
}

// Configured reports whether any server is set up.
func (r *Registry) Configured(ctx context.Context) bool {
	return len(r.ConfiguredServers(ctx)) > 0
}

// IndexReady reports whether any set-up server's index can answer.
func (r *Registry) IndexReady(ctx context.Context) bool {
	for _, s := range r.ConfiguredServers(ctx) {
		if s.IndexReady(ctx) {
			return true
		}
	}
	return false
}

// LocateAny finds a title on the first server (in order) that has it.
func (r *Registry) LocateAny(ctx context.Context, media string, ids ExternalIDs) (Item, bool) {
	for _, s := range r.ConfiguredServers(ctx) {
		if it, ok := s.Locate(ctx, media, ids); ok {
			return it, true
		}
	}
	return Item{}, false
}

// WatchLinks is a link per server that has the title, in order. None when no server has
// it.
func (r *Registry) WatchLinks(ctx context.Context, media string, ids ExternalIDs) []WatchLink {
	var out []WatchLink
	for _, s := range r.ConfiguredServers(ctx) {
		if u := s.WatchURL(ctx, media, ids); u != "" {
			out = append(out, WatchLink{Server: s.Kind(), Label: s.Kind().Label(), URL: u})
		}
	}
	return out
}

// IndexBuiltAt is the oldest index among the set-up servers — the moment from which every
// connected server's answer is current — and zero when any of them hasn't built one yet
// (or none is set up).
func (r *Registry) IndexBuiltAt(ctx context.Context) time.Time {
	var oldest time.Time
	for i, s := range r.ConfiguredServers(ctx) {
		at := s.IndexBuiltAt()
		if at.IsZero() {
			return time.Time{}
		}
		if i == 0 || at.Before(oldest) {
			oldest = at
		}
	}
	return oldest
}

// IndexStale asks every server's index for a rebuild after the delay.
func (r *Registry) IndexStale(after time.Duration) {
	if r == nil {
		return
	}
	for _, s := range r.servers {
		s.IndexStale(after)
	}
}

// OnIndexRebuilt registers fn with every server: it runs after any of them rebuilds.
func (r *Registry) OnIndexRebuilt(fn func()) {
	if r == nil {
		return
	}
	for _, s := range r.servers {
		s.OnIndexRebuilt(fn)
	}
}
