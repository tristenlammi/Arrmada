package metadata

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Discover's deeper grids: a filterable browse behind every row's "See all", and search
// past its first page. Each answer is one TMDB page with the total, so the browser can
// keep loading as it scrolls. Every filter is whitelisted and clamped here: nothing the
// browser sends reaches TMDB raw, include_adult is always false, a vote floor is always
// set, and every row goes through toItem (the adult filter).

// DiscoverPages is the optional capability behind the browse grid and paged search.
type DiscoverPages interface {
	Browse(ctx context.Context, q BrowseQuery) (Page, error)
	SearchPage(ctx context.Context, query string, page int) (SearchResults, error)
}

// BrowseQuery is one browse grid page. Media is "movie" or "series" ("all" only with the
// trending list). List names a fixed TMDB list (trending, popular, upcoming, top_rated,
// now_playing, hidden_gems), which ignores the filters; without one it's a filtered
// /discover. Zero values mean "no filter".
type BrowseQuery struct {
	Media                  string
	Genres                 []int
	YearFrom, YearTo       int
	RatingMin              float64
	RuntimeMin, RuntimeMax int
	Provider               int
	Language               string
	Sort                   string
	List                   string
	Page                   int
}

// Page is one page of a paged list.
type Page struct {
	Items      []DiscoverItem `json:"items"`
	Page       int            `json:"page"`
	TotalPages int            `json:"total_pages"`
}

// SearchResults is one page of a Discover search: titles, and the people on that page.
type SearchResults struct {
	Page
	People []PersonResult `json:"people"`
}

// PersonResult is a person in search results.
type PersonResult struct {
	ID                 int      `json:"id"`
	Name               string   `json:"name"`
	ProfileURL         string   `json:"profile_url,omitempty"`
	KnownForDepartment string   `json:"known_for_department,omitempty"`
	KnownFor           []string `json:"known_for,omitempty"`
}

// ErrBadQuery is a browse query the whitelist refuses (a bad sort, list or media).
var ErrBadQuery = errors.New("unsupported browse query")

const (
	maxBrowsePage    = 500 // TMDB serves no page past 500
	maxBrowseGenres  = 5
	ratingSortVotes  = 200 // a rating sort needs a real sample, or 10/10 from three votes wins
	browsePageTTL    = discoverListTTL
	searchPageTTL    = discoverSearchTTL
	personMinPopular = 1.0
)

var langCode = regexp.MustCompile(`^[a-z]{2}$`)

// normalize validates and clamps a query: an unknown media, list or sort is refused;
// numbers are clamped into sane ranges; the page into 1..500.
func (q BrowseQuery) normalize() (BrowseQuery, error) {
	switch q.Media {
	case "movie", "series":
	case "tv":
		q.Media = "series"
	case "all":
		if q.List != "trending" {
			return q, ErrBadQuery
		}
	case "":
		q.Media = "movie"
	default:
		return q, ErrBadQuery
	}
	switch q.List {
	case "", "trending", "popular", "upcoming", "top_rated", "now_playing", "hidden_gems":
	default:
		return q, ErrBadQuery
	}
	switch q.Sort {
	case "":
		q.Sort = "popularity.desc"
	case "popularity.desc", "vote_average.desc":
	case "primary_release_date.desc", "first_air_date.desc", "release_date.desc":
		q.Sort = "primary_release_date.desc"
		if q.Media == "series" {
			q.Sort = "first_air_date.desc"
		}
	case "revenue.desc":
		if q.Media == "series" {
			return q, ErrBadQuery
		}
	default:
		return q, ErrBadQuery
	}
	q.Page = min(max(q.Page, 1), maxBrowsePage)
	var genres []int
	for _, g := range q.Genres {
		if g > 0 && len(genres) < maxBrowseGenres {
			genres = append(genres, g)
		}
	}
	q.Genres = genres
	thisYear := time.Now().Year()
	clampYear := func(y int) int {
		if y <= 0 {
			return 0
		}
		return min(max(y, 1874), thisYear+5)
	}
	q.YearFrom, q.YearTo = clampYear(q.YearFrom), clampYear(q.YearTo)
	if q.YearFrom > 0 && q.YearTo > 0 && q.YearFrom > q.YearTo {
		q.YearFrom, q.YearTo = q.YearTo, q.YearFrom
	}
	q.RatingMin = min(max(q.RatingMin, 0), 10)
	q.RuntimeMin = min(max(q.RuntimeMin, 0), 600)
	q.RuntimeMax = min(max(q.RuntimeMax, 0), 600)
	q.Provider = max(q.Provider, 0)
	q.Language = strings.ToLower(strings.TrimSpace(q.Language))
	if q.Language != "" && !langCode.MatchString(q.Language) {
		q.Language = ""
	}
	return q, nil
}

