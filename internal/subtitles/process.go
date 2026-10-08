package subtitles

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// fileRef is everything a job needs to know about its file, resolved once up front: where
// it is, and the metadata a provider search keys on.
type fileRef struct {
	Path          string
	IMDB          string
	Title         string
	Year          int
	Season        int // 0 for movies
	Episode       int
	MediaKey      string // Job.key() format: "m:<id>" or "e:<series>:<season>:<episode>"
	SourceRelease string // the release name the file was imported from, when recorded
}

// langOutcome is what one rung of the ladder did for one language. It drives the job's
// note today; SUB-11 persists it so failures are visible per file.
type langOutcome struct {
	Lang   string
	Rung   string // "extract" | "download" | "ai"
	Result string // "done" | "empty" | "error" | "no_match" | "quota" | "model_missing" | "impossible" | "ai_failed"
	Detail string // human wording for the note, e.g. "no OpenSubtitles match"
}

// extractPick is one embedded text track to pull out to a sidecar.
type extractPick struct {
	Index int    // 0:s:N
	Lang  string // the wanted language it stands in for
	Path  string // where the SRT goes
}

// aiRunner is the local AI (whisper) as process() sees it. *whisperGen is the real one;
// tests swap in a fake that writes a sidecar without running anything.
type aiRunner interface {
	available() bool
	// canRun reports whether a model for that task is installed: translating needs
	// large-v3, transcribing takes turbo or large-v3.
	canRun(translate bool) bool
	generate(ctx context.Context, ffmpeg, video, srt, lang string, translate bool, stream int, progress func(int)) error
	Backend() string
	Device() string
}

// The seams below default to the real code paths; only tests set the fields.

func (s *Service) resolveRef(ctx context.Context, job *Job) (fileRef, bool) {
	if s.resolve != nil {
		return s.resolve(ctx, job)
	}
	return s.resolveFile(ctx, job)
}

func (s *Service) probeFile(ctx context.Context, path string) (*mediaInfo, error) {
	if s.probe != nil {
		return s.probe(ctx, path)
	}
	return s.probeCached(ctx, path)
}

func (s *Service) runExtract(ctx context.Context, path string, picks []extractPick) error {
	if s.extract != nil {
		return s.extract(ctx, path, picks)
	}
	return s.ffmpegExtract(ctx, path, picks)
}

func (s *Service) aiGen() aiRunner {
	if s.ai != nil {
		return s.ai
	}
	return s.whisper
}

// process ensures the kept-language subtitles exist for one file. Each missing language
// walks a ladder, best source first, and falls to the next rung whenever a rung can't
// produce it: an embedded text track (extract) → an OpenSubtitles download → local AI.
// A file with no download match, a provider error or a spent quota still gets its AI
// subtitle in the same job, rather than ending "nothing produced" and being searched
// again by every sweep.
func (s *Service) process(ctx context.Context, job *Job) {
	ref, ok := s.resolveRef(ctx, job)
	if !ok {
		s.finish(job, StateFailed, "file is gone")
		return
	}
	s.update(job, func(j *Job) { j.State = StateRunning; j.StartedAt = time.Now().Unix() })

	mi, _ := s.probeFile(ctx, ref.Path) // best-effort; nil → no embedded tracks known
	langs := s.languages(ctx)
	present := map[string]bool{}
	if job.Redo {
		// Make every kept language again; the sidecars there now get written over.
		s.event("info", fmt.Sprintf("Redoing %s: replacing its existing subtitles", ref.Title))
	} else {
		for _, l := range presentLanguages(ref.Path, langs, job.Kind != "episode") {
			present[strings.ToLower(l)] = true
		}
	}
	var remaining []string
	for _, l := range langs {
		if !present[strings.ToLower(l)] {
			remaining = append(remaining, l)
		}
	}
	if len(remaining) == 0 {
		s.finish(job, StateSkipped, "all kept languages already have subtitles")
		return
	}

	var outcomes []langOutcome
	remaining, outs := s.rungExtract(ctx, job, ref, mi, remaining)
	outcomes = append(outcomes, outs...)
	remaining, outs = s.rungDownload(ctx, job, ref, remaining)
	outcomes = append(outcomes, outs...)
	outcomes = append(outcomes, s.rungAI(ctx, job, ref, mi, remaining)...)

	produced := 0
	for _, o := range outcomes {
		if o.Result == "done" {
			produced++
		}
	}
	summary := outcomeNote(langs, outcomes)
	if ctx.Err() != nil {
		note := "stopped"
		if summary != "" {
			note = "stopped after " + summary
		}
		s.event("info", fmt.Sprintf("■ %s — %s", ref.Title, note))
		s.finish(job, StateCancelled, note)
		if produced > 0 {
			s.refreshSnapshot(context.WithoutCancel(ctx), job)
		}
		return
	}
	if summary == "" {
		summary = "no source could act" // every rung declined without a reason; shouldn't happen
	}
	s.event("info", fmt.Sprintf("✓ %s — %s", ref.Title, summary))
	if produced > 0 {
		s.finish(job, StateDone, summary)
		s.refreshSnapshot(ctx, job)
	} else {
		s.finish(job, StateSkipped, summary)
	}
}

