package subtitles

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/tristenlammi/arrmada/internal/movies"
	"github.com/tristenlammi/arrmada/internal/safego"
	"github.com/tristenlammi/arrmada/internal/series"
)

// JobState is the lifecycle of a subtitle-ensure job.
type JobState string

const (
	StateQueued  JobState = "queued"
	StateRunning JobState = "running"
	StateDone    JobState = "done"
	StateSkipped JobState = "skipped"
	StateFailed  JobState = "failed"
	// StateCancelled: stopped by the user, either before it ran or part-way through.
	StateCancelled JobState = "cancelled"
)

// ErrJobNotActive is returned by Cancel for a job that is already finished (or unknown).
var ErrJobNotActive = errors.New("job is not queued or running")

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
	// Progress is 0-100 while an AI run is in flight (extraction and downloads are too
	// quick to bother). Stays 0 for everything else.
	Progress  int    `json:"progress,omitempty"`
	Stage     string `json:"stage,omitempty"`      // what the running job is doing right now
	StartedAt int64  `json:"started_at,omitempty"` // unix seconds the worker picked it up
	// Redo ignores the sidecars already there and makes every kept language again,
	// replacing them — for when what's there is wrong (the AI read the dub, a bad
	// download) and deleting files by hand shouldn't be the only way back.
	Redo bool `json:"redo,omitempty"`
	// Priority is which line the job waits in: PrioImport, PrioManual or PrioSweep. The
	// worker always takes the lowest non-empty line first, so tonight's import isn't stuck
	// behind a sweep's worth of hour-long AI runs.
	Priority int `json:"priority"`
}

// Queue priorities, most urgent first. Each has its own FIFO line in Service.pending.
const (
	PrioImport = 0 // a file that just landed: someone is about to watch it
	PrioManual = 1 // a button press in the UI
	PrioSweep  = 2 // the periodic catch-up sweep and "Ensure all"
	numPrios   = 3
)

// clampPrio keeps an out-of-range priority from indexing past the pending lines.
func clampPrio(p int) int {
	if p < 0 {
		return 0
	}
	if p >= numPrios {
		return numPrios - 1
	}
	return p
}

// key identifies the file a job is for, so the same file can't be queued twice.
func (j *Job) key() string {
	if j.Kind == "episode" {
		return fmt.Sprintf("e:%d:%d:%d", j.SeriesID, j.Season, j.Episode)
	}
	return fmt.Sprintf("m:%d", j.MovieID)
}

// LogLine is one entry in the Subtitles activity console.
type LogLine struct {
	At    int64  `json:"at"`
	Level string `json:"level"` // "info" | "warn" | "error"
	Msg   string `json:"msg"`
}

// Run drains the queue in a single worker until ctx is cancelled (start it in a goroutine).
//
// The queue is a slice under the mutex rather than a channel. A channel send blocks once
// the buffer is full, and the one worker can be inside a whisper run for many minutes —
// so a sweep that queued a few hundred files would block the scheduler goroutine, and an
// import hook would block the import, for as long as it took the worker to drain. A
// slice grows; the caller always returns immediately.
func (s *Service) Run(ctx context.Context) {
	s.log.Info("subtitles: worker started")
	s.Rescan(ctx) // first library pass, in the background; the pages read it
	for {
		job := s.pop()
		if job == nil {
			select {
			case <-ctx.Done():
				return
			case <-s.wake:
			}
			continue
		}
		jctx, done := s.startJob(ctx, job)
		// A panic in one job fails that job and the worker moves on to the next, instead
		// of the whole app going down with it.
		if err := safego.Call(s.log, "subtitles job", func() error { s.process(jctx, job); return nil }); err != nil {
			s.finish(job, StateFailed, "internal error: "+err.Error())
		}
		done()
	}
}

// startJob gives a job its own cancellable context and records it as the running job, so
// Cancel can reach it. The returned func releases both; call it when process returns.
func (s *Service) startJob(ctx context.Context, job *Job) (context.Context, func()) {
	jctx, cancel := context.WithCancel(ctx)
	s.mu.Lock()
	s.running = job
	s.cancelRun = cancel
	s.mu.Unlock()
	return jctx, func() {
		cancel()
		s.mu.Lock()
		if s.running == job {
			s.running = nil
			s.cancelRun = nil
		}
		s.mu.Unlock()
	}
}

