package subtitles

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/tristenlammi/arrmada/internal/langs"
	"github.com/tristenlammi/arrmada/internal/store"
)

// Backoff: a language a run couldn't make isn't searched again by every six-hourly sweep.
// Each rung's outcome maps to a wait or to an event that clears it, and the sweep leaves
// the language alone until then. Import and manual runs ignore backoff — a button press
// always tries now — and a run that makes the subtitle deletes the row.

// Events that clear a backoff early, for Backoff.ClearOn and ClearBackoff.
const (
	EventCreds = "creds" // the OpenSubtitles credentials changed
	EventModel = "model" // a whisper model finished downloading
	EventLangs = "langs" // the kept languages changed
)

// Backoff is one row of subtitle_backoff: why a language is still missing for a file and
// when the sweep may try it again.
type Backoff struct {
	MediaKey      string          `json:"media_key"`
	Lang          string          `json:"lang"`
	Variant       string          `json:"variant,omitempty"`
	VideoPath     string          `json:"video_path"`
	PrimaryReason Result          `json:"primary_reason"` // the most actionable outcome
	Reasons       map[Rung]Result `json:"reasons"`        // each rung's outcome
	Detail        string          `json:"detail,omitempty"`
	Count         int             `json:"count"`                   // runs in a row with this primary reason
	NextRetryAt   int64           `json:"next_retry_at,omitempty"` // unix; 0 = only an event (or a button press) clears it
	ClearOn       []string        `json:"clear_on,omitempty"`      // EventCreds | EventModel | EventLangs
	UpdatedAt     int64           `json:"updated_at"`
}

// Blocked reports whether the sweep should leave this language alone for videoPath now.
// A row written for another file (the movie was upgraded) doesn't hold.
func (b Backoff) Blocked(videoPath string, now time.Time) bool {
	return b.VideoPath == videoPath && (b.NextRetryAt == 0 || b.NextRetryAt > now.Unix())
}

// Backoffs is every active row, keyed by BackoffKey — what one sweep loads once.
type Backoffs map[string]Backoff

// BackoffKey is a Backoffs key.
func BackoffKey(mediaKey, lang, variant string) string {
	return mediaKey + "|" + langs.Canonical(lang) + "|" + dbVariant(variant)
}

// Blocked reports whether a (file, language) is backing off; a nil set blocks nothing.
func (bs Backoffs) Blocked(mediaKey, lang, videoPath string, now time.Time) bool {
	return bs.BlockedV(mediaKey, lang, VariantFull, videoPath, now)
}

// BlockedV is Blocked for one variant of the language (a profile's forced want backs off
// on its own).
func (bs Backoffs) BlockedV(mediaKey, lang, variant, videoPath string, now time.Time) bool {
	b, ok := bs[BackoffKey(mediaKey, lang, variant)]
	return ok && b.Blocked(videoPath, now)
}

// reasonRank orders outcomes by how actionable they are: the primary reason is the one
// the owner can do something about first.
var reasonRank = map[Result]int{
	ResultAuthFailed:    1,
	ResultModelMissing:  2,
	ResultQuota:         3,
	ResultRateLimited:   4,
	ResultProviderError: 5,
	ResultAIFailed:      6,
	ResultOutOfSync:     7,
	ResultBelowScore:    8,
	ResultNoMatch:       9,
	ResultImpossible:    10,
	ResultNoAudio:       11,
	ResultOCRNeeded:     12,
}

// step picks the wait for the nth run in a row (1-based) from a ladder of waits.
func step(count int, waits ...time.Duration) time.Duration {
	if count < 1 {
		count = 1
	}
	if count > len(waits) {
		count = len(waits)
	}
	return waits[count-1]
}

const day = 24 * time.Hour

