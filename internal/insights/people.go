package insights

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/tristenlammi/arrmada/internal/geoip"
)

// The People page: one Plex user's viewing — totals, what they watch, on which devices
// and from where, how far they get, and how often their streams stall. Staff only, like
// the rest of Insights. It reads Plex video plays (stream_sessions) and nothing else:
// audiobook listening lives in its own tables and never appears here.

// ErrPersonNotFound is a user id with no Plex account row and no recorded plays.
var ErrPersonNotFound = errors.New("no such Plex user")

// PersonProfile is everything the People page shows for one person.
type PersonProfile struct {
	ID string `json:"id"`
	// Server is the server this identity watches on (plex | jellyfin). Someone on both
	// has a profile per server. Stalls stay zero for Jellyfin: it reports no buffering.
	Server     string `json:"server"`
	Username   string `json:"username"`
	AvatarURL  string `json:"avatar_url"`
	WindowDays int    `json:"window_days"` // 0 = all time
	FirstSeen  int64  `json:"first_seen"`  // first play in the window (unix seconds)
	LastSeen   int64  `json:"last_seen"`
	TotalPlays int    `json:"total_plays"`
	TotalSecs  int64  `json:"total_secs"` // watchedSum, as the People tab totals it
	// CompletionPct is the share of plays with a known runtime that reached 90%;
	// CompletionOf is how many plays that is out of (0 = nothing to judge by).
	CompletionPct float64         `json:"completion_pct"`
	CompletionOf  int             `json:"completion_of"`
	TopTitles     []TitleStat     `json:"top_titles"`
	Platforms     []NameStat      `json:"platforms"`
	Players       []NameStat      `json:"players"`
	IPs           []PersonIP      `json:"ips"`
	Decisions     PersonDecisions `json:"decisions"`
	Stalls        PersonStalls    `json:"stalls"`
	Recent        []HistoryEntry  `json:"recent"`
}

// PersonIP is one address a person streamed from.
type PersonIP struct {
	IP       string         `json:"ip"`
	Geo      geoip.Location `json:"geo"`
	Plays    int            `json:"plays"`
	LastSeen int64          `json:"last_seen"`
}

// PersonDecisions counts a person's plays by how they streamed.
type PersonDecisions struct {
	DirectPlay   int `json:"direct_play"`
	DirectStream int `json:"direct_stream"`
	Transcode    int `json:"transcode"`
}

// PersonStalls is a person's observed buffering: stall events, the stall time measured,
// and the share of their plays that stalled at least once.
type PersonStalls struct {
	Events  int     `json:"events"`
	StallMS int64   `json:"stall_ms"`
	RatePct float64 `json:"rate_pct"`
}

// avatarURL passes on a Plex account avatar only when it is a plex.tv address: the page
// loads it straight from there, so anything else (a server path, another host) is
// dropped rather than handed to the browser. If a content-security policy ever limits
// img-src, this becomes a proxied endpoint. A Jellyfin person's picture is a Jellyfin
// image path, which goes through the image proxy (the server's key stays here).
func avatarURL(thumb string) string {
	if strings.HasPrefix(thumb, "https://plex.tv/") {
		return thumb
	}
	if IsJellyfinImage(thumb) {
		return proxyImage(thumb)
	}
	return ""
}

