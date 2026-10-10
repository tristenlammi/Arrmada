// Package convert is the Convert module: it works through the Movies and TV libraries in
// the background, re-encoding wasteful video to HEVC or AV1 at the same visual quality and
// tidying audio/subtitle tracks — gently, inside the hours you choose, and without ever
// putting a file at risk.
package convert

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/tristenlammi/arrmada/internal/library"
	"github.com/tristenlammi/arrmada/internal/movies"
	"github.com/tristenlammi/arrmada/internal/series"
	"github.com/tristenlammi/arrmada/internal/settings"
)

// defaultScanAt is when the daily index sweep runs if the admin hasn't picked a time.
// Pre-dawn by default: the sweep only touches new or changed files, but on a big first
// run it can wake the array, so keep it away from prime viewing hours.
const defaultScanAt = "03:00"

// encodeNice is the scheduling priority encodes run at. 19 is the lowest — encoding is bulk
// background work that should yield instantly to Plex or anything else the user notices.
const encodeNice = 19

// maxJobHistory caps how many FINISHED jobs are kept for the activity list.
const maxJobHistory = 200

// JobState is a conversion's lifecycle stage.
type JobState string

const (
	StatePreparing JobState = "preparing" // analysing the file
	StateTesting   JobState = "testing"   // trying HEVC and AV1 on a few clips
	StateEncoding  JobState = "encoding"
	StateVerifying JobState = "verifying"
	StateReplacing JobState = "replacing"
	StateDone      JobState = "done"
	StateFailed    JobState = "failed"
	StateSkipped   JobState = "skipped"
	StateCancelled JobState = "cancelled"
)

// activeState reports whether a job is still running.
func activeState(st JobState) bool {
	switch st {
	case StatePreparing, StateTesting, StateEncoding, StateVerifying, StateReplacing:
		return true
	}
	return false
}

// Active reports whether a job in this state is still running.
func (st JobState) Active() bool { return activeState(st) }

// Job is one conversion of one library file — a movie or a TV episode.
type Job struct {
	ID          int64    `json:"id"`
	Key         string   `json:"key"`
	Kind        string   `json:"kind"` // "movie" | "episode"
	MovieID     int64    `json:"movie_id,omitempty"`
	SeriesID    int64    `json:"series_id,omitempty"`
	Season      int      `json:"season"` // NOT omitempty: season 0 is specials
	Episode     int      `json:"episode,omitempty"`
	Title       string   `json:"title"`
	State       JobState `json:"state"`
	Progress    float64  `json:"progress"` // 0..1
	FPS         float64  `json:"fps"`
	SpeedX      float64  `json:"speed_x"`      // × realtime
	DurationSec float64  `json:"duration_sec"` // source runtime (for the UI's ETA)
	Encoder     string   `json:"encoder"`
	Codec       string   `json:"codec,omitempty"` // the format it's converting to ("" = tracks only)
	SrcBytes    int64    `json:"src_bytes"`
	OutBytes    int64    `json:"out_bytes"`
	SSIM        float64  `json:"ssim,omitempty"` // quality score vs the source (0 = not measured)
	Note        string   `json:"note,omitempty"`
	Requested   bool     `json:"requested"`        // asked for by hand
	Paused      string   `json:"paused,omitempty"` // why it's paused; "" while running
	StartedAt   int64    `json:"started_at"`
	FinishedAt  int64    `json:"finished_at,omitempty"`

	cancel    context.CancelFunc // set while running, so the encode can be stopped mid-flight
	cancelled bool
	procs     map[int]bool // process groups currently running for this job (for pausing)
	rec       *jobRecord   // what the ledger keeps about it (see history.go)
}

