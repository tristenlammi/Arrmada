package metadata

import (
	"context"
	"encoding/json"
	"net/url"
	"sort"
	"strconv"
	"time"

	"github.com/tristenlammi/arrmada/internal/adultfilter"
)

// DiscoverItem is a unified movie/series card for the Discover experience. MediaType
// is normalized to the app's convention ("movie" | "series"), not TMDB's "tv".
type DiscoverItem struct {
	MediaType   string   `json:"media_type"`
	TMDBID      int      `json:"tmdb_id"`
	Title       string   `json:"title"`
	Year        int      `json:"year"`
	Overview    string   `json:"overview,omitempty"`
	PosterURL   string   `json:"poster_url,omitempty"`
	BackdropURL string   `json:"backdrop_url,omitempty"`
	VoteAverage float64  `json:"vote_average"`
	ReleaseDate string   `json:"release_date,omitempty"`
	Genres      []string `json:"genres,omitempty"` // up to three, for the card
}

// Genre is a TMDB genre (for the genre explorer).
type Genre struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

// DiscoveryProvider surfaces curated browse lists + rich detail for the Discover UI.
type DiscoveryProvider interface {
	Available() bool
	Trending(ctx context.Context, media string) ([]DiscoverItem, error)
	Popular(ctx context.Context, media string) ([]DiscoverItem, error)
	Upcoming(ctx context.Context, media string) ([]DiscoverItem, error)
	DiscoverByGenre(ctx context.Context, media string, genreID int) ([]DiscoverItem, error)
	Genres(ctx context.Context, media string) ([]Genre, error)
	MediaDetails(ctx context.Context, media string, tmdbID int) (*MediaDetail, error)
	Search(ctx context.Context, query string) ([]DiscoverItem, error)
	// Recommendations returns TMDB's "recommended" titles for one movie/series — the
	// seed for the personalized "Recommended for you" row.
	Recommendations(ctx context.Context, media string, tmdbID int) ([]DiscoverItem, error)
}

// CrewMember is a billed crew member (director/writer/producer/creator).
type CrewMember struct {
	Name       string `json:"name"`
	Job        string `json:"job"`
	ProfileURL string `json:"profile_url,omitempty"`
}

// Ratings aggregates scores from multiple sources. TMDB is always present; the rest
// come from OMDb (IMDB/Rotten Tomatoes/Metacritic) when configured.
type Ratings struct {
	TMDB           float64 `json:"tmdb,omitempty"`
	IMDB           string  `json:"imdb,omitempty"`            // e.g. "8.8"
	RottenTomatoes string  `json:"rotten_tomatoes,omitempty"` // e.g. "87%"
	Metacritic     string  `json:"metacritic,omitempty"`      // e.g. "74/100"
}

// MediaDetail is the full record behind the discover detail modal.
type MediaDetail struct {
	MediaType     string       `json:"media_type"`
	TMDBID        int          `json:"tmdb_id"`
	IMDBID        string       `json:"imdb_id,omitempty"`
	Title         string       `json:"title"`
	Year          int          `json:"year"`
	Overview      string       `json:"overview,omitempty"`
	PosterURL     string       `json:"poster_url,omitempty"`
	BackdropURL   string       `json:"backdrop_url,omitempty"`
	Runtime       int          `json:"runtime,omitempty"`
	Status        string       `json:"status,omitempty"`
	Network       string       `json:"network,omitempty"`
	Genres        []string     `json:"genres,omitempty"`
	Certification string       `json:"certification,omitempty"`
	Studios       []string     `json:"studios,omitempty"`
	Cast          []CastMember `json:"cast,omitempty"`
	Crew          []CrewMember `json:"crew,omitempty"`
	Ratings       Ratings      `json:"ratings"`
	// TrailerURL is a YouTube watch URL for the best available trailer, empty when TMDB
	// has no usable video.
	TrailerURL string `json:"trailer_url,omitempty"`
	// Similar is "more like this" — TMDB recommendations mapped to browse cards (same
	// shape as the Trending/Popular rows), capped and posterless entries dropped.
	Similar []DiscoverItem `json:"similar,omitempty"`
	// Seasons lists a series' regular seasons (specials left out), from the same /tv
	// payload as the rest: what the season picker and the per-season request states are
	// built on. Empty for movies.
	Seasons []SeasonSummary `json:"seasons,omitempty"`
	// Adult is TMDB's own adult flag. The detail endpoint refuses such a title outright
	// (the always-on adult filter), so it never reaches a browser as true; it is
	// serialised only so the disk cache keeps it.
	Adult bool `json:"adult,omitempty"`
}

