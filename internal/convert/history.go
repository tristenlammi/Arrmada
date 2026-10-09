package convert

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// The conversion ledger (convert_history). The job list on the Overview is in memory and
// capped, and the activity log is free text, so neither can say afterwards exactly what a
// conversion did to a file. Every outcome worth remembering gets one row here: what the file
// was, what it became, how it scored, which tracks went and why. Converted rows are kept for
// good; everything else is pruned after historyKeepDays.
//
// What is recorded:
//   - a finished conversion;
//   - a failure that isn't just a full disk (those back off and say so in Problems);
//   - a file kept as it was because the encode (or its test encode) didn't save enough or
//     couldn't reach the quality bar;
//   - any other end to a job whose full encode had started, cancelled included.
//
// Skips before any encode (already the target, still seeding, no room yet) are not: they
// are in Problems, and would only bury the real outcomes.

// historyKeepDays is how long rows other than finished conversions are kept.
const historyKeepDays = 90

// Ledger outcomes. in_progress is written when a full encode starts, so a restart mid-job
// leaves a row that says so instead of nothing.
const (
	OutcomeInProgress = "in_progress"
	OutcomeDone       = "done"
	OutcomeFailed     = "failed"
	OutcomeSkipped    = "skipped"
	OutcomeCancelled  = "cancelled"
)

// jobRecord collects what the ledger keeps about one job while it runs. Only the job's own
// goroutine touches it (process, finalizeOutput and finish all run there).
type jobRecord struct {
	srcPath, srcRelease string
	srcInfo, outInfo    *MediaInfo
	outPath             string
	outSize             int64
	plan                Plan
	hasPlan             bool
	skipKind            string
	ssimMean            float64
	ssimWindows         []float64
	reclaimDeferred     bool
	encodeStart         time.Time
	encodeEnd           time.Time
	historyID           int64 // the in_progress row, once the encode has started
}

// record returns the job's ledger record, creating it on first use. Created under s.mu so
// the pointer is never written while Jobs() copies the job.
func (s *Service) record(job *Job) *jobRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	if job.rec == nil {
		job.rec = &jobRecord{}
	}
	return job.rec
}

// setPlan remembers the plan a job is running with (it changes as the codec, crop and
// quality target are settled).
func (r *jobRecord) setPlan(p Plan) { r.plan, r.hasPlan = p, true }

// TrackDecision is one audio or subtitle track of the original and what the conversion did
// with it.
type TrackDecision struct {
	Type     string `json:"type"`  // "audio" | "subtitle"
	Index    int    `json:"index"` // position among that type's streams in the original
	Codec    string `json:"codec"`
	Lang     string `json:"lang,omitempty"`
	Title    string `json:"title,omitempty"`
	Channels int    `json:"channels,omitempty"`
	Forced   bool   `json:"forced,omitempty"`
	Image    bool   `json:"image,omitempty"` // an image subtitle (PGS/VobSub)
	Keep     bool   `json:"keep"`
	Reason   string `json:"reason,omitempty"`
}

