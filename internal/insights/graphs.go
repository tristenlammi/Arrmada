package insights

import (
	"context"
	"time"
)

// Graphs is the time-series bundle for the Graphs tab.
type Graphs struct {
	Days         []string   `json:"days"`         // date labels (chronological) for the daily chart
	DailyTV      []int      `json:"daily_tv"`     // per-day play counts, aligned to Days
	DailyMovies  []int      `json:"daily_movies"`
	DailyMusic   []int      `json:"daily_music"`
	ByDayOfWeek  []int      `json:"by_day_of_week"` // length 7, Sun..Sat
	ByHour       []int      `json:"by_hour"`        // length 24, 00..23
	TopPlatforms []NameStat `json:"top_platforms"`
	TopUsers     []NameStat `json:"top_users"`
	Bandwidth    []BWPoint  `json:"bandwidth"` // peak per hour bucket over the window
}

// BWPoint is one bandwidth bucket (peak within the bucket).
type BWPoint struct {
	T     string `json:"t"` // "YYYY-MM-DD HH:00" local
	Total int64  `json:"total_kbps"`
	LAN   int64  `json:"lan_kbps"`
	WAN   int64  `json:"wan_kbps"`
}

// Graphs computes all the time-series over the last windowDays.
func (s *Service) Graphs(ctx context.Context, windowDays int) (Graphs, error) {
	if windowDays <= 0 {
		windowDays = 30
	}
	since := time.Now().AddDate(0, 0, -(windowDays - 1)).Truncate(24 * time.Hour).Unix()

	g := Graphs{ByDayOfWeek: make([]int, 7), ByHour: make([]int, 24)}

	// Daily by media type — build a continuous day axis, then fill.
	days, idx := dayAxis(windowDays)
	g.Days = days
	g.DailyTV = make([]int, len(days))
	g.DailyMovies = make([]int, len(days))
	g.DailyMusic = make([]int, len(days))
	rows, err := s.repo.db.QueryContext(ctx, `
		SELECT strftime('%Y-%m-%d', started_at, 'unixepoch', 'localtime') d, media_type, COUNT(*)
		FROM stream_sessions WHERE started_at >= ? GROUP BY d, media_type`, since)
	if err != nil {
		return Graphs{}, err
	}
	for rows.Next() {
		var d, mt string
		var c int
		if err := rows.Scan(&d, &mt, &c); err != nil {
			rows.Close()
			return Graphs{}, err
		}
		i, ok := idx[d]
		if !ok {
			continue
		}
		switch mt {
		case "episode":
			g.DailyTV[i] += c
		case "movie":
			g.DailyMovies[i] += c
		case "track":
			g.DailyMusic[i] += c
		}
	}
	rows.Close()

	if err := scanBuckets(ctx, s.repo, `strftime('%w', started_at, 'unixepoch', 'localtime')`, since, g.ByDayOfWeek); err != nil {
		return Graphs{}, err
	}
	if err := scanBuckets(ctx, s.repo, `strftime('%H', started_at, 'unixepoch', 'localtime')`, since, g.ByHour); err != nil {
		return Graphs{}, err
	}

	g.TopPlatforms = toNameStats(mustNames(s.repo.topNames(ctx, "platform", "platform", since, false, 10)))
	g.TopUsers = toNameStats(mustNames(s.repo.topNames(ctx, "user_id", "user_name", since, false, 10)))

	bw, err := s.bandwidthSeries(ctx, since)
	if err != nil {
		return Graphs{}, err
	}
	g.Bandwidth = bw
	return g, nil
}

// dayAxis returns the last n calendar days (local) as "YYYY-MM-DD" labels + an index map.
func dayAxis(n int) ([]string, map[string]int) {
	days := make([]string, n)
	idx := make(map[string]int, n)
	today := time.Now()
	for i := 0; i < n; i++ {
		d := today.AddDate(0, 0, -(n - 1 - i)).Format("2006-01-02")
		days[i] = d
		idx[d] = i
	}
	return days, idx
}

// scanBuckets fills an int slice keyed by a strftime bucket expression (values are the bucket index).
func scanBuckets(ctx context.Context, r *repo, expr string, since int64, out []int) error {
	rows, err := r.db.QueryContext(ctx, `SELECT `+expr+` b, COUNT(*) FROM stream_sessions WHERE started_at >= ? GROUP BY b`, since)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var b string
		var c int
		if err := rows.Scan(&b, &c); err != nil {
			return err
		}
		if i := atoiSafe(b); i >= 0 && i < len(out) {
			out[i] = c
		}
	}
	return rows.Err()
}

func (s *Service) bandwidthSeries(ctx context.Context, since int64) ([]BWPoint, error) {
	rows, err := s.repo.db.QueryContext(ctx, `
		SELECT strftime('%Y-%m-%d %H:00', at, 'unixepoch', 'localtime') bucket,
		       MAX(total_kbps), MAX(lan_kbps), MAX(wan_kbps)
		FROM bandwidth_samples WHERE at >= ? GROUP BY bucket ORDER BY bucket`, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []BWPoint
	for rows.Next() {
		var p BWPoint
		if err := rows.Scan(&p.T, &p.Total, &p.LAN, &p.WAN); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func mustNames(rows []nameStatRow, _ error) []nameStatRow { return rows }

func atoiSafe(s string) int {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return -1
		}
		n = n*10 + int(c-'0')
	}
	if s == "" {
		return -1
	}
	return n
}