// Person builds one person's profile over the last windowDays (0 = all time, matching
// the People tab's totals). Every query filters on user_id (idx_stream_sessions_user).
func (s *Service) Person(ctx context.Context, userID string, windowDays int) (PersonProfile, error) {
	if userID == "" {
		return PersonProfile{}, ErrPersonNotFound
	}
	p := PersonProfile{ID: userID, WindowDays: max(windowDays, 0),
		TopTitles: []TitleStat{}, Platforms: []NameStat{}, Players: []NameStat{}, IPs: []PersonIP{}, Recent: []HistoryEntry{}}
	var since int64
	if windowDays > 0 {
		since = windowStart(windowDays)
	}
	db := s.repo.db

	var thumb string
	err := db.QueryRowContext(ctx, `SELECT username, thumb, server FROM plex_users WHERE id = ?`, userID).Scan(&p.Username, &thumb, &p.Server)
	known := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return PersonProfile{}, err
	}
	p.AvatarURL = avatarURL(thumb)

	var plays, completed, judged int
	var first, last, secs sql.NullInt64
	var dp, ds, tc, buffered, events int
	err = db.QueryRowContext(ctx, `
		SELECT COUNT(*), MIN(started_at), MAX(started_at), `+watchedSumFor(db)+`,
			COALESCE(SUM(CASE WHEN duration_ms > 0 AND view_offset_ms * 10 >= duration_ms * 9 THEN 1 ELSE 0 END),0),
			COALESCE(SUM(CASE WHEN duration_ms > 0 THEN 1 ELSE 0 END),0),
			COALESCE(SUM(CASE WHEN decision = 'direct_stream' THEN 1 ELSE 0 END),0),
			COALESCE(SUM(CASE WHEN decision = 'transcode' THEN 1 ELSE 0 END),0),
			COALESCE(SUM(CASE WHEN buffer_count > 0 THEN 1 ELSE 0 END),0),
			COALESCE(SUM(buffer_count),0)
		FROM stream_sessions WHERE user_id = ? AND started_at >= ?`, userID, since).
		Scan(&plays, &first, &last, &secs, &completed, &judged, &ds, &tc, &buffered, &events)
	if err != nil {
		return PersonProfile{}, err
	}
	if !known && plays == 0 {
		// No account row and nothing in the window: does this person exist at all?
		var any int
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM stream_sessions WHERE user_id = ?`, userID).Scan(&any); err != nil {
			return PersonProfile{}, err
		}
		if any == 0 {
			return PersonProfile{}, ErrPersonNotFound
		}
	}
	// A play with no recognised decision counts as direct play, as History shows it.
	dp = plays - ds - tc
	p.TotalPlays, p.FirstSeen, p.LastSeen, p.TotalSecs = plays, first.Int64, last.Int64, secs.Int64
	p.Decisions = PersonDecisions{DirectPlay: dp, DirectStream: ds, Transcode: tc}
	p.CompletionOf = judged
	if judged > 0 {
		p.CompletionPct = round1(float64(completed) * 100 / float64(judged))
	}
	p.Stalls.Events = events
	if plays > 0 {
		p.Stalls.RatePct = round1(float64(buffered) * 100 / float64(plays))
	}
	err = db.QueryRowContext(ctx, `
		SELECT COALESCE(SUM(b.duration_ms),0) FROM buffer_events b JOIN stream_sessions s ON s.id = b.session_id
		WHERE s.user_id = ? AND s.started_at >= ?`, userID, since).Scan(&p.Stalls.StallMS)
	if err != nil {
		return PersonProfile{}, err
	}
	if (p.Username == "" || p.Server == "") && plays > 0 {
		var name, server string
		_ = db.QueryRowContext(ctx, `SELECT user_name, server FROM stream_sessions WHERE user_id = ? ORDER BY started_at DESC LIMIT 1`, userID).Scan(&name, &server)
		if p.Username == "" {
			p.Username = name
		}
		if p.Server == "" {
			p.Server = server
		}
	}
	if p.Server == "" {
		p.Server = ServerPlex
	}

	// Top titles: a film by its title, a show by its name, with the show's poster.
	rows, err := db.QueryContext(ctx, `
		SELECT CASE WHEN media_type = 'episode' AND grandparent_title <> '' THEN grandparent_title ELSE title END AS t,
			COALESCE(NULLIF(MAX(CASE WHEN media_type = 'episode' THEN grandparent_thumb ELSE '' END),''), MAX(thumb)) AS th,
			COUNT(*) AS plays, `+watchedSumFor(db)+` AS secs
		FROM stream_sessions WHERE user_id = ? AND started_at >= ? AND media_type IN ('movie','episode')
		AND (CASE WHEN media_type = 'episode' AND grandparent_title <> '' THEN grandparent_title ELSE title END) <> ''
		GROUP BY t ORDER BY plays DESC, secs DESC LIMIT 12`, userID, since)
	if err != nil {
		return PersonProfile{}, err
	}
	for rows.Next() {
		var r titleStatRow
		if err := rows.Scan(&r.Title, &r.Thumb, &r.Plays, &r.Secs); err != nil {
			rows.Close()
			return PersonProfile{}, err
		}
		p.TopTitles = append(p.TopTitles, TitleStat{Title: r.Title, ThumbURL: proxyImage(r.Thumb), Plays: r.Plays, Secs: r.Secs})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return PersonProfile{}, err
	}

	if p.Platforms, err = s.personNames(ctx, "platform", userID, since); err != nil {
		return PersonProfile{}, err
	}
	if p.Players, err = s.personNames(ctx, "player", userID, since); err != nil {
		return PersonProfile{}, err
	}

	rows, err = db.QueryContext(ctx, `
		SELECT ip_address, COUNT(*), MAX(started_at) FROM stream_sessions
		WHERE user_id = ? AND started_at >= ? AND ip_address <> ''
		GROUP BY ip_address ORDER BY MAX(started_at) DESC LIMIT 20`, userID, since)
	if err != nil {
		return PersonProfile{}, err
	}
	for rows.Next() {
		var ip PersonIP
		if err := rows.Scan(&ip.IP, &ip.Plays, &ip.LastSeen); err != nil {
			rows.Close()
			return PersonProfile{}, err
		}
		if s.geo != nil {
			ip.Geo = s.geo.Lookup(ip.IP)
		} else {
			ip.Geo = geoip.Location{IP: ip.IP}
		}
		p.IPs = append(p.IPs, ip)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return PersonProfile{}, err
	}

	recent, _, err := s.repo.history(ctx, HistoryFilter{UserID: userID, From: since, Limit: 10})
	if err != nil {
		return PersonProfile{}, err
	}
	for _, r := range recent {
		p.Recent = append(p.Recent, s.toHistoryEntry(r))
	}
	return p, nil
}

// personNames ranks one person's plays by a column (platform, player).
func (s *Service) personNames(ctx context.Context, col, userID string, since int64) ([]NameStat, error) {
	rows, err := s.repo.db.QueryContext(ctx, `
		SELECT `+col+`, COUNT(*) AS plays, `+watchedSumFor(s.repo.db)+` AS secs FROM stream_sessions
		WHERE user_id = ? AND started_at >= ? AND `+col+` <> ''
		GROUP BY `+col+` ORDER BY plays DESC LIMIT 10`, userID, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []NameStat{}
	for rows.Next() {
		var n NameStat
		if err := rows.Scan(&n.Name, &n.Plays, &n.Secs); err != nil {
			return nil, err
		}
		n.ID = n.Name
		out = append(out, n)
	}
	return out, rows.Err()
}