// trackDecisions lists every audio and subtitle track of the original with whether the plan
// keeps it. Which tracks survive comes from keptAudio/keptSubs themselves — the very
// functions the encode and the verification use — so the ledger can't disagree with what was
// done; this only adds a reason in words.
func trackDecisions(mi *MediaInfo, plan Plan) []TrackDecision {
	if mi == nil {
		return nil
	}
	var out []TrackDecision
	keptA := map[int]bool{}
	for _, au := range keptAudio(mi, plan) {
		keptA[au.AudIndex] = true
	}
	wantedAudio := append([]string{}, plan.Audio.KeepLangs...)
	if plan.Audio.OriginalLang != "" {
		wantedAudio = append(wantedAudio, plan.Audio.OriginalLang)
	}
	for _, au := range mi.Audio {
		d := TrackDecision{Type: "audio", Index: au.AudIndex, Codec: au.Codec, Lang: au.Lang, Title: au.Title,
			Channels: au.Channels, Keep: keptA[au.AudIndex]}
		dropByRule := (plan.Audio.DropCommentary && au.Commentary) ||
			(len(plan.Audio.KeepLangs) > 0 && !langIn(au.Lang, wantedAudio))
		switch {
		case d.Keep && dropByRule:
			d.Reason = "kept — nothing else would remain"
		case !d.Keep && plan.Audio.DropCommentary && au.Commentary:
			d.Reason = "commentary"
		case !d.Keep:
			d.Reason = "language not kept"
		}
		out = append(out, d)
	}
	keptS := map[int]bool{}
	for _, sub := range keptSubs(mi, plan) {
		keptS[sub.SubIndex] = true
	}
	for _, sub := range mi.Subs {
		d := TrackDecision{Type: "subtitle", Index: sub.SubIndex, Codec: sub.Codec, Lang: sub.Lang,
			Forced: sub.Forced, Image: !sub.Text, Keep: keptS[sub.SubIndex]}
		if !d.Keep {
			langOK := len(plan.Subs.KeepLangs) == 0 || langIn(sub.Lang, plan.Subs.KeepLangs)
			switch {
			case !sub.Text && langOK && plan.Subs.ImageSubs == ImageSubsRemove:
				d.Reason = "image subtitles removed"
			case !sub.Text && langOK:
				d.Reason = "image subtitle covered by text"
			default:
				d.Reason = "language not kept"
			}
		}
		out = append(out, d)
	}
	return out
}

// HistoryEntry is one ledger row, for the History list and its detail view.
type HistoryEntry struct {
	ID          int64  `json:"id"`
	Key         string `json:"key"`
	Kind        string `json:"kind"` // "movie" | "episode"
	MovieID     int64  `json:"movie_id,omitempty"`
	SeriesID    int64  `json:"series_id,omitempty"`
	Season      int    `json:"season"` // NOT omitempty: season 0 is specials
	Episode     int    `json:"episode,omitempty"`
	Title       string `json:"title"`
	Outcome     string `json:"outcome"`
	OutcomeKind string `json:"outcome_kind,omitempty"`
	Note        string `json:"note,omitempty"`
	Requested   bool   `json:"requested"`

	SrcPath    string `json:"src_path,omitempty"`
	SrcRelease string `json:"src_release,omitempty"`
	SrcSize    int64  `json:"src_size"`
	SrcSpec    string `json:"src_spec,omitempty"` // "HEVC · 3840×2160 (2160p) · HDR10 · …"
	OutPath    string `json:"out_path,omitempty"`
	OutSize    int64  `json:"out_size"`
	OutSpec    string `json:"out_spec,omitempty"`
	Codec      string `json:"codec,omitempty"`
	CRF        int    `json:"crf,omitempty"`
	Encoder    string `json:"encoder,omitempty"`
	Crop       string `json:"crop,omitempty"`

	SSIMMean    float64   `json:"ssim_mean,omitempty"`
	SSIMMin     float64   `json:"ssim_min,omitempty"`
	SSIMWindows []float64 `json:"ssim_windows,omitempty"`

	Kept            []TrackDecision `json:"kept_tracks,omitempty"`
	Dropped         []TrackDecision `json:"dropped_tracks,omitempty"`
	Warnings        []string        `json:"warnings,omitempty"`
	ReclaimDeferred bool            `json:"reclaim_deferred,omitempty"`

	StartedAt  int64   `json:"started_at"`
	FinishedAt int64   `json:"finished_at,omitempty"`
	EncodeSecs float64 `json:"encode_secs,omitempty"`

	// Only on the single-row read: the full probed spec of the original and the result.
	SrcInfo *MediaInfo `json:"src_info,omitempty"`
	OutInfo *MediaInfo `json:"out_info,omitempty"`
}