// Cancel stops one job. A queued job is dropped from the queue on the spot; the running
// job has its context cancelled, which kills the ffmpeg/whisper child process, and the
// worker then records it as cancelled — whisper writes its SRT to a temp path and only
// moves it next to the video on success, so nothing half-written is left behind.
func (s *Service) Cancel(id int64) error {
	s.mu.Lock()
	var job *Job
	for _, j := range s.jobs {
		if j.ID == id {
			job = j
			break
		}
	}
	if job == nil {
		s.mu.Unlock()
		return ErrJobNotActive
	}
	switch job.State {
	case StateQueued:
		s.dropPendingLocked(job)
		job.State = StateCancelled
		job.Note = "removed from the queue"
		s.retireLocked(job)
		s.mu.Unlock()
		s.event("info", "Removed "+job.Title+" from the queue")
		return nil
	case StateRunning:
		if s.running == job && s.cancelRun != nil {
			s.cancelRun()
		}
		job.Note = "stopping…"
		s.mu.Unlock()
		s.event("info", "Stopping "+job.Title)
		return nil
	default:
		s.mu.Unlock()
		return ErrJobNotActive
	}
}

// ClearQueue drops every queued job (the running one is left alone — use Cancel for it).
// Returns how many were dropped.
func (s *Service) ClearQueue() int {
	s.mu.Lock()
	n := 0
	for p := range s.pending {
		for _, j := range s.pending[p] {
			j.State = StateCancelled
			j.Note = "queue cleared"
			s.retireLocked(j)
			n++
		}
		s.pending[p] = nil
	}
	s.mu.Unlock()
	if n > 0 {
		s.event("info", fmt.Sprintf("Cleared %d queued job(s)", n))
	}
	return n
}

// dropPendingLocked removes a job from whichever pending line holds it; the mutex must be held.
func (s *Service) dropPendingLocked(job *Job) {
	for p := range s.pending {
		for i, j := range s.pending[p] {
			if j == job {
				s.pending[p] = append(s.pending[p][:i], s.pending[p][i+1:]...)
				return
			}
		}
	}
}

// pendingLocked is the total number of waiting jobs across every line; the mutex must be held.
func (s *Service) pendingLocked() int {
	n := 0
	for p := range s.pending {
		n += len(s.pending[p])
	}
	return n
}

// pop takes the oldest job from the most urgent non-empty line, or nil.
func (s *Service) pop() *Job {
	s.mu.Lock()
	defer s.mu.Unlock()
	for p := range s.pending {
		if len(s.pending[p]) == 0 {
			continue
		}
		job := s.pending[p][0]
		s.pending[p] = s.pending[p][1:]
		return job
	}
	return nil
}

// enqueue registers a job and wakes the worker. Returns the existing job instead when the
// same file is already queued or running: the 6-hourly sweep re-queues everything still
// missing, and without this every file was in the queue several times over.
func (s *Service) enqueue(job *Job) *Job {
	s.mu.Lock()
	key := job.key()
	// Looked up, not scanned: a sweep queues thousands, and walking the list for each
	// one (with a key rendered per entry) was quadratic — slow enough under the race
	// detector to fail the "never blocks" test.
	if s.active == nil {
		s.active = map[string]*Job{}
	}
	job.Priority = clampPrio(job.Priority)
	if j, ok := s.active[key]; ok {
		if j.State == StateQueued {
			if job.Redo {
				j.Redo = true // the waiting job takes on the stronger intent
			}
			// Pressing Ensure on a file the sweep already queued moves it up to the
			// button's line rather than leaving it hours deep behind the sweep.
			if job.Priority < j.Priority {
				s.dropPendingLocked(j)
				j.Priority = job.Priority
				s.pending[j.Priority] = append(s.pending[j.Priority], j)
			}
		}
		s.mu.Unlock()
		return j
	}
	s.nextID++
	job.ID = s.nextID
	job.State = StateQueued
	job.At = time.Now().Unix()
	s.active[key] = job
	s.jobs = append([]*Job{job}, s.jobs...)
	// Keep the visible history bounded, but never drop a job that hasn't run yet. The
	// trim walks the whole list, so it runs once the finished tail has grown by a few
	// hundred rather than on every enqueue.
	if len(s.jobs) > 200+s.pendingLocked()+256 {
		kept := s.jobs[:0:0]
		for i, j := range s.jobs {
			if i < 200 || j.State == StateQueued || j.State == StateRunning {
				kept = append(kept, j)
			}
		}
		s.jobs = kept
	}
	s.pending[job.Priority] = append(s.pending[job.Priority], job)
	s.mu.Unlock()
	s.event("info", "Queued "+job.Title)
	select {
	case s.wake <- struct{}{}:
	default: // worker already awake
	}
	return job
}