// Service runs the conversion engine: workers that pick the next file worth converting,
// and the safe encode → verify → replace pipeline each one runs.
type Service struct {
	db       *sql.DB // for the swap journal; sub-stores hold their own handle
	movies   *movies.Service
	series   *series.Service
	settings *settings.Service
	log      *slog.Logger

	ffmpeg, ffprobe        string
	scratchDir, recycleDir string
	bin                    library.Bin // where originals go (SetBin); nil = the single bin at recycleDir
	hdr10plusTool          string      // HDR10+ metadata tool (empty if not bundled)

	// noNumaPools is set when x265's NUMA pool binding is blocked by the container's
	// seccomp profile — see numaPoolsBlocked. Encodes then run unpooled.
	noNumaPools bool

	encoders []Encoder
	failures *failureStore // repeated-failure tracking
	cache    *probeCache   // persisted ffprobe results (avoids re-analyzing on restart)
	skips    *skipStore    // persisted skip reasons, so they survive a restart and are visible
	index    *libraryIndex // persisted per-file library facts; what the lists read
	requests *requestStore // hand-picked files, persisted
	choices  *choiceStore  // per-file HEVC-vs-AV1 test outcomes
	measured *measureStore // what test encodes measured, per file and format
	history  *historyStore // the durable ledger of conversion outcomes

	// watching reports whether someone is watching Plex right now (from Insights).
	watching atomic.Pointer[func() bool]
	// binHeadroom reports the recycle bin's room under its cap (see SetBinHeadroom). binMu
	// makes "does it fit" and the move into the bin one step, so two workers finishing
	// together can't both fit into the same room.
	binHeadroom atomic.Pointer[binHeadroomFunc]
	binMu       sync.Mutex
	// libraryChanged hears about the folder of each swapped-in file (Plex's scanner).
	libraryChanged atomic.Pointer[func(kind, dir string)]

	indexMu       sync.Mutex // serializes index sweeps
	indexScanning atomic.Bool
	lastSweep     time.Time

	// Cached views over the index — see libcache.go.
	libCacheMu sync.Mutex
	statsCache libCacheEntry[*LibraryStats]
	tvCache    libCacheEntry[[]SeriesRollup]
	candCache  libCacheEntry[[]autoCand]

	mu      sync.Mutex
	jobs    []*Job
	nextID  int64
	pending map[string]*Job // item key → its running job, so a file is never converted twice at once
	wake    chan struct{}   // nudges idle workers (a request arrived, a setting changed)

	reclaimMu sync.Mutex // guards the reclaimed-bytes read-modify-write across workers

	hwBrokenMu sync.Mutex
	hwBroken   map[string]int // runtime failures per hardware encoder

	compare compareState

	logMu  sync.Mutex
	logBuf []LogLine
	logs   *logStore
	// logDBMu orders the log's database writes the way logMu orders the ring (see event).
	logDBMu sync.Mutex
}

// LogLine is one entry in the Convert activity log.
type LogLine struct {
	At    int64  `json:"at"`    // unix seconds
	Level string `json:"level"` // "info" | "warn" | "error"
	Msg   string `json:"msg"`
}

// event appends a human-readable line to the activity log (kept to the last maxLogLines,
// persisted so history survives a restart) and mirrors it to the structured log.
//
// A line identical to the one before it (same level, same text) is folded into that line as
// "msg (×N)" with the new time, instead of being appended: a retry loop used to fill the
// 5,000-line log with copies and push out the history that matters. Only consecutive
// repeats fold, so lines from two jobs that interleave stay separate. The structured log
// still gets the first line and every 10th repeat.
func (s *Service) event(level, msg string) {
	ln := LogLine{At: time.Now().Unix(), Level: level, Msg: msg}
	count := 1
	s.logMu.Lock()
	if n := len(s.logBuf); n > 0 {
		if last := s.logBuf[n-1]; last.Level == level {
			if base, c := splitRepeat(last.Msg); base == msg {
				count = c + 1
				ln.Msg = fmt.Sprintf("%s (×%d)", msg, count)
				s.logBuf[n-1] = ln
			}
		}
	}
	if count == 1 {
		s.logBuf = append(s.logBuf, ln)
		if len(s.logBuf) > maxLogLines {
			s.logBuf = s.logBuf[len(s.logBuf)-maxLogLines:]
		}
	}
	// The database writes happen in the same order as the ring changes — a fold must rewrite
	// the row it folded into, not a newer one — so the write lock is taken before the ring's
	// lock is let go. Readers of the ring (Logs) don't wait on the database.
	s.logDBMu.Lock()
	s.logMu.Unlock()
	if count == 1 {
		s.logs.append(context.Background(), ln)
	} else {
		s.logs.updateLast(context.Background(), ln)
	}
	s.logDBMu.Unlock()
	if s.log == nil || (count > 1 && count%10 != 0) {
		return
	}
	switch level {
	case "error", "warn":
		s.log.Warn("convert: " + ln.Msg)
	default:
		s.log.Info("convert: " + ln.Msg)
	}
}