// retryFor maps one rung's outcome to when it may be tried again (zero = not on a timer)
// and the event that clears it early ("" = none).
func retryFor(r Result, count int, now, quotaReset time.Time) (time.Time, string) {
	switch r {
	case ResultNoMatch, ResultBelowScore, ResultOutOfSync:
		return now.Add(step(count, 7*day, 30*day, 90*day)), ""
	case ResultQuota:
		if quotaReset.After(now) {
			return quotaReset, ""
		}
		return now.Add(day), ""
	case ResultRateLimited:
		return now.Add(10 * time.Minute), ""
	case ResultProviderError:
		return now.Add(step(count, 6*time.Hour, day, 3*day)), ""
	case ResultAIFailed:
		if count >= 3 {
			return time.Time{}, "" // a third crash in a row: only a button press tries again
		}
		return now.Add(step(count, day, 7*day)), ""
	case ResultAuthFailed:
		return time.Time{}, EventCreds
	case ResultModelMissing:
		return time.Time{}, EventModel
	case ResultImpossible:
		return time.Time{}, EventLangs
	}
	return time.Time{}, "" // no_audio, ocr_needed: nothing will change until the file does
}

// planBackoff turns one language's outcomes from a run into its backoff row. prev is the
// row already there (nil when none). ok is false when the outcomes say nothing to back
// off on (the language was made, or the run was stopped).
func planBackoff(prev *Backoff, mediaKey, lang, videoPath string, outs []Outcome, now, quotaReset time.Time) (Backoff, bool) {
	reasons := map[Rung]Result{}
	primary, detail := Result(""), ""
	for _, o := range outs {
		switch o.Result {
		case ResultOK, ResultCancelled:
			return Backoff{}, false
		}
		if _, ranked := reasonRank[o.Result]; !ranked {
			continue // empty or failed extraction: a later rung decided
		}
		reasons[o.Rung] = o.Result
		if primary == "" || reasonRank[o.Result] < reasonRank[primary] {
			primary, detail = o.Result, o.Detail
		}
	}
	if primary == "" {
		return Backoff{}, false
	}
	count := 1
	if prev != nil && prev.VideoPath == videoPath && prev.PrimaryReason == primary {
		count = prev.Count + 1
	}
	var next time.Time
	events := map[string]bool{}
	for _, r := range reasons {
		at, ev := retryFor(r, count, now, quotaReset)
		if !at.IsZero() && (next.IsZero() || at.Before(next)) {
			next = at
		}
		if ev != "" {
			events[ev] = true
		}
	}
	b := Backoff{
		MediaKey: mediaKey, Lang: langs.Canonical(lang), Variant: VariantFull, VideoPath: videoPath,
		PrimaryReason: primary, Reasons: reasons, Detail: detail, Count: count, UpdatedAt: now.Unix(),
	}
	if !next.IsZero() {
		b.NextRetryAt = next.Unix()
	}
	for ev := range events {
		b.ClearOn = append(b.ClearOn, ev)
	}
	sort.Strings(b.ClearOn)
	return b, true
}

// --- ledger storage ---

const backoffCols = `media_key, lang, variant, video_path, primary_reason, reasons_json, detail, count, next_retry_at, clear_on, updated_at`

func scanBackoff(sc interface{ Scan(...any) error }) (Backoff, error) {
	var (
		b                       Backoff
		variant, reasons, clear string
		primary                 string
		detail                  sql.NullString
		next                    sql.NullInt64
	)
	if err := sc.Scan(&b.MediaKey, &b.Lang, &variant, &b.VideoPath, &primary, &reasons, &detail, &b.Count, &next, &clear, &b.UpdatedAt); err != nil {
		return b, err
	}
	b.Variant, b.PrimaryReason, b.Detail, b.NextRetryAt = fromDBVariant(variant), Result(primary), detail.String, next.Int64
	_ = json.Unmarshal([]byte(reasons), &b.Reasons)
	if clear != "" {
		b.ClearOn = strings.Split(clear, ",")
	}
	return b, nil
}

func (l *Ledger) queryBackoffs(ctx context.Context, where string, args ...any) (Backoffs, error) {
	out := Backoffs{}
	if !l.ok() {
		return out, nil
	}
	rows, err := l.db.QueryContext(ctx, `SELECT `+backoffCols+` FROM subtitle_backoff`+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		b, err := scanBackoff(rows)
		if err != nil {
			return nil, err
		}
		out[BackoffKey(b.MediaKey, b.Lang, b.Variant)] = b
	}
	return out, rows.Err()
}

// LoadBackoffs reads every backoff row in one query — a sweep's whole view.
func (l *Ledger) LoadBackoffs(ctx context.Context) (Backoffs, error) {
	return l.queryBackoffs(ctx, "")
}

