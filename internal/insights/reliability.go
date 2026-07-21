package insights

import (
	"context"
	"strconv"
)

// Reliability is the buffering-history bundle — the "did things buffer, and why?" view
// Tautulli lacks.
type Reliability struct {
	Summary    ReliabilitySummary `json:"summary"`
	Causes     []CauseCount       `json:"causes"`
	ByUser     []BufferGroup      `json:"by_user"`
	ByPlatform []BufferGroup      `json:"by_platform"`
	ByTitle    []BufferGroup      `json:"by_title"`
	Events     []BufferEvent      `json:"events"`
}

// CauseCount is how many buffer spells fell into a diagnosed cause bucket.
type CauseCount struct {
	Cause   string `json:"cause"`
	Label   string `json:"label"`
	Count   int    `json:"count"`
	StallMS int64  `json:"stall_ms"` // observed stall time in this bucket
}

// ReliabilitySummary is the top-line health of the window.
type ReliabilitySummary struct {
	TotalSessions    int     `json:"total_sessions"`
	BufferedSessions int     `json:"buffered_sessions"`
	TotalEvents      int     `json:"total_events"`
	TotalStallMS     int64   `json:"total_stall_ms"` // observed stall time across the window
	BufferRatePct    float64 `json:"buffer_rate_pct"`
}

// BufferGroup is buffering rolled up by user / platform / title.
type BufferGroup struct {
	Name             string  `json:"name"`
	Sessions         int     `json:"sessions"`
	BufferedSessions int     `json:"buffered_sessions"`
	Events           int     `json:"events"`
	StallMS          int64   `json:"stall_ms"` // observed stall time for the group
	RatePct          float64 `json:"rate_pct"` // buffered / sessions
}

// BufferEvent is one recorded buffer spell with its stream context and diagnosed cause.
type BufferEvent struct {
	At         int64  `json:"at"`
	OffsetMS   int64  `json:"offset_ms"`
	DurationMS int64  `json:"duration_ms"` // observed stall time (0 = single-poll blip or legacy row)
	User       string `json:"user"`
	Title      string `json:"title"`
	Platform   string `json:"platform"`
	Decision   string `json:"decision"`
	Cause      string `json:"cause"`  // key: transcode | transcode_cpu | bandwidth | unknown
	Detail     string `json:"detail"` // human-readable "why"
}

// causeLabel maps a cause key to a short user-facing label.
func causeLabel(cause string) string {
	switch cause {
	case "transcode":
		return "Transcode overloaded"
	case "transcode_cpu":
		return "CPU transcode (no HW)"
	case "bandwidth":
		return "Bandwidth / network"
	default:
		return "Inconclusive"
	}
}

