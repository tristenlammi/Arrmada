package convert

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/tristenlammi/arrmada/internal/store"
)

// requestStore persists the files someone asked for by hand (convert_requests). They're
// converted before anything the runner picks on its own, they run outside the encode hours
// (you asked for them now), and they survive a restart.
type requestStore struct{ db store.Pool }

// Request is one hand-picked file waiting its turn.
type Request struct {
	Key         string `json:"key"`
	Title       string `json:"title"`
	RequestedAt int64  `json:"requested_at"`
}

func (r *requestStore) add(ctx context.Context, key, title string) error {
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO convert_requests (item_key, title, requested_at) VALUES (?, ?, ?)
		 ON CONFLICT(item_key) DO UPDATE SET title = excluded.title`,
		key, title, time.Now().Unix())
	return err
}

func (r *requestStore) remove(ctx context.Context, key string) {
	_, _ = r.db.ExecContext(ctx, `DELETE FROM convert_requests WHERE item_key = ?`, key)
}

func (r *requestStore) clear(ctx context.Context) int {
	res, err := r.db.ExecContext(ctx, `DELETE FROM convert_requests`)
	if err != nil {
		return 0
	}
	n, _ := res.RowsAffected()
	return int(n)
}

// list returns the waiting requests, oldest first — the order they'll run in.
func (r *requestStore) list(ctx context.Context) []Request {
	rows, err := r.db.QueryContext(ctx, `SELECT item_key, title, requested_at FROM convert_requests ORDER BY requested_at, item_key`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []Request
	for rows.Next() {
		var q Request
		if rows.Scan(&q.Key, &q.Title, &q.RequestedAt) == nil {
			out = append(out, q)
		}
	}
	return out
}

// item is a library file identified by its key ("movie:12", "movie:12:v:3",
// "episode:3:1:2").
type item struct {
	Kind      string // "movie" | "episode"
	MovieID   int64
	VersionID int64 // the movie's extra version track; 0 = its default file
	SeriesID  int64
	Season    int
	Episode   int
}

// parseKey turns an item key back into its parts.
func parseKey(key string) (item, bool) {
	parts := strings.Split(key, ":")
	switch {
	case len(parts) == 2 && parts[0] == "movie":
		id, err := strconv.ParseInt(parts[1], 10, 64)
		return item{Kind: "movie", MovieID: id}, err == nil && id > 0
	case len(parts) == 4 && parts[0] == "movie" && parts[2] == "v":
		// An extra version track. The default file's key is 'movie:ID' alone — one file,
		// one key — so 'movie:ID:v:0' isn't one.
		id, e1 := strconv.ParseInt(parts[1], 10, 64)
		vid, e2 := strconv.ParseInt(parts[3], 10, 64)
		return item{Kind: "movie", MovieID: id, VersionID: vid}, e1 == nil && e2 == nil && id > 0 && vid > 0
	case len(parts) == 4 && parts[0] == "episode":
		sid, e1 := strconv.ParseInt(parts[1], 10, 64)
		se, e2 := strconv.Atoi(parts[2])
		ep, e3 := strconv.Atoi(parts[3])
		return item{Kind: "episode", SeriesID: sid, Season: se, Episode: ep}, e1 == nil && e2 == nil && e3 == nil && sid > 0
	}
	return item{}, false
}

// key is the item's identity.
func (it item) key() string {
	if it.Kind == "episode" {
		return episodeKey(it.SeriesID, it.Season, it.Episode)
	}
	return movieVersionKey(it.MovieID, it.VersionID)
}

// job is a bare Job naming this item, for lookups that take one (resolveSource).
func (it item) job() *Job {
	return &Job{Kind: it.Kind, MovieID: it.MovieID, VersionID: it.VersionID,
		SeriesID: it.SeriesID, Season: it.Season, Episode: it.Episode}
}

// ItemKey builds the key for a movie track (versionID 0 = the default file) or an episode.
func ItemKey(kind string, movieID, versionID, seriesID int64, season, episode int) string {
	if kind == "episode" {
		return episodeKey(seriesID, season, episode)
	}
	return movieVersionKey(movieID, versionID)
}

// ErrAlreadyQueued means this exact file is already waiting or converting.
var ErrAlreadyQueued = fmt.Errorf("this file is already waiting or converting")
