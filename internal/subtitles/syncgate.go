package subtitles

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/tristenlammi/arrmada/internal/langs"
)

// The sync gate: every OpenSubtitles download without a hash match is checked against the
// file's own speech before it is saved. A hash match was timed against this exact file;
// anything else was timed against some release, and a different cut or frame rate is
// the commonest way a "covered" subtitle turns out unwatchable.
//
// Thresholds (confidence is sync.go's 0-100):
//
//	≥ syncKeep    the measured fit is trusted: kept, shifted/stretched into sync first
//	              when it is off by more than syncTolMs or by a frame rate
//	< syncReject  it doesn't fit this audio: refused and blocklisted for the file, and the
//	              next candidate (at most maxDownloadTries) or the AI rung runs
//	in between    kept as it came, Health "uncertain"
//
// A candidate scoring between scoreFloor and the minimum match score is a weak name
// match, so it needs a confident fit (≥ syncKeep) to be kept at all.
//
// Calibrated on synthetic speech-like films through a real AAC round trip
// (TestSyncCalibrationIT): matching subtitles — in sync, off by seconds, or timed for
// 25 or 24 fps — scored 86-100 with dialogue over a normal mix and about 60 under a score
// louder than the dialogue throughout (offsets still measured within 25 ms); unrelated
// subtitles scored 10-29; a subtitle for another cut (half of it 6 s off) 48-60.
const (
	syncKeep   = 70
	syncReject = 50
	syncTolMs  = 300
)

// syncVerdict is what the gate decided for one candidate.
type syncVerdict struct {
	keep   bool
	shift  bool // apply the measured correction before saving
	block  bool // never offer this file for this video again
	record bool // the check measured something worth keeping as Health
	detail string
}

// judgeSync applies the thresholds. judged is false when there was too little to measure
// (a handful of cues, no speech found); band is a candidate below the minimum match score.
func judgeSync(res SyncResult, judged, band bool) syncVerdict {
	switch {
	case !judged && band:
		return syncVerdict{detail: "a weak match whose sync couldn't be checked (too little speech or too few lines)"}
	case !judged:
		return syncVerdict{keep: true} // saved unchecked; Health says "not checked"
	case res.Confidence >= syncKeep:
		return syncVerdict{keep: true, record: true, shift: res.NeedsFix(syncTolMs)}
	case res.Confidence < syncReject:
		return syncVerdict{block: true, detail: fmt.Sprintf("out of sync with the audio (sync confidence %d)", res.Confidence)}
	case band:
		return syncVerdict{detail: fmt.Sprintf("a weak match whose sync is uncertain (sync confidence %d)", res.Confidence)}
	}
	return syncVerdict{keep: true, record: true}
}