// Logs returns the recent activity-log lines (oldest first).
func (s *Service) Logs() []LogLine {
	s.logMu.Lock()
	defer s.logMu.Unlock()
	out := make([]LogLine, len(s.logBuf))
	copy(out, s.logBuf)
	return out
}

// NewService wires the module, verifying which encoders actually work up front.
func NewService(db *sql.DB, mv *movies.Service, sr *series.Service, set *settings.Service, ffmpeg, ffprobe, scratchDir, recycleDir string, log *slog.Logger) *Service {
	_ = os.MkdirAll(scratchDir, 0o755)
	s := &Service{
		db:     db,
		movies: mv, series: sr, settings: set, log: log,
		ffmpeg: ffmpeg, ffprobe: ffprobe, scratchDir: scratchDir, recycleDir: recycleDir,
		encoders: detectEncoders(context.Background(), ffmpeg),
		failures: &failureStore{db: db}, cache: &probeCache{db: db}, logs: &logStore{db: db},
		index: &libraryIndex{db: db}, skips: &skipStore{db: db}, requests: &requestStore{db: db},
		choices: &choiceStore{db: db}, measured: &measureStore{db: db}, history: &historyStore{db: db},
		pending: map[string]*Job{},
		wake:    make(chan struct{}, 1),
	}
	s.logBuf = s.logs.recent(context.Background(), maxLogLines) // restore the log after a restart
	s.hdr10plusTool, _ = exec.LookPath("hdr10plus_tool")
	if s.noNumaPools = numaPoolsBlocked(context.Background(), ffmpeg); s.noNumaPools {
		log.Warn("convert: x265 NUMA thread pools are blocked by the container's seccomp policy — " +
			"encoding unpooled. Add cap_add: [SYS_NICE] to the container for full threading.")
	}
	var working []string
	for _, e := range s.encoders {
		if e.Available {
			working = append(working, e.Name)
		}
	}
	log.Info("convert: encoders verified", "working", strings.Join(working, ","), "hdr10plus", s.hdr10plusTool != "")
	return s
}

// SetWatching tells Convert how to ask whether someone is watching Plex, so it can pause.
func (s *Service) SetWatching(fn func() bool) {
	if fn == nil {
		s.watching.Store(nil)
		return
	}
	s.watching.Store(&fn)
}

func (s *Service) isWatching() bool {
	if fn := s.watching.Load(); fn != nil {
		return (*fn)()
	}
	return false
}

// SetLibraryChanged tells Convert who to tell when a converted file replaces its original
// (kind "movie" or "show", and the file's folder), so Plex rescans it and plays the new
// file. nil = nobody.
func (s *Service) SetLibraryChanged(fn func(kind, dir string)) {
	if fn == nil {
		s.libraryChanged.Store(nil)
		return
	}
	s.libraryChanged.Store(&fn)
}

// swapped reports a finished swap's folder.
func (s *Service) swapped(job *Job, finalPath string) {
	fn := s.libraryChanged.Load()
	if fn == nil || finalPath == "" {
		return
	}
	kind := "movie"
	if job.Kind == "episode" {
		kind = "show"
	}
	(*fn)(kind, filepath.Dir(finalPath))
}

// binHeadroomFunc reports how many more bytes the recycle bin takes before its cap purges
// anything; capped is false with no cap, enabled false when recycling is off.
type binHeadroomFunc func(ctx context.Context) (free int64, capped, enabled bool)

