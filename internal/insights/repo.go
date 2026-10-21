package insights

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/tristenlammi/arrmada/internal/mediaserver"
	"github.com/tristenlammi/arrmada/internal/store"
)

type repo struct{ db store.Pool }

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
	// The show an episode belongs to: its poster and rating key ('' for movies, and for
	// plays recorded before PLEX-20).
	GrandparentThumb     string
	GrandparentRatingKey string
	Player               string
	Platform             string
	Product              string
	IPAddress            string
	Location             string
	Decision             string
	StartedAt            int64 // epoch seconds
	StoppedAt            int64
	PausedMS             int64
	ViewOffsetMS         int64
	DurationMS           int64 // the MEDIA's runtime (progress % denominator), not watch time
	WatchedMS            int64 // actual time watched, when the source reports it; 0 = derive it
	VideoSrc             string
	VideoStream          string
	VideoSrcBitDepth     int    // 8, 10; 0 = not reported
	VideoSrcProfile      string // "main 10", "high"; "" = not reported
	AudioSrc             string
	AudioStream          string
	ContainerSrc         string
	ContainerStream      string
	HWTranscode          bool
	BufferCount          int
	ImportRunID          int64            // the Tautulli import run that brought this play in; 0 = live or older import
	Server               mediaserver.Kind // the server it was played on; "" = Plex
}

func (r *repo) insertSession(ctx context.Context, s sessionRecord) (int64, error) {
	return r.insertSessionEx(ctx, r.db, s)
}

// insertSessionEx is insertSession on the database or inside a transaction.
func (r *repo) insertSessionEx(ctx context.Context, ex store.Execer, s sessionRecord) (int64, error) {
	return store.InsertID(ctx, ex, `
		INSERT INTO stream_sessions
		 (session_key,user_id,user_name,rating_key,media_type,title,grandparent_title,parent_title,
		  media_index,parent_index,year,thumb,player,platform,product,ip_address,location,decision,
		  started_at,stopped_at,paused_ms,view_offset_ms,duration_ms,watched_ms,
		  video_src,video_stream,audio_src,audio_stream,container_src,container_stream,hw_transcode,buffer_count,import_run_id,
		  video_src_bitdepth,video_src_profile,grandparent_thumb,grandparent_rating_key,server)
		 VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		s.SessionKey, s.UserID, s.UserName, s.RatingKey, s.MediaType, s.Title, s.GrandparentTitle, s.ParentTitle,
		s.MediaIndex, s.ParentIndex, s.Year, s.Thumb, s.Player, s.Platform, s.Product, s.IPAddress, s.Location, s.Decision,
		s.StartedAt, s.StoppedAt, s.PausedMS, s.ViewOffsetMS, s.DurationMS, s.WatchedMS,
		s.VideoSrc, s.VideoStream, s.AudioSrc, s.AudioStream, s.ContainerSrc, s.ContainerStream, b2i(s.HWTranscode), s.BufferCount, s.ImportRunID,
		s.VideoSrcBitDepth, s.VideoSrcProfile, s.GrandparentThumb, s.GrandparentRatingKey, string(serverOf(s.Server)))
}

// sessionExists reports whether a session for this user + item + start time is already recorded
// (used to keep history imports idempotent on re-run).
func (r *repo) sessionExists(ctx context.Context, userID, ratingKey string, startedAt int64) (bool, error) {
	var n int
	err := r.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM stream_sessions WHERE user_id = ? AND rating_key = ? AND started_at = ?`,
		userID, ratingKey, startedAt).Scan(&n)
	return n > 0, err
}

// fillEmptyRatingKey looks for an imported play (no session key) by this user at this start
// second, with this title and no rating key — imported before the Tautulli client read
// numeric keys — and gives it ratingKey, plus the show's key when that is empty too. It
// reports whether it found one. Asking for an empty rating_key keeps the lookup on the
// (user_id, rating_key, started_at) dedupe index.
func (r *repo) fillEmptyRatingKey(ctx context.Context, userID string, startedAt int64, title, ratingKey, grandparentKey string) (bool, error) {
	var id int64
	err := r.db.QueryRowContext(ctx, `SELECT id FROM stream_sessions
		WHERE user_id = ? AND rating_key = '' AND started_at = ? AND session_key = '' AND title = ?
		ORDER BY id LIMIT 1`, userID, startedAt, title).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	_, err = r.db.ExecContext(ctx, `UPDATE stream_sessions SET rating_key = ?,
		grandparent_rating_key = CASE WHEN grandparent_rating_key = '' THEN ? ELSE grandparent_rating_key END
		WHERE id = ?`, ratingKey, grandparentKey, id)
	return err == nil, err
}

