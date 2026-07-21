package metadata

import (
	"context"
	"encoding/json"
	"net/url"
	"strconv"
	"time"
)

// DiscoverItem is a unified movie/series card for the Discover experience. MediaType
// is normalized to the app's convention ("movie" | "series"), not TMDB's "tv".
type DiscoverItem struct {
	MediaType   string  `json:"media_type"`
	TMDBID      int     `json:"tmdb_id"`
	Title       string  `json:"title"`
	Year        int     `json:"year"`
	Overview    string  `json:"overview,omitempty"`
	PosterURL   string  `json:"poster_url,omitempty"`
	BackdropURL string  `json:"backdrop_url,omitempty"`
	VoteAverage float64 `json:"vote_average"`
	ReleaseDate string  `json:"release_date,omitempty"`
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
func (t *TMDB) MediaDetails(ctx context.Context, media string, tmdbID int) (*MediaDetail, error) {
	if tvish(media) {
		return t.seriesDetail(ctx, tmdbID)
	}
	return t.movieDetail(ctx, tmdbID)
}

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
		Ratings: Ratings{TMDB: m.VoteAverage},
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
		Ratings: Ratings{TMDB: s.VoteAverage},
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
	return d, nil
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
		if di, ok := r.toItem(defaultMedia); ok {
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
}

// toItem maps a raw TMDB row to a DiscoverItem. defaultMedia ("movie"/"tv") applies
// when the row carries no media_type (single-type endpoints). Persons are dropped.
func (i tmdbDiscoverItem) toItem(defaultMedia string) (DiscoverItem, bool) {
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
	added time.Time
	exp   time.Time
}

// cachedDiscoverList serves a discover list from the TTL cache, fetching (and caching)
// on miss. Errors are returned uncached so a transient TMDB failure doesn't stick.
func (t *TMDB) cachedDiscoverList(ctx context.Context, path string, q url.Values, defaultMedia string, ttl time.Duration) ([]DiscoverItem, error) {
	key := path + "?" + q.Encode()
	now := time.Now()

	t.discMu.Lock()
	if e, ok := t.discCache[key]; ok && now.Before(e.exp) {
		items := e.items
		t.discMu.Unlock()
		return items, nil
	}
	t.discMu.Unlock()

	items, err := t.discoverList(ctx, path, q, defaultMedia)
	if err != nil {
		return nil, err
	}

	t.discMu.Lock()
	defer t.discMu.Unlock()
	if t.discCache == nil {
		t.discCache = map[string]discoverCacheEntry{}
	}
	for k, e := range t.discCache { // drop expired entries first
		if !now.Before(e.exp) {
			delete(t.discCache, k)
		}
	}
	if len(t.discCache) >= discoverCacheCap { // still full: evict the oldest entry
		var oldestKey string
		var oldestAt time.Time
		for k, e := range t.discCache {
			if oldestKey == "" || e.added.Before(oldestAt) {
				oldestKey, oldestAt = k, e.added
			}
		}
		delete(t.discCache, oldestKey)
	}
	t.discCache[key] = discoverCacheEntry{items: items, added: now, exp: now.Add(ttl)}
	return items, nil
}

func (t *TMDB) discoverList(ctx context.Context, path string, q url.Values, defaultMedia string) ([]DiscoverItem, error) {
	body, err := t.get(ctx, path, q)
	if err != nil {
		return nil, err
	}
	var payload struct {
		Results []tmdbDiscoverItem `json:"results"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, err
	}
	out := make([]DiscoverItem, 0, len(payload.Results))
	for _, r := range payload.Results {
		if di, ok := r.toItem(defaultMedia); ok {
			out = append(out, di)
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
	return t.cachedDiscoverList(ctx, "/trending/"+seg+"/week", url.Values{}, "", discoverListTTL)
}

// Popular returns popular movies or series.
func (t *TMDB) Popular(ctx context.Context, media string) ([]DiscoverItem, error) {
	if tvish(media) {
		return t.cachedDiscoverList(ctx, "/tv/popular", url.Values{}, "tv", discoverListTTL)
	}
	return t.cachedDiscoverList(ctx, "/movie/popular", url.Values{}, "movie", discoverListTTL)
}

// Upcoming returns titles airing/releasing soon (for requesting future titles). For
// movies that's TMDB's not-yet-released list; for series it's shows airing in the next
// 7 days (/tv/on_the_air). media: "movie" (default) | "series"/"tv".
//
// The two variants hit different TMDB paths, and the discover cache keys on
// path+query, so movie and series results can never collide.
func (t *TMDB) Upcoming(ctx context.Context, media string) ([]DiscoverItem, error) {
	if tvish(media) {
		return t.cachedDiscoverList(ctx, "/tv/on_the_air", url.Values{}, "tv", discoverListTTL)
	}
	return t.cachedDiscoverList(ctx, "/movie/upcoming", url.Values{}, "movie", discoverListTTL)
}

// DiscoverByGenre returns popular titles in a genre.
func (t *TMDB) DiscoverByGenre(ctx context.Context, media string, genreID int) ([]DiscoverItem, error) {
	q := url.Values{}
	q.Set("with_genres", strconv.Itoa(genreID))
	q.Set("sort_by", "popularity.desc")
	if tvish(media) {
		return t.cachedDiscoverList(ctx, "/discover/tv", q, "tv", discoverListTTL)
	}
	return t.cachedDiscoverList(ctx, "/discover/movie", q, "movie", discoverListTTL)
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
	return t.cachedDiscoverList(ctx, "/search/multi", q, "", discoverSearchTTL)
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
