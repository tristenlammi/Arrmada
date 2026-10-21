package insights

import (
	"context"
	"sort"
	"strings"
	"time"
)

// The Library tab: the library seen through what people watch — the most watched titles,
// what has sat unwatched since it was added, and the titles that keep forcing Plex to
// transcode (what Convert could fix). Most watched and the hot spots come from recorded
// plays only; never watched needs the Plex library index and says when it can't have it.
// The tab only links to a title's page and to Convert: nothing here starts a conversion.

// LibraryItem is one title on the Library tab. Media and TMDB come from the Plex library
// index ("" / 0 when it doesn't know the title); the HTTP layer fills ArrmadaID from them.
type LibraryItem struct {
	Kind      string `json:"kind"` // movie | show
	Title     string `json:"title"`
	Year      int    `json:"year,omitempty"`
	ThumbURL  string `json:"thumb_url"`
	RatingKey string `json:"rating_key,omitempty"`
	Media     string `json:"media,omitempty"` // movie | series: Arrmada's word, for links
	TMDB      int    `json:"tmdb,omitempty"`
	ArrmadaID int64  `json:"arrmada_id,omitempty"`

	Plays   int   `json:"plays,omitempty"`
	Secs    int64 `json:"secs,omitempty"`
	AddedAt int64 `json:"added_at,omitempty"` // never watched: when Plex added it
	// Server is where the plays were (plex | jellyfin), "" when on both; never watched
	// lists Plex's library.
	Server string `json:"server"`
}

// HotSpot is a title that keeps being transcoded, with why and where.
type HotSpot struct {
	LibraryItem
	Transcodes int      `json:"transcodes"`  // transcoded plays in the window
	TotalPlays int      `json:"total_plays"` // all its plays in the window
	TopPair    string   `json:"top_pair"`    // the most common "HEVC 4K → H264 1080p" (or "Audio TRUEHD → AAC")
	Platforms  []string `json:"platforms"`   // the platforms that transcode it most, up to 3
	TopCause   string   `json:"top_cause,omitempty"`
	CauseLabel string   `json:"cause_label,omitempty"` // the most common buffer cause while transcoding
}

// LibraryView is the Library tab's bundle.
type LibraryView struct {
	WindowDays        int           `json:"window_days"`
	MostWatchedMovies []LibraryItem `json:"most_watched_movies"`
	MostWatchedShows  []LibraryItem `json:"most_watched_shows"`
	// NeverWatched lists titles Plex added over neverWatchedAge ago that nobody has
	// played, oldest first, at most neverWatchedMax (NeverWatchedTotal counts them all).
	// NeverWatchedUnavailable: Plex isn't set up or its library index isn't built, so
	// the list can't be made (rather than "everything has been watched").
	NeverWatched            []LibraryItem `json:"never_watched"`
	NeverWatchedTotal       int           `json:"never_watched_total"`
	NeverWatchedUnavailable bool          `json:"never_watched_unavailable"`
	HotSpots                []HotSpot     `json:"hot_spots"`
}

const (
	neverWatchedAge = 30 * 24 * time.Hour
	neverWatchedMax = 200
	libraryTopN     = 25
)

// Library builds the Library tab over the last windowDays (most watched and hot spots;
// never watched is over all time). byDuration ranks most watched by hours.
func (s *Service) Library(ctx context.Context, windowDays int, byDuration bool, server string) (LibraryView, error) {
	if windowDays <= 0 {
		windowDays = 90
	}
	since := windowStart(windowDays)
	out := LibraryView{WindowDays: windowDays, MostWatchedMovies: []LibraryItem{}, MostWatchedShows: []LibraryItem{},
		NeverWatched: []LibraryItem{}, HotSpots: []HotSpot{}}
	var err error
	if out.MostWatchedMovies, err = s.mostWatched(ctx, "movie", since, byDuration, server); err != nil {
		return LibraryView{}, err
	}
	if out.MostWatchedShows, err = s.mostWatched(ctx, "episode", since, byDuration, server); err != nil {
		return LibraryView{}, err
	}
	if out.HotSpots, err = s.hotSpots(ctx, since, server); err != nil {
		return LibraryView{}, err
	}
	// Never watched is read off the Plex library index, so it's Plex's list (a Jellyfin
	// play of a title still counts as watching it).
	if server == ServerJellyfin || s.links.current() == nil || (s.settings != nil && !s.Configured(ctx)) {
		out.NeverWatchedUnavailable = true
		return out, nil
	}
	out.NeverWatched, out.NeverWatchedTotal, err = s.neverWatched(ctx, time.Now().Add(-neverWatchedAge))
	return out, err
}

// identify fills a title's Arrmada-facing ids from the Plex library index. A Jellyfin
// item id (32 hex digits) is never a Plex rating key, so a Jellyfin-only title stays
// unlinked.
func (s *Service) identify(it *LibraryItem) {
	if it.RatingKey == "" || isGUID(it.RatingKey) {
		return
	}
	if media, tmdb, ok := s.TMDBForRatingKey(it.RatingKey); ok {
		it.Media, it.TMDB = media, tmdb
	}
}