// CountBackoffs counts the rows parked for one primary reason.
func (l *Ledger) CountBackoffs(ctx context.Context, reason Result) (int, error) {
	if !l.ok() {
		return 0, nil
	}
	var n int
	err := l.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM subtitle_backoff WHERE primary_reason = ?`, string(reason)).Scan(&n)
	return n, err
}

// BackoffsFor reads one file's backoff rows.
func (l *Ledger) BackoffsFor(ctx context.Context, mediaKey string) (Backoffs, error) {
	return l.queryBackoffs(ctx, " WHERE media_key = ?", mediaKey)
}

// SaveBackoff writes (or replaces) a row.
func (l *Ledger) SaveBackoff(ctx context.Context, b Backoff) error {
	if !l.ok() {
		return nil
	}
	reasons, err := json.Marshal(b.Reasons)
	if err != nil {
		return err
	}
	var next any
	if b.NextRetryAt > 0 {
		next = b.NextRetryAt
	}
	if b.UpdatedAt == 0 {
		b.UpdatedAt = l.clock().Unix()
	}
	_, err = l.db.ExecContext(ctx, `INSERT INTO subtitle_backoff (`+backoffCols+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(media_key, lang, variant) DO UPDATE SET video_path = excluded.video_path,
		primary_reason = excluded.primary_reason, reasons_json = excluded.reasons_json, detail = excluded.detail,
		count = excluded.count, next_retry_at = excluded.next_retry_at, clear_on = excluded.clear_on,
		updated_at = excluded.updated_at`,
		b.MediaKey, langs.Canonical(b.Lang), dbVariant(b.Variant), b.VideoPath, string(b.PrimaryReason), string(reasons),
		nullStr(b.Detail), b.Count, next, strings.Join(b.ClearOn, ","), b.UpdatedAt)
	return err
}

// DeleteBackoff removes a row (the language was made).
func (l *Ledger) DeleteBackoff(ctx context.Context, mediaKey, lang, variant string) error {
	if !l.ok() {
		return nil
	}
	_, err := l.db.ExecContext(ctx, `DELETE FROM subtitle_backoff WHERE media_key = ? AND lang = ? AND variant = ?`,
		mediaKey, langs.Canonical(lang), dbVariant(variant))
	return err
}