// SeasonSummary is one season as TMDB lists it on the show: no episodes, just enough to
// say which seasons exist and how big they are.
type SeasonSummary struct {
	Number       int    `json:"number"`
	Name         string `json:"name,omitempty"`
	EpisodeCount int    `json:"episode_count"`
	AirDate      string `json:"air_date,omitempty"` // the season's first episode (YYYY-MM-DD); "" when not announced
	PosterURL    string `json:"poster_url,omitempty"`
}

// maxSimilar caps the "more like this" row on the detail record.
const maxSimilar = 12

// RatingProvider supplies external ratings by IMDB id (OMDb).
type RatingProvider interface {
	Available() bool
	Ratings(ctx context.Context, imdbID string) (Ratings, error)
}

// MediaDetails fetches the full detail record for the modal (no episode fetching for
// series — this is metadata only). Crew: movies get director/writer/producer; series
// get their creators.
//
// Kept for 12 hours (stale served while it refreshes), so reopening a sheet, hopping
// through "More like this" and creating a request from it don't each ask TMDB again. A
// day-old status or certification is fine for display; the key carries a version so a
// later change to what's stored can retire the old entries (v2 added series seasons).
func (t *TMDB) MediaDetails(ctx context.Context, media string, tmdbID int) (*MediaDetail, error) {
	kind := "movie"
	if tvish(media) {
		kind = "tv"
	}
	// v3: records carry Adult; a v2 copy would read as "not adult" until it expired.
	key := "tmdb:detail:v3:" + kind + ":" + strconv.Itoa(tmdbID)
	return swr(ctx, t.disk, key, mediaDetailTTL, func(ctx context.Context) (*MediaDetail, error) {
		if kind == "tv" {
			return t.seriesDetail(ctx, tmdbID)
		}
		return t.movieDetail(ctx, tmdbID)
	})
}

// mediaDetailTTL is how long a title's detail record is kept before it's refreshed.
const mediaDetailTTL = 12 * time.Hour

func (t *TMDB) movieDetail(ctx context.Context, tmdbID int) (*MediaDetail, error) {
	q := url.Values{}
	q.Set("append_to_response", "credits,release_dates,external_ids,videos,recommendations")
	body, err := t.get(ctx, "/movie/"+strconv.Itoa(tmdbID), q)
	if err != nil {
		return nil, err
	}
	var m tmdbMovie
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, err
	}
	d := &MediaDetail{
		MediaType: "movie", TMDBID: m.ID, IMDBID: firstNonEmpty(m.IMDBID, m.ExternalIDs.IMDBID),
		Title: m.Title, Year: yearOf(m.ReleaseDate), Overview: m.Overview, Runtime: m.Runtime,
		Status: m.Status, Certification: usCertification(m.ReleaseDates.Results),
		Ratings: Ratings{TMDB: m.VoteAverage}, Adult: m.Adult,
	}
	if m.PosterPath != "" {
		d.PosterURL = tmdbImageBase + m.PosterPath
	}
	if m.BackdropPath != "" {
		d.BackdropURL = tmdbBackdropBase + m.BackdropPath
	}
	for _, g := range m.Genres {
		d.Genres = append(d.Genres, g.Name)
	}
	for _, s := range m.ProductionCompanies {
		d.Studios = append(d.Studios, s.Name)
	}
	d.Cast = castOf(m.Credits.Cast)
	d.Crew = movieCrew(m.Credits.Crew)
	d.TrailerURL = bestTrailerURL(m.Videos.Results)
	d.Similar = recommendedItems(m.Recommendations.Results, "movie")
	return d, nil
}