// SetBinHeadroom tells Convert how to ask the recycle bin for its room under the cap.
func (s *Service) SetBinHeadroom(fn func(ctx context.Context) (free int64, capped, enabled bool)) {
	if fn == nil {
		s.binHeadroom.Store(nil)
		return
	}
	f := binHeadroomFunc(fn)
	s.binHeadroom.Store(&f)
}

// binRoomFor reports whether an original of size bytes can go to the recycle bin without
// the bin's cap purging it — and everything older — within the hour. A bin that's off or
// uncapped always has room (off means the original is deleted once the result is
// verified, which is what the owner chose). On false, reason says why for Problems.
func (s *Service) binRoomFor(ctx context.Context, size int64) (ok bool, reason string) {
	fn := s.binHeadroom.Load()
	if fn == nil {
		return true, ""
	}
	free, capped, enabled := (*fn)(ctx)
	if !enabled || !capped || size <= free {
		return true, ""
	}
	return false, fmt.Sprintf("the original (%s) doesn't fit in the recycle bin's free room (%s left under its size cap), "+
		"so it would be permanently deleted within the hour. Raise the cap in Settings → Recycle bin", humanBytes(size), humanBytes(free))
}

// cleanScratch removes leftover per-job scratch files (partial encodes, test clips, HDR10+
// sidecars) at startup — the debris of any conversion cut short by a restart. Only runs
// before the workers start, so nothing live is touched.
func (s *Service) cleanScratch(ctx context.Context) {
	seen := map[string]bool{}
	for _, dir := range []string{s.scratchDir, s.activeScratch(ctx)} {
		if dir == "" || seen[dir] {
			continue
		}
		seen[dir] = true
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		removed := 0
		for _, e := range entries {
			n := e.Name()
			if strings.HasPrefix(n, "convert-") || strings.HasPrefix(n, "sample-") || strings.HasPrefix(n, "trial-") ||
				strings.HasPrefix(n, "h10p-") || strings.HasPrefix(n, "dv-") {
				if os.RemoveAll(filepath.Join(dir, n)) == nil {
					removed++
				}
			}
		}
		if removed > 0 && s.log != nil {
			s.log.Info("convert: cleaned orphaned scratch files", "dir", dir, "count", removed)
		}
	}
}

// Hardware reports the verified encoders and a label for what conversions will run on.
func (s *Service) Hardware(ctx context.Context) (encoders []Encoder, using string) {
	p := s.prefs(ctx)
	using = "CPU (x265)"
	if p.allowAV1 {
		using = "CPU (x265 / SVT-AV1)"
	}
	if p.useGPU {
		if hw, ok := hardwareFor("hevc", s.encoders); ok && !s.hardwareIsBroken(hw.Name) {
			using = hw.Label + " · HDR on the CPU"
		}
	}
	return s.encoders, using
}

// Devices reports the available render nodes plus the one selected for hardware encoding.
func (s *Service) Devices(ctx context.Context) (devices []RenderDevice, selected string) {
	return renderDevices(), s.vaapiDev(ctx)
}

// vaapiDev resolves which render node VAAPI encodes on, falling back to the default node.
func (s *Service) vaapiDev(ctx context.Context) string {
	if d := strings.TrimSpace(s.settings.Get(ctx, keyVaapiDevice, "")); d != "" {
		return d
	}
	return vaapiDevice
}

// activeScratch resolves the transcode working directory: the configured override when set,
// otherwise the startup default. The heavy encode happens here before the finished file is
// moved into the library, so it should live on fast storage (an SSD/NVMe pool), never the
// array. A bad override falls back to the default.
func (s *Service) activeScratch(ctx context.Context) string {
	dir := strings.TrimSpace(s.settings.Get(ctx, keyScratchDir, ""))
	if dir == "" {
		dir = s.scratchDir
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		if s.log != nil {
			s.log.Warn("convert: transcode dir unusable, falling back to default", "dir", dir, "err", err)
		}
		_ = os.MkdirAll(s.scratchDir, 0o755)
		return s.scratchDir
	}
	return dir
}