// QueueMovie enqueues a subtitle-ensure job for one movie. redo replaces the sidecars
// already there rather than filling in what's missing; prio is the line it waits in
// (PrioImport, PrioManual or PrioSweep).
func (s *Service) QueueMovie(ctx context.Context, movieID int64, redo bool, prio int) (*Job, error) {
	m, err := s.movies.Get(ctx, movieID)
	if err != nil {
		return nil, err
	}
	if !m.HasFile || m.MovieFilePath == "" {
		return nil, fmt.Errorf("movie has no file")
	}
	return s.enqueue(&Job{Kind: "movie", MovieID: movieID, Title: m.Title, Redo: redo, Priority: prio}), nil
}

// QueueEpisode enqueues a subtitle-ensure job for one TV episode (redo and prio as for movies).
func (s *Service) QueueEpisode(ctx context.Context, seriesID int64, season, episode int, redo bool, prio int) (*Job, error) {
	path, _ := s.series.EpisodeFilePath(ctx, seriesID, season, episode)
	if path == "" {
		return nil, fmt.Errorf("episode has no file")
	}
	title := fmt.Sprintf("S%02dE%02d", season, episode)
	if sm, err := s.series.Get(ctx, seriesID); err == nil {
		title = fmt.Sprintf("%s - S%02dE%02d", sm.Title, season, episode)
	}
	return s.enqueue(&Job{Kind: "episode", SeriesID: seriesID, Season: season, Episode: episode, Title: title, Redo: redo, Priority: prio}), nil
}

// QueueSeries enqueues an ensure job for every episode of one show that has a file but
// lacks a kept language — the middle ground between one episode and the whole library.
// Returns how many were queued (already-queued episodes are deduped by enqueue).
func (s *Service) QueueSeries(ctx context.Context, seriesID int64) (int, error) {
	if _, err := s.series.Get(ctx, seriesID); err != nil {
		return 0, err
	}
	n := 0
	for _, e := range s.missingEpisodes(ctx, seriesID) {
		if _, err := s.QueueEpisode(ctx, seriesID, e.season, e.episode, false, PrioManual); err == nil {
			n++
		}
	}
	return n, nil
}

// OnMovieImported is the import hook: a movie that just landed gets its subtitles ensured
// now, rather than whenever the next 6-hourly sweep happens to run.
func (s *Service) OnMovieImported(ctx context.Context, movieID int64) {
	if !s.settings.GetBool(ctx, keyMoviesAuto, defaultMoviesAuto) {
		return
	}
	if _, err := s.QueueMovie(ctx, movieID, false, PrioImport); err != nil {
		s.log.Debug("subtitles: import hook skipped movie", "movie_id", movieID, "err", err)
	}
}

// OnMovieChanged keeps Subtitles in step with a movie whose files changed without an
// import — renamed, a file deleted, the movie removed. A movie that still has a file gets
// its Library entry recomputed at the new path (a rename carries its sidecars along, so
// nothing is queued; a missing language is the 6-hourly sweep's job, as before); one left
// with no file is forgotten (OnMovieRemoved). Safe to run again for the same state. An
// error means the movie couldn't be read, and the caller should try again.
func (s *Service) OnMovieChanged(ctx context.Context, movieID int64) error {
	m, err := s.movies.Get(ctx, movieID)
	if err != nil && !errors.Is(err, movies.ErrNotFound) {
		return err
	}
	if err != nil || !m.HasFile || m.MovieFilePath == "" {
		s.OnMovieRemoved(ctx, movieID)
		return nil
	}
	s.snap.mu.Lock()
	seen := !s.snap.at.IsZero()
	s.snap.mu.Unlock()
	if seen { // before the first pass there's nothing to patch; the pass will see it
		s.patchMovie(ctx, m, s.languages(ctx))
	}
	return nil
}

// OnMovieRemoved forgets a movie whose file (or the movie itself) is gone: its Library
// entry goes now rather than at the next pass, and an ensure job still waiting for it is
// dropped from the queue — there's no file left to subtitle. A job already running is
// left to finish (it fails on the missing file on its own).
func (s *Service) OnMovieRemoved(_ context.Context, movieID int64) {
	s.dropMovie(movieID)
	s.mu.Lock()
	n := 0
	for _, j := range s.jobs {
		if j.Kind == "movie" && j.MovieID == movieID && j.State == StateQueued {
			s.dropPendingLocked(j)
			j.State = StateCancelled
			j.Note = "the movie's file was removed"
			s.retireLocked(j)
			n++
		}
	}
	s.mu.Unlock()
	if n > 0 {
		s.log.Info("subtitles: dropped queued jobs for a removed movie file", "movie_id", movieID, "jobs", n)
	}
}