func (t *TMDB) seriesDetail(ctx context.Context, tmdbID int) (*MediaDetail, error) {
	q := url.Values{}
	q.Set("append_to_response", "credits,external_ids,videos,recommendations")
	body, err := t.get(ctx, "/tv/"+strconv.Itoa(tmdbID), q)
	if err != nil {
		return nil, err
	}
	var s tmdbSeries
	if err := json.Unmarshal(body, &s); err != nil {
		return nil, err
	}
	d := &MediaDetail{
		MediaType: "series", TMDBID: s.ID, IMDBID: s.ExternalIDs.IMDBID,
		Title: s.Name, Year: yearOf(s.FirstAirDate), Overview: s.Overview, Status: s.Status,
		Ratings: Ratings{TMDB: s.VoteAverage}, Adult: s.Adult,
	}
	if s.PosterPath != "" {
		d.PosterURL = tmdbImageBase + s.PosterPath
	}
	if s.BackdropPath != "" {
		d.BackdropURL = tmdbBackdropBase + s.BackdropPath
	}
	if len(s.Networks) > 0 {
		d.Network = s.Networks[0].Name
	}
	for _, g := range s.Genres {
		d.Genres = append(d.Genres, g.Name)
	}
	d.Cast = castOf(s.Credits.Cast)
	for _, c := range s.CreatedBy {
		cm := CrewMember{Name: c.Name, Job: "Creator"}
		if c.ProfilePath != "" {
			cm.ProfileURL = tmdbProfileBase + c.ProfilePath
		}
		d.Crew = append(d.Crew, cm)
	}
	d.TrailerURL = bestTrailerURL(s.Videos.Results)
	d.Similar = recommendedItems(s.Recommendations.Results, "tv")
	d.Seasons = seasonSummaries(s)
	return d, nil
}

// seasonSummaries is a show's regular seasons from its /tv payload, in season order.
// Specials (season 0) are left out: a request never asks for them.
func seasonSummaries(s tmdbSeries) []SeasonSummary {
	var out []SeasonSummary
	for _, sn := range s.Seasons {
		if sn.SeasonNumber <= 0 {
			continue
		}
		ss := SeasonSummary{Number: sn.SeasonNumber, Name: sn.Name, EpisodeCount: sn.EpisodeCount, AirDate: sn.AirDate}
		if sn.PosterPath != "" {
			ss.PosterURL = tmdbImageBase + sn.PosterPath
		}
		out = append(out, ss)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Number < out[j].Number })
	return out
}

// bestTrailerURL picks the best YouTube video from TMDB's videos list and returns its
// watch URL, or "" if none is usable. Preference: an official YouTube Trailer, then any
// YouTube Trailer, then any YouTube Teaser. Order within each tier follows TMDB's.
func bestTrailerURL(vids []tmdbVideo) string {
	var officialTrailer, trailer, teaser string
	for _, v := range vids {
		if v.Site != "YouTube" || v.Key == "" {
			continue
		}
		switch v.Type {
		case "Trailer":
			if v.Official && officialTrailer == "" {
				officialTrailer = v.Key
			}
			if trailer == "" {
				trailer = v.Key
			}
		case "Teaser":
			if teaser == "" {
				teaser = v.Key
			}
		}
	}
	key := firstNonEmpty(officialTrailer, firstNonEmpty(trailer, teaser))
	if key == "" {
		return ""
	}
	return "https://www.youtube.com/watch?v=" + key
}

// recommendedItems maps TMDB recommendation rows to Discover cards, reusing the same
// mapping as the browse lists (posterless/untyped rows dropped) and capping the result.
func recommendedItems(results []tmdbDiscoverItem, defaultMedia string) []DiscoverItem {
	out := make([]DiscoverItem, 0, maxSimilar)
	for _, r := range results {
		if len(out) >= maxSimilar {
			break
		}
		// No vote floor here: these are recommendations for a title the user already
		// opened, so an obscure-but-real match is legitimate. The adult flag and the
		// title filter inside toItem still apply.
		if di, ok := r.toItem(defaultMedia, 0, true); ok {
			out = append(out, di)
		}
	}
	return out
}

