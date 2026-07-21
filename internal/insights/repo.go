package insights

import (
	"context"
	"database/sql"
	"strings"
	"time"
)

type repo struct{ db *sql.DB }

// sessionRecord is a finalized play session ready to persist.
type sessionRecord struct {
	SessionKey       string
	UserID, UserName string
	RatingKey        string
	MediaType        string
	Title            string
	GrandparentTitle string
	ParentTitle      string
	MediaIndex       int
	ParentIndex      int
	Year             int
	Thumb            string
	Player           string
	Platform         string
	Product          string
	IPAddress        string
	Location         string
	Decision         string
	StartedAt        int64 // epoch seconds
	StoppedAt        int64
	PausedMS         int64
	ViewOffsetMS     int64
	DurationMS       int64
	VideoSrc         string
	VideoStream      string
	AudioSrc         string
	AudioStream      string
	ContainerSrc     string
	ContainerStream  string
	HWTranscode      bool
	BufferCount      int
}

func (r *repo) insertSession(ctx context.Context, s sessionRecord) (int64, error) {
	res, err := r.db.ExecContext(ctx, `
		INSERT INTO stream_sessions
		 (session_key,user_id,user_name,rating_key,media_type,title,grandparent_title,parent_title,
		  media_index,parent_index,year,thumb,player,platform,product,ip_address,location,decision,
		  started_at,stopped_at,paused_ms,view_offset_ms,duration_ms,
		  video_src,video_stream,audio_src,audio_stream,container_src,container_stream,hw_transcode,buffer_count)
		 VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		s.SessionKey, s.UserID, s.UserName, s.RatingKey, s.MediaType, s.Title, s.GrandparentTitle, s.ParentTitle,
		s.MediaIndex, s.ParentIndex, s.Year, s.Thumb, s.Player, s.Platform, s.Product, s.IPAddress, s.Location, s.Decision,
		s.StartedAt, s.StoppedAt, s.PausedMS, s.ViewOffsetMS, s.DurationMS,
		s.VideoSrc, s.VideoStream, s.AudioSrc, s.AudioStream, s.ContainerSrc, s.ContainerStream, b2i(s.HWTranscode), s.BufferCount)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// sessionExists reports whether a session for this user + item + start time is already recorded
// (used to keep history imports idempotent on re-run).
func (r *repo) sessionExists(ctx context.Context, userID, ratingKey string, startedAt int64) bool {
	var n int
	err := r.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM stream_sessions WHERE user_id = ? AND rating_key = ? AND started_at = ?`,
		userID, ratingKey, startedAt).Scan(&n)
	return err == nil && n > 0
}

func (r *repo) insertBufferEvent(ctx context.Context, sessionID, at, offsetMS, durationMS int64, cause, detail string) error {
	_, err := r.db.ExecContext(ctx, `INSERT INTO buffer_events (session_id,at,view_offset_ms,duration_ms,cause,detail) VALUES (?,?,?,?,?,?)`, sessionID, at, offsetMS, durationMS, cause, detail)
	return err
}

func (r *repo) insertBandwidth(ctx context.Context, at, total, lan, wan int64) error {
	_, err := r.db.ExecContext(ctx, `INSERT INTO bandwidth_samples (at,total_kbps,lan_kbps,wan_kbps) VALUES (?,?,?,?)`, at, total, lan, wan)
	return err
}

