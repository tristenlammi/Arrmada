package convert

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/tristenlammi/arrmada/internal/safego"
)

// The runner replaces the old queue. There's nothing to fill, drain or lose on a restart:
// while Convert is switched on and inside your encode hours, each worker asks "what's the
// most worthwhile file left?", converts it, and asks again. Files you pick by hand go first
// and run straight away. Outside the hours — or while someone is watching Plex — a running
// encode is frozen in place and resumed later, not thrown away.

// Run starts the workers until ctx is cancelled (start it in a goroutine).
func (s *Service) Run(ctx context.Context) {
	// A restart abandons any in-flight encode (the original is only replaced at the very
	// end, so nothing is lost). Clear its debris, then replay any file swap a crash cut off.
	s.cleanScratch(ctx)
	s.pruneResume(ctx) // a pieced encode's finished pieces stay for it to carry on (chunk.go)
	s.recoverSwaps(ctx)
	s.holdStartup(ctx) // the originals hold's self-check (hold.go)
	// Ledger rows still in progress belong to jobs the restart cut off; no worker has
	// started yet, so none of them is live.
	if n := s.history.interrupted(ctx); n > 0 {
		s.log.Info("convert: closed ledger rows interrupted by the restart", "count", n)
	}
	s.log.Info("convert: runner started", "workers", s.workerCount(ctx))
	if _, saved := s.settings.Lookup(keyCrop); !saved {
		// It used to default to on; an install that never chose keeps no crop now.
		s.log.Info("convert: black-bar crop is now off unless you turn it on in Convert's settings")
	}
	done := make(chan struct{})
	go s.pauseLoop(ctx, done)
	// Every possible worker exists; the ones above the configured count just wait. That
	// makes "conversions at once" take effect immediately instead of after a restart.
	// One more is the request lane (runner_lane.go): a file you asked for never waits
	// behind an automatic conversion frozen outside your hours.
	for i := 0; i <= requestLane; i++ {
		go s.worker(ctx, i)
	}
	<-ctx.Done()
	close(done)
}

func (s *Service) worker(ctx context.Context, idx int) {
	for {
		job := s.nextJob(ctx, idx)
		if job == nil {
			return
		}
		s.runJob(ctx, job)
	}
}

// nextJob blocks until there's something to convert, returning nil when ctx ends. The
// request lane only ever takes requests, and only when the normal slots are held up.
func (s *Service) nextJob(ctx context.Context, idx int) *Job {
	for {
		if ctx.Err() != nil {
			return nil
		}
		// Taken before looking, so a wake that lands between the look and the wait isn't
		// missed.
		wake := s.wakeCh()
		var job *Job
		switch {
		case idx == requestLane:
			job = s.pickRequest(ctx)
		case idx < s.workerCount(ctx):
			job = s.pickJob(ctx)
		}
		if job != nil {
			return job
		}
		select {
		case <-ctx.Done():
			return nil
		case <-wake:
		case <-time.After(2 * time.Minute):
		}
	}
}

// pickJob claims the next file to convert, or returns nil when there's nothing to do (or
// nothing allowed right now).
func (s *Service) pickJob(ctx context.Context) *Job {
	p := s.prefs(ctx)
	watching := p.pauseWatching && s.isWatching()
	if watching {
		return nil // not starting anything while someone is watching
	}
	// 1. Hand-picked files, oldest first. They run regardless of the hours: you asked.
	for _, r := range s.requests.list(ctx) {
		it, ok := parseKey(r.Key)
		if !ok {
			s.requests.remove(ctx, r.Key)
			continue
		}
		if job := s.claim(it, r.Title, true); job != nil {
			return job
		}
	}
	// 2. The runner's own picks, inside the hours — and not while a requested file runs on
	// the request lane: it would only start to wait for it.
	if !p.auto || !windowAllows(p.start, p.end) || s.laneBusy() {
		return nil
	}
	waiting := s.skips.waitingKeys(ctx)
	blocked := s.failures.blockedKeys(ctx, maxFailures)
	holdFull := s.holdTooFull(ctx) // the originals hold's budget (holdbudget.go)
	cands := s.autoCandidates(ctx, p)
	// A film with pieces already encoded (chunk.go) goes first: a restart interrupted it,
	// and starting something else would leave that work sitting in the scratch space.
	if p.resumable {
		if resumable := s.resumableKeys(ctx); len(resumable) > 0 {
			for _, c := range cands {
				if !resumable[c.Key] || waiting[c.Key] || blocked[c.Key] || holdFull(c) {
					continue
				}
				it, _ := parseKey(c.Key)
				if job := s.claim(it, c.Title, false); job != nil {
					return job
				}
			}
		}
	}
	for _, c := range cands {
		if waiting[c.Key] || blocked[c.Key] || holdFull(c) {
			continue
		}
		it, _ := parseKey(c.Key)
		if job := s.claim(it, c.Title, false); job != nil {
			return job
		}
	}
	return nil
}