// outcomeNote renders the ladder's outcomes as one line per language, in kept-language
// order, each rung's result chained with arrows: "en: no OpenSubtitles match →
// AI-generated · es: quota spent → whisper can't translate into es".
func outcomeNote(langs []string, outcomes []langOutcome) string {
	var parts []string
	for _, l := range langs {
		var steps []string
		for _, o := range outcomes {
			if strings.EqualFold(o.Lang, l) {
				steps = append(steps, o.Detail)
			}
		}
		if len(steps) > 0 {
			parts = append(parts, strings.ToLower(l)+": "+strings.Join(steps, " → "))
		}
	}
	return strings.Join(parts, " · ")
}

// without returns langs minus the ones in done (case-insensitive).
func without(langs []string, done map[string]bool) []string {
	var out []string
	for _, l := range langs {
		if !done[strings.ToLower(l)] {
			out = append(out, l)
		}
	}
	return out
}

// rungExtract pulls an embedded text track out for each language that has one. A track
// that extracts to nothing (an empty or broken stream) counts as not produced, so the
// language falls through to the next rung. Returns the languages still missing.
func (s *Service) rungExtract(ctx context.Context, job *Job, ref fileRef, mi *mediaInfo, langs []string) ([]string, []langOutcome) {
	if ctx.Err() != nil || mi == nil || len(langs) == 0 {
		return langs, nil
	}
	var picks []extractPick
	for _, l := range langs {
		for _, t := range mi.Subs {
			if t.Text && langMatches(t.Lang, l) {
				picks = append(picks, extractPick{Index: t.Index, Lang: l, Path: sidecarPath(ref.Path, strings.ToLower(l))})
				break
			}
		}
	}
	if len(picks) == 0 {
		return langs, nil
	}
	s.update(job, func(j *Job) { j.Stage = "extracting embedded subtitles" })
	err := s.runExtract(ctx, ref.Path, picks)
	if ctx.Err() != nil {
		return langs, nil // stopped: the caller reports it; nothing falls through
	}
	if err != nil {
		s.event("warn", fmt.Sprintf("%s: extract failed: %v", ref.Title, err))
	}
	var outs []langOutcome
	done := map[string]bool{}
	for _, p := range picks {
		fi, serr := os.Stat(p.Path)
		switch {
		case serr != nil:
			outs = append(outs, langOutcome{p.Lang, "extract", "error", "extract failed"})
		case fi.Size() == 0:
			_ = os.Remove(p.Path)
			outs = append(outs, langOutcome{p.Lang, "extract", "empty", "embedded track was empty"})
		default:
			done[strings.ToLower(p.Lang)] = true
			outs = append(outs, langOutcome{p.Lang, "extract", "done", "extracted"})
		}
	}
	return without(langs, done), outs
}

// pauser is implemented by providers that can sit out a spent quota; while paused, the
// download rung makes no requests at all — not even a search.
type pauser interface{ Paused() bool }

