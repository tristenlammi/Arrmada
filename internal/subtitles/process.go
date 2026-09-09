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

// process ensures the kept-language subtitles exist for one file, using the best source available
// per language (embedded text → extract; else a provider download). OCR + AI sources are
// recognised but not yet implemented — those languages are reported as pending.
func (s *Service) process(ctx context.Context, job *Job) {
	path, imdb, title, year, season, episode, ok := s.resolveFile(ctx, job)
	if !ok {
		s.finish(job, StateFailed, "file is gone")
		return
	}
	s.update(job, func(j *Job) { j.State = StateRunning; j.StartedAt = time.Now().Unix() })

	mi, _ := s.probeCached(ctx, path) // best-effort; nil → no embedded tracks known
	var subs []SubTrack
	if mi != nil {
		subs = mi.Subs
	}
	langs := s.languages(ctx)
	present := map[string]bool{}
	for _, l := range presentLanguages(path, langs, job.Kind != "episode") {
		present[strings.ToLower(l)] = true
	}
	canDownload := s.provider != nil && s.provider.CanDownload()
	aiOK := s.whisper.available()
	var audioLangs []string
	if mi != nil {
		audioLangs = mi.AudioLangs
	}

	type aiTask struct {
		lang      string
		translate bool
	}
	var extractLangs, downloadLangs []string
	var aiTasks []aiTask
	pending := 0
	for _, l := range langs {
		if present[strings.ToLower(l)] {
			continue
		}
		switch bestSource(subs, l, canDownload) {
		case "extract":
			extractLangs = append(extractLangs, l)
		case "download":
			downloadLangs = append(downloadLangs, l)
		case "ai":
			// Only what Whisper can actually do: transcribe when the audio is that language, or
			// translate to English. It can't translate into other languages.
			if aiOK {
				switch aiPlan(audioLangs, l) {
				case "transcribe":
					aiTasks = append(aiTasks, aiTask{l, false})
				case "translate":
					aiTasks = append(aiTasks, aiTask{l, true})
				default:
					pending++
				}
			} else {
				pending++
			}
		default: // ocr — not implemented yet
			pending++
		}
	}
	if len(extractLangs) == 0 && len(downloadLangs) == 0 && len(aiTasks) == 0 {
		if pending > 0 {
			s.finish(job, StateSkipped, fmt.Sprintf("%d language(s) need OCR/AI — coming soon", pending))
		} else {
			s.finish(job, StateSkipped, "all kept languages already have subtitles")
		}
		return
	}

	// stopped reports whether the user (or shutdown) cancelled the job; every stage's
	// error is really this when it's set, so the note says "stopped", not "failed".
	stopped := func() bool { return ctx.Err() != nil }

	extracted := 0
	if len(extractLangs) > 0 {
		s.update(job, func(j *Job) { j.Stage = "extracting embedded subtitles" })
		n, err := s.extractForLangs(ctx, path, subs, extractLangs)
		if err != nil && !stopped() {
			s.event("warn", fmt.Sprintf("%s: extract failed: %v", title, err))
		}
		extracted = n
	}
	downloaded := 0
	if len(downloadLangs) > 0 {
		// One hash per file, shared by every language's search.
		s.update(job, func(j *Job) { j.Stage = "searching OpenSubtitles" })
		hash, herr := osHash(path)
		if herr != nil {
			s.log.Debug("subtitles: could not hash file for provider search", "path", path, "err", herr)
		}
		for _, l := range downloadLangs {
			if stopped() {
				break
			}
			okDL, err := s.grabOne(ctx, imdb, title, year, season, episode, path, hash, l)
			switch {
			case errors.Is(err, ErrQuotaExhausted):
				// Say it once per job and stop trying: every further language would fail
				// the same way. The sweep re-queues the file after the quota resets.
				s.event("warn", fmt.Sprintf("%s: OpenSubtitles daily download quota is used up — will retry after it resets", title))
				pending += len(downloadLangs) - downloaded
				goto afterDownloads
			case err != nil:
				s.event("warn", fmt.Sprintf("%s: download %s failed: %v", title, l, err))
			case okDL:
				downloaded++
			}
		}
	}
afterDownloads:
	generated := 0
	for _, t := range aiTasks {
		if stopped() {
			break
		}
		verb := "transcribing"
		if t.translate {
			verb = "translating → en"
		}
		// Which audio track: a MULTI release has the dub too, and the wrong one gives a
		// subtitle in the wrong language under the right name.
		stream, track := audioStreamFor(mi, t.lang, t.translate)
		from := ""
		if stream >= 0 {
			from = fmt.Sprintf(" from audio track %d/%d", stream+1, len(mi.Audio))
			if track.Lang != "" {
				from += " (" + track.Lang + ")"
			}
		}
		s.event("info", fmt.Sprintf("AI %s %s (%s)%s…", verb, title, t.lang, from))
		started := time.Now()
		s.update(job, func(j *Job) { j.Stage = "AI " + verb + " (" + t.lang + ")"; j.Progress = 0 })
		onProgress := func(pct int) { s.update(job, func(j *Job) { j.Progress = pct }) }
		if err := s.whisper.generate(ctx, s.ffmpeg, path, sidecarPath(path, strings.ToLower(t.lang)), t.lang, t.translate, stream, onProgress); err != nil {
			if stopped() {
				break
			}
			s.event("warn", fmt.Sprintf("%s: AI %s failed: %v", title, t.lang, err))
		} else {
			generated++
			took := time.Since(started)
			msg := fmt.Sprintf("%s: AI subtitle written (%s) in %s on %s", title, t.lang, took.Round(time.Second), s.whisper.Backend())
			if dev := s.whisper.Device(); dev != "" {
				msg += " [" + dev + "]"
			}
			if mi != nil && mi.DurationSec > 0 && took > 0 {
				// The number that says whether the GPU is pulling its weight.
				dur := float64(mi.DurationSec)
				msg += fmt.Sprintf(" — %s of audio, %.1f× realtime", fmtAudio(dur), dur/took.Seconds())
			}
			s.event("info", msg)
		}
	}

	var parts []string
	if extracted > 0 {
		parts = append(parts, fmt.Sprintf("%d extracted", extracted))
	}
	if downloaded > 0 {
		parts = append(parts, fmt.Sprintf("%d downloaded", downloaded))
	}
	if generated > 0 {
		parts = append(parts, fmt.Sprintf("%d AI-generated", generated))
	}
	if pending > 0 {
		parts = append(parts, fmt.Sprintf("%d pending (OCR/AI)", pending))
	}
	if stopped() {
		note := "stopped"
		if len(parts) > 0 {
			note = "stopped after " + strings.Join(parts, " · ")
		}
		s.event("info", fmt.Sprintf("■ %s — %s", title, note))
		s.finish(job, StateCancelled, note)
		return
	}
	if len(parts) == 0 {
		parts = append(parts, "nothing produced")
	}
	note := strings.Join(parts, " · ")
	s.event("info", fmt.Sprintf("✓ %s — %s", title, note))
	if extracted+downloaded+generated > 0 {
		s.finish(job, StateDone, note)
		s.refreshSnapshot(ctx, job)
	} else {
		s.finish(job, StateSkipped, note)
	}
}