// castOf maps TMDB cast into billed cast members (capped, with photos).
func castOf(cast []tmdbCast) []CastMember {
	out := make([]CastMember, 0, maxCast)
	for _, c := range cast {
		if len(out) >= maxCast {
			break
		}
		cm := CastMember{Name: c.Name, Character: c.Character}
		if c.ProfilePath != "" {
			cm.ProfileURL = tmdbProfileBase + c.ProfilePath
		}
		out = append(out, cm)
	}
	return out
}

// movieCrew pulls director(s), writer(s), and producer(s) from the crew list, in that
// priority order, de-duplicated by name.
func movieCrew(crew []tmdbCrew) []CrewMember {
	want := map[string]string{
		"Director": "Director", "Writer": "Writer", "Screenplay": "Writer", "Story": "Writer",
		"Producer": "Producer", "Executive Producer": "Producer",
	}
	order := []string{"Director", "Writer", "Producer"}
	byRole := map[string][]CrewMember{}
	seen := map[string]bool{}
	for _, c := range crew {
		role, ok := want[c.Job]
		if !ok {
			continue
		}
		key := role + "|" + c.Name
		if seen[key] {
			continue
		}
		seen[key] = true
		cm := CrewMember{Name: c.Name, Job: role}
		if c.ProfilePath != "" {
			cm.ProfileURL = tmdbProfileBase + c.ProfilePath
		}
		byRole[role] = append(byRole[role], cm)
	}
	var out []CrewMember
	for _, role := range order {
		limit := 3
		for i, cm := range byRole[role] {
			if i >= limit {
				break
			}
			out = append(out, cm)
		}
	}
	return out
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

type tmdbDiscoverItem struct {
	ID           int     `json:"id"`
	MediaType    string  `json:"media_type"`
	Title        string  `json:"title"`          // movie
	Name         string  `json:"name"`           // tv
	ReleaseDate  string  `json:"release_date"`   // movie
	FirstAirDate string  `json:"first_air_date"` // tv
	Overview     string  `json:"overview"`
	PosterPath   string  `json:"poster_path"`
	BackdropPath string  `json:"backdrop_path"`
	VoteAverage  float64 `json:"vote_average"`
	VoteCount    int     `json:"vote_count"`
	Adult        bool    `json:"adult"`
	GenreIDs     []int   `json:"genre_ids"`
}

// browseMinVotes is the vote-count floor for BROWSE lists (trending, popular,
// upcoming, genre). Deliberately not applied to explicit search or to a title's
// own recommendations: there, the user asked for something specific and an
// obscure-but-real title must still be findable.
//
// It exists for adult content as much as for quality. TMDB's include_adult only
// filters rows it has actually flagged, and it routinely does NOT flag softcore
// / "pink film" catalogue entries — they're stored as ordinary movies. Those
// entries are near-universally sub-50-vote, so the floor catches what the adult
// flag misses, while every mainstream title clears it easily.
const browseMinVotes = 50

// toItem maps a raw TMDB row to a DiscoverItem. defaultMedia ("movie"/"tv") applies
// when the row carries no media_type (single-type endpoints). Persons are dropped.
// minVotes applies the browse floor (0 = no floor).
//
// Adult rows are ALWAYS dropped regardless of the caller: TMDB's own flag plus
// the shared title filter (internal/adultfilter, same rule the indexer uses).
// Every discovery surface — trending, popular, upcoming, genre, search, "more
// like this" — funnels through here, so this is the one place that has to hold.
// noiseGenres are TMDB TV genres that don't belong on a REQUEST app's browse rows:
// nobody torrents last night's talk show, news broadcast, or daily soap, yet they
// dominate TMDB's TV lists (especially on-the-air). Dropped from browse surfaces
// only — explicit search and deliberately browsing one of these genres still work.
// (Movie genre ids don't overlap these, so the check is safe for mixed lists.)
var noiseGenres = map[int]bool{
	10767: true, // Talk
	10763: true, // News
	10766: true, // Soap
}

// noiseGenreCSV is the same set for TMDB's without_genres param.
const noiseGenreCSV = "10767,10763,10766"

func hasNoiseGenre(ids []int) bool {
	for _, id := range ids {
		if noiseGenres[id] {
			return true
		}
	}
	return false
}

func (i tmdbDiscoverItem) toItem(defaultMedia string, minVotes int, dropNoise bool) (DiscoverItem, bool) {
	if dropNoise && hasNoiseGenre(i.GenreIDs) {
		return DiscoverItem{}, false
	}
	if i.Adult {
		return DiscoverItem{}, false
	}
	if i.VoteCount < minVotes {
		return DiscoverItem{}, false
	}
	mt := i.MediaType
	if mt == "" {
		mt = defaultMedia
	}
	title, date := i.Title, i.ReleaseDate
	var normalized string
	switch mt {
	case "movie":
		normalized = "movie"
	case "tv":
		normalized, title, date = "series", i.Name, i.FirstAirDate
	default:
		return DiscoverItem{}, false // person, or unknown
	}
	if title == "" || i.PosterPath == "" {
		return DiscoverItem{}, false // skip posterless/untitled — they read as broken cards
	}
	if adultfilter.Matches(title) {
		return DiscoverItem{}, false
	}
	di := DiscoverItem{
		MediaType: normalized, TMDBID: i.ID, Title: title, Year: yearOf(date),
		Overview: i.Overview, VoteAverage: i.VoteAverage, ReleaseDate: date,
		PosterURL: tmdbImageBase + i.PosterPath,
	}
	if i.BackdropPath != "" {
		di.BackdropURL = tmdbBackdropBase + i.BackdropPath
	}
	return di, true
}

// Discover list caching: the browse rows (trending/popular/upcoming/genre) are the same
// for every user and change rarely, so they get a 10-minute TTL. Search is per-query user
// input — still worth caching (the UI re-fires on focus/poll) but shorter-lived. The cache
// is TTL-only (no manual invalidation) and never stores errors. A size cap bounds memory
// against unbounded distinct search queries.
const (
	discoverListTTL   = 10 * time.Minute
	discoverSearchTTL = 2 * time.Minute
	discoverCacheCap  = 200
)

type discoverCacheEntry struct {
	items []DiscoverItem
	page  *SearchResults // a paged entry (cachedDiscoverPage); nil for a whole list
	added time.Time
	exp   time.Time
}

// storeDiscover puts an entry in the memory cache, dropping expired entries first and,
// when it's still full, the oldest one, so it never holds more than discoverCacheCap.
func (t *TMDB) storeDiscover(key string, e discoverCacheEntry) {
	t.discMu.Lock()
	defer t.discMu.Unlock()
	if t.discCache == nil {
		t.discCache = map[string]discoverCacheEntry{}
	}
	for k, old := range t.discCache {
		if !e.added.Before(old.exp) {
			delete(t.discCache, k)
		}
	}
	if len(t.discCache) >= discoverCacheCap {
		var oldestKey string
		var oldestAt time.Time
		for k, old := range t.discCache {
			if oldestKey == "" || old.added.Before(oldestAt) {
				oldestKey, oldestAt = k, old.added
			}
		}
		delete(t.discCache, oldestKey)
	}
	t.discCache[key] = e
}

// cachedDiscoverList serves a discover list from the TTL cache, fetching (and caching)
// on miss. Errors are returned uncached so a transient TMDB failure doesn't stick.
func (t *TMDB) cachedDiscoverList(ctx context.Context, path string, q url.Values, defaultMedia string, ttl time.Duration, minVotes int, dropNoise bool) ([]DiscoverItem, error) {
	return t.cachedDiscoverListN(ctx, path, q, defaultMedia, ttl, minVotes, dropNoise, 1)
}

// cachedDiscoverListN is cachedDiscoverList over the first `pages` pages of the list
// (the browse rows dedupe against each other on the page, so one page of 20 leaves a
// short strip). Memory first, then the disk cache — which also answers with a stale
// list while a fresh one is fetched — then TMDB.
func (t *TMDB) cachedDiscoverListN(ctx context.Context, path string, q url.Values, defaultMedia string, ttl time.Duration, minVotes int, dropNoise bool, pages int) ([]DiscoverItem, error) {
	if pages < 1 {
		pages = 1
	}
	key := path + "?" + q.Encode()
	if pages > 1 {
		key += "&pages=" + strconv.Itoa(pages)
	}
	now := time.Now()

	t.discMu.Lock()
	if e, ok := t.discCache[key]; ok && now.Before(e.exp) {
		items := e.items
		t.discMu.Unlock()
		return items, nil
	}
	t.discMu.Unlock()

	items, err := swr(ctx, t.disk, "tmdb:"+key, ttl, func(ctx context.Context) ([]DiscoverItem, error) {
		return t.discoverListPages(ctx, path, q, defaultMedia, minVotes, dropNoise, pages)
	})
	if err != nil {
		return nil, err
	}
	t.storeDiscover(key, discoverCacheEntry{items: items, added: now, exp: now.Add(ttl)})
	return items, nil
}

func (t *TMDB) discoverList(ctx context.Context, path string, q url.Values, defaultMedia string, minVotes int, dropNoise bool) ([]DiscoverItem, error) {
	return t.discoverListPages(ctx, path, q, defaultMedia, minVotes, dropNoise, 1)
}

// discoverListPages fetches pages 1..pages of a list and joins them (deduped by id),
// attaching genre names to each card.
func (t *TMDB) discoverListPages(ctx context.Context, path string, q url.Values, defaultMedia string, minVotes int, dropNoise bool, pages int) ([]DiscoverItem, error) {
	names := t.genreNames(ctx)
	seen := map[string]bool{}
	var out []DiscoverItem
	for page := 1; page <= pages; page++ {
		pq := url.Values{}
		for k, v := range q {
			pq[k] = v
		}
		if page > 1 {
			pq.Set("page", strconv.Itoa(page))
		}
		body, err := t.get(ctx, path, pq)
		if err != nil {
			if page > 1 && len(out) > 0 {
				break // the first page is the row; a failed second page just makes it shorter
			}
			return nil, err
		}
		var payload struct {
			Results []tmdbDiscoverItem `json:"results"`
		}
		if err := json.Unmarshal(body, &payload); err != nil {
			return nil, err
		}
		for _, r := range payload.Results {
			di, ok := r.toItem(defaultMedia, minVotes, dropNoise)
			if !ok {
				continue
			}
			k := di.MediaType + ":" + strconv.Itoa(di.TMDBID)
			if seen[k] {
				continue
			}
			seen[k] = true
			di.Genres = genreLabels(r.GenreIDs, names)
			out = append(out, di)
		}
		if len(payload.Results) < 20 {
			break // last page
		}
	}
	return out, nil
}

// tvPath maps the app's media arg to a TMDB path segment + default media type.
func tvish(media string) bool { return media == "tv" || media == "series" }

// Trending returns this week's trending titles. media: "all" | "movie" | "series".
func (t *TMDB) Trending(ctx context.Context, media string) ([]DiscoverItem, error) {
	seg := "all"
	switch {
	case media == "movie":
		seg = "movie"
	case tvish(media):
		seg = "tv"
	}
	return t.cachedDiscoverListN(ctx, "/trending/"+seg+"/week", url.Values{}, "", discoverListTTL, browseMinVotes, true, 2)
}

// Popular returns popular movies or series.
func (t *TMDB) Popular(ctx context.Context, media string) ([]DiscoverItem, error) {
	if tvish(media) {
		return t.cachedDiscoverListN(ctx, "/tv/popular", url.Values{}, "tv", discoverListTTL, browseMinVotes, true, 2)
	}
	q := url.Values{}
	if r := t.regionCode(); r != "" {
		q.Set("region", r)
	}
	return t.cachedDiscoverListN(ctx, "/movie/popular", q, "movie", discoverListTTL, browseMinVotes, true, 2)
}

// Upcoming returns titles airing/releasing soon (for requesting future titles). For
// movies that's TMDB's not-yet-released list; for series it's shows airing in the next
// 7 days (/tv/on_the_air). media: "movie" (default) | "series"/"tv".
//
// The two variants hit different TMDB paths, and the discover cache keys on
// path+query, so movie and series results can never collide.
func (t *TMDB) Upcoming(ctx context.Context, media string) ([]DiscoverItem, error) {
	if tvish(media) {
		return t.cachedDiscoverList(ctx, "/tv/on_the_air", url.Values{}, "tv", discoverListTTL, 0, true)
	}
	q := url.Values{}
	if r := t.regionCode(); r != "" {
		q.Set("region", r)
	}
	return t.cachedDiscoverList(ctx, "/movie/upcoming", q, "movie", discoverListTTL, 0, true)
}

// DiscoverByGenre returns popular titles in a genre.
func (t *TMDB) DiscoverByGenre(ctx context.Context, media string, genreID int) ([]DiscoverItem, error) {
	q := url.Values{}
	q.Set("with_genres", strconv.Itoa(genreID))
	q.Set("sort_by", "popularity.desc")
	q.Set("include_adult", "false") // /discover/* honours this; trending/popular don't
	// Server-side floor: /discover supports it, so the page arrives already filtered
	// instead of being thinned client-side. The client floor stays as the backstop.
	q.Set("vote_count.gte", strconv.Itoa(browseMinVotes))
	// Deliberately browsing one of the noise genres (the Talk/News/Soap chips) is
	// explicit intent — show it. Any OTHER genre excludes them server-side too, so
	// TMDB fills the page with real titles instead of us thinning it.
	dropNoise := !noiseGenres[genreID]
	if tvish(media) {
		if dropNoise {
			q.Set("without_genres", noiseGenreCSV)
		}
		return t.cachedDiscoverList(ctx, "/discover/tv", q, "tv", discoverListTTL, browseMinVotes, dropNoise)
	}
	if r := t.regionCode(); r != "" {
		q.Set("region", r)
	}
	return t.cachedDiscoverList(ctx, "/discover/movie", q, "movie", discoverListTTL, browseMinVotes, dropNoise)
}

// Search runs a combined movie+TV search (TMDB /search/multi), dropping people and
// posterless hits. Powers the Discover search bar.
func (t *TMDB) Search(ctx context.Context, query string) ([]DiscoverItem, error) {
	if query == "" {
		return nil, nil
	}
	q := url.Values{}
	q.Set("query", query)
	q.Set("include_adult", "false")
	return t.cachedDiscoverList(ctx, "/search/multi", q, "", discoverSearchTTL, 0, false)
}

// Recommendations returns TMDB's recommended titles for one movie/series. No vote
// floor — these are contextual to a seed the user already engaged with, so an
// obscure-but-relevant match is legitimate; the adult flag and title filter still
// apply inside the mapper. Cached like the browse lists (10 min), keyed on the id.
func (t *TMDB) Recommendations(ctx context.Context, media string, tmdbID int) ([]DiscoverItem, error) {
	seg, def := "movie", "movie"
	if tvish(media) {
		seg, def = "tv", "tv"
	}
	path := "/" + seg + "/" + strconv.Itoa(tmdbID) + "/recommendations"
	return t.cachedDiscoverList(ctx, path, url.Values{}, def, discoverListTTL, 0, true)
}

// Genres returns the genre list for movies or series.
func (t *TMDB) Genres(ctx context.Context, media string) ([]Genre, error) {
	path := "/genre/movie/list"
	if tvish(media) {
		path = "/genre/tv/list"
	}
	body, err := t.get(ctx, path, url.Values{})
	if err != nil {
		return nil, err
	}
	var payload struct {
		Genres []Genre `json:"genres"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, err
	}
	return payload.Genres, nil
}
