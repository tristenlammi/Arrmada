package insights

import (
	"context"
	"errors"
	"sort"
	"strconv"
	"time"

	"github.com/tristenlammi/arrmada/internal/store"
)

// Graphs is the time-series bundle for the Graphs tab. Every play series is in the
// bundle's Metric: a count of plays, or hours watched (watchedSum, the same per-row figure
// the People tab totals), so one switch turns every chart into watch time.
type Graphs struct {
	Metric       string     `json:"metric"`   // plays | duration
	Days         []string   `json:"days"`     // date labels (chronological) for the daily chart
	DailyTV      []float64  `json:"daily_tv"` // per-day values, aligned to Days
	DailyMovies  []float64  `json:"daily_movies"`
	DailyMusic   []float64  `json:"daily_music"`
	ByDayOfWeek  []float64  `json:"by_day_of_week"` // length 7, Sun..Sat
	ByHour       []float64  `json:"by_hour"`        // length 24, 00..23
	TopPlatforms []NameStat `json:"top_platforms"`
	TopUsers     []NameStat `json:"top_users"`
	// The same plays split by how they streamed, aligned to Days. Per day the three add up
	// to TV + movies + music: a play with no recognised decision (an import that didn't
	// say) counts as direct play, as the History badge shows it.
	DailyDirectPlay   []float64 `json:"daily_direct_play"`
	DailyDirectStream []float64 `json:"daily_direct_stream"`
	DailyTranscode    []float64 `json:"daily_transcode"`
	Bandwidth         []BWPoint `json:"bandwidth"` // peak per hour bucket over the window
}

// BWPoint is one bandwidth bucket (peak within the bucket).
type BWPoint struct {
	T     string `json:"t"` // "YYYY-MM-DD HH:00" local
	Total int64  `json:"total_kbps"`
	LAN   int64  `json:"lan_kbps"`
	WAN   int64  `json:"wan_kbps"`
}

// Graph metrics: what every play series counts.
const (
	MetricPlays    = "plays"
	MetricDuration = "duration"
)

// metricExpr is the SQL aggregate for a metric: plays, or hours watched.
func metricExpr(db store.Execer, metric string) string {
	if metric == MetricDuration {
		return `COALESCE(` + watchedSumFor(db) + `,0) / 3600.0`
	}
	return `COUNT(*)`
}

// graphMediaTypes are the media types the daily chart draws; the stream-type series reads
// the same rows so its lines add up to the daily total.
const graphMediaTypes = `media_type IN ('episode','movie','track')`

// Graphs computes all the time-series over the last windowDays, in plays or hours.
// server narrows every series to one server's plays ("" for both).
func (s *Service) Graphs(ctx context.Context, windowDays int, metric, server string) (Graphs, error) {
	if windowDays <= 0 {
		windowDays = 30
	}
	if metric != MetricDuration {
		metric = MetricPlays
	}
	agg := metricExpr(s.repo.db, metric)
	// Local-midnight-aligned start (windowStart) so this matches the 'localtime'-grouped SQL and the
	// local dayAxis labels — a UTC-truncated start made the first day of every window mis-count.
	since := windowStart(windowDays)

	g := Graphs{Metric: metric, ByDayOfWeek: make([]float64, 7), ByHour: make([]float64, 24)}

	// Daily by media type and by stream decision — build a continuous day axis, then fill.
	days, idx := dayAxis(windowDays)
	g.Days = days
	n := len(days)
	g.DailyTV, g.DailyMovies, g.DailyMusic = make([]float64, n), make([]float64, n), make([]float64, n)
	g.DailyDirectPlay, g.DailyDirectStream, g.DailyTranscode = make([]float64, n), make([]float64, n), make([]float64, n)
	err := s.dailySeries(ctx, `media_type`, agg, since, server, idx, func(i int, key string, v float64) {
		switch key {
		case "episode":
			g.DailyTV[i] += v
		case "movie":
			g.DailyMovies[i] += v
		case "track":
			g.DailyMusic[i] += v
		}
	})
	if err != nil {
		return Graphs{}, err
	}
	err = s.dailySeries(ctx, `decision`, agg, since, server, idx, func(i int, key string, v float64) {
		switch key {
		case "transcode":
			g.DailyTranscode[i] += v
		case "direct_stream":
			g.DailyDirectStream[i] += v
		default:
			g.DailyDirectPlay[i] += v
		}
	})
	if err != nil {
		return Graphs{}, err
	}

	if err := scanBuckets(ctx, s.repo, "weekday", agg, since, server, g.ByDayOfWeek); err != nil {
		return Graphs{}, err
	}
	if err := scanBuckets(ctx, s.repo, "hour", agg, since, server, g.ByHour); err != nil {
		return Graphs{}, err
	}

	// These used to run through mustNames, which took the error and dropped it on the
	// floor: a failed query rendered as an empty Top Platforms / Top Users card, so a
	// broken query and a quiet week looked identical.
	byDuration := metric == MetricDuration
	plats, err := s.repo.topNames(ctx, "platform", "platform", since, byDuration, 10, server)
	if err != nil {
		return Graphs{}, err
	}
	g.TopPlatforms = toNameStats(plats)
	users, err := s.repo.topNames(ctx, "user_id", "user_name", since, byDuration, 10, server)
	if err != nil {
		return Graphs{}, err
	}
	g.TopUsers = toNameStats(users)

	bw, err := s.bandwidthSeries(ctx, since, server)
	if err != nil {
		return Graphs{}, err
	}
	g.Bandwidth = bw
	return g, nil
}