// browseRequest is the TMDB path and query for a normalized browse query, with the media
// TMDB rows default to and the vote floor and noise rule toItem applies.
func (t *TMDB) browseRequest(q BrowseQuery) (path string, v url.Values, def string, minVotes int, dropNoise bool) {
	v = url.Values{}
	v.Set("include_adult", "false")
	seg, def := "movie", "movie"
	if tvish(q.Media) {
		seg, def = "tv", "tv"
	}
	dropNoise = true
	switch q.List {
	case "trending":
		tseg := seg
		if q.Media == "all" {
			tseg, def = "all", ""
		}
		v.Set("vote_count.gte", strconv.Itoa(browseMinVotes))
		return "/trending/" + tseg + "/week", v, def, browseMinVotes, true
	case "popular":
		if seg == "movie" {
			if r := t.regionCode(); r != "" {
				v.Set("region", r)
			}
		}
		v.Set("vote_count.gte", strconv.Itoa(browseMinVotes))
		return "/" + seg + "/popular", v, def, browseMinVotes, true
	case "top_rated":
		if seg == "movie" {
			v.Set("region", t.regionOrDefault())
		}
		v.Set("vote_count.gte", strconv.Itoa(browseMinVotes))
		return "/" + seg + "/top_rated", v, def, browseMinVotes, true
	case "upcoming":
		// Not out yet, so few votes: the adult flag and the title filter still apply.
		v.Set("vote_count.gte", "0")
		if seg == "tv" {
			return "/tv/on_the_air", v, def, 0, true
		}
		if r := t.regionCode(); r != "" {
			v.Set("region", r)
		}
		return "/movie/upcoming", v, def, 0, true
	case "now_playing":
		v.Set("vote_count.gte", "0")
		if seg == "tv" {
			return "/tv/airing_today", v, def, 0, true
		}
		v.Set("region", t.regionOrDefault())
		return "/movie/now_playing", v, def, 0, true
	case "hidden_gems":
		v.Set("sort_by", "vote_average.desc")
		v.Set("vote_average.gte", strconv.FormatFloat(hiddenGemMinVote, 'f', 1, 64))
		v.Set("vote_count.gte", "300")
		v.Set("vote_count.lte", "3000")
		since := time.Now().AddDate(-12, 0, 0).Format("2006-01-02")
		if seg == "tv" {
			v.Set("first_air_date.gte", since)
			v.Set("without_genres", noiseGenreCSV)
		} else {
			v.Set("primary_release_date.gte", since)
			v.Set("region", t.regionOrDefault())
		}
		return "/discover/" + seg, v, def, 0, true
	}

	// A filtered /discover.
	v.Set("sort_by", q.Sort)
	floor := browseMinVotes
	if q.Sort == "vote_average.desc" {
		floor = ratingSortVotes
	}
	v.Set("vote_count.gte", strconv.Itoa(floor))
	if len(q.Genres) > 0 {
		ids := make([]string, len(q.Genres))
		for i, g := range q.Genres {
			ids[i] = strconv.Itoa(g)
			if noiseGenres[g] {
				dropNoise = false // a Talk/News/Soap chip chosen on purpose
			}
		}
		v.Set("with_genres", strings.Join(ids, ","))
	}
	if seg == "tv" && dropNoise {
		v.Set("without_genres", noiseGenreCSV)
	}
	dateKey := "primary_release_date"
	if seg == "tv" {
		dateKey = "first_air_date"
	}
	if q.YearFrom > 0 {
		v.Set(dateKey+".gte", strconv.Itoa(q.YearFrom)+"-01-01")
	}
	if q.YearTo > 0 {
		v.Set(dateKey+".lte", strconv.Itoa(q.YearTo)+"-12-31")
	}
	if q.RatingMin > 0 {
		v.Set("vote_average.gte", strconv.FormatFloat(q.RatingMin, 'f', 1, 64))
	}
	if q.RuntimeMin > 0 {
		v.Set("with_runtime.gte", strconv.Itoa(q.RuntimeMin))
	}
	if q.RuntimeMax > 0 {
		v.Set("with_runtime.lte", strconv.Itoa(q.RuntimeMax))
	}
	if q.Provider > 0 {
		v.Set("with_watch_providers", strconv.Itoa(q.Provider))
		v.Set("watch_region", t.regionOrDefault())
	}
	if q.Language != "" {
		v.Set("with_original_language", q.Language)
	}
	if seg == "movie" {
		if r := t.regionCode(); r != "" {
			v.Set("region", r)
		}
	}
	return "/discover/" + seg, v, def, floor, dropNoise
}

// Browse is one page of a browse grid.
func (t *TMDB) Browse(ctx context.Context, q BrowseQuery) (Page, error) {
	q, err := q.normalize()
	if err != nil {
		return Page{}, err
	}
	path, v, def, minVotes, dropNoise := t.browseRequest(q)
	v.Set("page", strconv.Itoa(q.Page))
	sr, err := t.cachedDiscoverPage(ctx, path, v, def, browsePageTTL, minVotes, dropNoise, false)
	return sr.Page, err
}

