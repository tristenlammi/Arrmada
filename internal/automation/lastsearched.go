package automation

import (
	"context"
	"strings"
)

// LastSearchedAt is when each of ids was last searched for (unix ms, the newest stored
// attempt, upgrade searches left out) — just the time, for a requester's "last checked"
// line, which runs on every poll of the requests list and needs none of the rest of
// LatestAttempts. Titles never searched are absent.
func (c *Coordinator) LastSearchedAt(ctx context.Context, kind string, ids []int64) (map[int64]int64, error) {
	out := map[int64]int64{}
	const chunk = 500 // well under SQLite's bound-parameter limit
	for start := 0; start < len(ids); start += chunk {
		part := ids[start:min(start+chunk, len(ids))]
		args := make([]any, 0, len(part)+1)
		args = append(args, kind)
		for _, id := range part {
			args = append(args, id)
		}
		rows, err := c.db.QueryContext(ctx, `SELECT media_id, MAX(started_at) FROM search_attempts
			WHERE media_type = ? AND scope != '`+ScopeUpgrade+`' AND media_id IN (?`+strings.Repeat(",?", len(part)-1)+`)
			GROUP BY media_id`, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id, at int64
			if err := rows.Scan(&id, &at); err != nil {
				rows.Close()
				return nil, err
			}
			out[id] = at
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}