// providerPaused reports whether the provider is waiting out a spent quota.
func (s *Service) providerPaused() bool {
	p, ok := s.provider.(pauser)
	return ok && p.Paused()
}

// rungDownload searches OpenSubtitles for each language. A language with no match, a
// provider error or a spent quota stays in the returned list for the AI rung. Skipped
// without a single request when downloads aren't configured or the quota is paused.
func (s *Service) rungDownload(ctx context.Context, job *Job, ref fileRef, langs []string) ([]string, []langOutcome) {
	if ctx.Err() != nil || len(langs) == 0 || s.provider == nil || !s.provider.CanDownload() {
		return langs, nil
	}
	var outs []langOutcome
	if s.providerPaused() {
		for _, l := range langs {
			outs = append(outs, langOutcome{l, "download", "quota", "quota spent"})
		}
		return langs, outs
	}
	// One hash per file, shared by every language's search.
	s.update(job, func(j *Job) { j.Stage = "searching OpenSubtitles" })
	hash, herr := osHash(ref.Path)
	if herr != nil {
		s.log.Debug("subtitles: could not hash file for provider search", "path", ref.Path, "err", herr)
	}
	done := map[string]bool{}
	quotaHit := false
	for _, l := range langs {
		if ctx.Err() != nil {
			return without(langs, done), outs
		}
		if quotaHit {
			// Every further language would fail the same way; leave them for AI.
			outs = append(outs, langOutcome{l, "download", "quota", "quota spent"})
			continue
		}
		okDL, err := s.grabOne(ctx, ref.IMDB, ref.Title, ref.Year, ref.Season, ref.Episode, ref.Path, hash, l)
		switch {
		case ctx.Err() != nil:
			return without(langs, done), outs
		case errors.Is(err, ErrQuotaExhausted):
			// Say it once per job. The sweep re-queues anything still missing after the reset.
			s.event("warn", fmt.Sprintf("%s: OpenSubtitles daily download quota is used up — will retry after it resets", ref.Title))
			quotaHit = true
			outs = append(outs, langOutcome{l, "download", "quota", "quota spent"})
		case err != nil:
			s.event("warn", fmt.Sprintf("%s: download %s failed: %v", ref.Title, l, err))
			outs = append(outs, langOutcome{l, "download", "error", "OpenSubtitles error"})
		case okDL:
			done[strings.ToLower(l)] = true
			outs = append(outs, langOutcome{l, "download", "done", "downloaded"})
		default:
			outs = append(outs, langOutcome{l, "download", "no_match", "no OpenSubtitles match"})
		}
	}
	return without(langs, done), outs
}