// claim creates the job for an item, or returns nil when it's already being converted.
func (s *Service) claim(it item, title string, requested bool) *Job {
	job, fresh := s.claimPending(it.key(), func(id int64) *Job {
		j := it.job()
		j.ID, j.Title, j.State, j.Requested, j.StartedAt = id, title, StatePreparing, requested, time.Now().Unix()
		return j
	})
	if !fresh {
		return nil
	}
	return job
}

// runJob wraps process() with per-job cancellation.
func (s *Service) runJob(ctx context.Context, job *Job) {
	jobCtx, cancel := context.WithCancel(withJob(ctx, job)) // every tool below registers with the job
	defer cancel()
	s.update(job, func(j *Job) { j.cancel = cancel })
	if job.lane {
		// Freeze the automatic conversion it jumped ahead of now, not at the next tick —
		// and let it carry on as soon as this one ends.
		s.applyPauses(ctx)
		defer s.applyPauses(ctx)
	}
	// A panic in one file's conversion fails that job (below, through finish, like any
	// other failure) instead of killing the worker — or, before, the whole app mid-encode.
	panicErr := safego.Call(s.log, "convert: "+job.Title, func() error { s.process(jobCtx, job); return nil })
	s.update(job, func(j *Job) { j.cancel = nil })
	// process() can return with the job still marked active if ctx was cancelled under it;
	// make sure it always ends up finished, or its claim would never be released.
	s.mu.Lock()
	stillActive := activeState(job.State)
	s.mu.Unlock()
	if stillActive {
		if panicErr != nil {
			s.finish(job, StateFailed, "internal error: "+panicErr.Error())
			return
		}
		if ctx.Err() != nil {
			// Shutting down: not the user's cancel, so don't record a skip for it.
			s.update(job, func(j *Job) { j.State, j.Note = StateCancelled, "stopped by a restart" })
			s.releasePending(job.Key, job)
			return
		}
		s.finish(job, StateCancelled, "cancelled")
	}
}

// --- what to convert next ---------------------------------------------------------------

// autoCand is one file the runner could pick, ranked by what converting it gains.
type autoCand struct {
	Key     string `json:"key"`
	Title   string `json:"title"`
	Kind    string `json:"kind"`
	Video   bool   `json:"video"`   // re-encode (false = tracks only)
	Saving  int64  `json:"saving"`  // estimated bytes saved
	Size    int64  `json:"size"`    // current size
	Tracks  string `json:"tracks"`  // what happens to the tracks, if anything
	Reason  string `json:"reason"`  // "video", "video + tracks", "tracks"
	Codec   string `json:"codec"`   // likely target
	Current string `json:"current"` // current video codec
	// An extra version track of the movie (0 = its default file) and that track's name;
	// Title already carries the name.
	VersionID    int64  `json:"version_id,omitempty"`
	VersionLabel string `json:"version_label,omitempty"`
	// ETASec and Nights are the time forecast, set on Status's Up next (throughput.go).
	ETASec int64 `json:"eta_sec,omitempty"`
	Nights int   `json:"nights,omitempty"`

	// needScratch is the scratch space this file will need, for the Settings indicator.
	needScratch int64
	// effects is what converting it loses, for the Switch-on summary (autopreview.go).
	effects candEffects
	// facts are what the time forecast reads about the file.
	facts forecastFacts
}

// candScratch is the scratch space a candidate will need, worked out the way the job will
// (scratchNeeded). Whether a file carries HDR10+ is only known once the whole stream is
// read, so any HEVC HDR10 file is assumed to: that pipeline holds the stream twice, and
// under-promising room is what makes a big remux fail hours in.
func candScratch(mi MediaInfo, plan Plan, size int64) int64 {
	return candScratchFor(mi, plan, size, false)
}

