package subtitles

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Fix sync: Check sync, then apply what it measured. The download gate has always done
// this for an OpenSubtitles file more than syncTolMs off; a subtitle that was already
// there, or came from somewhere else, was only ever measured.
//
// A person asking for a fix wants the small ones too (a show 200-800 ms off reads as wrong
// all the way through), but the gate's measurement leans by 100-200 ms with how a
// subtitle's lines end (syncrefine.go). So the fix refines it by where lines start, and
// moves a small offset only on that evidence:
//
//   - refined, one audio language: anything past syncFixTolMs moves;
//   - not refinable (music under everything, few fresh lines), or audio in more than one
//     language (the subtitle may be timed to the other track): only what the gate itself
//     would move, past syncTolMs or a frame-rate stretch.
//
// The measurement has to be trusted (syncKeep) either way. What is written is the file as
// it was measured, moved: if the file changed while the audio was being read (a new
// download, a nudge), nothing is written. One fix per file at a time.

// syncFixTolMs is how far off a subtitle must run, by its line starts, before Fix sync
// moves it. Speech starts are found to the 10 ms frame, but a line is cued a little ahead
// of its first word and the energy gate opens a little after it: under about a tenth of a
// second the reading is as much convention as error.
const syncFixTolMs = 120

// What a fix came to.
const (
	FixFixed    = "fixed"
	FixInSync   = "in_sync"  // measured, and within what this audio lets it tell
	FixUnsure   = "unsure"   // measured, but not trusted enough to move the file
	FixUnjudged = "unjudged" // too little speech or too few lines to measure
	FixSkipped  = "skipped"  // a show-wide fix passing over a file timed from this video
)

// ErrFixChanged: the subtitle was replaced or edited while its audio was being read.
var ErrFixChanged = errors.New("the subtitle changed while it was being measured — nothing was moved; try again")

// ErrFixOtherFile: the language's subtitle Arrmada uses is a different file than the one named.
var ErrFixOtherFile = errors.New("that isn't the subtitle Arrmada uses for this language")

// FixOpts narrows a fix.
type FixOpts struct {
	// Name, when set, is the sidecar the person clicked: the fix is refused unless that is
	// the file the language resolves to (two English files beside one video).
	Name string
	// Bulk is a show-wide fix: files timed from this very video (its own embedded track,
	// AI made from its audio, a hash-matched download) are passed over.
	Bulk bool
}

// SyncFix is one sidecar's fix.
type SyncFix struct {
	SyncReport
	Outcome string `json:"outcome"`
	// ShiftMs and Scale are what was applied when Outcome is FixFixed.
	ShiftMs int     `json:"shift_ms,omitempty"`
	Scale   float64 `json:"scale,omitempty"`
	// Refined says the offset was settled by where lines start, not only by overlap.
	Refined bool `json:"refined"`
	// Words says it for a toast or a log line: "shifted −0.45 s", "already in sync".
	Words string `json:"words"`
}

// timedFromThisVideo reports a provenance that is in sync by construction.
func timedFromThisVideo(f SubtitleFile) bool {
	switch f.Source {
	case SourceEmbedded, SourceAITranscribe, SourceAITranslate:
		return true
	case SourceOpenSubtitles:
		return f.HashMatch
	}
	return false
}