// ScratchInfo reports the resolved transcode directory and its free space.
func (s *Service) ScratchInfo(ctx context.Context) (dir string, freeBytesN int64) {
	dir = s.activeScratch(ctx)
	return dir, int64(freeBytes(dir))
}

// Reclaimed returns the cumulative bytes saved by conversions (persisted).
func (s *Service) Reclaimed(ctx context.Context) int64 {
	n, _ := strconv.ParseInt(s.settings.Get(ctx, keyReclaimed, "0"), 10, 64)
	return n
}

func (s *Service) addReclaimed(ctx context.Context, delta int64) {
	if delta <= 0 {
		return
	}
	s.reclaimMu.Lock() // read-modify-write must be atomic across workers
	defer s.reclaimMu.Unlock()
	_ = s.settings.Set(ctx, keyReclaimed, strconv.FormatInt(s.Reclaimed(ctx)+delta, 10))
}

// Candidate is a library file plus its probed spec and whether it needs work.
type Candidate struct {
	Kind      string     `json:"kind"` // "movie" | "episode"
	Key       string     `json:"key"`
	MovieID   int64      `json:"movie_id,omitempty"`
	SeriesID  int64      `json:"series_id,omitempty"`
	Season    int        `json:"season"` // NOT omitempty — season 0 is specials
	Episode   int        `json:"episode,omitempty"`
	Title     string     `json:"title"`
	Year      int        `json:"year,omitempty"`
	PosterURL string     `json:"poster_url,omitempty"`
	Path      string     `json:"path"`
	Info      *MediaInfo `json:"info,omitempty"`
	Candidate bool       `json:"candidate"` // falls short of the target in some way
	Worth     bool       `json:"worth"`     // worth converting (see Needs.Worth)
	SaveBytes int64      `json:"save_bytes"`
	Needs     Needs      `json:"needs"`
	EstBytes  int64      `json:"est_bytes"` // rough estimate of the converted size
	Tracks    string     `json:"tracks,omitempty"`
}

// Library returns every movie with its spec and what it needs, from the index.
func (s *Service) Library(ctx context.Context) ([]Candidate, error) {
	return s.indexedCandidates(ctx, "movie", 0)
}

// LibraryConvertible returns only the files that still need work.
func (s *Service) LibraryConvertible(ctx context.Context, mediaType string, seriesID int64) ([]Candidate, error) {
	all, err := s.indexedCandidates(ctx, mediaType, seriesID)
	if err != nil {
		return nil, err
	}
	out := make([]Candidate, 0, len(all))
	for _, c := range all {
		if c.Worth {
			out = append(out, c)
		}
	}
	return out, nil
}

// LibraryTV returns one show's episodes (seriesID > 0) with their specs and needs.
func (s *Service) LibraryTV(ctx context.Context, seriesID int64) ([]Candidate, error) {
	return s.indexedCandidates(ctx, "episode", seriesID)
}

// Blocklist returns the files left alone after repeated failures, with titles resolved.
func (s *Service) Blocklist(ctx context.Context) ([]Blocked, error) {
	list, err := s.failures.list(ctx)
	if err != nil {
		return nil, err
	}
	out := list[:0]
	for _, b := range list {
		if b.Count < maxFailures {
			continue // still being retried — not blocked yet
		}
		b.Title = s.titleForKey(ctx, b.Kind, b.MovieID, b.SeriesID, b.Season, b.Episode, b.Key)
		out = append(out, b)
	}
	return out, nil
}

// Skips returns the files that couldn't be converted and why, with titles resolved.
func (s *Service) Skips(ctx context.Context) ([]Skipped, error) {
	list, err := s.skips.list(ctx)
	if err != nil {
		return nil, err
	}
	for i := range list {
		list[i].Title = s.titleForKey(ctx, list[i].MediaKind, list[i].MovieID, list[i].SeriesID, list[i].Season, list[i].Episode, list[i].Key)
	}
	return list, nil
}