// HistoryFilter narrows the History list. Before is the cursor a previous page returned.
type HistoryFilter struct {
	Outcome string // one of the Outcome* values; "" = every finished outcome
	Media   string // "movie" | "episode"; "" = both
	Q       string // title contains
	Before  string
	Limit   int
}

// historyStore persists the ledger.
type historyStore struct{ db *sql.DB }

// historyRow is the full set of columns one write sets.
type historyRow struct {
	key, kind                        string
	movieID, seriesID                int64
	season, episode                  int
	title, outcome, outcomeKind      string
	note                             string
	requested                        bool
	srcPath, srcRelease, srcInfoJSON string
	srcSize                          int64
	outPath, outInfoJSON             string
	outSize                          int64
	codec, encoder, crop             string
	crf                              int
	ssimMean, ssimMin                float64
	ssimWindowsJSON                  string
	keptJSON, droppedJSON, warnJSON  string
	reclaimDeferred                  bool
	startedAt, finishedAt            int64
	encodeSecs                       float64
}

// begin writes the in_progress row for a job whose full encode is starting and returns its
// id (0 if it couldn't be written — finish then inserts the row whole).
func (h *historyStore) begin(ctx context.Context, r historyRow) int64 {
	if h == nil || h.db == nil {
		return 0
	}
	r.outcome = OutcomeInProgress
	return h.insert(ctx, r)
}

// finish writes a job's final row: an update of its in_progress row when there is one,
// otherwise a new row.
func (h *historyStore) finish(ctx context.Context, id int64, r historyRow) int64 {
	if h == nil || h.db == nil {
		return 0
	}
	if id > 0 {
		res, err := h.db.ExecContext(ctx, `UPDATE convert_history SET
			  title = ?, outcome = ?, outcome_kind = ?, note = ?, requested = ?,
			  src_path = ?, src_release = ?, src_size = ?, src_info_json = ?,
			  out_path = ?, out_size = ?, out_info_json = ?, codec = ?, crf = ?, encoder = ?,
			  ssim_mean = ?, ssim_min = ?, ssim_windows_json = ?, crop = ?,
			  kept_tracks_json = ?, dropped_tracks_json = ?, warnings_json = ?, reclaim_deferred = ?,
			  started_at = ?, finished_at = ?, encode_secs = ?
			WHERE id = ?`,
			r.title, r.outcome, r.outcomeKind, r.note, boolInt(r.requested),
			r.srcPath, r.srcRelease, r.srcSize, r.srcInfoJSON,
			r.outPath, r.outSize, r.outInfoJSON, r.codec, r.crf, r.encoder,
			r.ssimMean, r.ssimMin, r.ssimWindowsJSON, r.crop,
			r.keptJSON, r.droppedJSON, r.warnJSON, boolInt(r.reclaimDeferred),
			r.startedAt, r.finishedAt, r.encodeSecs, id)
		if err == nil {
			if n, _ := res.RowsAffected(); n > 0 {
				return id
			}
		}
	}
	return h.insert(ctx, r)
}