// candScratchFor is candScratch with resumable pieces on or off: a film that would be
// encoded in pieces (chunk.go) holds the pieces and the joined film at once. Whether it
// ends up on the GPU, and so in one go, isn't known here; the larger figure is the safe one.
func candScratchFor(mi MediaInfo, plan Plan, size int64, resumable bool) int64 {
	if mi.SizeBytes <= 0 {
		mi.SizeBytes = size
	}
	maybeHDR10Plus := mi.HDR == "HDR10+" || (codecClass(mi.VideoCodec) == "hevc" && mi.EncodeHDR() == "HDR10")
	pieced := resumable && plan.VideoCodec != "" && chunkPlan(&mi) != nil
	return scratchNeeded(&mi, plan, maybeHDR10Plus || pieced)
}

// scratchNeedWindow is how many upcoming files the scratch indicator looks ahead over.
const scratchNeedWindow = 20

// ScratchNeed is the most scratch space any of the next few files the runner would pick will
// need, and that file's title — the figure the Settings indicator compares free space with.
// Files waiting on a skip or blocked by failures are passed over, as the runner does. A
// file with pieces already encoded (chunk.go) needs that much less: they're on the disk.
func (s *Service) ScratchNeed(ctx context.Context) (int64, string) {
	waiting := s.skips.waitingKeys(ctx)
	blocked := s.failures.blockedKeys(ctx, maxFailures)
	p := s.prefs(ctx)
	var done map[string]int64
	if p.resumable {
		done = s.resumeBytes(ctx)
	}
	var need int64
	var title string
	seen := 0
	for _, c := range s.autoCandidates(ctx, p) {
		if waiting[c.Key] || blocked[c.Key] {
			continue
		}
		if n := max(c.needScratch-done[c.Key], 0); n > need {
			need, title = n, c.Title
		}
		if seen++; seen == scratchNeedWindow {
			break
		}
	}
	return need, title
}

// autoCandidates is every file that needs work, best first: re-encodes by estimated saving,
// then track-only tidy-ups (quick, but they save little). Cached against the index — the
// scan reads every row and lists every folder for subtitle sidecars, which must not run on
// each pick.
func (s *Service) autoCandidates(ctx context.Context, p prefs) []autoCand {
	gen, key := s.indexGen(), p.cacheKey()
	s.libCacheMu.Lock()
	if c := s.candCache; c.fresh(gen, key) {
		s.libCacheMu.Unlock()
		return c.v
	}
	s.libCacheMu.Unlock()
	v := s.computeCandidates(ctx, p)
	s.libCacheMu.Lock()
	s.candCache = libCacheEntry[[]autoCand]{gen: gen, key: key, at: time.Now(), v: v, ok: true}
	s.libCacheMu.Unlock()
	return v
}

func (s *Service) computeCandidates(ctx context.Context, p prefs) []autoCand {
	if s.index == nil {
		return nil
	}
	rows, err := s.index.db.QueryContext(ctx,
		`SELECT media_type, movie_id, version_id, version_label, series_id, season, episode, title, size_bytes, info_json, path, orig_lang
		   FROM convert_library WHERE info_json <> ''`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	dirCache := map[string][]string{}
	var out []autoCand
	for rows.Next() {
		var mediaType, label, title, infoJSON, path, orig string
		var movieID, versionID, seriesID, size int64
		var season, episode int
		if rows.Scan(&mediaType, &movieID, &versionID, &label, &seriesID, &season, &episode, &title, &size, &infoJSON, &path, &orig) != nil {
			continue
		}
		var mi MediaInfo
		if json.Unmarshal([]byte(infoJSON), &mi) != nil {
			continue
		}
		plan, n := p.planForTitle(&mi, path, titleRef{Kind: mediaType, MovieID: movieID, SeriesID: seriesID, OrigLang: orig}, dirCache)
		if !n.Worth {
			continue
		}
		c := autoCand{Key: ItemKey(mediaType, movieID, versionID, seriesID, season, episode),
			Title: trackTitle(title, label, versionID), Kind: mediaType, VersionID: versionID, VersionLabel: label,
			Video: n.Video, Size: size, Tracks: trackSummary(&mi, plan), Current: strings.ToUpper(mi.VideoCodec),
			needScratch: candScratchFor(mi, plan, size, p.resumable), effects: effectsOf(&mi, plan, n, size, seriesID, season),
			facts: factsFor(&mi, plan, size)}
		if n.Video {
			c.Codec = plan.VideoCodec
		}
		c.Saving = n.Save
		switch {
		case n.Video && c.Tracks != "":
			c.Reason = "video + tracks"
		case n.Video:
			c.Reason = "video"
		default:
			c.Reason = "tracks"
		}
		out = append(out, c)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Video != out[j].Video {
			return out[i].Video
		}
		if out[i].Saving != out[j].Saving {
			return out[i].Saving > out[j].Saving
		}
		return out[i].Size > out[j].Size
	})
	return out
}