// ClearSkip forgets an item's skip (or all of them) so it's tried again.
func (s *Service) ClearSkip(ctx context.Context, key string) error {
	defer s.wakeUp()
	s.invalidateLibraryCache()
	if key == "" {
		return s.skips.clearAll(ctx)
	}
	s.skips.clear(ctx, key)
	return nil
}

// BinSettingsChanged is told when the recycle bin's cap or retention is saved. Files waiting
// for room in the bin are tried again straight away rather than up to a day later: raising
// the cap is exactly what their Problems entry asks for.
func (s *Service) BinSettingsChanged(ctx context.Context) {
	if s.skips.clearKind(ctx, SkipBinFull) > 0 {
		s.invalidateLibraryCache()
		s.wakeUp()
	}
}

// ClearBlocklist forgets an item's failures (or all of them when key is empty).
func (s *Service) ClearBlocklist(ctx context.Context, key string) error {
	defer s.wakeUp()
	s.invalidateLibraryCache()
	if key == "" {
		_, err := s.failures.db.ExecContext(ctx, `DELETE FROM convert_failures`)
		return err
	}
	s.failures.clearFailures(ctx, key)
	return nil
}

// titleForKey resolves a display title for a movie or episode, falling back to the raw key.
func (s *Service) titleForKey(ctx context.Context, kind string, movieID, seriesID int64, season, episode int, key string) string {
	switch kind {
	case "movie":
		if s.movies != nil {
			if m, err := s.movies.Get(ctx, movieID); err == nil {
				return m.Title
			}
		}
	case "episode":
		if s.series != nil {
			if sm, err := s.series.Get(ctx, seriesID); err == nil {
				return fmt.Sprintf("%s - S%02dE%02d", sm.Title, season, episode)
			}
		}
	}
	return key
}

// hwBrokenThreshold is how many distinct hardware-encode failures it takes before the
// encoder is treated as broken for the rest of the run. One failure can be one bad file.
const hwBrokenThreshold = 2

// markHardwareBroken records that a hardware encoder failed; after hwBrokenThreshold
// failures it stops being used for the rest of the run.
func (s *Service) markHardwareBroken(name, reason string) {
	s.hwBrokenMu.Lock()
	if s.hwBroken == nil {
		s.hwBroken = map[string]int{}
	}
	s.hwBroken[name]++
	crossed := s.hwBroken[name] == hwBrokenThreshold
	s.hwBrokenMu.Unlock()
	if crossed {
		s.log.Warn("convert: hardware encoder failed repeatedly — using the CPU for the rest of this run",
			"encoder", name, "err", reason)
		s.event("warn", name+" failed repeatedly on this machine — converting on the CPU instead")
	} else {
		s.log.Warn("convert: hardware encode failed for this file — retrying on CPU", "encoder", name, "err", reason)
	}
}

func (s *Service) hardwareIsBroken(name string) bool {
	s.hwBrokenMu.Lock()
	defer s.hwBrokenMu.Unlock()
	return s.hwBroken[name] >= hwBrokenThreshold
}

// Jobs returns a snapshot of recent jobs (newest first).
func (s *Service) Jobs() []Job {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Job, len(s.jobs))
	for i, j := range s.jobs {
		out[i] = *j
		out[i].procs, out[i].rec = nil, nil
	}
	return out
}

func (s *Service) update(job *Job, fn func(*Job)) {
	s.mu.Lock()
	fn(job)
	s.mu.Unlock()
}

// claimPending atomically claims an item key with a freshly-built job, or reports that the
// item is already being converted. Check and insert are one critical section so two
// workers can never convert the same file at once.
func (s *Service) claimPending(key string, mk func(id int64) *Job) (*Job, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if j, ok := s.pending[key]; ok {
		return j, false
	}
	s.nextID++
	job := mk(s.nextID)
	job.Key = key
	s.pending[key] = job
	s.jobs = append([]*Job{job}, s.jobs...) // newest first
	s.trimJobsLocked()
	return job, true
}