func (h *historyStore) insert(ctx context.Context, r historyRow) int64 {
	res, err := h.db.ExecContext(ctx, `INSERT INTO convert_history (
		  item_key, kind, movie_id, series_id, season, episode, title, outcome, outcome_kind, note, requested,
		  src_path, src_release, src_size, src_info_json, out_path, out_size, out_info_json, codec, crf, encoder,
		  ssim_mean, ssim_min, ssim_windows_json, crop, kept_tracks_json, dropped_tracks_json, warnings_json,
		  reclaim_deferred, started_at, finished_at, encode_secs)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.key, r.kind, r.movieID, r.seriesID, r.season, r.episode, r.title, r.outcome, r.outcomeKind, r.note, boolInt(r.requested),
		r.srcPath, r.srcRelease, r.srcSize, r.srcInfoJSON, r.outPath, r.outSize, r.outInfoJSON, r.codec, r.crf, r.encoder,
		r.ssimMean, r.ssimMin, r.ssimWindowsJSON, r.crop, r.keptJSON, r.droppedJSON, r.warnJSON,
		boolInt(r.reclaimDeferred), r.startedAt, r.finishedAt, r.encodeSecs)
	if err != nil {
		return 0
	}
	id, _ := res.LastInsertId()
	return id
}

// interrupted closes the rows a restart cut off. Runs at startup, before any worker, so
// every in_progress row left is from a job that no longer exists.
func (h *historyStore) interrupted(ctx context.Context) int64 {
	if h == nil || h.db == nil {
		return 0
	}
	res, err := h.db.ExecContext(ctx,
		`UPDATE convert_history SET outcome = ?, note = 'interrupted by a restart before it finished', finished_at = ?
		 WHERE outcome = ?`, OutcomeFailed, time.Now().Unix(), OutcomeInProgress)
	if err != nil {
		return 0
	}
	n, _ := res.RowsAffected()
	return n
}

// prune drops rows other than finished conversions once they're historyKeepDays old. Those
// are the record of what was changed (and what a revert works from), so they stay.
func (h *historyStore) prune(ctx context.Context) {
	if h == nil || h.db == nil {
		return
	}
	cutoff := time.Now().AddDate(0, 0, -historyKeepDays).Unix()
	_, _ = h.db.ExecContext(ctx,
		`DELETE FROM convert_history WHERE outcome NOT IN (?, ?) AND finished_at > 0 AND finished_at < ?`,
		OutcomeDone, OutcomeInProgress, cutoff)
}

const historyCols = `id, item_key, kind, movie_id, series_id, season, episode, title, outcome, outcome_kind, note, requested,
	src_path, src_release, src_size, src_info_json, out_path, out_size, out_info_json, codec, crf, encoder,
	ssim_mean, ssim_min, ssim_windows_json, crop, kept_tracks_json, dropped_tracks_json, warnings_json,
	reclaim_deferred, started_at, finished_at, encode_secs`

// list returns one page of the ledger, newest first, and the cursor for the next page (""
// when this was the last). Rows still in progress are left out unless asked for: the
// Overview already shows what's running.
func (h *historyStore) list(ctx context.Context, f HistoryFilter) ([]HistoryEntry, string, error) {
	if f.Limit <= 0 {
		f.Limit = 50
	}
	f.Limit = min(f.Limit, 200)
	where := []string{}
	var args []any
	if f.Outcome != "" {
		where = append(where, "outcome = ?")
		args = append(args, f.Outcome)
	} else {
		where = append(where, "outcome <> ?")
		args = append(args, OutcomeInProgress)
	}
	if f.Media != "" {
		where = append(where, "kind = ?")
		args = append(args, f.Media)
	}
	if q := strings.TrimSpace(f.Q); q != "" {
		where = append(where, "title LIKE ? ESCAPE '\\'")
		args = append(args, "%"+likeEscape(q)+"%")
	}
	if f.Before != "" {
		at, id, ok := parseHistoryCursor(f.Before)
		if !ok {
			return nil, "", ErrHistoryCursor
		}
		where = append(where, "(finished_at < ? OR (finished_at = ? AND id < ?))")
		args = append(args, at, at, id)
	}
	args = append(args, f.Limit+1)
	rows, err := h.db.QueryContext(ctx, `SELECT `+historyCols+` FROM convert_history WHERE `+
		strings.Join(where, " AND ")+` ORDER BY finished_at DESC, id DESC LIMIT ?`, args...)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	out := []HistoryEntry{}
	for rows.Next() {
		e, err := scanHistory(rows, false)
		if err != nil {
			return nil, "", err
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	next := ""
	if len(out) > f.Limit {
		out = out[:f.Limit]
		last := out[len(out)-1]
		next = fmt.Sprintf("%d:%d", last.FinishedAt, last.ID)
	}
	return out, next, nil
}

// ErrNoHistory is returned for a ledger row that doesn't exist; ErrHistoryCursor for a
// page cursor the list didn't hand out.
var (
	ErrNoHistory     = errors.New("no such conversion record")
	ErrHistoryCursor = errors.New("invalid history cursor")
)

// get returns one row with the full probed spec of both files.
func (h *historyStore) get(ctx context.Context, id int64) (HistoryEntry, error) {
	row := h.db.QueryRowContext(ctx, `SELECT `+historyCols+` FROM convert_history WHERE id = ?`, id)
	e, err := scanHistory(row, true)
	if errors.Is(err, sql.ErrNoRows) {
		return e, ErrNoHistory
	}
	return e, err
}

type rowScanner interface{ Scan(dest ...any) error }

func scanHistory(sc rowScanner, full bool) (HistoryEntry, error) {
	var e HistoryEntry
	var requested, deferred int
	var srcInfo, outInfo, windows, kept, dropped, warns string
	if err := sc.Scan(&e.ID, &e.Key, &e.Kind, &e.MovieID, &e.SeriesID, &e.Season, &e.Episode, &e.Title,
		&e.Outcome, &e.OutcomeKind, &e.Note, &requested,
		&e.SrcPath, &e.SrcRelease, &e.SrcSize, &srcInfo, &e.OutPath, &e.OutSize, &outInfo, &e.Codec, &e.CRF, &e.Encoder,
		&e.SSIMMean, &e.SSIMMin, &windows, &e.Crop, &kept, &dropped, &warns,
		&deferred, &e.StartedAt, &e.FinishedAt, &e.EncodeSecs); err != nil {
		return e, err
	}
	e.Requested, e.ReclaimDeferred = requested == 1, deferred == 1
	_ = unmarshalIf(windows, &e.SSIMWindows)
	_ = unmarshalIf(kept, &e.Kept)
	_ = unmarshalIf(dropped, &e.Dropped)
	_ = unmarshalIf(warns, &e.Warnings)
	var si, oi MediaInfo
	if unmarshalIf(srcInfo, &si) {
		e.SrcSpec = mediaSpec(&si)
		if full {
			e.SrcInfo = &si
		}
	}
	if unmarshalIf(outInfo, &oi) {
		e.OutSpec = mediaSpec(&oi)
		if full {
			e.OutInfo = &oi
		}
	}
	return e, nil
}

// unmarshalIf decodes a JSON column when it holds anything, reporting whether it did.
func unmarshalIf(raw string, v any) bool {
	return raw != "" && json.Unmarshal([]byte(raw), v) == nil
}

func parseHistoryCursor(c string) (at, id int64, ok bool) {
	a, b, found := strings.Cut(c, ":")
	if !found {
		return 0, 0, false
	}
	at, e1 := strconv.ParseInt(a, 10, 64)
	id, e2 := strconv.ParseInt(b, 10, 64)
	return at, id, e1 == nil && e2 == nil
}

// likeEscape makes a search term match literally inside a LIKE pattern.
func likeEscape(q string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(q)
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func jsonString(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(b)
}

// --- the Service side ---------------------------------------------------------------------

// History returns one page of the conversion ledger.
func (s *Service) History(ctx context.Context, f HistoryFilter) ([]HistoryEntry, string, error) {
	if s.history == nil || s.history.db == nil {
		return []HistoryEntry{}, "", nil
	}
	return s.history.list(ctx, f)
}

// HistoryEntry returns one ledger row in full.
func (s *Service) HistoryEntry(ctx context.Context, id int64) (HistoryEntry, error) {
	if s.history == nil || s.history.db == nil {
		return HistoryEntry{}, ErrNoHistory
	}
	return s.history.get(ctx, id)
}

// beginHistory writes the in_progress row as a job's full encode starts.
func (s *Service) beginHistory(job *Job) {
	rec := s.record(job)
	rec.encodeStart = time.Now()
	r := s.historyRowFor(job, OutcomeInProgress, "")
	rec.historyID = s.history.begin(context.Background(), r)
}

// ledgerWorthy decides whether a job's end belongs in the ledger (see the top of this file).
func ledgerWorthy(state JobState, note string, rec *jobRecord) bool {
	if !rec.encodeStart.IsZero() {
		return true // the in_progress row must be closed, whatever ended the job
	}
	switch state {
	case StateDone:
		return true
	case StateFailed:
		return !transientFailure(note)
	case StateSkipped:
		// Kept as it was on a test encode's evidence: an outcome, not a wait.
		return rec.skipKind == SkipNotSmaller || rec.skipKind == SkipQualityGate
	}
	return false
}

// recordOutcome writes a finished job's ledger row, when it has one. Called from finish,
// outside s.mu.
func (s *Service) recordOutcome(job *Job, state JobState, note string) {
	rec := s.record(job)
	if !ledgerWorthy(state, note, rec) {
		return
	}
	outcome := map[JobState]string{StateDone: OutcomeDone, StateFailed: OutcomeFailed,
		StateSkipped: OutcomeSkipped, StateCancelled: OutcomeCancelled}[state]
	r := s.historyRowFor(job, outcome, note)
	rec.historyID = s.history.finish(context.Background(), rec.historyID, r)
}

// historyRowFor assembles a row from the job and its record as they stand.
func (s *Service) historyRowFor(job *Job, outcome, note string) historyRow {
	s.mu.Lock()
	j := *job
	s.mu.Unlock()
	rec := s.record(job)
	r := historyRow{
		key: j.Key, kind: j.Kind, movieID: j.MovieID, seriesID: j.SeriesID, season: j.Season, episode: j.Episode,
		title: j.Title, outcome: outcome, note: note, requested: j.Requested, encoder: j.Encoder,
		srcPath: rec.srcPath, srcRelease: rec.srcRelease, srcSize: j.SrcBytes,
		outPath: rec.outPath, outSize: rec.outSize, reclaimDeferred: rec.reclaimDeferred,
		startedAt: j.StartedAt, finishedAt: j.FinishedAt,
	}
	if outcome == OutcomeInProgress {
		r.finishedAt = 0
	}
	if outcome == OutcomeSkipped {
		r.outcomeKind = rec.skipKind
	}
	if rec.srcInfo != nil {
		r.srcSize = rec.srcInfo.SizeBytes
		r.srcInfoJSON = jsonString(rec.srcInfo)
	}
	if rec.outInfo != nil {
		r.outInfoJSON = jsonString(rec.outInfo)
	}
	if rec.hasPlan {
		r.codec, r.crf, r.crop = rec.plan.VideoCodec, rec.plan.Quality, rec.plan.Crop.filter()
		if r.codec == "" {
			r.crf = 0 // a track tidy-up copies the video
		}
		if rec.srcInfo != nil {
			var kept, dropped []TrackDecision
			for _, d := range trackDecisions(rec.srcInfo, rec.plan) {
				if d.Keep {
					kept = append(kept, d)
				} else {
					dropped = append(dropped, d)
				}
			}
			r.keptJSON, r.droppedJSON = jsonString(kept), jsonString(dropped)
			if outcome == OutcomeDone {
				if w := planWarnings(rec.srcInfo, rec.plan); len(w) > 0 {
					r.warnJSON = jsonString(w)
				}
			}
		}
	}
	if len(rec.ssimWindows) > 0 {
		r.ssimMean, r.ssimMin = rec.ssimMean, rec.ssimWindows[0]
		for _, w := range rec.ssimWindows {
			r.ssimMin = min(r.ssimMin, w)
		}
		r.ssimWindowsJSON = jsonString(rec.ssimWindows)
	}
	if !rec.encodeStart.IsZero() && outcome != OutcomeInProgress {
		end := rec.encodeEnd
		if end.IsZero() {
			end = time.Now()
		}
		r.encodeSecs = end.Sub(rec.encodeStart).Seconds()
	}
	return r
}
