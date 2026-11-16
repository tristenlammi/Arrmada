package subtitles

import (
	"context"
	"errors"
	"fmt"
	"os"
)

// Fix sync: Check sync, then apply what it measured. The download gate has always done
// this for an OpenSubtitles file more than syncTolMs off; a subtitle that was already
// there, or came from somewhere else, was only ever measured. A person asking for a fix
// wants the small ones too (a show 200-800 ms off reads as wrong all the way through),
// so anything past syncFixTolMs moves. The measurement has to be trusted (syncKeep):
// below that the file is left alone and the timing buttons are theirs to use.

// syncFixTolMs is how far off a subtitle must run before Fix sync moves it, and before its
// Health says it runs late. Through real encoded audio the measurement lands within about
// 55 ms of the truth (TestSyncCalibrationIT and TestSyncToneBurstsIT, Nov 2026), so a
// smaller reading may be the measurement, not the subtitle.
const syncFixTolMs = 120

// What a fix came to.
const (
	FixFixed    = "fixed"
	FixInSync   = "in_sync"  // measured, and within syncFixTolMs
	FixUnsure   = "unsure"   // measured, but not trusted enough to move the file
	FixUnjudged = "unjudged" // too little speech or too few lines to measure
)

// SyncFix is one sidecar's fix.
type SyncFix struct {
	SyncReport
	Outcome string `json:"outcome"`
	// ShiftMs and Scale are what was applied when Outcome is FixFixed.
	ShiftMs int     `json:"shift_ms,omitempty"`
	Scale   float64 `json:"scale,omitempty"`
	// Words says it for a toast or a log line: "moved 0.45 s earlier", "already in sync".
	Words string `json:"words"`
}

// FixSync measures one language's sidecar against the video's speech and, when the
// measurement is trusted and the subtitle is more than syncFixTolMs off (or timed for
// another frame rate), rewrites its timing to match. The sidecar is found from the
// catalog, as CheckSync finds it. ErrFileBusy while a subtitle job is on the video.
func (s *Service) FixSync(ctx context.Context, m MediaRef, lang, variant string) (SyncFix, error) {
	t, err := s.SyncTarget(ctx, m, lang, variant)
	if err != nil {
		return SyncFix{}, err
	}
	if s.fileBusy(t.ref.MediaKey) {
		return SyncFix{}, ErrFileBusy
	}
	rep, err := s.CheckSync(ctx, m, lang, variant)
	if err != nil {
		return SyncFix{}, err
	}
	fix := SyncFix{SyncReport: rep}
	res := rep.Sync
	switch {
	case !rep.Judged:
		fix.Outcome, fix.Words = FixUnjudged, "too little speech or too few lines to measure"
		return fix, nil
	case res.Confidence < syncKeep:
		fix.Outcome = FixUnsure
		fix.Words = fmt.Sprintf("left as it is: the measurement isn't sure enough (%d of 100)", res.Confidence)
		return fix, nil
	case !res.NeedsFix(syncFixTolMs):
		fix.Outcome, fix.Words = FixInSync, "already in sync"
		return fix, nil
	}
	if s.fileBusy(t.ref.MediaKey) { // a job may have started while the audio was read
		return SyncFix{}, ErrFileBusy
	}
	if fi, err := os.Stat(t.path); err == nil && fi.Size() > maxPreviewBytes {
		return SyncFix{}, ErrNotShiftable // not a real subtitle; don't rewrite it
	}
	shift, scale := res.ShiftMs(), res.scale()
	if err := writeShifted(t.path, shift, scale); err != nil {
		return SyncFix{}, fmt.Errorf("fix %s: %w", t.Name, err)
	}
	// The row CheckSync just wrote holds the offset as measured. The file has moved by
	// exactly that, so it reads as in sync now, with the total shift it carries.
	cctx := context.WithoutCancel(ctx)
	check := SyncCheck{Confidence: res.Confidence, ShiftMs: shift, Scale: scale}
	if live, owned, err := s.ledger.LiveFile(cctx, t.path); err == nil && owned && live.SyncShiftMs != nil {
		check.ShiftMs += *live.SyncShiftMs
	}
	if _, err := s.ledger.RecordSync(cctx, t.path, check); err != nil {
		s.log.Warn("subtitles: couldn't record a sync fix", "path", t.path, "err", err)
	}
	var f SubtitleFile
	check.apply(&f)
	f.SyncCheckedAt = rep.Health.CheckedAt
	fix.Health = healthOf(f)
	fix.Outcome, fix.ShiftMs, fix.Scale = FixFixed, shift, scale
	fix.Words = shiftWords(shift, scale)
	s.sidecarChanged(m.Kind, t.ref.Path, t.path, SidecarShifted)
	s.event("info", fmt.Sprintf("%s: %s subtitle %s to match the audio (sync fixed)", t.ref.Title, t.lang, fix.Words))
	s.refreshSnapshot(cctx, t.job)
	return fix, nil
}