// Reliability computes the buffering-history bundle over the last windowDays.
func (s *Service) Reliability(ctx context.Context, windowDays int) (Reliability, error) {
	if windowDays <= 0 {
		windowDays = 30
	}
	since := windowStart(windowDays)
	// Non-nil so empty windows serialize as [] not null (a null crashes the Reliability tab).
	out := Reliability{Causes: []CauseCount{}, Events: []BufferEvent{}}

	// Summary.
	err := s.repo.db.QueryRowContext(ctx, `
		SELECT COUNT(*), COALESCE(SUM(CASE WHEN buffer_count>0 THEN 1 ELSE 0 END),0), COALESCE(SUM(buffer_count),0)
		FROM stream_sessions WHERE started_at >= ?`, since).
		Scan(&out.Summary.TotalSessions, &out.Summary.BufferedSessions, &out.Summary.TotalEvents)
	if err != nil {
		return Reliability{}, err
	}
	if out.Summary.TotalSessions > 0 {
		out.Summary.BufferRatePct = round1(float64(out.Summary.BufferedSessions) * 100 / float64(out.Summary.TotalSessions))
	}
	_ = s.repo.db.QueryRowContext(ctx,
		`SELECT COALESCE(SUM(duration_ms),0) FROM buffer_events WHERE at >= ?`, since).
		Scan(&out.Summary.TotalStallMS)

	// Offenders — only groups that actually buffered, worst first.
	titleExpr := `CASE WHEN media_type='episode' AND grandparent_title<>'' THEN grandparent_title ELSE title END`
	if out.ByUser, err = s.bufferGroups(ctx, "user_name", since); err != nil {
		return Reliability{}, err
	}
	if out.ByPlatform, err = s.bufferGroups(ctx, "platform", since); err != nil {
		return Reliability{}, err
	}
	if out.ByTitle, err = s.bufferGroups(ctx, titleExpr, since); err != nil {
		return Reliability{}, err
	}

	// Cause breakdown — how many buffer spells fell into each diagnosed bucket.
	crows, err := s.repo.db.QueryContext(ctx, `
		SELECT COALESCE(NULLIF(cause,''),'unknown') c, COUNT(*), COALESCE(SUM(duration_ms),0) FROM buffer_events
		WHERE at >= ? GROUP BY c ORDER BY SUM(duration_ms) DESC, COUNT(*) DESC`, since)
	if err != nil {
		return Reliability{}, err
	}
	for crows.Next() {
		var cc CauseCount
		if err := crows.Scan(&cc.Cause, &cc.Count, &cc.StallMS); err != nil {
			crows.Close()
			return Reliability{}, err
		}
		cc.Label = causeLabel(cc.Cause)
		out.Causes = append(out.Causes, cc)
	}
	crows.Close()

	// Recent buffer-event timeline.
	rows, err := s.repo.db.QueryContext(ctx, `
		SELECT b.at, b.view_offset_ms, b.duration_ms, b.cause, b.detail, s.user_name, s.title, s.grandparent_title, s.media_type, s.platform, s.decision
		FROM buffer_events b JOIN stream_sessions s ON s.id = b.session_id
		WHERE b.at >= ? ORDER BY b.at DESC LIMIT 60`, since)
	if err != nil {
		return Reliability{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var e BufferEvent
		var gp, mt string
		if err := rows.Scan(&e.At, &e.OffsetMS, &e.DurationMS, &e.Cause, &e.Detail, &e.User, &e.Title, &gp, &mt, &e.Platform, &e.Decision); err != nil {
			return Reliability{}, err
		}
		if mt == "episode" && gp != "" {
			e.Title = gp
		}
		out.Events = append(out.Events, e)
	}
	return out, rows.Err()
}

// Offender floor: one sampled blip must never put someone on a leaderboard. A group
// appears only with a repeated pattern (>= minOffenderEvents observed stalls) or real
// felt time (>= minOffenderStallMS). Sampled telemetry earns a spot; a fluke doesn't.
const (
	minOffenderEvents  = 3
	minOffenderStallMS = 10_000
)

// bufferGroups rolls sessions up by a column/expression, keeping only groups whose
// buffering clears the offender floor, worst observed stall time first.
func (s *Service) bufferGroups(ctx context.Context, groupExpr string, since int64) ([]BufferGroup, error) {
	q := `SELECT g, COUNT(*), SUM(buffered), SUM(events), SUM(stall_ms) FROM (
			SELECT ` + groupExpr + ` g,
				CASE WHEN s.buffer_count>0 THEN 1 ELSE 0 END buffered,
				s.buffer_count events,
				COALESCE((SELECT SUM(b.duration_ms) FROM buffer_events b WHERE b.session_id = s.id),0) stall_ms
			FROM stream_sessions s WHERE s.started_at >= ? AND ` + groupExpr + ` <> ''
		)
		GROUP BY g
		HAVING SUM(events) >= ` + strconv.Itoa(minOffenderEvents) + ` OR SUM(stall_ms) >= ` + strconv.Itoa(minOffenderStallMS) + `
		ORDER BY SUM(stall_ms) DESC, SUM(events) DESC LIMIT 10`
	rows, err := s.repo.db.QueryContext(ctx, q, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []BufferGroup{} // non-nil so an empty roll-up serializes as [] not null
	for rows.Next() {
		var g BufferGroup
		if err := rows.Scan(&g.Name, &g.Sessions, &g.BufferedSessions, &g.Events, &g.StallMS); err != nil {
			return nil, err
		}
		if g.Sessions > 0 {
			g.RatePct = round1(float64(g.BufferedSessions) * 100 / float64(g.Sessions))
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

func round1(f float64) float64 {
	return float64(int(f*10+0.5)) / 10
}