// releasePending drops a finished job's claim so the file can be picked again later.
func (s *Service) releasePending(key string, job *Job) {
	s.mu.Lock()
	if s.pending[key] == job {
		delete(s.pending, key)
	}
	s.mu.Unlock()
}

// trimJobsLocked drops the oldest FINISHED jobs once history grows past the cap. Callers
// must hold s.mu.
func (s *Service) trimJobsLocked() {
	finished := 0
	for _, j := range s.jobs {
		if !activeState(j.State) {
			finished++
		}
	}
	if finished <= maxJobHistory {
		return
	}
	drop := finished - maxJobHistory
	kept := make([]*Job, 0, len(s.jobs)-drop)
	for i := len(s.jobs) - 1; i >= 0; i-- { // oldest first
		if drop > 0 && !activeState(s.jobs[i].State) {
			drop--
			continue
		}
		kept = append(kept, s.jobs[i])
	}
	for i, j := 0, len(kept)-1; i < j; i, j = i+1, j-1 {
		kept[i], kept[j] = kept[j], kept[i]
	}
	s.jobs = kept
}

// Cancel stops a running job. The original is only ever replaced at the very end of a job,
// so cancelling leaves the library untouched. The file isn't picked again for a while.
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
		return fmt.Errorf("no such job")
	}
	if !activeState(job.State) {
		s.mu.Unlock()
		return fmt.Errorf("job already finished")
	}
	job.cancelled = true
	cancel := job.cancel
	s.mu.Unlock()
	if cancel != nil {
		cancel() // kills ffmpeg; the job unwinds and finishes as cancelled
	}
	return nil
}

// wasCancelled reports whether the user cancelled this job.
func (s *Service) wasCancelled(job *Job) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return job.cancelled
}

// finishSkip ends a job as skipped and records the reason durably, so it shows in Problems
// and the file isn't picked again until it could make a difference.
func (s *Service) finishSkip(job *Job, kind, note string) {
	if s.wasCancelled(job) {
		s.finish(job, StateCancelled, "cancelled")
		return
	}
	s.skips.record(context.Background(), job.Key, kind, note)
	s.record(job).skipKind = kind
	s.finish(job, StateSkipped, note)
}

// finishAfterEncode skips a job that already burned a full encode and would do so again:
// the outcome is deterministic for that file. Counting it toward the failure limit is what
// stops the library re-encoding the same unshrinkable file forever.
func (s *Service) finishAfterEncode(job *Job, kind, note string) {
	if s.wasCancelled(job) {
		s.finish(job, StateCancelled, "cancelled")
		return
	}
	s.failures.recordFailure(context.Background(), job.Key, note)
	s.finishSkip(job, kind, note)
}

// transientFailure reports whether a failure describes a condition that will clear on its
// own. These must not count toward the failure limit: three nights of a full scratch volume
// used to permanently blocklist large parts of the library. Case is ignored: ffmpeg says
// "No space left on device".
func transientFailure(note string) bool {
	note = strings.ToLower(note)
	for _, marker := range []string{"not enough scratch space", "source file is gone", "no space left on device"} {
		if strings.Contains(note, marker) {
			return true
		}
	}
	return false
}