// mostWatched ranks movies (by title) or shows (by show name) — the same grouping as the
// watch-statistics cards — carrying a rating key to link them by.
func (s *Service) mostWatched(ctx context.Context, mediaType string, since int64, byDuration bool, server string) ([]LibraryItem, error) {
	// A title watched on both servers is one row (it's the same title), Server "" then. Its
	// rating key is whichever server's sorts last, which only links it when that's Plex's.
	srv := serverAnd("", server)
	q := `SELECT title AS t, MAX(year), MAX(thumb), MAX(rating_key), COUNT(*) AS plays, ` + watchedSumFor(s.repo.db) + ` AS secs, ` + rowServer + `
		FROM stream_sessions WHERE media_type = 'movie' AND started_at >= ? AND title <> ''` + srv + `
		GROUP BY t ORDER BY ` + orderBy(byDuration) + ` LIMIT ?`
	kind := "movie"
	if mediaType == "episode" {
		kind = "show"
		q = `SELECT grandparent_title AS t, 0, COALESCE(NULLIF(MAX(grandparent_thumb),''), MAX(thumb)), MAX(grandparent_rating_key),
				COUNT(*) AS plays, ` + watchedSumFor(s.repo.db) + ` AS secs, ` + rowServer + `
			FROM stream_sessions WHERE media_type = 'episode' AND started_at >= ? AND grandparent_title <> ''` + srv + `
			GROUP BY t ORDER BY ` + orderBy(byDuration) + ` LIMIT ?`
	}
	rows, err := s.repo.db.QueryContext(ctx, q, since, libraryTopN)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []LibraryItem{}
	for rows.Next() {
		it := LibraryItem{Kind: kind}
		var thumb string
		if err := rows.Scan(&it.Title, &it.Year, &thumb, &it.RatingKey, &it.Plays, &it.Secs, &it.Server); err != nil {
			return nil, err
		}
		it.ThumbURL = proxyImage(thumb)
		s.identify(&it)
		out = append(out, it)
	}
	return out, rows.Err()
}

// neverWatched lists the indexed movies and shows added before cutoff that no recorded
// play matches. A film matches by rating key or title; a show by its rating key (recorded
// on episodes since PLEX-20) or, for older plays and imports, its name.
func (s *Service) neverWatched(ctx context.Context, cutoff time.Time) ([]LibraryItem, int, error) {
	seenKey, seenMovie, seenShow := map[string]bool{}, map[string]bool{}, map[string]bool{}
	rows, err := s.repo.db.QueryContext(ctx, `
		SELECT DISTINCT media_type, rating_key, grandparent_rating_key, title, grandparent_title FROM stream_sessions
		WHERE media_type IN ('movie','episode')`)
	if err != nil {
		return nil, 0, err
	}
	for rows.Next() {
		var mt, rk, grk, title, show string
		if err := rows.Scan(&mt, &rk, &grk, &title, &show); err != nil {
			rows.Close()
			return nil, 0, err
		}
		if mt == "movie" {
			seenKey[rk] = true
			seenMovie[strings.ToLower(title)] = true
		} else {
			seenKey[grk] = true
			seenShow[strings.ToLower(show)] = true
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	delete(seenKey, "")

	var all []LibraryItem
	for _, kind := range []string{"movie", "show"} {
		for _, it := range s.PlexItems(kind) {
			if it.AddedAt <= 0 || it.AddedAt > cutoff.Unix() || seenKey[it.RatingKey] {
				continue
			}
			if (kind == "movie" && seenMovie[strings.ToLower(it.Title)]) || (kind == "show" && seenShow[strings.ToLower(it.Title)]) {
				continue
			}
			li := LibraryItem{Kind: kind, Title: it.Title, Year: it.Year, ThumbURL: proxyImage(it.Thumb), RatingKey: it.RatingKey, AddedAt: it.AddedAt, Server: ServerPlex}
			s.identify(&li)
			all = append(all, li)
		}
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].AddedAt < all[j].AddedAt })
	total := len(all)
	if len(all) > neverWatchedMax {
		all = all[:neverWatchedMax]
	}
	if all == nil {
		all = []LibraryItem{}
	}
	return all, total, nil
}

// hotKey groups a play by title: a film by its rating key (its title when an import has
// none), an episode by its show's.
const hotKey = `CASE WHEN media_type = 'episode'
		THEN 's|' || CASE WHEN grandparent_rating_key <> '' THEN grandparent_rating_key ELSE lower(grandparent_title) END
		ELSE 'm|' || CASE WHEN rating_key <> '' THEN rating_key ELSE lower(title) END END`