// resolveFile resolves a job's file path + metadata (title/year/imdb, needed for provider searches).
func (s *Service) resolveFile(ctx context.Context, job *Job) (path, imdb, title string, year, season, episode int, ok bool) {
	if job.Kind == "episode" {
		full, err := s.series.Get(ctx, job.SeriesID)
		if err != nil {
			return "", "", "", 0, 0, 0, false
		}
		p, _ := s.series.EpisodeFilePath(ctx, job.SeriesID, job.Season, job.Episode)
		if p == "" {
			return "", "", "", 0, 0, 0, false
		}
		return p, full.IMDBID, full.Title, full.Year, job.Season, job.Episode, true
	}
	m, err := s.movies.Get(ctx, job.MovieID)
	if err != nil || !m.HasFile || m.MovieFilePath == "" {
		return "", "", "", 0, 0, 0, false
	}
	return m.MovieFilePath, m.IMDBID, m.Title, m.Year, 0, 0, true
}

// extractForLangs pulls the first embedded text track matching each wanted language out to an SRT
// sidecar named for that language, in a single ffmpeg pass (one read, all tracks). 0-byte outputs
// (empty tracks) are pruned. Returns how many real sidecars were written.
func (s *Service) extractForLangs(ctx context.Context, path string, subs []SubTrack, langs []string) (int, error) {
	type out struct {
		index int
		path  string
	}
	var outs []out
	for _, l := range langs {
		for _, t := range subs {
			if t.Text && langMatches(t.Lang, l) {
				outs = append(outs, out{t.Index, sidecarPath(path, strings.ToLower(l))})
				break
			}
		}
	}
	if len(outs) == 0 {
		return 0, nil
	}
	args := []string{"-y", "-hide_banner", "-i", path}
	for _, o := range outs {
		args = append(args, "-map", fmt.Sprintf("0:s:%d", o.index), "-c:s", "srt", o.path)
	}
	runErr := exec.CommandContext(ctx, s.ffmpeg, args...).Run()
	kept := 0
	for _, o := range outs {
		if fi, err := os.Stat(o.path); err == nil {
			if fi.Size() == 0 {
				_ = os.Remove(o.path)
				continue
			}
			kept++
		}
	}
	return kept, runErr
}