// gateSync is the download rung's syncGate. It returns the bytes to save (shifted into
// sync when the check says so), or ok=false with the reason it was refused.
func (s *Service) gateSync(ctx context.Context, ref fileRef, lang string, c ScoredCandidate, srt []byte) ([]byte, bool, string) {
	audio := ref.audio
	if audio == nil {
		// A grab outside a job (the legacy per-title search): its own audio, for this
		// one check.
		mi, _ := s.probeFile(ctx, ref.Path)
		audio = s.newJobAudio(ref.Path, mi)
		defer audio.close()
	}
	band := c.Score < s.minScore(ctx)
	track, err := audio.track(ctx, audio.syncStream(lang))
	if err != nil {
		if ctx.Err() != nil {
			return nil, false, "stopped"
		}
		s.log.Warn("subtitles: could not read the audio for a sync check", "path", ref.Path, "err", err)
		if band {
			return nil, false, "a weak match whose sync couldn't be checked: " + shortErr(err, 100)
		}
		return srt, true, "" // a good name match, saved unchecked
	}
	res, judged := track.CheckJudged(srt)
	v := judgeSync(res, judged, band)
	s.log.Info("subtitle sync check", "media", ref.MediaKey, "lang", lang, "file_id", c.FileID, "score", c.Score,
		"offset_ms", res.OffsetMs, "scale", res.Scale, "confidence", res.Confidence, "coverage", res.Coverage,
		"judged", judged, "keep", v.keep, "shift", v.shift)
	if v.block {
		if err := s.ledger.Block(context.WithoutCancel(ctx), BlockEntry{
			Provider: providerOpenSubtitles, FileID: c.FileID, MediaKey: ref.MediaKey, Lang: lang, Reason: BlockOutOfSync,
		}); err != nil {
			s.log.Warn("subtitles: could not blocklist an out-of-sync subtitle", "media", ref.MediaKey, "file_id", c.FileID, "err", err)
		}
		s.event("info", fmt.Sprintf("%s: %s candidate %s refused — %s", ref.Title, lang, c.FileID, v.detail))
	}
	if !v.keep {
		return nil, false, v.detail
	}
	out := srt
	check := SyncCheck{OffsetMs: res.OffsetMs, Confidence: res.Confidence, At: time.Now().Unix()}
	if v.shift {
		out = []byte(shiftSRT(string(srt), res.ShiftMs(), res.Scale))
		check.ShiftMs, check.Scale = res.ShiftMs(), res.Scale
		s.event("info", fmt.Sprintf("%s: %s subtitle %s to match the audio", ref.Title, lang, shiftWords(res.ShiftMs(), res.Scale)))
	}
	if v.record {
		audio.remember(lang, c.FileID, check)
	}
	return out, true, ""
}

// syncReady reads the audio a subtitle in lang would be checked against (extracting it
// once for the job), so a weak match isn't downloaded when its sync can't be checked.
// Outside a job there is nothing to share, and the gate reads the audio itself.
func (s *Service) syncReady(ctx context.Context, ref fileRef, lang string) error {
	if ref.audio == nil {
		return nil
	}
	_, err := ref.audio.track(ctx, ref.audio.syncStream(lang))
	return err
}

// shiftWords says what a correction did: "shifted −3.0 s", "re-timed 25→23.976 fps and
// shifted +1.2 s".
func shiftWords(shiftMs int, scale float64) string {
	var parts []string
	if scale != 0 && scale != 1 {
		parts = append(parts, fmt.Sprintf("stretched ×%.4f", scale))
	}
	if shiftMs != 0 || len(parts) == 0 {
		parts = append(parts, "shifted "+signedSeconds(shiftMs))
	}
	return strings.Join(parts, " and ")
}

// signedSeconds renders a shift as "−3.0 s" / "+0.45 s".
func signedSeconds(ms int) string {
	sign := "+"
	if ms < 0 {
		sign, ms = "−", -ms
	}
	return sign + secondsWords(ms)
}

// secondsWords is a length of time in seconds: one decimal from a second up ("3.0 s"),
// two below it ("0.45 s"), where a tenth is most of the amount.
func secondsWords(ms int) string {
	ms = (ms + 5) / 10 * 10 // the measurement's own grain
	if ms < 1000 && ms%100 != 0 {
		return fmt.Sprintf("%.2f s", float64(ms)/1000)
	}
	return fmt.Sprintf("%.1f s", float64(ms)/1000)
}

// --- on-demand check ---

// MediaRef names one video by its catalog identity (never by a path).
type MediaRef struct {
	Kind     string `json:"kind"` // "movie" | "episode"
	MovieID  int64  `json:"movie_id,omitempty"`
	SeriesID int64  `json:"series_id,omitempty"`
	Season   int    `json:"season,omitempty"`
	Episode  int    `json:"episode,omitempty"`
}

func (m MediaRef) job() *Job {
	return &Job{Kind: m.Kind, MovieID: m.MovieID, SeriesID: m.SeriesID, Season: m.Season, Episode: m.Episode}
}

// Errors from CheckSync, for the handler's status codes.
var (
	ErrNoVideo   = errors.New("that title has no file on disk")
	ErrNoSidecar = errors.New("no subtitle in that language sits next to the file")
	ErrNotSRT    = errors.New("only .srt subtitles can be sync-checked")
)

