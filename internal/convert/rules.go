package convert

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
)

// ErrRuleNotFound is returned when a rule id doesn't exist.
var ErrRuleNotFound = errors.New("convert rule not found")

// Filter is one condition a rule matches on (a linear flow's "when" — Rules v2 R2).
type Filter struct {
	Field string `json:"field"` // codec | resolution | size | container | hdr
	Op    string `json:"op"`    // is | not | gte | lte | gt | lt
	Value string `json:"value"` // stringified; parsed per field
}

// Action is one step in a rule's pipeline (the "do"). Params are type-specific.
type Action struct {
	Type   string            `json:"type"`             // transcode | extract_subs | add_stereo | loudnorm | keep_audio_langs
	Params map[string]string `json:"params,omitempty"` // e.g. {"langs":"eng,jpn"} or {"codec":"hevc","quality":"24"}
}

// Step is a node in a rule's flow (Rules v2 R4): either an action, or a condition that
// branches into then/else sub-flows. Arbitrarily nestable — this is what turns the linear
// rule into a branching flow ("if 4K → quality 22, else → 24").
type Step struct {
	Type   string  `json:"type"` // "action" | "condition"
	Action *Action `json:"action,omitempty"`
	Filter *Filter `json:"filter,omitempty"`
	Then   []Step  `json:"then,omitempty"`
	Else   []Step  `json:"else,omitempty"`
}

// Rule is a flow: filters (ALL gate whether the rule applies) + a body of steps. A body
// that's all actions is a linear rule (R2); conditions make it branch (R4).
type Rule struct {
	ID        int64    `json:"id"`
	Name      string   `json:"name"`
	Enabled   bool     `json:"enabled"`
	Auto      bool     `json:"auto"`
	Filters   []Filter `json:"filters"`
	Actions   []Action `json:"actions"` // linear body (back-compat); ignored when Steps is set
	Steps     []Step   `json:"steps"`   // branching body (takes precedence)
	// Schedule: when Auto is on, the rule runs on the sweep only within this window
	// ("HH:MM"–"HH:MM"; empty = any time; may wrap past midnight).
	WindowStart string `json:"window_start,omitempty"`
	WindowEnd   string `json:"window_end,omitempty"`
	CreatedAt   string `json:"created_at,omitempty"`
	// Match summary, filled at read time (not stored).
	Matches   int   `json:"matches"`
	SaveBytes int64 `json:"save_bytes"`
}

// matches reports whether a probed file satisfies every filter. It does NOT gate on the
// save-space "is this an inefficient codec" candidacy — a rule may legitimately target an
// already-HEVC file (downscale, container change, HDR re-tag). The worker's wouldBeNoOp check
// skips any job that would actually change nothing, so matching purely on filters is safe.
func (r *Rule) matches(c Candidate) bool {
	if c.Info == nil {
		return false
	}
	for _, f := range r.Filters {
		if !filterMatch(f, c.Info) {
			return false
		}
	}
	return true
}

func filterMatch(f Filter, mi *MediaInfo) bool {
	switch f.Field {
	case "codec":
		in := containsFold(splitCSV(f.Value), mi.VideoCodec)
		if f.Op == "not" {
			return !in
		}
		return in
	case "resolution":
		want, have := resRank(f.Value), resRank(mi.Resolution)
		switch f.Op {
		case "gte":
			return have >= want
		case "lte":
			return have <= want
		case "is":
			return have == want
		}
	case "bitrate":
		// Value is Mbps; compare against the file's overall bitrate. Length-independent, so a
		// rule can target "everything above 40 Mbps" regardless of runtime.
		v, _ := strconv.ParseFloat(f.Value, 64)
		mbps := float64(mi.BitrateKbps) / 1000.0
		switch f.Op {
		case "gt":
			return mbps > v
		case "lt":
			return mbps < v
		case "gte":
			return mbps >= v
		case "lte":
			return mbps <= v
		}
	case "size":
		v, _ := strconv.ParseInt(f.Value, 10, 64)
		switch f.Op {
		case "gt":
			return mi.SizeBytes > v
		case "lt":
			return mi.SizeBytes < v
		case "gte":
			return mi.SizeBytes >= v
		case "lte":
			return mi.SizeBytes <= v
		}
	case "container":
		is := strings.EqualFold(mi.Container, f.Value)
		if f.Op == "not" {
			return !is
		}
		return is
	case "hdr":
		is := strings.EqualFold(mi.HDR, f.Value)
		if f.Op == "not" {
			return !is
		}
		return is
	}
	return true // unknown field → don't filter on it
}