// rungAI generates each language still missing from the file's audio with local whisper —
// the last rung, so every outcome it records says why when it can't.
func (s *Service) rungAI(ctx context.Context, job *Job, ref fileRef, mi *mediaInfo, langs []string) []langOutcome {
	if ctx.Err() != nil || len(langs) == 0 {
		return nil
	}
	ai := s.aiGen()
	var audioLangs []string
	if mi != nil {
		audioLangs = mi.AudioLangs
	}
	var outs []langOutcome
	for _, l := range langs {
		if ctx.Err() != nil {
			break
		}
		if !ai.available() {
			outs = append(outs, langOutcome{l, "ai", "model_missing", "no whisper model installed"})
			continue
		}
		// Only what whisper can actually do: transcribe when the audio is that language, or
		// translate to English. It can't translate into other languages.
		plan := aiPlan(audioLangs, l)
		if plan == "" {
			outs = append(outs, langOutcome{l, "ai", "impossible", "whisper can't translate into " + strings.ToLower(l)})
			continue
		}
		translate := plan == "translate"
		if !ai.canRun(translate) {
			outs = append(outs, langOutcome{l, "ai", "model_missing", "needs large-v3 to translate"})
			continue
		}
		verb := "transcribing"
		if translate {
			verb = "translating → en"
		}
		// Which audio track: a MULTI release has the dub too, and the wrong one gives a
		// subtitle in the wrong language under the right name.
		stream, track := audioStreamFor(mi, l, translate)
		from := ""
		if stream >= 0 {
			from = fmt.Sprintf(" from audio track %d/%d", stream+1, len(mi.Audio))
			if track.Lang != "" {
				from += " (" + track.Lang + ")"
			}
		}
		s.event("info", fmt.Sprintf("AI %s %s (%s)%s…", verb, ref.Title, l, from))
		started := time.Now()
		s.update(job, func(j *Job) { j.Stage = "AI " + verb + " (" + l + ")"; j.Progress = 0 })
		onProgress := func(pct int) { s.update(job, func(j *Job) { j.Progress = pct }) }
		if err := ai.generate(ctx, s.ffmpeg, ref.Path, sidecarPath(ref.Path, strings.ToLower(l)), l, translate, stream, onProgress); err != nil {
			if ctx.Err() != nil {
				break
			}
			s.event("warn", fmt.Sprintf("%s: AI %s failed: %v", ref.Title, l, err))
			outs = append(outs, langOutcome{l, "ai", "ai_failed", "AI failed: " + shortErr(err, 120)})
			continue
		}
		outs = append(outs, langOutcome{l, "ai", "done", "AI-generated"})
		took := time.Since(started)
		msg := fmt.Sprintf("%s: AI subtitle written (%s) in %s on %s", ref.Title, l, took.Round(time.Second), ai.Backend())
		if dev := ai.Device(); dev != "" {
			msg += " [" + dev + "]"
		}
		if mi != nil && mi.DurationSec > 0 && took > 0 {
			// The number that says whether the GPU is pulling its weight.
			dur := float64(mi.DurationSec)
			msg += fmt.Sprintf(" — %s of audio, %.1f× realtime", fmtAudio(dur), dur/took.Seconds())
		}
		s.event("info", msg)
	}
	return outs
}

// shortErr is the tail of an error message, for a job note that has to fit on one line.
func shortErr(err error, n int) string {
	msg := strings.ReplaceAll(err.Error(), "\n", " ")
	if r := []rune(msg); len(r) > n {
		return "…" + string(r[len(r)-n:])
	}
	return msg
}

// resolveFile resolves a job's file path + metadata (title/year/imdb, needed for provider searches).
func (s *Service) resolveFile(ctx context.Context, job *Job) (fileRef, bool) {
	if job.Kind == "episode" {
		full, err := s.series.Get(ctx, job.SeriesID)
		if err != nil {
			return fileRef{}, false
		}
		p, _ := s.series.EpisodeFilePath(ctx, job.SeriesID, job.Season, job.Episode)
		if p == "" {
			return fileRef{}, false
		}
		ref := fileRef{Path: p, IMDB: full.IMDBID, Title: full.Title, Year: full.Year,
			Season: job.Season, Episode: job.Episode, MediaKey: job.key()}
		for _, sn := range full.Seasons {
			for _, e := range sn.Episodes {
				if e.SeasonNumber == job.Season && e.EpisodeNumber == job.Episode {
					ref.SourceRelease = e.SourceRelease
				}
			}
		}
		return ref, true
	}
	m, err := s.movies.Get(ctx, job.MovieID)
	if err != nil || !m.HasFile || m.MovieFilePath == "" {
		return fileRef{}, false
	}
	return fileRef{Path: m.MovieFilePath, IMDB: m.IMDBID, Title: m.Title, Year: m.Year,
		MediaKey: job.key(), SourceRelease: m.SourceRelease}, true
}

// ffmpegExtract pulls every picked embedded track out to its SRT sidecar in a single
// ffmpeg pass (one read of the file, all tracks). Empty outputs are left for the caller to
// judge and prune.
func (s *Service) ffmpegExtract(ctx context.Context, path string, picks []extractPick) error {
	if len(picks) == 0 {
		return nil
	}
	args := []string{"-y", "-hide_banner", "-i", path}
	for _, p := range picks {
		args = append(args, "-map", fmt.Sprintf("0:s:%d", p.Index), "-c:s", "srt", p.Path)
	}
	return exec.CommandContext(ctx, s.ffmpeg, args...).Run()
}