// --- requests ----------------------------------------------------------------------------

// Request asks for one file to be converted now: it goes before anything the runner would
// pick, and runs regardless of the encode hours. A previous skip or failure is forgotten —
// asking by hand is how you say "try it again".
func (s *Service) Request(ctx context.Context, key string) error {
	it, ok := parseKey(key)
	if !ok {
		return fmt.Errorf("invalid item")
	}
	s.mu.Lock()
	_, running := s.pending[key]
	s.mu.Unlock()
	if running {
		return ErrAlreadyQueued
	}
	_, title, _, ok := s.resolveSource(ctx, it.job())
	if !ok {
		return fmt.Errorf("this title has no file to convert")
	}
	if it.Kind == "episode" {
		title = s.episodeTitle(ctx, it)
	}
	s.skips.clear(ctx, key)
	s.failures.clearFailures(ctx, key)
	if err := s.requests.add(ctx, key, title); err != nil {
		return err
	}
	s.event("info", "Requested "+title)
	s.invalidateLibraryCache()
	s.wakeUp()
	return nil
}

// RequestSeries asks for every episode of a show (or one season, season >= 0) that needs
// work. Files already known not to convert (a permanent skip, repeated failures) are left
// out — a bulk request shouldn't re-run a hundred known dead ends.
func (s *Service) RequestSeries(ctx context.Context, seriesID int64, season int) (int, error) {
	eps, err := s.indexedCandidates(ctx, "episode", seriesID)
	if err != nil {
		return 0, err
	}
	waiting := s.skips.permanentKeys(ctx)
	blocked := s.failures.blockedKeys(ctx, maxFailures)
	n := 0
	for _, c := range eps {
		if !c.Needs.Worth || (season >= 0 && c.Season != season) || waiting[c.Key] || blocked[c.Key] {
			continue
		}
		s.mu.Lock()
		_, running := s.pending[c.Key]
		s.mu.Unlock()
		if running {
			continue
		}
		s.skips.clear(ctx, c.Key)
		if s.requests.add(ctx, c.Key, c.Title) == nil {
			n++
		}
	}
	if n > 0 {
		s.event("info", fmt.Sprintf("Requested %d episode%s", n, plural(n)))
		s.invalidateLibraryCache()
		s.wakeUp()
	}
	return n, nil
}

// CancelRequest removes a waiting request (empty key = all of them).
func (s *Service) CancelRequest(ctx context.Context, key string) int {
	defer s.invalidateLibraryCache()
	if key == "" {
		return s.requests.clear(ctx)
	}
	s.requests.remove(ctx, key)
	return 1
}