// dailySeries sums agg per local day and per value of keyCol over the charted media types,
// handing each (day index, key, value) to put. Days outside the axis are skipped.
func (s *Service) dailySeries(ctx context.Context, keyCol, agg string, since int64, server string, idx map[string]int, put func(i int, key string, v float64)) error {
	day := `strftime('%Y-%m-%d', started_at, 'unixepoch', 'localtime')`
	postgres := store.DialectOf(s.repo.db) == store.Postgres
	if postgres {
		// Bucket epoch seconds with Go's application-local timezone, not the
		// remote database's timezone. This also handles DST and fixed zones
		// without guessing an equivalent PostgreSQL/IANA zone name.
		day = "started_at"
	}
	rows, err := s.repo.db.QueryContext(ctx, `
		SELECT `+day+` d, `+keyCol+`, `+agg+`
		FROM stream_sessions WHERE started_at >= ? AND `+graphMediaTypes+serverAnd("", server)+` GROUP BY d, `+keyCol, since)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var d, key string
		var v float64
		if err := rows.Scan(&d, &key, &v); err != nil {
			return err
		}
		if postgres {
			at, err := strconv.ParseInt(d, 10, 64)
			if err != nil {
				return err
			}
			d = time.Unix(at, 0).In(time.Local).Format("2006-01-02")
		}
		if i, ok := idx[d]; ok {
			put(i, key, v)
		}
	}
	return rows.Err()
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

// scanBuckets fills weekday or hour buckets with the aggregate. PostgreSQL
// groups by epoch second first, then the application applies its local timezone.
func scanBuckets(ctx context.Context, r *repo, kind, agg string, since int64, server string, out []float64) error {
	expr := `strftime('%H', started_at, 'unixepoch', 'localtime')`
	if kind == "weekday" {
		expr = `strftime('%w', started_at, 'unixepoch', 'localtime')`
	}
	postgres := store.DialectOf(r.db) == store.Postgres
	if postgres {
		expr = "started_at"
	}
	rows, err := r.db.QueryContext(ctx, `SELECT `+expr+` b, `+agg+` FROM stream_sessions WHERE started_at >= ?`+serverAnd("", server)+` GROUP BY b`, since)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var b string
		var c float64
		if err := rows.Scan(&b, &c); err != nil {
			return err
		}
		i := atoiSafe(b)
		if postgres {
			at, err := strconv.ParseInt(b, 10, 64)
			if err != nil {
				return err
			}
			local := time.Unix(at, 0).In(time.Local)
			i = local.Hour()
			if kind == "weekday" {
				i = int(local.Weekday())
			}
		}
		if i >= 0 && i < len(out) {
			out[i] += c
		}
	}
	return rows.Err()
}

// bandwidthSeries is the peak per hour. Each server's poller records its own samples, so
// across both servers the hour's figure is each server's peak added together: an upper
// bound when the two peaked at different moments, never an undercount.
func (s *Service) bandwidthSeries(ctx context.Context, since int64, server string) ([]BWPoint, error) {
	if store.DialectOf(s.repo.db) == store.Postgres {
		return s.bandwidthLocal(ctx, since, server)
	}
	rows, err := s.repo.db.QueryContext(ctx, `
		SELECT bucket, SUM(t), SUM(l), SUM(w) FROM (
			SELECT strftime('%Y-%m-%d %H:00', at, 'unixepoch', 'localtime') bucket, server,
			       MAX(total_kbps) t, MAX(lan_kbps) l, MAX(wan_kbps) w
			FROM bandwidth_samples WHERE at >= ?`+serverAnd("", server)+` GROUP BY bucket, server) AS peaks
		GROUP BY bucket ORDER BY bucket`, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []BWPoint{} // non-nil so the JSON is [] not null (a null white-screens the Graphs tab)
	for rows.Next() {
		var p BWPoint
		if err := rows.Scan(&p.T, &p.Total, &p.LAN, &p.WAN); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Service) bandwidthLocal(ctx context.Context, since int64, server string) ([]BWPoint, error) {
	rows, err := s.repo.db.QueryContext(ctx, `SELECT at, server, MAX(total_kbps), MAX(lan_kbps), MAX(wan_kbps)
		FROM bandwidth_samples WHERE at >= ?`+serverAnd("", server)+` GROUP BY at, server`, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type key struct{ bucket, server string }
	peaks := map[key]BWPoint{}
	for rows.Next() {
		var at int64
		var server string
		var point BWPoint
		if err := rows.Scan(&at, &server, &point.Total, &point.LAN, &point.WAN); err != nil {
			return nil, err
		}
		point.T = time.Unix(at, 0).In(time.Local).Format("2006-01-02 15:00")
		k := key{point.T, server}
		if previous, exists := peaks[k]; exists {
			point.Total = max(point.Total, previous.Total)
			point.LAN = max(point.LAN, previous.LAN)
			point.WAN = max(point.WAN, previous.WAN)
		}
		peaks[k] = point
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	totals := map[string]BWPoint{}
	for k, point := range peaks {
		total := totals[k.bucket]
		total.T = k.bucket
		var ok bool
		if total.Total, ok = bandwidthSum(total.Total, point.Total); !ok {
			return nil, errors.New("bandwidth total exceeds signed 64-bit range")
		}
		if total.LAN, ok = bandwidthSum(total.LAN, point.LAN); !ok {
			return nil, errors.New("bandwidth LAN total exceeds signed 64-bit range")
		}
		if total.WAN, ok = bandwidthSum(total.WAN, point.WAN); !ok {
			return nil, errors.New("bandwidth WAN total exceeds signed 64-bit range")
		}
		totals[k.bucket] = total
	}
	out := make([]BWPoint, 0, len(totals))
	for _, point := range totals {
		out = append(out, point)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].T < out[j].T })
	return out, nil
}

func bandwidthSum(a, b int64) (int64, bool) {
	sum := a + b
	return sum, !(b > 0 && sum < a || b < 0 && sum > a)
}

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