// SyncReport is an on-demand check's result.
type SyncReport struct {
	Path string     `json:"path"`
	Lang string     `json:"lang"`
	Sync SyncResult `json:"sync"`
	// Judged is false when there was too little to measure; Health then reads "unknown".
	Judged bool       `json:"judged"`
	Health LangHealth `json:"health"`
}

// CheckSync measures how the sidecar for one language lines up with the video's speech
// and stores the result as its Health. The video and the sidecar are found from the
// catalog, never from a path the caller supplies. The file is only measured, never
// changed: it may not be Arrmada's. An unowned sidecar gets a provenance row with source
// "unknown" to hold the result.
func (s *Service) CheckSync(ctx context.Context, m MediaRef, lang, variant string) (SyncReport, error) {
	ms, err := s.measureSync(ctx, m, lang, variant)
	if err != nil {
		return SyncReport{}, err
	}
	return s.recordCheck(ctx, ms)
}

// syncMeasure is one sidecar measured against its video's speech: what Check sync records
// and what Fix sync goes on to use.
type syncMeasure struct {
	t      SyncTargetInfo
	raw    []byte // the sidecar exactly as measured
	res    SyncResult
	judged bool
	track  *speechTrack
	// audioLangs is how many different languages the video's audio tracks are tagged with.
	audioLangs int
}

// measureSync finds the sidecar, reads it and the audio it is checked against, and
// measures. Nothing is recorded or changed.
func (s *Service) measureSync(ctx context.Context, m MediaRef, lang, variant string) (syncMeasure, error) {
	t, err := s.SyncTarget(ctx, m, lang, variant)
	if err != nil {
		return syncMeasure{}, err
	}
	raw, err := os.ReadFile(t.path)
	if err != nil {
		return syncMeasure{}, err
	}
	mi, _ := s.probeFile(ctx, t.ref.Path)
	audio := s.newJobAudio(t.ref.Path, mi)
	defer audio.close()
	track, err := audio.track(ctx, audio.syncStream(t.lang))
	if err != nil {
		return syncMeasure{}, fmt.Errorf("could not read the audio: %w", err)
	}
	res, judged := track.CheckJudged(raw)
	seen := map[string]bool{}
	if mi != nil {
		for _, a := range mi.withDetected().Audio {
			if !a.untagged() {
				seen[langs.Canonical(a.Lang)] = true
			}
		}
	}
	return syncMeasure{t: t, raw: raw, res: res, judged: judged, track: track, audioLangs: len(seen)}, nil
}

// recordCheck stores a measurement as the sidecar's Health and reports it.
func (s *Service) recordCheck(ctx context.Context, ms syncMeasure) (SyncReport, error) {
	job, ref, path, lang, variant := ms.t.job, ms.t.ref, ms.t.path, ms.t.lang, ms.t.variant
	res, judged := ms.res, ms.judged
	rep := SyncReport{Path: filepath.Base(path), Lang: lang, Sync: res, Judged: judged}
	if !judged {
		rep.Health = LangHealth{State: HealthUnknown, CheckedAt: time.Now().Unix()}
		return rep, nil // nothing measured: the stored Health stays as it was
	}
	check := SyncCheck{OffsetMs: res.OffsetMs, Confidence: res.Confidence, At: time.Now().Unix()}
	cctx := context.WithoutCancel(ctx)
	if live, owned, err := s.ledger.LiveFile(cctx, path); err != nil {
		return SyncReport{}, err
	} else if owned {
		// The file is measured as it is now, an earlier automatic shift already in it; the
		// record of that shift stays, so Health can still say it was moved.
		if live.SyncShiftMs != nil {
			check.ShiftMs = *live.SyncShiftMs
		}
		if live.SyncScale != nil {
			check.Scale = *live.SyncScale
		}
		_, err = s.ledger.RecordSync(cctx, live.Path, check)
		if err != nil {
			return SyncReport{}, err
		}
	} else {
		f := SubtitleFile{Path: path, Lang: lang, Variant: variant, Source: SourceUnknown}
		check.apply(&f)
		s.recordFile(cctx, job, ref, f)
	}
	f := SubtitleFile{}
	check.apply(&f)
	rep.Health = healthOf(f)
	s.event("info", fmt.Sprintf("%s: %s subtitle sync checked — %s", ref.Title, lang, rep.Health.Note))
	s.refreshSnapshot(cctx, job)
	return rep, nil
}