// OnSeriesImported is the import hook for TV: the episodes the import just placed get
// their subtitles ensured now. Only those — it used to be told just the series and queued
// every episode of the show still missing a language, so one new episode of a long-running
// show put the whole back-catalogue in the queue in front of whatever came next. Anything
// older that is missing is the 6-hourly sweep's job.
func (s *Service) OnSeriesImported(ctx context.Context, seriesID int64, episodes []series.EpisodeRef) {
	if !s.settings.GetBool(ctx, keySeriesAuto, defaultSeriesAuto) {
		return
	}
	n := 0
	for _, e := range episodes {
		if _, err := s.QueueEpisode(ctx, seriesID, e.Season, e.Episode, false, PrioImport); err == nil {
			n++
		} else {
			s.log.Debug("subtitles: import hook skipped episode", "series_id", seriesID, "season", e.Season, "episode", e.Episode, "err", err)
		}
	}
	if n > 0 {
		s.log.Info("subtitles: import hook queued episodes", "series_id", seriesID, "count", n)
	}
}

// SweepMissing enqueues an ensure job for every downloaded file still missing a kept-language
// subtitle (media = "movies" | "tv"). Returns how many jobs were queued.
//
// Decides "missing" from the sidecars on disk alone. It used to go through Library(),
// which probes every video file for embedded tracks — a full ffprobe pass over the whole
// catalogue, every six hours, to answer a question a directory listing answers.
func (s *Service) SweepMissing(ctx context.Context, media string) (int, error) {
	n := 0
	if media == "tv" {
		list, err := s.series.List(ctx)
		if err != nil {
			return 0, err
		}
		for _, sm := range list {
			if ctx.Err() != nil {
				return n, ctx.Err()
			}
			for _, e := range s.missingEpisodes(ctx, sm.ID) {
				if _, err := s.QueueEpisode(ctx, sm.ID, e.season, e.episode, false, PrioSweep); err == nil {
					n++
				}
			}
		}
		return n, nil
	}
	list, err := s.movies.List(ctx)
	if err != nil {
		return 0, err
	}
	langs := s.languages(ctx)
	for _, m := range list {
		if ctx.Err() != nil {
			return n, ctx.Err()
		}
		if !m.HasFile || m.MovieFilePath == "" {
			continue
		}
		if !movieNeedsSweep(m.MovieFilePath, langs) {
			continue
		}
		if _, err := s.QueueMovie(ctx, m.ID, false, PrioSweep); err == nil {
			n++
		}
	}
	return n, nil
}

// movieNeedsSweep reports whether the sweep should queue a movie: some kept language has
// no paired sidecar. A language covered only by an orphaned subtitle (one named for no
// video — the owner's file under an old name, most likely) doesn't count: regenerating
// those every six hours would bury what the owner chose. Ensure still replaces it.
func movieNeedsSweep(path string, langs []string) bool {
	sc := scanSidecars(path, langs, "movie")
	missing := missingOf(langs, sc.Present)
	return len(without(missing, orphanCovered(langs, sc.Present, sc.Orphans))) > 0
}

type epRef struct{ season, episode int }

// missingEpisodes lists one show's episodes that have a file but lack a kept language.
func (s *Service) missingEpisodes(ctx context.Context, seriesID int64) []epRef {
	full, err := s.series.Get(ctx, seriesID)
	if err != nil {
		return nil
	}
	langs := s.languages(ctx)
	var out []epRef
	for _, sn := range full.Seasons {
		for _, e := range sn.Episodes {
			if !e.HasFile || e.FilePath == "" {
				continue
			}
			if len(missingOf(langs, scanSidecars(e.FilePath, langs, "episode").Present)) == 0 {
				continue
			}
			out = append(out, epRef{e.SeasonNumber, e.EpisodeNumber})
		}
	}
	return out
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

// Pending reports how many jobs are waiting in every line, for the status line.
func (s *Service) Pending() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pendingLocked()
}

// update mutates a job under lock.
func (s *Service) update(job *Job, fn func(*Job)) {
	s.mu.Lock()
	fn(job)
	s.mu.Unlock()
}

// finish sets a job's terminal state + note, and clears the in-flight stage/progress.
func (s *Service) finish(job *Job, state JobState, note string) {
	s.mu.Lock()
	job.State, job.Note, job.Stage, job.Progress = state, note, "", 0
	s.retireLocked(job)
	s.mu.Unlock()
}

// retireLocked forgets a job that is no longer queued or running, so its file can be
// queued again. The mutex must be held.
func (s *Service) retireLocked(job *Job) {
	if k := job.key(); s.active[k] == job {
		delete(s.active, k)
	}
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

// missingOf returns the wanted languages that aren't present (case-insensitive).
func missingOf(wanted, present []string) []string {
	have := make(map[string]bool, len(present))
	for _, p := range present {
		have[strings.ToLower(p)] = true
	}
	var out []string
	for _, w := range wanted {
		if !have[strings.ToLower(w)] {
			out = append(out, w)
		}
	}
	return out
}