// FixSync measures one language's sidecar against the video's speech and, when the
// measurement is trusted and the subtitle is off by more than this audio lets it tell,
// rewrites its timing to match. The sidecar is found from the catalog, as CheckSync finds
// it. ErrFileBusy while a subtitle job, or another fix, is on the file.
func (s *Service) FixSync(ctx context.Context, m MediaRef, lang, variant string, opts FixOpts) (SyncFix, error) {
	t, err := s.SyncTarget(ctx, m, lang, variant)
	if err != nil {
		return SyncFix{}, err
	}
	if opts.Name != "" && opts.Name != t.Name {
		return SyncFix{}, fmt.Errorf("%w: it uses %s", ErrFixOtherFile, t.Name)
	}
	if s.fileBusy(t.ref.MediaKey) {
		return SyncFix{}, ErrFileBusy
	}
	if _, running := s.fixing.LoadOrStore(t.path, true); running {
		return SyncFix{}, ErrFileBusy
	}
	defer s.fixing.Delete(t.path)
	if opts.Bulk {
		if live, owned, err := s.ledger.LiveFile(ctx, t.path); err == nil && owned && timedFromThisVideo(live) {
			return SyncFix{SyncReport: SyncReport{Path: t.Name, Lang: t.lang}, Outcome: FixSkipped, Words: "timed from this video already"}, nil
		}
	}
	ms, err := s.measureSync(ctx, m, lang, variant)
	if err != nil {
		return SyncFix{}, err
	}
	res := ms.res
	fix := SyncFix{}
	tol := syncTolMs
	if ms.judged && res.Confidence >= syncKeep && ms.audioLangs <= 1 {
		if r, ok := ms.track.refineByStarts(parseSRT(string(ms.raw)), res); ok {
			res, fix.Refined, tol = r, true, syncFixTolMs
		}
	}
	ms.res = res
	rep, err := s.recordCheck(ctx, ms)
	if err != nil {
		return SyncFix{}, err
	}
	fix.SyncReport = rep
	switch {
	case !rep.Judged:
		fix.Outcome, fix.Words = FixUnjudged, "too little speech or too few lines to measure"
		return fix, nil
	case res.Confidence < syncKeep:
		fix.Outcome = FixUnsure
		fix.Words = fmt.Sprintf("left as it is: the measurement isn't sure enough (%d of 100)", res.Confidence)
		return fix, nil
	case !res.NeedsFix(tol):
		fix.Outcome, fix.Words = FixInSync, "already in sync"
		if !fix.Refined {
			fix.Words = "in sync as far as this audio can tell (within 0.3 s)"
		}
		return fix, nil
	}
	if s.fileBusy(t.ref.MediaKey) { // a job may have started while the audio was read
		return SyncFix{}, ErrFileBusy
	}
	if len(ms.raw) > maxPreviewBytes {
		return SyncFix{}, ErrNotShiftable // not a real subtitle; don't rewrite it
	}
	// Only the bytes that were measured are moved. A file that changed meanwhile is
	// someone else's newer work, and this measurement says nothing about it.
	if now, err := os.ReadFile(t.path); err != nil || !bytes.Equal(now, ms.raw) {
		return SyncFix{}, ErrFixChanged
	}
	shift, scale := res.ShiftMs(), res.scale()
	if err := writeBytesAtomic(t.path, []byte(shiftSRT(string(ms.raw), shift, scale))); err != nil {
		return SyncFix{}, fmt.Errorf("fix %s: %w", t.Name, err)
	}
	// recordCheck just wrote the offset as measured. The file has moved by exactly that, so
	// it reads as in sync now, carrying the total shift (and any stretch) applied to it.
	cctx := context.WithoutCancel(ctx)
	check := SyncCheck{Confidence: res.Confidence, ShiftMs: shift, Scale: scale}
	if live, owned, err := s.ledger.LiveFile(cctx, t.path); err == nil && owned {
		if live.SyncShiftMs != nil {
			check.ShiftMs += *live.SyncShiftMs
		}
		if live.SyncScale != nil && (scale == 0 || scale == 1) {
			check.Scale = *live.SyncScale
		}
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

// writeBytesAtomic replaces a file through a temp file beside it, so a player never reads
// half of it.
func writeBytesAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".arrmada-shift-*.srt")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return err
	}
	_ = os.Chmod(name, 0o644)
	if err := os.Rename(name, path); err != nil {
		os.Remove(name)
		return err
	}
	return nil
}

// SeriesSyncFix is a show's (or one season's) fix: every episode's subtitles in the
// languages its profile keeps.
type SeriesSyncFix struct {
	Subtitles int `json:"subtitles"` // sidecars looked at
	Fixed     int `json:"fixed"`
	InSync    int `json:"in_sync"`
	Unsure    int `json:"unsure"`  // measured but not trusted, or too little to measure
	Skipped   int `json:"skipped"` // timed from the video itself: nothing to fix
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
			fix, err := s.FixSync(ctx, m, lang, "", FixOpts{Bulk: true})
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
			case FixSkipped:
				out.Skipped++
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
	s := fmt.Sprintf("%d fixed, %d already in sync", r.Fixed, r.InSync+r.Skipped)
	if r.Unsure > 0 {
		s += fmt.Sprintf(", %d left alone (not sure enough)", r.Unsure)
	}
	if r.Failed > 0 {
		s += fmt.Sprintf(", %d failed", r.Failed)
	}
	return s + "."
}