// normVariant reads a variant from a request: "full" and "" are the full subtitle. It
// returns one of the variant constants, never the request's own string, since the
// variant ends up in a sidecar's file name.
func normVariant(v string) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case VariantSDH:
		return VariantSDH
	case VariantForced:
		return VariantForced
	}
	return VariantFull
}

// SyncTargetInfo is a sidecar an on-demand check would measure, found from the catalog.
type SyncTargetInfo struct {
	Title string // for the job's label
	Key   string // the job target: one check per (file, language, variant) at a time
	Name  string // the sidecar's file name

	job           *Job
	ref           fileRef
	path          string
	lang, variant string
}

// SyncTarget finds the sidecar CheckSync would measure, without reading any audio: the
// handler calls it first so an unknown title or a missing sidecar is answered at once.
func (s *Service) SyncTarget(ctx context.Context, m MediaRef, lang, variant string) (SyncTargetInfo, error) {
	if m.Kind != "movie" && m.Kind != "episode" {
		return SyncTargetInfo{}, ErrNoVideo
	}
	job := m.job()
	ref, ok := s.resolveRef(ctx, job)
	if !ok {
		return SyncTargetInfo{}, ErrNoVideo
	}
	if ref.MediaKey == "" {
		ref.MediaKey = job.key()
	}
	// Only a language in the table names a sidecar: Canonical passes an unknown tag
	// through, and the tag becomes part of a file name ("x/../../a" would leave the
	// video's folder).
	l, known := langs.Lookup(lang)
	if !known {
		return SyncTargetInfo{}, ErrBadLanguage
	}
	lang, variant = l.Code, normVariant(variant)
	kept := s.titleLangs(ctx, job.Kind, job.MovieID, job.SeriesID) // the first claims an untagged sidecar
	path, ok := findSidecar(ref.Path, lang, variant, len(kept) > 0 && kept[0] == lang)
	if !ok && variant == VariantFull {
		// The language's full subtitle may be an SDH one (".en.sdh.srt").
		variant = VariantSDH
		path, ok = findSidecar(ref.Path, lang, variant, false)
	}
	if !ok {
		return SyncTargetInfo{}, ErrNoSidecar
	}
	if !strings.EqualFold(filepath.Ext(path), ".srt") {
		return SyncTargetInfo{}, ErrNotSRT
	}
	return SyncTargetInfo{Title: ref.Title, Key: ref.MediaKey + ":" + lang + ":" + dbVariant(variant), Name: filepath.Base(path), job: job, ref: ref, path: path, lang: lang, variant: variant}, nil
}

// findSidecar is the paired sidecar for a language and variant: the canonical name first
// ("<base>.en.srt"), else any sidecar named for the video in that language and variant
// ("<base>.eng.srt", or an untagged "<base>.srt" when untagged — the language is the
// first kept one, which an untagged sidecar counts as).
func findSidecar(video, lang, variant string, untagged bool) (string, bool) {
	if p := sidecarPathV(video, lang, variant); fileExists(p) {
		return p, true
	}
	sc := scanSidecars(video, []string{lang}, "episode")
	for i, lv := range sc.Variants {
		if lv.Variant != variant || i >= len(sc.Names) {
			continue
		}
		if (lv.Lang == "" && untagged && variant == VariantFull) || (lv.Lang != "" && langMatches(lv.Lang, lang)) {
			return filepath.Join(filepath.Dir(video), sc.Names[i]), true
		}
	}
	return "", false
}

func fileExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir()
}