// ClearBackoff deletes every row an event clears, so the next sweep tries them again.
// Returns how many went.
func (l *Ledger) ClearBackoff(ctx context.Context, event string) (int64, error) {
	if !l.ok() || event == "" {
		return 0, nil
	}
	res, err := l.db.ExecContext(ctx, `DELETE FROM subtitle_backoff WHERE `+store.LikeASCII(l.db, "',' || clear_on || ','", "?", false), "%,"+event+",%")
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// --- service ---

// ClearBackoff lets the next sweep retry everything parked on an event: EventCreds after
// the OpenSubtitles credentials change, EventModel after a whisper model downloads,
// EventLangs after the kept languages change.
func (s *Service) ClearBackoff(ctx context.Context, event string) {
	n, err := s.ledger.ClearBackoff(context.WithoutCancel(ctx), event)
	if err != nil {
		s.log.Warn("subtitles: could not clear backoff", "event", event, "err", err)
		return
	}
	if n > 0 {
		s.event("info", fmt.Sprintf("%d file language(s) waiting on %s will be tried again by the next sweep", n, eventWords(event)))
	}
}

func eventWords(event string) string {
	switch event {
	case EventCreds:
		return "the OpenSubtitles login"
	case EventModel:
		return "a whisper model"
	case EventLangs:
		return "a language change"
	}
	return event
}

// quotaResetAt is when the provider's spent quota resets (zero when it isn't paused).
func (s *Service) quotaResetAt() time.Time {
	if q, ok := s.provider.(quotaReporter); ok {
		_, reset := q.Quota()
		return reset
	}
	return time.Time{}
}

// updateBackoff records, after a run, when each language it couldn't make may be tried
// again — and forgets the ones it made. Bookkeeping: errors are logged, never the job's.
func (s *Service) updateBackoff(ctx context.Context, ref fileRef, wanted []string, outcomes []Outcome) {
	s.updateBackoffV(ctx, ref, wanted, VariantFull, outcomes)
}

// outcomeVariant is the want an outcome is for: forced, or the full subtitle (SDH included).
func outcomeVariant(o Outcome) string {
	if o.Variant == VariantForced {
		return VariantForced
	}
	return VariantFull
}

// updateBackoffV is updateBackoff for one variant's wants: only that variant's outcomes
// count, and its rows are its own.
func (s *Service) updateBackoffV(ctx context.Context, ref fileRef, wanted []string, variant string, outcomes []Outcome) {
	if s.ledger == nil || len(wanted) == 0 {
		return
	}
	ctx = context.WithoutCancel(ctx)
	prev, err := s.ledger.BackoffsFor(ctx, ref.MediaKey)
	if err != nil {
		s.log.Warn("subtitles: could not read backoff", "media_key", ref.MediaKey, "err", err)
		return
	}
	now, reset := time.Now(), s.quotaResetAt()
	for _, l := range wanted {
		var outs []Outcome
		for _, o := range outcomes {
			if strings.EqualFold(o.Lang, l) && outcomeVariant(o) == variant {
				outs = append(outs, o)
			}
		}
		key := BackoffKey(ref.MediaKey, l, variant)
		var p *Backoff
		if b, ok := prev[key]; ok {
			p = &b
		}
		b, park := planBackoff(p, ref.MediaKey, l, ref.Path, outs, now, reset)
		b.Variant = variant
		if park && variant == VariantForced && b.PrimaryReason == ResultImpossible && !slices.Contains(b.ClearOn, EventCreds) {
			// Only a download can make a forced subtitle the file doesn't carry: setting up
			// OpenSubtitles is worth another look, not just a language change.
			b.ClearOn = append(b.ClearOn, EventCreds)
			sort.Strings(b.ClearOn)
		}
		var err error
		switch {
		case park:
			err = s.ledger.SaveBackoff(ctx, b)
		case p != nil && madeIt(outs):
			err = s.ledger.DeleteBackoff(ctx, ref.MediaKey, l, variant)
		}
		if err != nil {
			s.log.Warn("subtitles: could not record backoff", "media_key", ref.MediaKey, "lang", l, "err", err)
		}
	}
}

// dropBackedOff splits langs into those to try now and notes for those still backing
// off for this file. A backoff that can't be read doesn't block anything.
func (s *Service) dropBackedOff(ctx context.Context, ref fileRef, langs []string) (keep, parked []string) {
	return s.dropBackedOffV(ctx, ref, langs, VariantFull)
}

// dropBackedOffV is dropBackedOff for one variant's wants.
func (s *Service) dropBackedOffV(ctx context.Context, ref fileRef, langs []string, variant string) (keep, parked []string) {
	if len(langs) == 0 {
		return nil, nil
	}
	bs, err := s.ledger.BackoffsFor(ctx, ref.MediaKey)
	if err != nil {
		s.log.Warn("subtitles: could not read backoff", "media_key", ref.MediaKey, "err", err)
		return langs, nil
	}
	now := time.Now()
	for _, l := range langs {
		if b, ok := bs[BackoffKey(ref.MediaKey, l, variant)]; ok && b.Blocked(ref.Path, now) {
			parked = append(parked, backoffNote(b))
			continue
		}
		keep = append(keep, l)
	}
	return keep, parked
}

func madeIt(outs []Outcome) bool {
	for _, o := range outs {
		if o.Result == ResultOK {
			return true
		}
	}
	return false
}

// backoffNote says when a parked language comes up again, for a sweep job that found
// nothing to do yet: "en: no OpenSubtitles match — next try 12 Nov".
func backoffNote(b Backoff) string {
	when := "after a manual retry"
	switch {
	case b.NextRetryAt > 0:
		when = "next try " + time.Unix(b.NextRetryAt, 0).Format("2 Jan 15:04")
	case len(b.ClearOn) > 0:
		var ws []string
		for _, ev := range b.ClearOn {
			ws = append(ws, eventWords(ev))
		}
		when = "waiting on " + strings.Join(ws, " or ")
	}
	reason := b.Detail
	if reason == "" {
		reason = string(b.PrimaryReason)
	}
	return wantLabel(b.Lang, b.Variant) + ": " + reason + " — " + when
}
