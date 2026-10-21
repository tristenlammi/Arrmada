package convert

import (
	"context"
	"fmt"
	"time"

	"github.com/tristenlammi/arrmada/internal/store"
)

// Stall is one reason conversions are waiting for room right now, for the Needs-you feed:
// how many files it holds up, and the latest of their notes (which carry the figures:
// "needs ~91 GB of scratch in /transcode, it has 40 GB free").
type Stall struct {
	Reason string    // a space skip kind (stalledKind)
	Files  int       // files waiting on it
	Title  string    // the most recent of them
	Note   string    // that file's reason
	Since  time.Time // when the oldest of them was last tried
}

// stallFresh is how recently a file must have been tried for its space skip to still mean
// "stuck": a space skip is retried after at most a day, so one older than this belongs to a
// file nothing is trying any more (Convert switched off, the file gone from the list).
const stallFresh = 48 * time.Hour

// Stalls reports the space problems holding conversions up, one per reason, the most
// recently hit first.
// Nothing while Convert is off and no file is asked for: then nothing is waiting.
func (s *Service) Stalls(ctx context.Context) ([]Stall, error) {
	if s.skips == nil || s.skips.db == nil {
		return nil, nil
	}
	// The one setting, not prefs(): the feed asks every 30 seconds.
	if !s.settings.GetBool(ctx, keyAuto, false) && len(s.requests.list(ctx)) == 0 {
		return nil, nil
	}
	rows, err := s.skips.db.QueryContext(ctx,
		`SELECT item_key, kind, reason, updated_at FROM convert_skips
		  WHERE kind IN (?, ?, ?, ?) AND updated_at >= `+store.UTCNowText(s.skips.db, "?")+`
		  ORDER BY updated_at DESC`,
		SkipNoScratch, SkipLibraryFull, SkipBinFull, "hold_full", fmt.Sprintf("-%d hours", int(stallFresh.Hours())))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	byReason := map[string]*Stall{}
	latestKey := map[string]string{}
	var order []string
	for rows.Next() {
		var key, kind, reason, updated string
		if err := rows.Scan(&key, &kind, &reason, &updated); err != nil {
			return nil, err
		}
		at, _ := time.ParseInLocation("2006-01-02 15:04:05", updated, time.UTC)
		st := byReason[kind]
		if st == nil {
			// Newest first, so the first row of a reason is its latest file.
			st = &Stall{Reason: kind, Note: reason, Since: at}
			byReason[kind], latestKey[kind] = st, key
			order = append(order, kind)
		}
		st.Files++
		if !at.IsZero() && at.Before(st.Since) {
			st.Since = at
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close() // before the title lookups, which query too
	out := make([]Stall, 0, len(order))
	for _, k := range order {
		st := byReason[k]
		b := Blocked{Key: latestKey[k]}
		parseItemKey(&b)
		st.Title = s.titleForKey(ctx, b.Kind, b.MovieID, b.VersionID, b.SeriesID, b.Season, b.Episode, b.Key)
		out = append(out, *st)
	}
	return out, nil
}