// SearchPage is one page of a Discover search (/search/multi): titles and people.
func (t *TMDB) SearchPage(ctx context.Context, query string, page int) (SearchResults, error) {
	query = strings.TrimSpace(query)
	page = min(max(page, 1), maxBrowsePage)
	if query == "" {
		return SearchResults{Page: Page{Items: []DiscoverItem{}, Page: 1}, People: []PersonResult{}}, nil
	}
	v := url.Values{}
	v.Set("query", query)
	v.Set("include_adult", "false")
	v.Set("page", strconv.Itoa(page))
	return t.cachedDiscoverPage(ctx, "/search/multi", v, "", searchPageTTL, 0, false, true)
}

// cachedDiscoverPage is cachedDiscoverList for one page, keeping its total page count
// (and, for a search, the people on it). It shares the memory cache's size cap; the disk
// cache serves a stale page while a fresh one is fetched.
func (t *TMDB) cachedDiscoverPage(ctx context.Context, path string, q url.Values, defaultMedia string, ttl time.Duration, minVotes int, dropNoise, people bool) (SearchResults, error) {
	key := "page:" + path + "?" + q.Encode()
	now := time.Now()
	t.discMu.Lock()
	if e, ok := t.discCache[key]; ok && now.Before(e.exp) && e.page != nil {
		sr := *e.page
		t.discMu.Unlock()
		return sr, nil
	}
	t.discMu.Unlock()

	sr, err := swr(ctx, t.disk, "tmdb:"+key, ttl, func(ctx context.Context) (SearchResults, error) {
		return t.discoverPage(ctx, path, q, defaultMedia, minVotes, dropNoise, people)
	})
	if err != nil {
		return SearchResults{}, err
	}
	t.storeDiscover(key, discoverCacheEntry{page: &sr, added: now, exp: now.Add(ttl)})
	return sr, nil
}

// discoverPage fetches one page and maps it: titles through toItem, people (when asked
// for) through personResult.
func (t *TMDB) discoverPage(ctx context.Context, path string, q url.Values, defaultMedia string, minVotes int, dropNoise, people bool) (SearchResults, error) {
	body, err := t.get(ctx, path, q)
	if err != nil {
		return SearchResults{}, err
	}
	var payload struct {
		Page       int `json:"page"`
		TotalPages int `json:"total_pages"`
		Results    []struct {
			tmdbDiscoverItem
			Name               string             `json:"name"`
			ProfilePath        string             `json:"profile_path"`
			KnownForDepartment string             `json:"known_for_department"`
			KnownFor           []tmdbDiscoverItem `json:"known_for"`
		} `json:"results"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return SearchResults{}, err
	}
	names := t.genreNames(ctx)
	sr := SearchResults{Page: Page{Items: []DiscoverItem{}, Page: max(payload.Page, 1), TotalPages: min(max(payload.TotalPages, 1), maxBrowsePage)}, People: []PersonResult{}}
	seen := map[string]bool{}
	for _, r := range payload.Results {
		row := r.tmdbDiscoverItem
		row.Name = r.Name
		if row.MediaType == "person" {
			if p, ok := personResult(r.ID, r.Name, r.ProfilePath, r.KnownForDepartment, r.Adult, r.Popularity, r.KnownFor); ok && people {
				sr.People = append(sr.People, p)
			}
			continue
		}
		di, ok := row.toItem(defaultMedia, minVotes, dropNoise)
		if !ok {
			continue
		}
		k := di.MediaType + ":" + strconv.Itoa(di.TMDBID)
		if seen[k] {
			continue
		}
		seen[k] = true
		di.Genres = genreLabels(row.GenreIDs, names)
		sr.Items = append(sr.Items, di)
	}
	return sr, nil
}

// personResult is a search hit for a person, or false when it must not be shown: an adult
// performer, someone without a photo, or a near-unknown (popularity under 1). Their
// known-for titles go through the same mapping as every card, so an adult title is never
// named.
func personResult(id int, name, profile, dept string, adult bool, popularity float64, knownFor []tmdbDiscoverItem) (PersonResult, bool) {
	if adult || id <= 0 || name == "" || profile == "" || popularity < personMinPopular {
		return PersonResult{}, false
	}
	p := PersonResult{ID: id, Name: name, ProfileURL: tmdbProfileBase + profile, KnownForDepartment: dept}
	for _, kf := range knownFor {
		if di, ok := kf.toItem("", 0, false); ok && len(p.KnownFor) < 3 {
			p.KnownFor = append(p.KnownFor, di.Title)
		}
	}
	return p, true
}