// SeriesSyncFix is a show's (or one season's) fix: every episode's subtitles in the
// languages its profile keeps.
type SeriesSyncFix struct {
	Subtitles int `json:"subtitles"` // sidecars looked at
	Fixed     int `json:"fixed"`
	InSync    int `json:"in_sync"`
	Unsure    int `json:"unsure"` // measured but not trusted, or too little to measure
	Failed    int `json:"failed"`
	// Notes lists what wasn't simply fine, for the job's result: "S01E03 en: moved 0.45 s
	// earlier", "S01E07 en: left as it is: …". Capped.
	Notes []string `json:"notes,omitempty"`
}

const maxSyncFixNotes = 200

// FixSyncSeries runs FixSync over a show's episodes that have a file (season >= 0: only
// that season), one after another, in each language the show's profile keeps. progress is
// told after each episode. An episode with no .srt in a language is passed over. It stops
// when ctx ends, returning what it did so far.
func (s *Service) FixSyncSeries(ctx context.Context, seriesID int64, season int, progress func(done, total int, last string)) (SeriesSyncFix, error) {
	if s.series == nil {
		return SeriesSyncFix{}, ErrNoVideo
	}
	sm, err := s.series.Get(ctx, seriesID)
	if err != nil {
		return SeriesSyncFix{}, err
	}
	var eps []MediaRef
	for _, sn := range sm.Seasons {
		if season >= 0 && sn.SeasonNumber != season {
			continue
		}
		for _, e := range sn.Episodes {
			if e.HasFile && e.FilePath != "" {
				eps = append(eps, MediaRef{Kind: "episode", SeriesID: seriesID, Season: e.SeasonNumber, Episode: e.EpisodeNumber})
			}
		}
	}
	return s.fixSyncAll(ctx, eps, s.titleLangs(ctx, "episode", 0, seriesID), progress)
}

// fixSyncAll is FixSyncSeries's loop over the videos it found.
func (s *Service) fixSyncAll(ctx context.Context, videos []MediaRef, langs []string, progress func(done, total int, last string)) (SeriesSyncFix, error) {
	var out SeriesSyncFix
	name := func(m MediaRef) string {
		if m.Kind == "episode" {
			return fmt.Sprintf("S%02dE%02d", m.Season, m.Episode)
		}
		return "movie"
	}
	note := func(m MediaRef, lang, words string) {
		if len(out.Notes) < maxSyncFixNotes {
			out.Notes = append(out.Notes, fmt.Sprintf("%s %s: %s", name(m), lang, words))
		}
	}
	for i, m := range videos {
		if ctx.Err() != nil {
			return out, ctx.Err()
		}
		last := ""
		for _, lang := range langs {
			fix, err := s.FixSync(ctx, m, lang, "")
			switch {
			case errors.Is(err, ErrNoSidecar), errors.Is(err, ErrNotSRT), errors.Is(err, ErrNoVideo):
				continue // nothing of that language to fix here
			case err != nil:
				if ctx.Err() != nil {
					return out, ctx.Err()
				}
				out.Subtitles++
				out.Failed++
				note(m, lang, "couldn't be fixed: "+shortErr(err, 120))
				continue
			}
			out.Subtitles++
			switch fix.Outcome {
			case FixFixed:
				out.Fixed++
				note(m, lang, fix.Words)
			case FixInSync:
				out.InSync++
			default:
				out.Unsure++
				note(m, lang, fix.Words)
			}
			last = name(m) + ": " + fix.Words
		}
		if progress != nil {
			progress(i+1, len(videos), last)
		}
	}
	return out, nil
}

// Words sums a show's fix up in a sentence.
func (r SeriesSyncFix) Words() string {
	if r.Subtitles == 0 {
		return "No .srt subtitles to check."
	}
	s := fmt.Sprintf("%d fixed, %d already in sync", r.Fixed, r.InSync)
	if r.Unsure > 0 {
		s += fmt.Sprintf(", %d left alone (not sure enough)", r.Unsure)
	}
	if r.Failed > 0 {
		s += fmt.Sprintf(", %d failed", r.Failed)
	}
	return s + "."
}