// hotSpots ranks the titles transcoded most often in the window, with the most common
// source → stream video pair, the platforms doing it and the usual buffer cause.
func (s *Service) hotSpots(ctx context.Context, since int64, server string) ([]HotSpot, error) {
	type agg struct {
		HotSpot
		pairs, platforms map[string]int
		causes           map[string]int64
		order            int
	}
	byKey := map[string]*agg{}
	// A title's key is its rating key, which is one server's: each server's transcodes of a
	// title are their own row.
	srv := serverAnd("", server)
	rows, err := s.repo.db.QueryContext(ctx, `
		SELECT `+hotKey+`, media_type, title, grandparent_title, year, rating_key, grandparent_rating_key, thumb, grandparent_thumb,
			video_src, video_stream, audio_src, audio_stream, platform, server
		FROM stream_sessions WHERE started_at >= ? AND decision = 'transcode' AND media_type IN ('movie','episode')`+srv+`
		ORDER BY started_at DESC`, since)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var key, mt, title, show, rk, grk, thumb, gthumb, vsrc, vstream, asrc, astream, platform, server string
		var year int
		if err := rows.Scan(&key, &mt, &title, &show, &year, &rk, &grk, &thumb, &gthumb, &vsrc, &vstream, &asrc, &astream, &platform, &server); err != nil {
			rows.Close()
			return nil, err
		}
		a := byKey[key]
		if a == nil {
			a = &agg{pairs: map[string]int{}, platforms: map[string]int{}, causes: map[string]int64{}, order: len(byKey)}
			// The newest play names the title and gives its poster.
			if mt == "episode" {
				a.LibraryItem = LibraryItem{Kind: "show", Title: show, RatingKey: grk, ThumbURL: proxyImage(posterPath(mt, thumb, gthumb))}
			} else {
				a.LibraryItem = LibraryItem{Kind: "movie", Title: title, Year: year, RatingKey: rk, ThumbURL: proxyImage(thumb)}
			}
			a.Server = server
			byKey[key] = a
		}
		a.Transcodes++
		// What was converted: the video when it was, else the audio (video copied).
		switch {
		case vstream != "":
			a.pairs[orDash(vsrc)+" → "+vstream]++
		case astream != "":
			a.pairs["Audio "+orDash(asrc)+" → "+astream]++
		case vsrc != "":
			// Recorded live, nothing converted: the container or protocol was remuxed.
			// (Imported plays carry no stream details at all and add nothing here.)
			a.pairs["Remux (video and audio copied)"]++
		}
		if platform != "" {
			a.platforms[platform]++
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(byKey) == 0 {
		return []HotSpot{}, nil
	}

	// Every play of these titles in the window, for "n of m plays", and the buffer causes
	// seen while they were transcoding.
	rows, err = s.repo.db.QueryContext(ctx, `SELECT `+hotKey+`, COUNT(*) FROM stream_sessions
		WHERE started_at >= ? AND media_type IN ('movie','episode')`+srv+` GROUP BY 1`, since)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var key string
		var n int
		if err := rows.Scan(&key, &n); err != nil {
			rows.Close()
			return nil, err
		}
		if a := byKey[key]; a != nil {
			a.TotalPlays = n
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows, err = s.repo.db.QueryContext(ctx, `SELECT `+hotKey+`, COALESCE(NULLIF(b.cause,''),'unknown'), COUNT(*)
		FROM buffer_events b JOIN stream_sessions ON stream_sessions.id = b.session_id
		WHERE started_at >= ? AND decision = 'transcode' AND media_type IN ('movie','episode')`+serverAnd("stream_sessions.", server)+` GROUP BY 1, 2`, since)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var key, cause string
		var n int64
		if err := rows.Scan(&key, &cause, &n); err != nil {
			rows.Close()
			return nil, err
		}
		if a := byKey[key]; a != nil {
			a.causes[cause] += n
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	list := make([]*agg, 0, len(byKey))
	for _, a := range byKey {
		list = append(list, a)
	}
	// Most transcodes first; ties go to the more recently transcoded.
	sort.Slice(list, func(i, j int) bool {
		if list[i].Transcodes != list[j].Transcodes {
			return list[i].Transcodes > list[j].Transcodes
		}
		return list[i].order < list[j].order
	})
	if len(list) > libraryTopN {
		list = list[:libraryTopN]
	}
	out := make([]HotSpot, 0, len(list))
	for _, a := range list {
		h := a.HotSpot
		h.TopPair = topOf(a.pairs)
		h.Platforms = topN(a.platforms, 3)
		if c := topOf64(a.causes); c != "" {
			h.TopCause, h.CauseLabel = c, causeLabel(c)
		}
		s.identify(&h.LibraryItem)
		out = append(out, h)
	}
	return out, nil
}

func orDash(v string) string {
	if v == "" {
		return "—"
	}
	return v
}

// topN is the n most common keys, most common first (ties alphabetical, so it's stable).
func topN(m map[string]int, n int) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if m[keys[i]] != m[keys[j]] {
			return m[keys[i]] > m[keys[j]]
		}
		return keys[i] < keys[j]
	})
	if len(keys) > n {
		keys = keys[:n]
	}
	return keys
}

func topOf(m map[string]int) string {
	if t := topN(m, 1); len(t) > 0 {
		return t[0]
	}
	return ""
}

func topOf64(m map[string]int64) string {
	best, bestN := "", int64(-1)
	for k, n := range m {
		if n > bestN || (n == bestN && k < best) {
			best, bestN = k, n
		}
	}
	return best
}
