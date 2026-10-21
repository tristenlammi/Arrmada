package insights

import (
	"context"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/tristenlammi/arrmada/internal/geoip"
	"github.com/tristenlammi/arrmada/internal/jellyfin"
	"github.com/tristenlammi/arrmada/internal/store"
)

// watchedExprFor is the per-row watched seconds.
//
// Prefer what the source actually reported (watched_ms). Falling back to wall time is only
// sound for a live-tracked session, where the poller observes both the start and the stop.
// An imported Tautulli row's wall time also spans every minute a client sat idle without
// sending a stop — which is how the Users tab came to show 24-34 hours per play against a
// real average nearer half an hour.
//
// The reported figure is still capped at the wall span: a source can under-report watch
// time honestly, but it can never have watched longer than the session existed.
//
// Each branch clamps at 0 so a corrupt or in-progress row (stopped_at <= started_at, or a
// paused span longer than the wall time) can't drag a total negative. Play COUNT(*) still
// includes such rows — only the watch time is clamped.
func watchedExprFor(q store.Execer) string {
	// These time/count columns are NOT NULL in both schemas. Keep integer
	// division so SQL totals and watchedSecs agree even for partial seconds.
	return `(CASE WHEN watched_ms > 0 THEN ` + store.ScalarMin(q, "watched_ms/1000", store.ScalarMax(q, "0", "stopped_at - started_at")) +
		` ELSE ` + store.ScalarMax(q, "0", "(stopped_at - started_at) - paused_ms/1000") + ` END)`
}

// watchedSumFor sums per-row watched seconds.
func watchedSumFor(q store.Execer) string { return `SUM(` + watchedExprFor(q) + `)` }

// windowStart returns the unix-second start of a windowDays window aligned to LOCAL midnight, so
// day-grouped SQL (which uses 'localtime') and the day-axis labels agree at the window boundary.
// It spans windowDays calendar days ending today. Shared by Stats, Graphs and Reliability so the
// three tabs report over the same span.
func windowStart(windowDays int) int64 {
	if windowDays <= 0 {
		windowDays = 30
	}
	t := time.Now()
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.Local).
		AddDate(0, 0, -(windowDays - 1)).Unix()
}

// --- repo: aggregate queries over stream_sessions ---

type titleStatRow struct {
	Title  string
	Thumb  string
	Plays  int
	Secs   int64
	Server string
}
type nameStatRow struct {
	ID     string
	Name   string
	Plays  int
	Secs   int64
	Server string
}

func orderBy(byDuration bool) string {
	if byDuration {
		return "secs DESC"
	}
	return "plays DESC"
}

