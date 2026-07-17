package subtitles

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
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
	s.update(job, func(j *Job) { j.State = StateRunning })

	mi, _ := s.probeCached(ctx, path) // best-effort; nil → no embedded tracks known
	var subs []SubTrack
	if mi != nil {
		subs = mi.Subs
	}
	langs := s.languages(ctx)
	present := map[string]bool{}
	for _, l := range presentLanguages(path, langs) {
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

	extracted := 0
	if len(extractLangs) > 0 {
		n, err := s.extractForLangs(ctx, path, subs, extractLangs)
		if err != nil {
			s.event("warn", fmt.Sprintf("%s: extract failed: %v", title, err))
		}
		extracted = n
	}
	downloaded := 0
	for _, l := range downloadLangs {
		if okDL, err := s.grabOne(ctx, imdb, title, year, season, episode, path, l); err != nil {
			s.event("warn", fmt.Sprintf("%s: download %s failed: %v", title, l, err))
		} else if okDL {
			downloaded++
		}
	}
	generated := 0
	for _, t := range aiTasks {
		verb := "transcribing"
		if t.translate {
			verb = "translating → en"
		}
		s.event("info", fmt.Sprintf("AI %s %s (%s)…", verb, title, t.lang))
		if err := s.whisper.generate(ctx, s.ffmpeg, path, sidecarPath(path, strings.ToLower(t.lang)), t.lang, t.translate); err != nil {
			s.event("warn", fmt.Sprintf("%s: AI %s failed: %v", title, t.lang, err))
		} else {
			generated++
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
	if len(parts) == 0 {
		parts = append(parts, "nothing produced")
	}
	note := strings.Join(parts, " · ")
	s.event("info", fmt.Sprintf("✓ %s — %s", title, note))
	if extracted+downloaded+generated > 0 {
		s.finish(job, StateDone, note)
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