// planFor compiles the rule's flow into a conversion Plan for a specific file — walking the
// step tree, evaluating condition nodes against the file's MediaInfo so branches resolve.
// (With branching the Plan is file-dependent; mi is nil only for structure-only callers.)
func (r *Rule) planFor(mi *MediaInfo) Plan {
	p := Plan{VideoCodec: "hevc", Quality: 0, VFRToCFR: true, Container: "mkv"}
	applySteps(r.effectiveSteps(), mi, &p)
	return p
}

// effectiveSteps returns the branching body, or the linear actions wrapped as action steps.
func (r *Rule) effectiveSteps() []Step {
	if len(r.Steps) > 0 {
		return r.Steps
	}
	out := make([]Step, len(r.Actions))
	for i := range r.Actions {
		a := r.Actions[i]
		out[i] = Step{Type: "action", Action: &a}
	}
	return out
}

func applySteps(steps []Step, mi *MediaInfo, p *Plan) {
	for _, s := range steps {
		if s.Type == "condition" && s.Filter != nil {
			if mi != nil && filterMatch(*s.Filter, mi) {
				applySteps(s.Then, mi, p)
			} else {
				applySteps(s.Else, mi, p)
			}
			continue
		}
		if s.Action != nil {
			applyAction(*s.Action, p)
		}
	}
}

func applyAction(a Action, p *Plan) {
	switch a.Type {
	case "transcode":
		if c := a.Params["codec"]; c != "" {
			p.VideoCodec = normCodec(c)
		}
		if q, err := strconv.Atoi(a.Params["quality"]); err == nil && q > 0 {
			p.Quality = q
		}
	case "remux": // container/track work only — copy the video stream
		p.VideoCodec = ""
	case "scale": // downscale to a target height (never upscales; enforced in the compiler)
		if h, err := strconv.Atoi(a.Params["height"]); err == nil && h > 0 {
			p.ScaleHeight = h
		}
	case "container": // set the output container (mkv | mp4)
		if f := strings.ToLower(strings.TrimSpace(a.Params["format"])); f == "mkv" || f == "mp4" {
			p.Container = f
		}
	case "extract_subs":
		p.Subs.ExtractText = true
		p.Subs.ExtractCC = true
	case "add_stereo":
		p.Audio.AddStereo = true
	case "loudnorm":
		p.Audio.Loudnorm = true
	case "keep_audio_langs":
		p.Audio.KeepLangs = splitCSV(a.Params["langs"])
	case "health_check": // read-only corruption scan (no transcode) — see service.process
		p.HealthCheck = true
		p.VideoCodec = ""
	case "raw_ffmpeg": // advanced escape hatch: append raw output args verbatim
		p.ExtraArgs = append(p.ExtraArgs, splitArgs(a.Params["args"])...)
	}
}

// normCodec maps friendly codec names to the ffmpeg codec family the compiler expects.
func normCodec(c string) string {
	switch strings.ToLower(strings.TrimSpace(c)) {
	case "h264", "avc", "x264":
		return "h264"
	case "av1":
		return "av1"
	case "copy", "remux", "none":
		return ""
	default:
		return "hevc"
	}
}

// splitArgs splits a raw ffmpeg arg string on whitespace (simple; quoting isn't supported —
// this is a power-user escape hatch, kept deliberately literal).
func splitArgs(s string) []string { return strings.Fields(s) }

// resRank maps a resolution label to the rank filters compare against.
func resRank(res string) int {
	switch res {
	case "2160p":
		return 2160
	case "1080p":
		return 1080
	case "720p":
		return 720
	case "SD":
		return 480
	}
	return 0
}