func (r *repo) topTitles(ctx context.Context, mediaType, groupCol string, since int64, byDuration bool, limit int, server string) ([]titleStatRow, error) {
	// An episode's own thumb is a landscape still: a show's row takes its poster, recorded
	// since PLEX-20, and falls back to the thumb only for older plays and imports (whose
	// Tautulli thumb is already a poster).
	thumb := `MAX(thumb)`
	if mediaType == "episode" {
		thumb = `COALESCE(NULLIF(MAX(grandparent_thumb),''), MAX(thumb))`
	}
	q := `SELECT ` + groupCol + ` AS t, ` + thumb + ` AS th, COUNT(*) AS plays, ` + watchedSumFor(r.db) + ` AS secs, ` + rowServer + `
		FROM stream_sessions WHERE media_type = ? AND started_at >= ? AND ` + groupCol + ` <> ''` + serverAnd("", server) + `
		GROUP BY ` + groupCol + ` ORDER BY ` + orderBy(byDuration) + ` LIMIT ?`
	rows, err := r.db.QueryContext(ctx, q, mediaType, since, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []titleStatRow
	for rows.Next() {
		var s titleStatRow
		if err := rows.Scan(&s.Title, &s.Thumb, &s.Plays, &s.Secs, &s.Server); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func (r *repo) topNames(ctx context.Context, idCol, nameCol string, since int64, byDuration bool, limit int, server string) ([]nameStatRow, error) {
	// Group by the ID column (not the display name) so the id the UI uses for drill-down links is a
	// real one, and carry a representative name via MAX(nameCol) rather than an arbitrary pick.
	q := `SELECT ` + idCol + ` AS id, MAX(` + nameCol + `) AS name, COUNT(*) AS plays, ` + watchedSumFor(r.db) + ` AS secs, ` + rowServer + `
		FROM stream_sessions WHERE started_at >= ? AND ` + idCol + ` <> ''` + serverAnd("", server) + `
		GROUP BY ` + idCol + ` ORDER BY ` + orderBy(byDuration) + ` LIMIT ?`
	rows, err := r.db.QueryContext(ctx, q, since, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []nameStatRow
	for rows.Next() {
		var s nameStatRow
		if err := rows.Scan(&s.ID, &s.Name, &s.Plays, &s.Secs, &s.Server); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

type userStatRow struct {
	ID, Username, Thumb, Server                 string
	LastSeen                                    int64
	LastIP, LastPlatform, LastPlayer, LastTitle string
	TotalPlays                                  int
	TotalSecs                                   int64
}

func (r *repo) users(ctx context.Context, server string) ([]userStatRow, error) {
	// The last-session join is pinned to a single row via MAX(id) at that started_at, so two
	// sessions sharing a started_at don't fan the user out into duplicate rows.
	q := `SELECT u.id, u.username, u.thumb, u.server,
			COALESCE(a.last,0), COALESCE(a.plays,0), COALESCE(a.secs,0),
			COALESCE(l.ip_address,''), COALESCE(l.platform,''), COALESCE(l.player,''), COALESCE(l.title,'')
		FROM plex_users u
		LEFT JOIN (SELECT user_id, COUNT(*) plays, ` + watchedSumFor(r.db) + ` secs, MAX(started_at) last
				   FROM stream_sessions GROUP BY user_id) a ON a.user_id = u.id
		LEFT JOIN stream_sessions l ON l.id = (
			SELECT x.id FROM stream_sessions x
			WHERE x.user_id = u.id AND x.started_at = a.last
			ORDER BY x.id DESC LIMIT 1)` + serverWhere("u.", server) + `
		ORDER BY COALESCE(a.plays,0) DESC, u.username ASC`
	rows, err := r.db.QueryContext(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []userStatRow
	for rows.Next() {
		var u userStatRow
		if err := rows.Scan(&u.ID, &u.Username, &u.Thumb, &u.Server, &u.LastSeen, &u.TotalPlays, &u.TotalSecs,
			&u.LastIP, &u.LastPlatform, &u.LastPlayer, &u.LastTitle); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// --- service: watch statistics, users, libraries, recently-added ---

// TitleStat / NameStat are ranked entries for the watch-statistics cards. Server is the
// server the entry's plays were on, "" when they were on both (a film watched on each).
type TitleStat struct {
	Title    string `json:"title"`
	ThumbURL string `json:"thumb_url"`
	Plays    int    `json:"plays"`
	Secs     int64  `json:"secs"`
	Server   string `json:"server"`
}
type NameStat struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Plays  int    `json:"plays"`
	Secs   int64  `json:"secs"`
	Server string `json:"server"`
}

// Stats is the home watch-statistics bundle over a window.
type Stats struct {
	Movies    []TitleStat    `json:"most_watched_movies"`
	Shows     []TitleStat    `json:"most_watched_shows"`
	Users     []NameStat     `json:"most_active_users"`
	Platforms []NameStat     `json:"most_active_platforms"`
	Recent    []HistoryEntry `json:"recently_watched"`
}

// Stats computes the watch-statistics cards over the last windowDays, on one server or
// both (server "").
func (s *Service) Stats(ctx context.Context, windowDays int, byDuration bool, server string) (Stats, error) {
	if windowDays <= 0 {
		windowDays = 30
	}
	since := windowStart(windowDays)
	const n = 5
	movies, err := s.repo.topTitles(ctx, "movie", "title", since, byDuration, n, server)
	if err != nil {
		return Stats{}, err
	}
	shows, _ := s.repo.topTitles(ctx, "episode", "grandparent_title", since, byDuration, n, server)
	users, _ := s.repo.topNames(ctx, "user_id", "user_name", since, byDuration, n, server)
	plats, _ := s.repo.topNames(ctx, "platform", "platform", since, byDuration, n, server)
	recent, _, _ := s.repo.history(ctx, HistoryFilter{Limit: 8, Server: server})

	out := Stats{
		Movies:    toTitleStats(movies),
		Shows:     toTitleStats(shows),
		Users:     toNameStats(users),
		Platforms: toNameStats(plats),
		Recent:    make([]HistoryEntry, 0, len(recent)),
	}
	for _, r := range recent {
		out.Recent = append(out.Recent, s.toHistoryEntry(r))
	}
	return out, nil
}

func toTitleStats(rows []titleStatRow) []TitleStat {
	out := make([]TitleStat, 0, len(rows))
	for _, r := range rows {
		out = append(out, TitleStat{Title: r.Title, ThumbURL: proxyImage(r.Thumb), Plays: r.Plays, Secs: r.Secs, Server: r.Server})
	}
	return out
}
func toNameStats(rows []nameStatRow) []NameStat {
	out := make([]NameStat, 0, len(rows))
	for _, r := range rows {
		out = append(out, NameStat{ID: r.ID, Name: r.Name, Plays: r.Plays, Secs: r.Secs, Server: r.Server})
	}
	return out
}

// UserEntry is one row of the Users table.
type UserEntry struct {
	ID           string         `json:"id"`
	Server       string         `json:"server"` // one row per server identity: the server this person watches on
	Username     string         `json:"username"`
	AvatarURL    string         `json:"avatar_url"` // a plex.tv avatar, a proxied Jellyfin picture, or "" (see avatarURL)
	LastSeen     int64          `json:"last_seen"`
	LastIP       string         `json:"last_ip"`
	LastPlatform string         `json:"last_platform"`
	LastPlayer   string         `json:"last_player"`
	LastTitle    string         `json:"last_title"`
	TotalPlays   int            `json:"total_plays"`
	TotalSecs    int64          `json:"total_secs"`
	Geo          geoip.Location `json:"geo"`
}

// Users returns per-user activity aggregates, of one server's people or both (server "").
func (s *Service) Users(ctx context.Context, server string) ([]UserEntry, error) {
	rows, err := s.repo.users(ctx, server)
	if err != nil {
		return nil, err
	}
	out := make([]UserEntry, 0, len(rows))
	for _, u := range rows {
		out = append(out, UserEntry{
			ID: u.ID, Server: u.Server, Username: u.Username, AvatarURL: avatarURL(u.Thumb), LastSeen: u.LastSeen, LastIP: u.LastIP,
			LastPlatform: u.LastPlatform, LastPlayer: u.LastPlayer, LastTitle: u.LastTitle,
			TotalPlays: u.TotalPlays, TotalSecs: u.TotalSecs, Geo: s.geo.Lookup(u.LastIP),
		})
	}
	return out, nil
}

// LibraryStat is a library section with its item count.
type LibraryStat struct {
	Title  string `json:"title"`
	Type   string `json:"type"` // movie | show | artist
	Count  int64  `json:"count"`
	Server string `json:"server"`
}

// Libraries returns the connected servers' libraries with counts, live from each (server
// "" for both). One server failing leaves its libraries out; it's an error only when every
// server asked failed.
func (s *Service) Libraries(ctx context.Context, server string) ([]LibraryStat, error) {
	out := []LibraryStat{}
	asked, failed := 0, 0
	var firstErr error
	if server != ServerJellyfin && s.configured(ctx) {
		asked++
		c := s.client(ctx)
		if libs, err := c.Libraries(ctx); err != nil {
			failed, firstErr = failed+1, err
		} else {
			for _, l := range libs {
				count, _ := c.SectionTotal(ctx, l.Key)
				out = append(out, LibraryStat{Title: l.Title, Type: l.Type, Count: count, Server: ServerPlex})
			}
		}
	}
	if w := s.jfw.Load(); server != ServerPlex && w != nil && w.svc.Configured(ctx) {
		asked++
		if libs, err := jellyfinLibraries(ctx, w.svc.Client(ctx)); err != nil {
			failed++
			if firstErr == nil {
				firstErr = err
			}
		} else {
			out = append(out, libs...)
		}
	}
	if asked == 0 {
		// Nothing connected: what Plex's client says (not configured).
		_, err := s.client(ctx).Libraries(ctx)
		return nil, err
	}
	if failed == asked {
		return nil, firstErr
	}
	return out, nil
}

// jellyfinLibraries is Jellyfin's movie, show and music libraries with their title counts.
func jellyfinLibraries(ctx context.Context, c *jellyfin.Client) ([]LibraryStat, error) {
	vfs, err := c.VirtualFolders(ctx)
	if err != nil {
		return nil, err
	}
	var out []LibraryStat
	for _, vf := range vfs {
		typ, items := "", []string(nil)
		switch vf.CollectionType {
		case "movies":
			typ, items = "movie", []string{jellyfin.TypeMovie}
		case "tvshows":
			typ, items = "show", []string{jellyfin.TypeSeries}
		case "music":
			typ, items = "artist", []string{"MusicArtist"}
		default:
			continue
		}
		st := LibraryStat{Title: vf.Name, Type: typ, Server: ServerJellyfin}
		if vf.ItemID != "" {
			// One item is enough: the answer's total is the count.
			if p, err := c.Items(ctx, jellyfin.ItemsQuery{ParentID: vf.ItemID, IncludeItemTypes: items, Recursive: true, Fields: []string{}, Limit: 1}); err == nil {
				st.Count = int64(p.TotalRecordCount)
			}
		}
		out = append(out, st)
	}
	return out, nil
}

// RecentItem is a recently-added item for the strip.
type RecentItem struct {
	Title    string `json:"title"`
	Subtitle string `json:"subtitle"`
	Type     string `json:"type"`
	ThumbURL string `json:"thumb_url"`
	AddedAt  int64  `json:"added_at"`
	Server   string `json:"server"`
}

// RecentlyAdded returns recently-added library items, live from the connected servers
// (server "" for both), newest first. Like Libraries, one server failing leaves its items
// out.
func (s *Service) RecentlyAdded(ctx context.Context, limit int, server string) ([]RecentItem, error) {
	if limit <= 0 {
		limit = 20
	}
	out := []RecentItem{}
	asked, failed := 0, 0
	var firstErr error
	if server != ServerJellyfin && s.configured(ctx) {
		asked++
		if items, err := s.client(ctx).RecentlyAdded(ctx, limit); err != nil {
			failed, firstErr = failed+1, err
		} else {
			for _, it := range items {
				title, sub := it.Title, ""
				if it.Type == "episode" && it.GrandparentTitle != "" {
					title = it.GrandparentTitle
					sub = it.Title
				} else if it.Year > 0 {
					sub = strconv.Itoa(it.Year)
				}
				out = append(out, RecentItem{Title: title, Subtitle: sub, Type: it.Type, ThumbURL: proxyImage(it.Thumb), AddedAt: it.AddedAt, Server: ServerPlex})
			}
		}
	}
	if w := s.jfw.Load(); server != ServerPlex && w != nil && w.svc.Configured(ctx) {
		asked++
		if items, err := w.svc.Client(ctx).RecentlyAdded(ctx, limit, ""); err != nil {
			failed++
			if firstErr == nil {
				firstErr = err
			}
		} else {
			for _, it := range items {
				ri := RecentItem{Title: it.Name, Type: "movie", ThumbURL: proxyImage(JellyfinItemImage(it.ID)), AddedAt: it.DateCreated.Unix(), Server: ServerJellyfin}
				if !isGUID(it.ID) {
					ri.ThumbURL = ""
				}
				if it.Type == jellyfin.TypeEpisode {
					ri.Type, ri.Title, ri.Subtitle = "episode", it.SeriesName, it.Name
					if isGUID(it.SeriesID) {
						ri.ThumbURL = proxyImage(JellyfinItemImage(it.SeriesID))
					}
				} else if it.ProductionYear > 0 {
					ri.Subtitle = strconv.Itoa(it.ProductionYear)
				}
				out = append(out, ri)
			}
		}
	}
	if asked == 0 {
		_, err := s.client(ctx).RecentlyAdded(ctx, limit)
		return nil, err
	}
	if failed == asked {
		return nil, firstErr
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].AddedAt > out[j].AddedAt })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// WatchedTitle is one thing a user has played, in library-matchable form: the
// show title for episodes, the movie title otherwise.
type WatchedTitle struct {
	Title     string
	MediaType string // "movie" | "episode"
	Year      int
}

// RecentlyWatchedByUser returns the distinct titles a person has recently watched, most
// recent first — the watch-history seed for the personalized "Recommended for you" row.
// userIDs are the person's ids on the media servers: the numeric Plex account id (what the
// Plex-login flow stores on the Arrmada user as plex_id) and/or their Jellyfin user id,
// which is what sessions key on; plays on either count. Episodes collapse to their show
// title so one binge is one seed.
func (s *Service) RecentlyWatchedByUser(ctx context.Context, limit int, userIDs ...string) ([]WatchedTitle, error) {
	var ids []any
	for _, id := range userIDs {
		if id != "" {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return nil, nil
	}
	if limit <= 0 {
		limit = 20
	}
	// GROUP BY the library-facing title so a re-watch or a multi-episode binge is a
	// single seed; order by the most recent play of each.
	rows, err := s.repo.db.QueryContext(ctx,
		`SELECT
		   CASE WHEN media_type = 'episode' AND grandparent_title != '' THEN grandparent_title ELSE title END AS lib_title,
		   media_type, MAX(year) AS year, MAX(started_at) AS last_at
		 FROM stream_sessions
		 WHERE user_id IN (?`+strings.Repeat(",?", len(ids)-1)+`) AND (CASE WHEN media_type = 'episode' AND grandparent_title != '' THEN grandparent_title ELSE title END) != ''
		 GROUP BY lib_title, media_type
		 ORDER BY last_at DESC
		 LIMIT ?`, append(ids, limit)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []WatchedTitle
	for rows.Next() {
		var w WatchedTitle
		var lastAt int64
		if err := rows.Scan(&w.Title, &w.MediaType, &w.Year, &lastAt); err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}