func (s *Service) episodeTitle(ctx context.Context, it item) string {
	if s.series != nil {
		if sm, err := s.series.Get(ctx, it.SeriesID); err == nil {
			return fmt.Sprintf("%s - S%02dE%02d", sm.Title, it.Season, it.Episode)
		}
	}
	return fmt.Sprintf("S%02dE%02d", it.Season, it.Episode)
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// --- status ------------------------------------------------------------------------------

// Status is what the runner is doing, in words, plus what's coming up.
type Status struct {
	State     string     `json:"state"` // working | starting | paused | waiting | off | done
	Message   string     `json:"message"`
	Auto      bool       `json:"auto"`
	Window    string     `json:"window,omitempty"` // "01:00–06:00", empty = any time
	Watching  bool       `json:"watching"`
	Requests  []Request  `json:"requests"`
	UpNext    []autoCand `json:"up_next"`
	Remaining int        `json:"remaining"` // files the runner would still convert
	// Problems counts the files that would convert but are held back by a skip (waiting to
	// be retried, or never retried) or by repeated failures — what the Problems tab lists.
	// They aren't in Remaining, so "done" says how many there are rather than "everything".
	Problems int `json:"problems"`
	// The time forecast (throughput.go): the whole remaining list's encoding time, spread
	// over the conversions that run at once, in seconds and in nights of the encode hours;
	// and how much encoding the running conversions have done — what a restart would lose.
	BacklogSec    int64 `json:"backlog_sec"`
	BacklogNights int   `json:"backlog_nights"`
	InFlightSecs  int64 `json:"in_flight_secs"`
}

// Status reports the runner's state and the next few files.
func (s *Service) Status(ctx context.Context) Status {
	p := s.prefs(ctx)
	st := Status{Auto: p.auto, Watching: s.isWatching(), Requests: s.requests.list(ctx)}
	if st.Requests == nil {
		st.Requests = []Request{}
	}
	if p.start != "" && p.end != "" {
		st.Window = p.start + "–" + p.end
	}
	waiting := s.skips.waitingKeys(ctx)
	blocked := s.failures.blockedKeys(ctx, maxFailures)
	requested := map[string]bool{}
	for _, r := range st.Requests {
		requested[r.Key] = true
	}
	s.mu.Lock()
	running := map[string]bool{}
	active, pausedN, paused := 0, 0, ""
	for _, j := range s.jobs {
		if activeState(j.State) {
			running[j.Key] = true
			active++
			if j.Paused != "" {
				pausedN++
				paused = j.Paused
			}
		}
	}
	s.mu.Unlock()
	// A request that's already converting is in Now, not waiting in Up next.
	waitingReqs := st.Requests[:0:0]
	for _, r := range st.Requests {
		if !running[r.Key] {
			waitingReqs = append(waitingReqs, r)
		}
	}
	st.Requests = waitingReqs
	st.UpNext = []autoCand{}
	var remaining []autoCand
	for _, c := range s.autoCandidates(ctx, p) {
		if running[c.Key] || requested[c.Key] {
			continue
		}
		if waiting[c.Key] || blocked[c.Key] {
			st.Problems++
			continue
		}
		st.Remaining++
		remaining = append(remaining, c)
		if len(st.UpNext) < 8 {
			st.UpNext = append(st.UpNext, c)
		}
	}
	s.forecastStatus(ctx, &st, p, remaining)
	inHours := windowAllows(p.start, p.end)
	watchBlock := p.pauseWatching && st.Watching
	switch {
	case active > 0 && pausedN == active:
		st.State, st.Message = "paused", "Paused — "+paused
	case active > 0 && pausedN > 0:
		// A requested file running while an automatic one waits (or is outside its hours)
		// is still working: say both.
		n := active - pausedN
		st.State, st.Message = "working", fmt.Sprintf("Converting %d file%s · %d paused — %s", n, plural(n), pausedN, paused)
	case active > 0:
		st.State, st.Message = "working", fmt.Sprintf("Converting %d file%s", active, plural(active))
	case watchBlock && (len(st.Requests) > 0 || (p.auto && inHours && st.Remaining > 0)):
		st.State, st.Message = "paused", "Waiting — someone is watching"
	case len(st.Requests) > 0:
		st.State, st.Message = "starting", "Starting your requested files"
	case !p.auto:
		st.State, st.Message = "off", "Automatic conversion is off — switch it on in Settings, or convert files by hand from the Library"
	case st.Remaining == 0 && st.Problems > 0:
		// Never "everything's converted" while files sit in Problems: some wait for a retry
		// (still seeding, a full disk), some need you to look.
		st.State, st.Message = "done", fmt.Sprintf("Everything convertible is done — %d file%s couldn't be converted (see Problems)", st.Problems, plural(st.Problems))
	case st.Remaining == 0:
		st.State, st.Message = "done", "Everything's converted — new files are picked up as they arrive"
	case !inHours:
		st.State, st.Message = "waiting", fmt.Sprintf("Waiting for your encode hours (%s)", st.Window)
	default:
		st.State, st.Message = "starting", "Starting the next file"
		// The next file waits on the originals hold's budget (holdbudget.go).
		if msg := s.holdPausedMessage(ctx, st.UpNext[0].Size); msg != "" {
			st.State, st.Message = "paused", msg
		}
	}
	return st
}

// --- pausing -----------------------------------------------------------------------------

// pauseReason says why a job should be paused right now, or "" if it may run. laneBusy says
// a requested file is running on the request lane: an automatic job waits for it.
func (s *Service) pauseReason(job *Job, p prefs, watching, laneBusy bool) string {
	if watching {
		// On Plex or Jellyfin: Insights' Watching covers every monitored server.
		return "someone is watching"
	}
	if !job.Requested && laneBusy {
		return laneWaitReason
	}
	if !job.Requested && !windowAllows(p.start, p.end) {
		return fmt.Sprintf("outside your encode hours — resumes at %s", p.start)
	}
	return ""
}

// pauseLoop freezes and resumes running encodes as the hours and Plex activity change.
func (s *Service) pauseLoop(ctx context.Context, done <-chan struct{}) {
	t := time.NewTicker(15 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-done:
			return
		case <-ctx.Done():
			return
		case <-t.C:
			s.applyPauses(ctx)
		}
	}
}

