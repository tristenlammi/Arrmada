package subtitles

import (
	"context"
	"fmt"
	"time"
)

// JobState is the lifecycle of a subtitle-ensure job.
type JobState string

const (
	StateQueued  JobState = "queued"
	StateRunning JobState = "running"
	StateDone    JobState = "done"
	StateSkipped JobState = "skipped"
	StateFailed  JobState = "failed"
)

// Job is one file's "make sure the kept-language subtitles exist" task — the unit the Queue tab
// shows. Extraction and downloads happen inside process().
type Job struct {
	ID       int64    `json:"id"`
	Kind     string   `json:"kind"` // "movie" | "episode"
	MovieID  int64    `json:"movie_id,omitempty"`
	SeriesID int64    `json:"series_id,omitempty"`
	Season   int      `json:"season,omitempty"`
	Episode  int      `json:"episode,omitempty"`
	Title    string   `json:"title"`
	State    JobState `json:"state"`
	Note     string   `json:"note,omitempty"`
	At       int64    `json:"at"` // unix seconds queued
}

// LogLine is one entry in the Subtitles activity console.
type LogLine struct {
	At    int64  `json:"at"`
	Level string `json:"level"` // "info" | "warn" | "error"
	Msg   string `json:"msg"`
}

// Run drains the queue in a single worker until ctx is cancelled (start it in a goroutine).
// One worker keeps subtitle I/O gentle and ordering simple.
func (s *Service) Run(ctx context.Context) {
	s.log.Info("subtitles: worker started")
	for {
		select {
		case <-ctx.Done():
			return
		case job := <-s.queue:
			s.process(ctx, job)
		}
	}
}

// enqueue registers a job (newest-first, capped) and hands it to the worker.
func (s *Service) enqueue(job *Job) *Job {
	s.mu.Lock()
	s.nextID++
	job.ID = s.nextID
	job.State = StateQueued
	job.At = time.Now().Unix()
	s.jobs = append([]*Job{job}, s.jobs...)
	if len(s.jobs) > 200 {
		s.jobs = s.jobs[:200]
	}
	s.mu.Unlock()
	s.event("info", "Queued "+job.Title)
	s.queue <- job
	return job
}

// QueueMovie enqueues a subtitle-ensure job for one movie.
func (s *Service) QueueMovie(ctx context.Context, movieID int64) (*Job, error) {
	m, err := s.movies.Get(ctx, movieID)
	if err != nil {
		return nil, err
	}
	if !m.HasFile || m.MovieFilePath == "" {
		return nil, fmt.Errorf("movie has no file")
	}
	return s.enqueue(&Job{Kind: "movie", MovieID: movieID, Title: m.Title}), nil
}

// QueueEpisode enqueues a subtitle-ensure job for one TV episode.
func (s *Service) QueueEpisode(ctx context.Context, seriesID int64, season, episode int) (*Job, error) {
	path, _ := s.series.EpisodeFilePath(ctx, seriesID, season, episode)
	if path == "" {
		return nil, fmt.Errorf("episode has no file")
	}
	title := fmt.Sprintf("S%02dE%02d", season, episode)
	if sm, err := s.series.Get(ctx, seriesID); err == nil {
		title = fmt.Sprintf("%s - S%02dE%02d", sm.Title, season, episode)
	}
	return s.enqueue(&Job{Kind: "episode", SeriesID: seriesID, Season: season, Episode: episode, Title: title}), nil
}

// SweepMissing enqueues an ensure job for every downloaded file still missing a kept-language
// subtitle (media = "movies" | "tv"). Returns how many jobs were queued.
func (s *Service) SweepMissing(ctx context.Context, media string) (int, error) {
	lib, err := s.Library(ctx, media)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, fs := range lib {
		if fs.Missing == 0 {
			continue
		}
		if fs.Kind == "episode" {
			if _, err := s.QueueEpisode(ctx, fs.SeriesID, fs.Season, fs.Episode); err == nil {
				n++
			}
		} else if _, err := s.QueueMovie(ctx, fs.MovieID); err == nil {
			n++
		}
	}
	return n, nil
}

// Jobs returns a snapshot of recent jobs (newest first).
func (s *Service) Jobs() []Job {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Job, len(s.jobs))
	for i, j := range s.jobs {
		out[i] = *j
	}
	return out
}

// update mutates a job under lock.
func (s *Service) update(job *Job, fn func(*Job)) {
	s.mu.Lock()
	fn(job)
	s.mu.Unlock()
}

// finish sets a job's terminal state + note.
func (s *Service) finish(job *Job, state JobState, note string) {
	s.update(job, func(j *Job) { j.State = state; j.Note = note })
}

// event appends a line to the activity console (kept to the last 500) and mirrors it to the log.
func (s *Service) event(level, msg string) {
	s.logMu.Lock()
	s.logBuf = append(s.logBuf, LogLine{At: time.Now().Unix(), Level: level, Msg: msg})
	if len(s.logBuf) > 500 {
		s.logBuf = s.logBuf[len(s.logBuf)-500:]
	}
	s.logMu.Unlock()
	if level == "warn" || level == "error" {
		s.log.Warn("subtitles: " + msg)
	} else {
		s.log.Info("subtitles: " + msg)
	}
}

// Logs returns the recent activity console lines (oldest first).
func (s *Service) Logs() []LogLine {
	s.logMu.Lock()
	defer s.logMu.Unlock()
	out := make([]LogLine, len(s.logBuf))
	copy(out, s.logBuf)
	return out
}