func (r *repo) insertBufferEvent(ctx context.Context, sessionID, at, offsetMS, durationMS int64, cause, detail string) error {
	return r.insertBufferEventEx(ctx, r.db, sessionID, at, offsetMS, durationMS, cause, detail)
}

func (r *repo) insertBufferEventEx(ctx context.Context, ex execer, sessionID, at, offsetMS, durationMS int64, cause, detail string) error {
	_, err := ex.ExecContext(ctx, `INSERT INTO buffer_events (session_id,at,view_offset_ms,duration_ms,cause,detail) VALUES (?,?,?,?,?,?)`, sessionID, at, offsetMS, durationMS, cause, detail)
	return err
}

func (r *repo) insertBandwidth(ctx context.Context, server mediaserver.Kind, at, total, lan, wan int64) error {
	_, err := r.db.ExecContext(ctx, `INSERT INTO bandwidth_samples (at,total_kbps,lan_kbps,wan_kbps,server) VALUES (?,?,?,?,?)`,
		at, total, lan, wan, string(serverOf(server)))
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

// upsertUser saves a person of one server (plex_users holds both: a Jellyfin id is a
// 32-hex GUID, a Plex id a short number, so they never collide).
func (r *repo) upsertUser(ctx context.Context, server mediaserver.Kind, id, username, thumb string, at int64) error {
	if id == "" {
		return nil
	}
	// Keep the existing avatar when the incoming thumb is blank (imports may not carry one — don't
	// clobber a good avatar with ''), and never move last_seen_at backwards when importing old rows.
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO plex_users (id,username,thumb,last_seen_at,server) VALUES (?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET
			username=CASE WHEN excluded.username <> '' THEN excluded.username ELSE plex_users.username END,
			thumb=CASE WHEN excluded.thumb <> '' THEN excluded.thumb ELSE plex_users.thumb END,
			last_seen_at=`+store.ScalarMax(r.db, "plex_users.last_seen_at", "excluded.last_seen_at"),
		id, username, thumb, at, string(serverOf(server)))
	return err
}

// repointUser moves every session filed under `from` onto `to`, returning how many moved.
// Used to fold the Plex owner placeholder ("1") into the real account id.
func (r *repo) repointUser(ctx context.Context, from, to string) (int64, error) {
	res, err := r.db.ExecContext(ctx,
		`UPDATE stream_sessions SET user_id = ? WHERE user_id = ?`, to, from)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// deleteUserIfUnused retires a plex_users row once no session references it. Guarded so a
// partial merge can never orphan sessions against a user the Users tab can't join to.
func (r *repo) deleteUserIfUnused(ctx context.Context, id string) error {
	_, err := r.db.ExecContext(ctx,
		`DELETE FROM plex_users WHERE id = ?
		   AND NOT EXISTS (SELECT 1 FROM stream_sessions WHERE user_id = ?)`, id, id)
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
	// Server narrows to one server's plays ("plex", "jellyfin"); "" is both.
	Server string
	// From and To bound the start time (epoch seconds, To exclusive); 0 = open.
	From int64
	To   int64
	// Group folds a person's resumed sittings of one item into one row: a new row starts
	// when the gap since the previous sitting ended is over resumeGap.
	Group  bool
	Limit  int
	Offset int
}

// resumeGap is the longest pause between two sittings of the same item by the same person
// that still counts as one viewing: twenty minutes away is a resume, a film paused
// overnight is two evenings.
const resumeGap = 3 * 60 * 60

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
	// The show's poster and rating key, for episodes recorded since PLEX-20 ('' otherwise).
	GrandparentThumb     string `json:"grandparent_thumb"`
	GrandparentRatingKey string `json:"grandparent_rating_key"`
	Player               string `json:"player"`
	Platform             string `json:"platform"`
	Product              string `json:"product"`
	IPAddress            string `json:"ip_address"`
	Location             string `json:"location"`
	Decision             string `json:"decision"`
	StartedAt            int64  `json:"started_at"`
	StoppedAt            int64  `json:"stopped_at"`
	PausedMS             int64  `json:"paused_ms"`
	ViewOffsetMS         int64  `json:"view_offset_ms"`
	DurationMS           int64  `json:"duration_ms"`
	WatchedMS            int64  `json:"watched_ms"`
	VideoSrc             string `json:"video_src"`
	VideoStream          string `json:"video_stream"`
	AudioSrc             string `json:"audio_src"`
	AudioStream          string `json:"audio_stream"`
	ContainerSrc         string `json:"container_src"`
	ContainerStream      string `json:"container_stream"`
	HWTranscode          bool   `json:"hw_transcode"`
	BufferCount          int    `json:"buffer_count"`
	Server               string `json:"server"` // plex | jellyfin
}

const historyCols = `id,user_id,user_name,title,grandparent_title,parent_title,media_index,parent_index,year,
	media_type,thumb,player,platform,product,ip_address,location,decision,started_at,stopped_at,paused_ms,
	view_offset_ms,duration_ms,watched_ms,video_src,video_stream,audio_src,audio_stream,container_src,container_stream,
	hw_transcode,buffer_count,grandparent_thumb,grandparent_rating_key,server`

// historyScanDest lists a HistoryRow's fields in historyCols order, for rows.Scan.
func historyScanDest(h *HistoryRow, hw *int) []any {
	return []any{&h.ID, &h.UserID, &h.UserName, &h.Title, &h.GrandparentTitle, &h.ParentTitle,
		&h.MediaIndex, &h.ParentIndex, &h.Year, &h.MediaType, &h.Thumb, &h.Player, &h.Platform, &h.Product,
		&h.IPAddress, &h.Location, &h.Decision, &h.StartedAt, &h.StoppedAt, &h.PausedMS, &h.ViewOffsetMS,
		&h.DurationMS, &h.WatchedMS, &h.VideoSrc, &h.VideoStream, &h.AudioSrc, &h.AudioStream, &h.ContainerSrc,
		&h.ContainerStream, hw, &h.BufferCount, &h.GrandparentThumb, &h.GrandparentRatingKey, &h.Server}
}

func (r *repo) history(ctx context.Context, f HistoryFilter) ([]HistoryRow, int, error) {
	var out []HistoryRow
	total, err := r.historyEach(ctx, f, func(h HistoryRow) error { out = append(out, h); return nil })
	return out, total, err
}

// historyEach runs the history query (one page: f.Limit rows from f.Offset, newest first)
// and hands each row to fn. It returns the total number of rows the filters match, for
// paging.
func (r *repo) historyEach(ctx context.Context, f HistoryFilter, fn func(HistoryRow) error) (int, error) {
	where, args := historyWhere(r.db, f)
	// total count for pagination
	var total int
	if err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM stream_sessions`+where, args...).Scan(&total); err != nil {
		return 0, err
	}
	q := `SELECT ` + historyCols + ` FROM stream_sessions` + where + ` ORDER BY started_at DESC, id DESC LIMIT ? OFFSET ?`
	rows, err := r.db.QueryContext(ctx, q, append(args, f.Limit, f.Offset)...)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	for rows.Next() {
		var h HistoryRow
		var hw int
		if err := rows.Scan(historyScanDest(&h, &hw)...); err != nil {
			return 0, err
		}
		h.HWTranscode = hw != 0
		if err := fn(h); err != nil {
			return 0, err
		}
	}
	return total, rows.Err()
}

// GroupedRow is a viewing made of one or more sittings: the latest sitting's row, spanning
// the first start to the last stop, with the furthest position reached.
type GroupedRow struct {
	HistoryRow
	Sessions    int   // sittings folded into this row
	WatchedSecs int64 // summed watched time of every sitting (watchedExpr each)
}

// itemKey identifies "the same item" for grouping. Plays without a rating key (some
// imports) fall back to their title identity, so they never all merge into one item.
// Migration 0308 indexes this exact expression (idx_stream_sessions_item) and its
// triggers repeat it: change all of them together.
const itemKey = `CASE WHEN rating_key <> '' THEN rating_key
	ELSE media_type || '|' || title || '|' || grandparent_title || '|' || parent_index || '|' || media_index END`

// itemKeyOf is itemKey over the stream_sessions row aliased t.
func itemKeyOf(t string) string {
	return fmt.Sprintf(`CASE WHEN %[1]s.rating_key <> '' THEN %[1]s.rating_key
	ELSE %[1]s.media_type || '|' || %[1]s.title || '|' || %[1]s.grandparent_title || '|' || %[1]s.parent_index || '|' || %[1]s.media_index END`, t)
}

// historyGrouped is history with resumed sittings folded together (HistoryFilter.Group):
// a sitting starts a viewing when it is a person's first of the item, or when the one
// before it stopped more than resumeGap earlier. Viewings are paged by their starting
// sitting, newest first, and only the page's viewings are summed from their sittings —
// the sittings from a viewing's start up to the next viewing's. The total counts
// viewings, so paging agrees with what is listed.
//
// Grouping every play, or one person's, reads the starts marked on the rows
// (stream_sessions.viewing_start, kept by migration 0308's triggers): a page is a few
// index reads. Any other filter picks sittings in a way that moves where viewings break
// (a date range cuts one in half; a decision filter drops the sittings between), so those
// work the starts out over the filtered sittings with a window function instead.
func (r *repo) historyGrouped(ctx context.Context, f HistoryFilter, fn func(GroupedRow) error) (int, error) {
	// A server filter keeps whole viewings: a person and an item belong to one server, so
	// it never cuts one in two and the marked starts still hold.
	if f.Type == "" && f.Decision == "" && f.Query == "" && f.From == 0 && f.To == 0 {
		return r.historyGroupedMarked(ctx, f, fn)
	}
	return r.historyGroupedWindow(ctx, f, fn)
}

// groupedPageSQL is the end of both grouped queries: given a CTE named page with each
// listed viewing's start (p_id, p_user, p_item, p_start), the next viewing's start in the
// same partition (p_next_start, p_next_id; NULL for the last) and p_total, it sums each
// viewing's sittings (those also matching and, the filters) and returns the rows
// historyGroupedScan reads. The page's columns are named apart (p_*), so the join's
// filters and the watched expression read stream_sessions' own columns unqualified.
func groupedPageSQL(db store.Execer, and string) string {
	return `v AS (
			SELECT p_id, p_start, p_total, MAX(stopped_at) AS stop, MAX(view_offset_ms) AS off, SUM(buffer_count) AS bufs,
				SUM(paused_ms) AS paused, SUM(` + watchedExprFor(db) + `) AS watched, COUNT(*) AS sessions, MAX(id) AS last_id
			FROM page JOIN stream_sessions ON user_id = p_user AND ` + itemKey + ` = p_item
				AND (started_at, id) >= (p_start, p_id)
				AND (p_next_start IS NULL OR (started_at, id) < (p_next_start, p_next_id))` + and + `
			GROUP BY p_id, p_start, p_total)
		SELECT v.p_start, v.stop, v.off, v.bufs, v.paused, v.watched, v.sessions, v.p_total, ` + prefixed("s.", historyCols) + `
		FROM v JOIN stream_sessions s ON s.id = v.last_id
		ORDER BY v.p_start DESC, v.p_id DESC`
}

// historyGroupedMarked is historyGrouped over every play (or f.UserID's) from the marked
// starts.
func (r *repo) historyGroupedMarked(ctx context.Context, f HistoryFilter, fn func(GroupedRow) error) (int, error) {
	where, args := " WHERE s.viewing_start = 1"+serverAnd("s.", f.Server), []any(nil)
	if f.UserID != "" {
		where, args = where+" AND s.user_id = ?", []any{f.UserID}
	}
	var total int
	if err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM stream_sessions s`+where, args...).Scan(&total); err != nil {
		return 0, err
	}
	if f.Offset >= total {
		return total, nil
	}
	_, err := r.historyGroupedScan(ctx, groupedMarkedSQL(r.db, where), append(append([]any{total}, args...), f.Limit, f.Offset), fn)
	return total, err
}

// groupedMarkedSQL is historyGroupedMarked's page query: the page's marked starts (where
// filters stream_sessions s), each with the next marked start of its item — the sittings
// before that one are this viewing's. Arguments: the total, where's, limit, offset.
func groupedMarkedSQL(db store.Execer, where string) string {
	next := func(col string) string {
		return `(SELECT n.` + col + ` FROM stream_sessions n
			WHERE n.user_id = s.user_id AND ` + itemKeyOf("n") + ` = ` + itemKeyOf("s") + `
			  AND n.viewing_start = 1 AND (n.started_at, n.id) > (s.started_at, s.id)
			ORDER BY n.started_at, n.id LIMIT 1)`
	}
	return `WITH page AS (
			SELECT s.id AS p_id, s.user_id AS p_user, ` + itemKeyOf("s") + ` AS p_item, s.started_at AS p_start,
				` + next("started_at") + ` AS p_next_start, ` + next("id") + ` AS p_next_id, CAST(? AS bigint) AS p_total
			FROM stream_sessions s` + where + `
			ORDER BY s.started_at DESC, s.id DESC LIMIT ? OFFSET ?),
		` + groupedPageSQL(db, "")
}

// historyGroupedWindow is historyGrouped for any filters. The filters pick the sittings
// first, then LAG over each person's sittings of an item finds the gap since the previous
// one ended — one pass over the filtered plays per page.
func (r *repo) historyGroupedWindow(ctx context.Context, f HistoryFilter, fn func(GroupedRow) error) (int, error) {
	conds, args := historyConds(r.db, f)
	where, and := "", ""
	if len(conds) > 0 {
		where = " WHERE " + strings.Join(conds, " AND ")
		and = " AND " + strings.Join(conds, " AND ")
	}
	gap := strconv.Itoa(resumeGap)
	q := `WITH f AS (
			SELECT id, user_id, ` + itemKey + ` AS item, started_at,
				LAG(stopped_at) OVER (PARTITION BY user_id, ` + itemKey + ` ORDER BY started_at, id) AS prev_stop
			FROM stream_sessions` + where + `),
		st AS (SELECT id, user_id, item, started_at FROM f WHERE prev_stop IS NULL OR started_at - prev_stop > ` + gap + `),
		p AS (
			SELECT id AS p_id, user_id AS p_user, item AS p_item, started_at AS p_start,
				LEAD(started_at) OVER w AS p_next_start, LEAD(id) OVER w AS p_next_id, COUNT(*) OVER () AS p_total
			FROM st WINDOW w AS (PARTITION BY user_id, item ORDER BY started_at, id)),
		page AS (SELECT * FROM p ORDER BY p_start DESC, p_id DESC LIMIT ? OFFSET ?),
		` + groupedPageSQL(r.db, and)
	all := append(append(append([]any{}, args...), f.Limit, f.Offset), args...)
	total, err := r.historyGroupedScan(ctx, q, all, fn)
	if err != nil || total >= 0 {
		return max(total, 0), err
	}
	if f.Offset == 0 {
		return 0, nil
	}
	// A page past the end lists nothing, so the window total isn't there: count apart.
	err = r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM (
		SELECT started_at, LAG(stopped_at) OVER (PARTITION BY user_id, `+itemKey+` ORDER BY started_at, id) AS prev_stop
		FROM stream_sessions`+where+`)
		WHERE prev_stop IS NULL OR started_at - prev_stop > `+gap, args...).Scan(&total)
	return total, err
}