// pruneBandwidth deletes bandwidth_samples older than `before` (epoch seconds), returning the
// number of rows removed. The poller inserts a sample every cycle even when idle, so without
// pruning this table grows unbounded (~17k rows/day).
func (r *repo) pruneBandwidth(ctx context.Context, before int64) (int64, error) {
	res, err := r.db.ExecContext(ctx, `DELETE FROM bandwidth_samples WHERE at < ?`, before)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (r *repo) upsertUser(ctx context.Context, id, username, thumb string, at int64) error {
	if id == "" {
		return nil
	}
	// Keep the existing avatar when the incoming thumb is blank (imports may not carry one — don't
	// clobber a good avatar with ''), and never move last_seen_at backwards when importing old rows.
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO plex_users (id,username,thumb,last_seen_at) VALUES (?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET
			username=excluded.username,
			thumb=CASE WHEN excluded.thumb <> '' THEN excluded.thumb ELSE plex_users.thumb END,
			last_seen_at=MAX(plex_users.last_seen_at, excluded.last_seen_at)`,
		id, username, thumb, at)
	return err
}

// PruneBandwidth deletes bandwidth samples older than `before`, returning the number removed.
// A scheduler should call this periodically (e.g. PruneBandwidth(ctx, now.Add(-90*24*time.Hour)))
// because the poller records a bandwidth sample every cycle even with zero active streams.
func (s *Service) PruneBandwidth(ctx context.Context, before time.Time) (int64, error) {
	return s.repo.pruneBandwidth(ctx, before.Unix())
}

// HistoryFilter narrows the history query.
type HistoryFilter struct {
	UserID   string
	Type     string // movie | episode | track
	Decision string // direct_play | direct_stream | transcode
	Query    string // matches title / show / user
	Limit    int
	Offset   int
}

// HistoryRow is one recorded play, with the fields the table + deep-dive need.
type HistoryRow struct {
	ID               int64  `json:"id"`
	UserID           string `json:"user_id"`
	UserName         string `json:"user_name"`
	Title            string `json:"title"`
	GrandparentTitle string `json:"grandparent_title"`
	ParentTitle      string `json:"parent_title"`
	MediaIndex       int    `json:"media_index"`
	ParentIndex      int    `json:"parent_index"`
	Year             int    `json:"year"`
	MediaType        string `json:"media_type"`
	Thumb            string `json:"thumb"`
	Player           string `json:"player"`
	Platform         string `json:"platform"`
	Product          string `json:"product"`
	IPAddress        string `json:"ip_address"`
	Location         string `json:"location"`
	Decision         string `json:"decision"`
	StartedAt        int64  `json:"started_at"`
	StoppedAt        int64  `json:"stopped_at"`
	PausedMS         int64  `json:"paused_ms"`
	ViewOffsetMS     int64  `json:"view_offset_ms"`
	DurationMS       int64  `json:"duration_ms"`
	VideoSrc         string `json:"video_src"`
	VideoStream      string `json:"video_stream"`
	AudioSrc         string `json:"audio_src"`
	AudioStream      string `json:"audio_stream"`
	ContainerSrc     string `json:"container_src"`
	ContainerStream  string `json:"container_stream"`
	HWTranscode      bool   `json:"hw_transcode"`
	BufferCount      int    `json:"buffer_count"`
}

const historyCols = `id,user_id,user_name,title,grandparent_title,parent_title,media_index,parent_index,year,
	media_type,thumb,player,platform,product,ip_address,location,decision,started_at,stopped_at,paused_ms,
	view_offset_ms,duration_ms,video_src,video_stream,audio_src,audio_stream,container_src,container_stream,
	hw_transcode,buffer_count`

func (r *repo) history(ctx context.Context, f HistoryFilter) ([]HistoryRow, int, error) {
	where, args := historyWhere(f)
	// total count for pagination
	var total int
	if err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM stream_sessions`+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	q := `SELECT ` + historyCols + ` FROM stream_sessions` + where + ` ORDER BY started_at DESC LIMIT ? OFFSET ?`
	rows, err := r.db.QueryContext(ctx, q, append(args, f.Limit, f.Offset)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []HistoryRow
	for rows.Next() {
		var h HistoryRow
		var hw int
		if err := rows.Scan(&h.ID, &h.UserID, &h.UserName, &h.Title, &h.GrandparentTitle, &h.ParentTitle,
			&h.MediaIndex, &h.ParentIndex, &h.Year, &h.MediaType, &h.Thumb, &h.Player, &h.Platform, &h.Product,
			&h.IPAddress, &h.Location, &h.Decision, &h.StartedAt, &h.StoppedAt, &h.PausedMS, &h.ViewOffsetMS,
			&h.DurationMS, &h.VideoSrc, &h.VideoStream, &h.AudioSrc, &h.AudioStream, &h.ContainerSrc,
			&h.ContainerStream, &hw, &h.BufferCount); err != nil {
			return nil, 0, err
		}
		h.HWTranscode = hw != 0
		out = append(out, h)
	}
	return out, total, rows.Err()
}

func historyWhere(f HistoryFilter) (string, []any) {
	var conds []string
	var args []any
	if f.UserID != "" {
		conds = append(conds, "user_id = ?")
		args = append(args, f.UserID)
	}
	if f.Type != "" {
		conds = append(conds, "media_type = ?")
		args = append(args, f.Type)
	}
	if f.Decision != "" {
		conds = append(conds, "decision = ?")
		args = append(args, f.Decision)
	}
	if f.Query != "" {
		conds = append(conds, "(title LIKE ? OR grandparent_title LIKE ? OR user_name LIKE ?)")
		like := "%" + f.Query + "%"
		args = append(args, like, like, like)
	}
	if len(conds) == 0 {
		return "", nil
	}
	return " WHERE " + strings.Join(conds, " AND "), args
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}