func (s *Service) finish(job *Job, state JobState, note string) {
	if state != StateDone && s.wasCancelled(job) {
		state, note = StateCancelled, "cancelled"
	}
	s.update(job, func(j *Job) {
		j.State, j.Note, j.Paused = state, note, ""
		j.FinishedAt = time.Now().Unix()
		if state == StateDone {
			j.Progress = 1
		}
	})
	s.recordOutcome(job, state, note)
	ctx := context.Background()
	switch state {
	case StateDone:
		saved := job.SrcBytes - job.OutBytes
		if job.Codec == "" {
			s.event("info", fmt.Sprintf("✓ Done %s — tracks tidied (%s)", job.Title, note))
		} else {
			s.event("info", fmt.Sprintf("✓ Done %s — %s → %s (saved %s)", job.Title, humanBytes(job.SrcBytes), humanBytes(job.OutBytes), humanBytes(saved)))
		}
		s.failures.clearFailures(ctx, job.Key)
		s.skips.clear(ctx, job.Key)
	case StateFailed:
		s.event("error", fmt.Sprintf("✗ Failed %s — %s", job.Title, note))
		if transientFailure(note) {
			// It will clear on its own, so it doesn't count toward the failure limit — but
			// it must still wait, or the runner picks the same file straight back up.
			s.skips.record(ctx, job.Key, SkipTransient, note)
		} else {
			s.failures.recordFailure(ctx, job.Key, note)
		}
	case StateSkipped:
		s.event("info", fmt.Sprintf("Skipped %s — %s", job.Title, note))
	case StateCancelled:
		s.event("info", "Cancelled "+job.Title)
		if !job.Requested {
			// Otherwise the runner would pick the very file you just stopped, right away.
			s.skips.record(ctx, job.Key, SkipCancelled, "cancelled by you")
		}
	}
	s.requests.remove(ctx, job.Key)
	s.releasePending(job.Key, job)
	s.invalidateLibraryCache()
	s.wakeUp()
}

// reindexConverted updates the library index for the file a job just converted.
func (s *Service) reindexConverted(ctx context.Context, job *Job) {
	var err error
	if job.Kind == "episode" {
		err = s.IndexSeries(ctx, job.SeriesID)
	} else {
		err = s.IndexMovie(ctx, job.MovieID)
	}
	if err != nil {
		s.log.Warn("convert: reindex after convert failed", "title", job.Title, "err", err)
	}
}

// lineTail keeps the most recent N lines of a stream, for error reporting.
type lineTail struct {
	mu    sync.Mutex
	max   int
	lines []string
}

func (t *lineTail) add(line string) {
	line = strings.TrimSpace(line)
	if line == "" {
		return
	}
	t.mu.Lock()
	t.lines = append(t.lines, line)
	if len(t.lines) > t.max {
		t.lines = t.lines[len(t.lines)-t.max:]
	}
	t.mu.Unlock()
}

func (t *lineTail) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.lines) == 0 {
		return "no output from ffmpeg"
	}
	return strings.Join(t.lines, " | ")
}

func fileSize(path string) int64 {
	if fi, err := os.Stat(path); err == nil {
		return fi.Size()
	}
	return 0
}

// humanBytes renders a byte count for the activity log.
func humanBytes(b int64) string {
	switch {
	case b >= 1<<40:
		return fmt.Sprintf("%.2f TB", float64(b)/(1<<40))
	case b >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(b)/(1<<30))
	case b >= 1<<20:
		return fmt.Sprintf("%.0f MB", float64(b)/(1<<20))
	default:
		return fmt.Sprintf("%d B", b)
	}
}

// SetBin points retired originals at bin (the per-library bins in the app). Call it at
// startup, before the runner starts.
func (s *Service) SetBin(b library.Bin) { s.bin = b }

// retire moves a library file out of the way before it's replaced: into the recycle bin, or
// deleted outright if the admin switched the bin off. It never silently relocates a file.
func (s *Service) retire(path string) error {
	bin := s.bin
	if bin == nil {
		bin = library.SingleBin(s.recycleDir)
	}
	_, offErr := bin.For(path)
	dst, err := library.RemoveToBin(bin, path)
	switch {
	case err != nil:
		return err
	case errors.Is(offErr, library.ErrRecycleDisabled):
		s.log.Info("convert: original deleted (recycle bin is off)", "path", path)
	case dst != "":
		s.log.Info("convert: original recycled", "to", dst)
	}
	return nil
}

// moveFile moves src to dst, falling back to copy+remove across filesystems (scratch is
// usually a different volume from the library). The copy is fsynced before the source is
// removed. Callers must pass a temporary dst and rename it into place.
func moveFile(src, dst string) error {
	if err := os.Rename(src, dst); err == nil {
		return nil
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Sync(); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Remove(src)
}