// historyGroupedScan runs a grouped query (groupedPageSQL's columns) and hands each
// viewing to fn. It returns the rows' p_total, or -1 when the page listed nothing.
func (r *repo) historyGroupedScan(ctx context.Context, q string, args []any, fn func(GroupedRow) error) (int, error) {
	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	total, seen := 0, false
	for rows.Next() {
		var g GroupedRow
		var hw int
		var start, stop, off, bufs, paused int64
		dest := append([]any{&start, &stop, &off, &bufs, &paused, &g.WatchedSecs, &g.Sessions, &total}, historyScanDest(&g.HistoryRow, &hw)...)
		if err := rows.Scan(dest...); err != nil {
			return 0, err
		}
		seen = true
		g.HWTranscode = hw != 0
		g.StartedAt, g.StoppedAt, g.ViewOffsetMS, g.BufferCount, g.PausedMS = start, stop, off, int(bufs), paused
		if err := fn(g); err != nil {
			return 0, err
		}
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	if !seen {
		return -1, nil
	}
	return total, nil
}

// prefixed qualifies each column of a comma-separated list with a table alias.
func prefixed(alias, cols string) string {
	parts := strings.Split(cols, ",")
	for i, c := range parts {
		parts[i] = alias + strings.TrimSpace(c)
	}
	return strings.Join(parts, ",")
}

func historyWhere(db store.Execer, f HistoryFilter) (string, []any) {
	conds, args := historyConds(db, f)
	if len(conds) == 0 {
		return "", nil
	}
	return " WHERE " + strings.Join(conds, " AND "), args
}

// historyConds is History's filters as SQL conditions on stream_sessions' columns.
func historyConds(db store.Execer, f HistoryFilter) ([]string, []any) {
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
		conds = append(conds, "("+store.LikeASCII(db, "title", "?", false)+" OR "+store.LikeASCII(db, "grandparent_title", "?", false)+" OR "+store.LikeASCII(db, "user_name", "?", false)+")")
		like := "%" + f.Query + "%"
		args = append(args, like, like, like)
	}
	if f.From > 0 {
		conds = append(conds, "started_at >= ?")
		args = append(args, f.From)
	}
	if f.To > 0 {
		conds = append(conds, "started_at < ?")
		args = append(args, f.To)
	}
	if c := serverAnd("", f.Server); c != "" {
		conds = append(conds, strings.TrimPrefix(c, " AND "))
	}
	return conds, args
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}
