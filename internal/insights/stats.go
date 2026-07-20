package insights

import (
	"context"
	"strconv"
	"time"

	"github.com/tristenlammi/arrmada/internal/geoip"
)

// watchedExpr is the per-row watched seconds (wall time minus paused).
const watchedExpr = `((stopped_at - started_at) - paused_ms/1000)`

// --- repo: aggregate queries over stream_sessions ---

type titleStatRow struct {
	Title string
	Thumb string
	Plays int
	Secs  int64
}
type nameStatRow struct {
	ID    string
	Name  string
	Plays int
	Secs  int64
}

func orderBy(byDuration bool) string {
	if byDuration {
		return "secs DESC"
	}
	return "plays DESC"
}

func (r *repo) topTitles(ctx context.Context, mediaType, groupCol string, since int64, byDuration bool, limit int) ([]titleStatRow, error) {
	q := `SELECT ` + groupCol + ` AS t, MAX(thumb) AS th, COUNT(*) AS plays, SUM(` + watchedExpr + `) AS secs
		FROM stream_sessions WHERE media_type = ? AND started_at >= ? AND ` + groupCol + ` <> ''
		GROUP BY ` + groupCol + ` ORDER BY ` + orderBy(byDuration) + ` LIMIT ?`
	rows, err := r.db.QueryContext(ctx, q, mediaType, since, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []titleStatRow
	for rows.Next() {
		var s titleStatRow
		if err := rows.Scan(&s.Title, &s.Thumb, &s.Plays, &s.Secs); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func (r *repo) topNames(ctx context.Context, idCol, nameCol string, since int64, byDuration bool, limit int) ([]nameStatRow, error) {
	q := `SELECT ` + idCol + ` AS id, ` + nameCol + ` AS name, COUNT(*) AS plays, SUM(` + watchedExpr + `) AS secs
		FROM stream_sessions WHERE started_at >= ? AND ` + nameCol + ` <> ''
		GROUP BY ` + nameCol + ` ORDER BY ` + orderBy(byDuration) + ` LIMIT ?`
	rows, err := r.db.QueryContext(ctx, q, since, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []nameStatRow
	for rows.Next() {
		var s nameStatRow
		if err := rows.Scan(&s.ID, &s.Name, &s.Plays, &s.Secs); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

type userStatRow struct {
	ID, Username, Thumb                         string
	LastSeen                                    int64
	LastIP, LastPlatform, LastPlayer, LastTitle string
	TotalPlays                                  int
	TotalSecs                                   int64
}

func (r *repo) users(ctx context.Context) ([]userStatRow, error) {
	q := `SELECT u.id, u.username, u.thumb,
			COALESCE(a.last,0), COALESCE(a.plays,0), COALESCE(a.secs,0),
			COALESCE(l.ip_address,''), COALESCE(l.platform,''), COALESCE(l.player,''), COALESCE(l.title,'')
		FROM plex_users u
		LEFT JOIN (SELECT user_id, COUNT(*) plays, SUM(` + watchedExpr + `) secs, MAX(started_at) last
				   FROM stream_sessions GROUP BY user_id) a ON a.user_id = u.id
		LEFT JOIN stream_sessions l ON l.user_id = u.id AND l.started_at = a.last
		ORDER BY COALESCE(a.plays,0) DESC, u.username ASC`
	rows, err := r.db.QueryContext(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []userStatRow
	for rows.Next() {
		var u userStatRow
		if err := rows.Scan(&u.ID, &u.Username, &u.Thumb, &u.LastSeen, &u.TotalPlays, &u.TotalSecs,
			&u.LastIP, &u.LastPlatform, &u.LastPlayer, &u.LastTitle); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// --- service: watch statistics, users, libraries, recently-added ---

// TitleStat / NameStat are ranked entries for the watch-statistics cards.
type TitleStat struct {
	Title    string `json:"title"`
	ThumbURL string `json:"thumb_url"`
	Plays    int    `json:"plays"`
	Secs     int64  `json:"secs"`
}
type NameStat struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Plays int    `json:"plays"`
	Secs  int64  `json:"secs"`
}

// Stats is the home watch-statistics bundle over a window.
type Stats struct {
	Movies    []TitleStat    `json:"most_watched_movies"`
	Shows     []TitleStat    `json:"most_watched_shows"`
	Users     []NameStat     `json:"most_active_users"`
	Platforms []NameStat     `json:"most_active_platforms"`
	Recent    []HistoryEntry `json:"recently_watched"`
}

// Stats computes the watch-statistics cards over the last windowDays.
func (s *Service) Stats(ctx context.Context, windowDays int, byDuration bool) (Stats, error) {
	if windowDays <= 0 {
		windowDays = 30
	}
	since := time.Now().AddDate(0, 0, -windowDays).Unix()
	const n = 5
	movies, err := s.repo.topTitles(ctx, "movie", "title", since, byDuration, n)
	if err != nil {
		return Stats{}, err
	}
	shows, _ := s.repo.topTitles(ctx, "episode", "grandparent_title", since, byDuration, n)
	users, _ := s.repo.topNames(ctx, "user_id", "user_name", since, byDuration, n)
	plats, _ := s.repo.topNames(ctx, "platform", "platform", since, byDuration, n)
	recent, _, _ := s.repo.history(ctx, HistoryFilter{Limit: 8})

	out := Stats{
		Movies:    toTitleStats(movies),
		Shows:     toTitleStats(shows),
		Users:     toNameStats(users),
		Platforms: toNameStats(plats),
		Recent:    make([]HistoryEntry, 0, len(recent)),
	}
	for _, r := range recent {
		out.Recent = append(out.Recent, HistoryEntry{HistoryRow: r, ThumbURL: proxyImage(r.Thumb), Subtitle: historySubtitle(r)})
	}
	return out, nil
}

func toTitleStats(rows []titleStatRow) []TitleStat {
	out := make([]TitleStat, 0, len(rows))
	for _, r := range rows {
		out = append(out, TitleStat{Title: r.Title, ThumbURL: proxyImage(r.Thumb), Plays: r.Plays, Secs: r.Secs})
	}
	return out
}
func toNameStats(rows []nameStatRow) []NameStat {
	out := make([]NameStat, 0, len(rows))
	for _, r := range rows {
		out = append(out, NameStat{ID: r.ID, Name: r.Name, Plays: r.Plays, Secs: r.Secs})
	}
	return out
}

// UserEntry is one row of the Users table.
type UserEntry struct {
	ID           string         `json:"id"`
	Username     string         `json:"username"`
	LastSeen     int64          `json:"last_seen"`
	LastIP       string         `json:"last_ip"`
	LastPlatform string         `json:"last_platform"`
	LastPlayer   string         `json:"last_player"`
	LastTitle    string         `json:"last_title"`
	TotalPlays   int            `json:"total_plays"`
	TotalSecs    int64          `json:"total_secs"`
	Geo          geoip.Location `json:"geo"`
}

// Users returns per-user activity aggregates.
func (s *Service) Users(ctx context.Context) ([]UserEntry, error) {
	rows, err := s.repo.users(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]UserEntry, 0, len(rows))
	for _, u := range rows {
		out = append(out, UserEntry{
			ID: u.ID, Username: u.Username, LastSeen: u.LastSeen, LastIP: u.LastIP,
			LastPlatform: u.LastPlatform, LastPlayer: u.LastPlayer, LastTitle: u.LastTitle,
			TotalPlays: u.TotalPlays, TotalSecs: u.TotalSecs, Geo: s.geo.Lookup(u.LastIP),
		})
	}
	return out, nil
}

// LibraryStat is a library section with its item count.
type LibraryStat struct {
	Title string `json:"title"`
	Type  string `json:"type"` // movie | show | artist
	Count int64  `json:"count"`
}

// Libraries returns the Plex library sections with counts (live from Plex).
func (s *Service) Libraries(ctx context.Context) ([]LibraryStat, error) {
	c := s.client(ctx)
	libs, err := c.Libraries(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]LibraryStat, 0, len(libs))
	for _, l := range libs {
		count, _ := c.SectionTotal(ctx, l.Key)
		out = append(out, LibraryStat{Title: l.Title, Type: l.Type, Count: count})
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
}

// RecentlyAdded returns recently-added library items (live from Plex).
func (s *Service) RecentlyAdded(ctx context.Context, limit int) ([]RecentItem, error) {
	items, err := s.client(ctx).RecentlyAdded(ctx, limit)
	if err != nil {
		return nil, err
	}
	out := make([]RecentItem, 0, len(items))
	for _, it := range items {
		title, sub := it.Title, ""
		if it.Type == "episode" && it.GrandparentTitle != "" {
			title = it.GrandparentTitle
			sub = it.Title
		} else if it.Year > 0 {
			sub = strconv.Itoa(it.Year)
		}
		out = append(out, RecentItem{Title: title, Subtitle: sub, Type: it.Type, ThumbURL: proxyImage(it.Thumb), AddedAt: it.AddedAt})
	}
	return out, nil
}