func splitCSV(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
func containsFold(list []string, v string) bool {
	for _, x := range list {
		if strings.EqualFold(x, v) {
			return true
		}
	}
	return false
}

// --- persistence ---

type ruleRepo struct{ db *sql.DB }

const ruleCols = `id, name, enabled, auto, filters_json, actions_json, steps_json, window_start, window_end, created_at`

func scanRule(row interface{ Scan(...any) error }) (Rule, error) {
	var (
		r                       Rule
		en, au                  int
		filters, actions, steps string
	)
	if err := row.Scan(&r.ID, &r.Name, &en, &au, &filters, &actions, &steps, &r.WindowStart, &r.WindowEnd, &r.CreatedAt); err != nil {
		return Rule{}, err
	}
	r.Enabled = en != 0
	r.Auto = au != 0
	if filters != "" {
		_ = json.Unmarshal([]byte(filters), &r.Filters)
	}
	if actions != "" {
		_ = json.Unmarshal([]byte(actions), &r.Actions)
	}
	if steps != "" {
		_ = json.Unmarshal([]byte(steps), &r.Steps)
	}
	return r, nil
}

func (rr *ruleRepo) list(ctx context.Context) ([]Rule, error) {
	rows, err := rr.db.QueryContext(ctx, `SELECT `+ruleCols+` FROM convert_rules ORDER BY id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Rule
	for rows.Next() {
		r, err := scanRule(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (rr *ruleRepo) get(ctx context.Context, id int64) (Rule, error) {
	row := rr.db.QueryRowContext(ctx, `SELECT `+ruleCols+` FROM convert_rules WHERE id = ?`, id)
	r, err := scanRule(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Rule{}, ErrRuleNotFound
	}
	return r, err
}

func (rr *ruleRepo) create(ctx context.Context, r Rule) (Rule, error) {
	filters, _ := json.Marshal(r.Filters)
	actions, _ := json.Marshal(r.Actions)
	steps, _ := json.Marshal(r.Steps)
	res, err := rr.db.ExecContext(ctx,
		`INSERT INTO convert_rules (name, enabled, auto, filters_json, actions_json, steps_json, window_start, window_end) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		r.Name, b2i(r.Enabled), b2i(r.Auto), string(filters), string(actions), string(steps), r.WindowStart, r.WindowEnd)
	if err != nil {
		return Rule{}, err
	}
	id, _ := res.LastInsertId()
	return rr.get(ctx, id)
}

func (rr *ruleRepo) setEnabled(ctx context.Context, id int64, enabled bool) error {
	res, err := rr.db.ExecContext(ctx, `UPDATE convert_rules SET enabled = ? WHERE id = ?`, b2i(enabled), id)
	return affected(res, err)
}

func (rr *ruleRepo) delete(ctx context.Context, id int64) error {
	res, err := rr.db.ExecContext(ctx, `DELETE FROM convert_rules WHERE id = ?`, id)
	return affected(res, err)
}

// --- failure quarantine (C4) ---

func (rr *ruleRepo) recordFailure(ctx context.Context, movieID int64, errMsg string) {
	if len(errMsg) > 300 {
		errMsg = errMsg[:300]
	}
	_, _ = rr.db.ExecContext(ctx,
		`INSERT INTO convert_failures (movie_id, count, last_error, updated_at)
		 VALUES (?, 1, ?, datetime('now'))
		 ON CONFLICT(movie_id) DO UPDATE SET count = count + 1, last_error = excluded.last_error, updated_at = datetime('now')`,
		movieID, errMsg)
}

func (rr *ruleRepo) clearFailures(ctx context.Context, movieID int64) {
	_, _ = rr.db.ExecContext(ctx, `DELETE FROM convert_failures WHERE movie_id = ?`, movieID)
}

func (rr *ruleRepo) failureCount(ctx context.Context, movieID int64) int {
	var n int
	_ = rr.db.QueryRowContext(ctx, `SELECT count FROM convert_failures WHERE movie_id = ?`, movieID).Scan(&n)
	return n
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

func affected(res sql.Result, err error) error {
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrRuleNotFound
	}
	return nil
}