// ActiveSummary is what a restart would interrupt: how many conversions are running, how
// long the longest-running one has been going, and how far it has got (0..1). No titles,
// so the restart confirm can show it on any staff page.
func (s *Service) ActiveSummary() (n int, longestSec int64, progress float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().Unix()
	for _, j := range s.jobs {
		if !activeState(j.State) {
			continue
		}
		n++
		age := int64(0)
		if j.StartedAt > 0 && now > j.StartedAt {
			age = now - j.StartedAt
		}
		if n == 1 || age > longestSec {
			longestSec, progress = age, j.Progress
		}
	}
	return n, longestSec, progress
}

func (s *Service) applyPauses(ctx context.Context) {
	if !canSuspend {
		return
	}
	p := s.prefs(ctx)
	watching := p.pauseWatching && s.isWatching()
	s.mu.Lock()
	defer s.mu.Unlock()
	laneBusy := s.laneBusyLocked()
	for _, j := range s.jobs {
		if !activeState(j.State) || j.cancelled {
			continue
		}
		reason := s.pauseReason(j, p, watching, laneBusy)
		if reason == j.Paused {
			continue
		}
		for pid := range j.procs {
			if reason != "" {
				_ = suspendGroup(pid)
			} else {
				_ = resumeGroup(pid)
			}
		}
		if reason != "" {
			s.event("info", fmt.Sprintf("Paused %s — %s", j.Title, reason))
		} else {
			s.event("info", "Resumed "+j.Title)
		}
		j.Paused = reason
	}
}

// trackProc registers a running ffmpeg process group with its job so it can be paused —
// and pauses it at once if the job is paused right now.
func (s *Service) trackProc(job *Job, pid int) {
	if job == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if job.procs == nil {
		job.procs = map[int]bool{}
	}
	job.procs[pid] = true
	if job.Paused != "" && canSuspend {
		_ = suspendGroup(pid)
	}
}

func (s *Service) untrackProc(job *Job, pid int) {
	if job == nil {
		return
	}
	s.mu.Lock()
	delete(job.procs, pid)
	s.mu.Unlock()
}

// waitAllowed blocks before a heavy step while the job should be paused, so a quality-gate
// retry or the next pipeline stage doesn't start in the middle of someone's film. Returns
// false when ctx ends.
func (s *Service) waitAllowed(ctx context.Context, job *Job) bool {
	for {
		p := s.prefs(ctx)
		reason := s.pauseReason(job, p, p.pauseWatching && s.isWatching(), s.laneBusy())
		if reason == "" {
			s.update(job, func(j *Job) { j.Paused = "" })
			return true
		}
		s.update(job, func(j *Job) { j.Paused = reason })
		select {
		case <-ctx.Done():
			return false
		case <-time.After(30 * time.Second):
		}
	}
}

// windowAllows reports whether now falls within an "HH:MM"–"HH:MM" window. Empty or
// unparseable means "always". Windows may wrap past midnight (22:00–06:00).
func windowAllows(start, end string) bool {
	return windowAllowsAt(start, end, time.Now())
}

func windowAllowsAt(start, end string, now time.Time) bool {
	if start == "" || end == "" {
		return true
	}
	s0, ok1 := parseHM(start)
	e0, ok2 := parseHM(end)
	if !ok1 || !ok2 || s0 == e0 {
		return true
	}
	cur := now.Hour()*60 + now.Minute()
	if s0 < e0 {
		return cur >= s0 && cur < e0
	}
	return cur >= s0 || cur < e0
}

// parseHM parses "HH:MM" into minutes-since-midnight.
func parseHM(v string) (int, bool) {
	p := strings.SplitN(v, ":", 2)
	if len(p) != 2 {
		return 0, false
	}
	h, e1 := strconv.Atoi(strings.TrimSpace(p[0]))
	m, e2 := strconv.Atoi(strings.TrimSpace(p[1]))
	if e1 != nil || e2 != nil || h < 0 || h > 23 || m < 0 || m > 59 {
		return 0, false
	}
	return h*60 + m, true
}
