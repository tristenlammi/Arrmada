package convert

import (
	"bufio"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/tristenlammi/arrmada/internal/library"
	"github.com/tristenlammi/arrmada/internal/movies"
	"github.com/tristenlammi/arrmada/internal/series"
	"github.com/tristenlammi/arrmada/internal/settings"
)

const (
	keySkipHardlinked = "convert_skip_hardlinked"
	keyReclaimed      = "convert_reclaimed_bytes"
	keyKeepAudioLangs = "convert_keep_audio_langs" // CSV; empty = keep all audio
	keyAddStereo      = "convert_add_stereo"       // add an AAC 2.0 downmix beside surround
	keyLoudnorm       = "convert_loudnorm"         // EBU R128 loudness normalize
	keyTargetCodec    = "convert_target_codec"     // "hevc" | "av1" — what the library is converted to
	keyAuto           = "convert_auto"             // auto-convert the library on the schedule
	keyQualityGate    = "convert_quality_gate"     // reject/retry an encode that scores too low (SSIM)
	keyMinSSIM        = "convert_min_ssim"         // minimum acceptable SSIM vs the source
	keyWorkers        = "convert_workers"          // number of concurrent encode workers (default 1)
	keySweepStart     = "convert_sweep_start"      // auto-sweep window start "HH:MM" (empty = always)
	keySweepEnd       = "convert_sweep_end"        // auto-sweep window end   "HH:MM"
	keyMaxFailures    = "convert_max_failures"     // blocklist a movie after this many convert failures
	keyScratchDir     = "convert_scratch_dir"      // transcode working dir override (empty = the startup default)
	keyVaapiDevice    = "convert_vaapi_device"     // which /dev/dri/renderD* VAAPI encodes on (empty = default)
	keyScanAt         = "convert_scan_at"          // "HH:MM" — when the daily library index sweep runs
	keyCPUCores       = "convert_cpu_cores"        // max cores a CPU encode may use (0 = half the box)
	keyCPUAboveHeight = "convert_cpu_above_height" // use CPU at/above this height (0 = never)
	keyRecodeModern   = "convert_recode_modern"    // also re-encode files already in a modern codec
)

// defaultMinSSIM is the quality bar an encode must clear against its source.
//
// 0.97, not the old 0.95: at 0.95 an encode with visible degradation passes, which makes
// the gate decorative when the whole promise of the module is that you won't see a
// difference. The gate is the only mechanism enforcing that promise, so it has to be set
// where "passed" actually means "indistinguishable".
const (
	defaultMinSSIM      = "0.97"
	defaultMinSSIMValue = 0.97
)

// encodeNice is the scheduling priority CPU encodes run at. 19 is the lowest — encoding is
// bulk background work that should yield instantly to Plex, game servers, or anything else
// the user actually notices.
const encodeNice = 19

// cpuCores is how many cores a CPU encode may use. Default is half the machine: encoding a
// library takes days, and it runs on a server that is also doing other things, so taking the
// whole box is never the right default. Combined with the low priority above, a CPU encode
// becomes something that fills idle capacity rather than something you schedule around.
func (s *Service) cpuCores(ctx context.Context) int {
	n, _ := strconv.Atoi(s.settings.Get(ctx, keyCPUCores, "0"))
	if n <= 0 {
		n = runtime.NumCPU() / 2
	}
	if n < 1 {
		n = 1
	}
	if max := runtime.NumCPU(); n > max {
		n = max
	}
	return n
}

// defaultScanAt is when the daily index sweep runs if the admin hasn't picked a time.
// Pre-dawn by default: the sweep only touches new or changed files, but on a big first
// run it can wake the array, so keep it away from prime viewing hours.
const defaultScanAt = "03:00"

// qualityRetries is how many times the quality gate re-encodes at a higher quality before
// giving up and keeping the original.
const qualityRetries = 2

// maxQueued bounds the in-memory queue. "Convert all" over a TV library is thousands of
// files, and the old 256 slot buffer turned that into a blocking send that never returned —
// especially with the encode window parking the workers. The queue isn't persisted, so a
// restart drops whatever hasn't run; re-running "Convert all" re-queues the remainder.
const maxQueued = 16384

// maxJobHistory caps how many FINISHED jobs are retained for the Queue tab. Pending and
// running jobs are never trimmed — dropping them would hide work that's still going to
// happen.
const maxJobHistory = 200

// JobState is a conversion job's lifecycle stage.
type JobState string

const (
	StateQueued    JobState = "queued"
	StateEncoding  JobState = "encoding"
	StateVerifying JobState = "verifying"
	StateReplacing JobState = "replacing"
	StateDone      JobState = "done"
	StateFailed    JobState = "failed"
	StateSkipped   JobState = "skipped"
	StateCancelled JobState = "cancelled"
)

// Job is one conversion of one library file — a movie or a TV episode.
type Job struct {
	ID          int64    `json:"id"`
	Kind        string   `json:"kind"` // "movie" | "episode" (empty = movie, for back-compat)
	MovieID     int64    `json:"movie_id,omitempty"`
	SeriesID    int64    `json:"series_id,omitempty"`
	Season      int      `json:"season"` // NOT omitempty: season 0 is specials, and must survive
	Episode     int      `json:"episode,omitempty"`
	Title       string   `json:"title"`
	State       JobState `json:"state"`
	Progress    float64  `json:"progress"` // 0..1
	FPS         float64  `json:"fps"`
	SpeedX      float64  `json:"speed_x"`      // × realtime
	DurationSec float64  `json:"duration_sec"` // source runtime (for the UI's ETA)
	Encoder     string   `json:"encoder"`
	SrcBytes    int64    `json:"src_bytes"`
	OutBytes    int64    `json:"out_bytes"`
	SSIM        float64  `json:"ssim,omitempty"` // quality-gate score vs the source (0 = not measured)
	Note        string   `json:"note,omitempty"` // skip reason / error

	plan      Plan               // the compiled plan this job runs (not serialized)
	cancel    context.CancelFunc // set while running, so the encode can be stopped mid-flight
	cancelled bool               // cancellation requested; the worker checks before starting
}

// Service runs the conversion engine: a single worker draining a queue, doing the safe
// encode→verify→replace pipeline. One worker keeps this slice simple; the multi-worker
// pool + scheduling arrive in a later phase.
type Service struct {
	movies   *movies.Service
	series   *series.Service
	settings *settings.Service
	log      *slog.Logger

	ffmpeg, ffprobe        string
	scratchDir, recycleDir string

	doviTool, hdr10plusTool string // Dolby Vision / HDR10+ metadata tools (empty if not bundled)

	// noNumaPools is set when x265's NUMA pool binding is blocked by the container's
	// seccomp profile — see numaPoolsBlocked. Encodes then run unpooled, which is slower
	// but doesn't crash.
	noNumaPools bool

	encoders []Encoder
	encoder  Encoder
	failures *failureStore // quarantine blocklist (repeated-failure tracking)
	cache    *probeCache   // persisted ffprobe results (avoids re-analyzing on restart)
	skips    *skipStore    // persisted skip reasons, so they survive a restart and are visible
	index    *libraryIndex // persisted per-file library facts; what the library list reads

	indexMu   sync.Mutex // serializes index sweeps so a scheduled one can't overlap an import-triggered one
	lastSweep time.Time

	mu        sync.Mutex
	reclaimMu sync.Mutex // guards the reclaimed-bytes read-modify-write across workers
	jobs      []*Job
	nextID    int64
	queue     chan *Job

	// hwBroken records hardware encoders that failed at runtime. detectEncoders can only
	// tell us an encoder is COMPILED IN and its device node exists — not that it actually
	// works. Quick Sync in particular can be present and still fail to initialise, and
	// without this every single job pays for another doomed attempt before falling back.
	hwBrokenMu sync.Mutex
	hwBroken   map[string]bool

	// pending maps an item key (see failures.go) to its live job so the same file is never
	// queued twice. The sweep re-runs ConvertAll over a queue that can take days to drain,
	// which without this re-queues everything — including files currently encoding, letting
	// two workers write the same output. Guarded by mu.
	pending map[string]*Job

	logMu  sync.Mutex
	logBuf []LogLine // recent human-readable convert events, for the UI console
	logs   *logStore // durable mirror of logBuf so history survives a restart
}

// LogLine is one entry in the Convert activity console.
type LogLine struct {
	At    int64  `json:"at"`    // unix seconds
	Level string `json:"level"` // "info" | "warn" | "error"
	Msg   string `json:"msg"`
}

// event appends a human-readable line to the convert console (kept to the last maxLogLines,
// persisted to the DB so history survives a restart) and mirrors it to the structured log.
func (s *Service) event(level, msg string) {
	ln := LogLine{At: time.Now().Unix(), Level: level, Msg: msg}
	s.logMu.Lock()
	s.logBuf = append(s.logBuf, ln)
	if len(s.logBuf) > maxLogLines {
		s.logBuf = s.logBuf[len(s.logBuf)-maxLogLines:]
	}
	s.logMu.Unlock()
	s.logs.append(context.Background(), ln)
	switch level {
	case "error", "warn":
		s.log.Warn("convert: " + msg)
	default:
		s.log.Info("convert: " + msg)
	}
}

// Logs returns the recent convert console lines (oldest first).
func (s *Service) Logs() []LogLine {
	s.logMu.Lock()
	defer s.logMu.Unlock()
	out := make([]LogLine, len(s.logBuf))
	copy(out, s.logBuf)
	return out
}

// NewService wires the module, detecting the best available HEVC encoder up front.
func NewService(db *sql.DB, mv *movies.Service, sr *series.Service, set *settings.Service, ffmpeg, ffprobe, scratchDir, recycleDir string, log *slog.Logger) *Service {
	_ = os.MkdirAll(scratchDir, 0o755)
	encs := detectEncoders(context.Background(), ffmpeg)
	s := &Service{
		movies: mv, series: sr, settings: set, log: log,
		ffmpeg: ffmpeg, ffprobe: ffprobe, scratchDir: scratchDir, recycleDir: recycleDir,
		encoders: encs, encoder: bestHEVC(encs), failures: &failureStore{db: db},
		cache: &probeCache{db: db}, logs: &logStore{db: db}, index: &libraryIndex{db: db},
		skips:   &skipStore{db: db},
		queue:   make(chan *Job, maxQueued),
		pending: map[string]*Job{},
	}
	s.logBuf = s.logs.recent(context.Background(), maxLogLines) // restore the console after a restart
	s.doviTool, _ = exec.LookPath("dovi_tool")
	s.hdr10plusTool, _ = exec.LookPath("hdr10plus_tool")
	if s.noNumaPools = numaPoolsBlocked(context.Background(), ffmpeg); s.noNumaPools {
		log.Warn("convert: x265 NUMA thread pools are blocked by the container's seccomp policy — " +
			"encoding unpooled. Add cap_add: [SYS_NICE] to the container for full threading.")
	}
	log.Info("convert: encoder selected", "encoder", s.encoder.Label, "name", s.encoder.Name,
		"dolby_vision", s.doviTool != "", "hdr10plus", s.hdr10plusTool != "")
	return s
}

// Run starts the worker pool draining the queue until ctx is cancelled (start it in a
// goroutine). The worker count comes from settings (default 1); process() is concurrency-safe,
// with per-job scratch files and mutex-guarded shared state.
func (s *Service) Run(ctx context.Context) {
	// A restart abandons any in-flight encode (jobs aren't persisted; the original
	// file is only replaced at the very end, so nothing is lost). Sweep the scratch
	// dir of the partial files those jobs left behind so /transcode doesn't fill up.
	s.cleanScratch(ctx)
	n := s.workerCount(ctx)
	s.log.Info("convert: worker pool started", "workers", n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case job := <-s.queue:
					// Queueing isn't converting: the window is "quiet hours for
					// encoding", so a job queued outside it waits here rather than
					// starting immediately. Without this, "Convert all" would kick
					// off thousands of encodes the moment it's clicked.
					if !s.waitForWindow(ctx, job) {
						return
					}
					s.runJob(ctx, job)
				}
			}
		}()
	}
	wg.Wait()
}

// cleanScratch removes leftover per-job scratch files (partial encodes, samples,
// HDR sidecars) from the working dir at startup — the debris of any convert cut
// short by a restart. Only runs before workers start, so nothing live is touched.
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
			if strings.HasPrefix(n, "convert-") || strings.HasPrefix(n, "sample-") ||
				strings.HasPrefix(n, "h10p-") || strings.HasPrefix(n, "dv-") {
				if os.Remove(filepath.Join(dir, n)) == nil {
					removed++
				}
			}
		}
		if removed > 0 {
			s.log.Info("convert: cleaned orphaned scratch files", "dir", dir, "count", removed)
		}
	}
}

// workerCount reads the configured concurrency (clamped to a sane range).
func (s *Service) workerCount(ctx context.Context) int {
	n, _ := strconv.Atoi(s.settings.Get(ctx, keyWorkers, "1"))
	if n < 1 {
		n = 1
	}
	if n > 8 {
		n = 8
	}
	return n
}

// Hardware reports detected encoders + the selected one.
func (s *Service) Hardware() (encoders []Encoder, selected Encoder) {
	return s.encoders, s.encoder
}

// Devices reports the available render nodes plus the one currently selected for
// VAAPI — so the UI can let the user pick the discrete card over the iGPU.
func (s *Service) Devices(ctx context.Context) (devices []RenderDevice, selected string) {
	return renderDevices(), s.vaapiDev(ctx)
}

// vaapiDev resolves which render node VAAPI encodes on (the Convert → VAAPI device
// setting), falling back to the default node.
func (s *Service) vaapiDev(ctx context.Context) string {
	if d := strings.TrimSpace(s.settings.Get(ctx, keyVaapiDevice, "")); d != "" {
		return d
	}
	return vaapiDevice
}

// activeScratch resolves the transcode working directory: the configured override
// (Convert → Transcode directory) when set, otherwise the startup default. This
// is where the heavy encode happens before the finished file is moved into the
// library, so it should live on fast storage (an SSD/NVMe pool), never the array.
// The directory is created if missing; a bad override falls back to the default.
func (s *Service) activeScratch(ctx context.Context) string {
	dir := strings.TrimSpace(s.settings.Get(ctx, keyScratchDir, ""))
	if dir == "" {
		dir = s.scratchDir
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		s.log.Warn("convert: transcode dir unusable, falling back to default", "dir", dir, "err", err)
		_ = os.MkdirAll(s.scratchDir, 0o755)
		return s.scratchDir
	}
	return dir
}

// ScratchInfo reports the resolved transcode directory and its free space (for
// the Convert UI).
func (s *Service) ScratchInfo(ctx context.Context) (dir string, freeBytesN int64) {
	dir = s.activeScratch(ctx)
	return dir, int64(freeBytes(dir))
}

// encoderFor picks the encoder for a plan's target codec (hardware if available, else CPU).
func (s *Service) encoderFor(codec string) Encoder { return encoderFor(codec, s.encoders) }

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

// Candidate is a library file plus its probed spec and whether "Save space" would act on it.
type Candidate struct {
	Kind      string     `json:"kind"` // "movie" | "episode"
	MovieID   int64      `json:"movie_id,omitempty"`
	SeriesID  int64      `json:"series_id,omitempty"`
	Season    int        `json:"season"` // NOT omitempty — season 0 is specials
	Episode   int        `json:"episode,omitempty"`
	Title     string     `json:"title"`
	Year      int        `json:"year,omitempty"`
	PosterURL string     `json:"poster_url,omitempty"`
	Path      string     `json:"path"`
	Info      *MediaInfo `json:"info,omitempty"`
	Candidate bool       `json:"candidate"` // would Save space convert it
	EstBytes  int64      `json:"est_bytes"` // rough estimate of converted size
}

// Library returns every downloaded movie with its spec + convert candidacy.
//
// Reads the persisted index (migration 0058) rather than walking the library and probing
// per request, so this is one query with no filesystem access.
func (s *Service) Library(ctx context.Context) ([]Candidate, error) {
	return s.indexedCandidates(ctx, "movie", 0)
}

// LibraryConvertible returns only the files that still need conversion. The movies tab
// otherwise returns every movie with its full MediaInfo — the same unbounded-payload problem
// the TV roll-up solved — so this lets the UI ask for just the actionable rows.
func (s *Service) LibraryConvertible(ctx context.Context, mediaType string, seriesID int64) ([]Candidate, error) {
	all, err := s.indexedCandidates(ctx, mediaType, seriesID)
	if err != nil {
		return nil, err
	}
	out := make([]Candidate, 0, len(all))
	for _, c := range all {
		if c.Candidate {
			out = append(out, c)
		}
	}
	return out, nil
}

// LibraryTV returns every downloaded TV episode with its spec + convert candidacy,
// reading the same persisted index as Library. Pass seriesID > 0 to scope to one show —
// that's what keeps the TV tab responsive at library scale.
func (s *Service) LibraryTV(ctx context.Context, seriesID int64) ([]Candidate, error) {
	return s.indexedCandidates(ctx, "episode", seriesID)
}

// QueueMovie enqueues a Save-space conversion of a movie (using the default plan built from
// the global settings) and returns the created job.
func (s *Service) QueueMovie(ctx context.Context, movieID int64) (*Job, error) {
	return s.queueMovie(ctx, movieID, s.defaultPlan(ctx))
}

// QueueEpisode enqueues a Save-space conversion of one TV episode.
func (s *Service) QueueEpisode(ctx context.Context, seriesID int64, season, episode int) (*Job, error) {
	return s.queueEpisode(ctx, seriesID, season, episode, s.defaultPlan(ctx))
}

func (s *Service) queueEpisode(ctx context.Context, seriesID int64, season, episode int, plan Plan) (*Job, error) {
	if s.series == nil {
		return nil, fmt.Errorf("series module not available")
	}
	path, _ := s.series.EpisodeFilePath(ctx, seriesID, season, episode)
	if path == "" {
		return nil, fmt.Errorf("episode has no file to convert")
	}
	title := fmt.Sprintf("S%02dE%02d", season, episode)
	if sm, err := s.series.Get(ctx, seriesID); err == nil {
		title = fmt.Sprintf("%s - S%02dE%02d", sm.Title, season, episode)
	}
	return s.enqueueEpisode(ctx, seriesID, season, episode, title, plan)
}

// enqueueEpisodeIndexed queues an episode using facts already read from the library index.
// Bulk callers ("Convert all", per-series/season) go through here: the lookup version costs a
// series.Get AND an EpisodeFilePath per episode, which at thousands of episodes is thousands
// of redundant queries — each series.Get loads every season and episode of that show.
func (s *Service) enqueueEpisodeIndexed(ctx context.Context, c Candidate, plan Plan) (*Job, error) {
	if c.Path == "" {
		return nil, fmt.Errorf("episode has no file to convert")
	}
	return s.enqueueEpisode(ctx, c.SeriesID, c.Season, c.Episode, c.Title, plan)
}

func (s *Service) enqueueEpisode(ctx context.Context, seriesID int64, season, episode int, title string, plan Plan) (*Job, error) {
	key := episodeKey(seriesID, season, episode)
	if existing, ok := s.reservePending(key); !ok {
		return existing, ErrAlreadyQueued
	}
	s.mu.Lock()
	s.nextID++
	job := &Job{ID: s.nextID, Kind: "episode", SeriesID: seriesID, Season: season, Episode: episode,
		Title: title, State: StateQueued, Encoder: s.encoder.Label, plan: plan}
	s.pending[key] = job
	s.jobs = append([]*Job{job}, s.jobs...)
	s.trimJobsLocked()
	s.mu.Unlock()
	s.event("info", "Queued "+job.Title)
	if err := s.enqueue(ctx, job); err != nil {
		return nil, err
	}
	return job, nil
}

// defaultPlan is the single conversion plan derived from the global settings: transcode to the
// chosen codec at maximum-retention quality, keep audio untouched, subtitles carried through
// (the Subtitles module handles them), MKV container.
func (s *Service) defaultPlan(ctx context.Context) Plan {
	codec := s.targetCodec(ctx)
	return Plan{
		VideoCodec: codec,
		Quality:    maxQualityCRF(codec),
		VFRToCFR:   true,
		Container:  "mkv",
		// These three were readable and writable through the settings API but never read
		// here, so setting them did nothing at all. The AudioPlan machinery they feed
		// (compileOutputArgs, estimatePlanSize) has always been implemented.
		Audio: AudioPlan{
			KeepLangs: splitCSV(s.settings.Get(ctx, keyKeepAudioLangs, "")),
			AddStereo: s.settings.GetBool(ctx, keyAddStereo, false),
			Loudnorm:  s.settings.GetBool(ctx, keyLoudnorm, false),
		},
	}
}

// splitCSV parses a comma-separated setting into trimmed, non-empty values.
func splitCSV(v string) []string {
	var out []string
	for _, p := range strings.Split(v, ",") {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// recodesModern reports whether files already in a modern codec (HEVC, AV1) should be
// re-encoded into the target anyway. Off by default — see isCandidateCodec for why.
func (s *Service) recodesModern(ctx context.Context) bool {
	return s.settings.GetBool(ctx, keyRecodeModern, false)
}

// targetCodec is the codec the library is being converted to (hevc | av1), default HEVC.
func (s *Service) targetCodec(ctx context.Context) string {
	if s.settings.Get(ctx, keyTargetCodec, "hevc") == "av1" {
		return "av1"
	}
	return "hevc"
}

// maxQualityCRF is the quality-preserving CRF for a codec — deliberately higher-quality than the
// space-first default; the SSIM quality gate (on by default) catches anything that still drops.
func maxQualityCRF(codec string) int {
	if codec == "av1" {
		return 24
	}
	return 20
}

// isCandidateFor reports whether a file should be converted for the chosen target codec — i.e.
// it's a real video stream that isn't already that codec.
func isCandidateFor(mi *MediaInfo, target string) bool {
	return isCandidateCodec(mi.VideoCodec, target, false)
}

// modernCodec reports whether a codec is already an efficient modern one. These are the
// files there's little to gain from re-encoding — they're the DESTINATION of a conversion,
// not a wasteful source in need of one.
func modernCodec(c string) bool {
	switch codecClass(c) {
	case "hevc", "av1":
		return true
	}
	return false
}

// isCandidateCodec decides whether a file's codec makes it worth converting to target.
//
// The point of the module is taking wasteful old encodes — H.264, MPEG-2, VC-1 — and
// bringing them to a modern codec. Re-encoding something already modern is a different
// proposition entirely:
//
//   - HEVC → AV1 is a SECOND lossy generation for maybe 20-30% more space, compounding the
//     artifacts of an encode that's already efficient.
//   - AV1 → HEVC is worse still: a second generation that also produces a BIGGER file,
//     since AV1 is the more efficient codec. There's no space to save, only quality to
//     lose. The only reason to want it is device compatibility.
//
// So neither happens by default. recodeModern is the opt-in for both.
func isCandidateCodec(codec, target string, recodeModern bool) bool {
	if codec == "" || strings.EqualFold(codec, target) {
		return false
	}
	if modernCodec(codec) && !recodeModern {
		return false
	}
	return true
}

func (s *Service) queueMovie(ctx context.Context, movieID int64, plan Plan) (*Job, error) {
	m, err := s.movies.Get(ctx, movieID)
	if err != nil {
		return nil, err
	}
	if !m.HasFile || m.MovieFilePath == "" {
		return nil, fmt.Errorf("movie has no file to convert")
	}
	key := movieKey(movieID)
	if existing, ok := s.reservePending(key); !ok {
		return existing, ErrAlreadyQueued
	}
	s.mu.Lock()
	s.nextID++
	job := &Job{ID: s.nextID, MovieID: movieID, Title: m.Title, State: StateQueued, Encoder: s.encoder.Label, plan: plan}
	s.pending[key] = job
	s.jobs = append([]*Job{job}, s.jobs...) // newest first
	s.trimJobsLocked()
	s.mu.Unlock()
	s.event("info", "Queued "+job.Title)
	if err := s.enqueue(ctx, job); err != nil {
		return nil, err
	}
	return job, nil
}

// finishSkip ends a job as skipped and records the reason durably. The in-memory job list
// holds only the last 200 entries and is lost on restart, so without this a skip was
// effectively invisible: the file stayed a candidate, kept inflating the reclaimable
// figure, and was re-probed by every sweep with nothing to show for it.
func (s *Service) finishSkip(job *Job, kind, note string) {
	s.skips.record(context.Background(), jobKey(job), kind, note)
	s.finish(job, StateSkipped, note)
}

// finishAfterEncode skips a job that already burned a full encode (or three, via the quality
// gate) and would do so again on every sweep: the outcome is deterministic for that file, and
// it stays a candidate because its codec never changed. Counting it toward the blocklist is
// what stops the library re-transcoding the same unshrinkable file forever.
func (s *Service) finishAfterEncode(job *Job, kind, note string) {
	s.failures.recordFailure(context.Background(), jobKey(job), note)
	s.finishSkip(job, kind, note)
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

// pendingState reports whether a job still has work left to do (so it must not be trimmed).
func pendingState(st JobState) bool {
	switch st {
	case StateQueued, StateEncoding, StateVerifying, StateReplacing:
		return true
	}
	return false
}

// trimJobsLocked drops the oldest FINISHED jobs once history grows past the cap, leaving
// every queued/running job in place. Callers must hold s.mu.
func (s *Service) trimJobsLocked() {
	finished := 0
	for _, j := range s.jobs {
		if !pendingState(j.State) {
			finished++
		}
	}
	if finished <= maxJobHistory {
		return
	}
	drop := finished - maxJobHistory
	kept := make([]*Job, 0, len(s.jobs)-drop)
	for i := len(s.jobs) - 1; i >= 0; i-- { // oldest first
		if drop > 0 && !pendingState(s.jobs[i].State) {
			drop--
			continue
		}
		kept = append(kept, s.jobs[i])
	}
	// kept is oldest-first; restore newest-first ordering.
	for i, j := 0, len(kept)-1; i < j; i, j = i+1, j-1 {
		kept[i], kept[j] = kept[j], kept[i]
	}
	s.jobs = kept
}

// reservePending claims an item key for a new job, or reports the job already working on it.
// Callers must NOT hold mu.
func (s *Service) reservePending(key string) (*Job, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if j, ok := s.pending[key]; ok {
		return j, false
	}
	return nil, true
}

// releasePending drops a finished job's claim so the file can be queued again later.
func (s *Service) releasePending(key string, job *Job) {
	s.mu.Lock()
	if s.pending[key] == job {
		delete(s.pending, key)
	}
	s.mu.Unlock()
}

// ErrAlreadyQueued means this exact file already has a job waiting or running.
var ErrAlreadyQueued = errors.New("this file is already queued")

// Cancel stops a job: a queued one never starts, a running one has its encode killed. The
// original file is only ever replaced at the very end of a job, so cancelling mid-encode
// leaves the library untouched.
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
	if !pendingState(job.State) {
		s.mu.Unlock()
		return fmt.Errorf("job already finished")
	}
	job.cancelled = true
	cancel := job.cancel
	s.mu.Unlock()

	if cancel != nil {
		cancel() // kills ffmpeg; process() unwinds and marks the job cancelled
		return nil
	}
	// Still sitting in the queue — mark it now so the UI updates immediately; the worker
	// will drop it when it reaches the front.
	s.finish(job, StateCancelled, "cancelled")
	return nil
}

// CancelQueued cancels everything not yet started, leaving in-flight encodes alone. This is
// the escape hatch for "I queued my whole library with the wrong settings".
func (s *Service) CancelQueued() int {
	s.mu.Lock()
	var targets []*Job
	for _, j := range s.jobs {
		if j.State == StateQueued {
			j.cancelled = true
			targets = append(targets, j)
		}
	}
	s.mu.Unlock()
	for _, j := range targets {
		s.finish(j, StateCancelled, "cancelled")
	}
	if len(targets) > 0 {
		s.event("info", fmt.Sprintf("Cancelled %d queued job%s", len(targets), plural(len(targets))))
	}
	return len(targets)
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// Blocklist returns the files automation is currently skipping because they kept failing,
// with titles resolved. Without this the user has no way to discover that a handful of files
// silently never convert.
func (s *Service) Blocklist(ctx context.Context) ([]Blocked, error) {
	list, err := s.failures.list(ctx)
	if err != nil {
		return nil, err
	}
	for i := range list {
		b := &list[i]
		switch b.Kind {
		case "movie":
			if s.movies != nil {
				if m, err := s.movies.Get(ctx, b.MovieID); err == nil {
					b.Title = m.Title
				}
			}
		case "episode":
			if s.series != nil {
				if sm, err := s.series.Get(ctx, b.SeriesID); err == nil {
					b.Title = fmt.Sprintf("%s - S%02dE%02d", sm.Title, b.Season, b.Episode)
				}
			}
		}
		if b.Title == "" {
			b.Title = b.Key
		}
	}
	return list, nil
}

// Skips returns the files that couldn't be converted and why, with titles resolved.
// Without this the reasons lived only in a 200-entry in-memory list that vanished on
// restart, so "some of my library never converts" was undiscoverable.
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

// ClearSkip forgets an item's skip (or all of them) so it's retried next time.
func (s *Service) ClearSkip(ctx context.Context, key string) error {
	if key == "" {
		return s.skips.clearAll(ctx)
	}
	s.skips.clear(ctx, key)
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

// ClearBlocklist forgets an item's failures (or all of them when key is empty) so it will be
// retried by the next convert.
func (s *Service) ClearBlocklist(ctx context.Context, key string) error {
	if key == "" {
		_, err := s.failures.db.ExecContext(ctx, `DELETE FROM convert_failures`)
		return err
	}
	s.failures.clearFailures(ctx, key)
	return nil
}

// markHardwareBroken records that a hardware encoder failed, so it stops being preferred.
func (s *Service) markHardwareBroken(name, reason string) {
	s.hwBrokenMu.Lock()
	if s.hwBroken == nil {
		s.hwBroken = map[string]bool{}
	}
	first := !s.hwBroken[name]
	s.hwBroken[name] = true
	s.hwBrokenMu.Unlock()
	if first {
		s.log.Warn("convert: hardware encoder failed — using the CPU for the rest of this run",
			"encoder", name, "err", reason)
		s.event("warn", name+" failed on this machine — converting on the CPU instead")
	}
}

func (s *Service) hardwareIsBroken(name string) bool {
	s.hwBrokenMu.Lock()
	defer s.hwBrokenMu.Unlock()
	return s.hwBroken[name]
}

// enqueue hands a job to the workers without blocking forever if the queue is saturated.
func (s *Service) enqueue(ctx context.Context, job *Job) error {
	select {
	case s.queue <- job:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	default:
		s.finishSkip(job, SkipQueueFull, "convert queue is full — try again once it drains")
		return fmt.Errorf("convert queue is full")
	}
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

// --- Rules (C2) ---

func (s *Service) maxFailures(ctx context.Context) int {
	n, _ := strconv.Atoi(s.settings.Get(ctx, keyMaxFailures, "3"))
	if n < 1 {
		n = 3
	}
	return n
}

// SampleResult is the outcome of a 30-second sample encode — a content-exact size estimate.
type SampleResult struct {
	MovieID   int64  `json:"movie_id"`
	Title     string `json:"title"`
	SrcBytes  int64  `json:"src_bytes"`
	EstBytes  int64  `json:"est_bytes"` // extrapolated full-file size
	Percent   int    `json:"percent"`   // reduction %
	SampleSec int    `json:"sample_sec"`
}

// SampleMovie samples one movie with the default plan.
func (s *Service) SampleMovie(ctx context.Context, movieID int64) (SampleResult, error) {
	return s.sample(ctx, movieID, s.defaultPlan(ctx))
}

func (s *Service) sample(ctx context.Context, movieID int64, plan Plan) (SampleResult, error) {
	m, err := s.movies.Get(ctx, movieID)
	if err != nil {
		return SampleResult{}, err
	}
	if !m.HasFile || m.MovieFilePath == "" {
		return SampleResult{}, fmt.Errorf("movie has no file")
	}
	src := m.MovieFilePath
	mi, err := probe(ctx, s.ffprobe, src)
	if err != nil {
		return SampleResult{}, err
	}
	dur := mi.DurationSec
	if dur <= 0 {
		dur = 1
	}
	ext := ".mkv"
	if plan.Container == "mp4" {
		ext = ".mp4"
	}

	// Sample several short slices spread across the film so the estimate averages out
	// scene complexity (a single slice can land on an unrepresentatively easy/busy scene).
	// Short clips just get one full-length pass.
	type slice struct{ start, length float64 }
	var slices []slice
	if dur > 120 {
		for _, frac := range []float64{0.20, 0.50, 0.80} {
			slices = append(slices, slice{start: dur * frac, length: 12})
		}
	} else {
		start, length := 0.0, 30.0
		if dur > 90 {
			start = dur * 0.35
		}
		if dur-start < length {
			length = dur - start
		}
		if length <= 0 {
			start, length = 0, dur
		}
		slices = append(slices, slice{start: start, length: length})
	}

	encodeSlice := func(enc Encoder, sl slice, dst string) error {
		args := []string{"-y", "-hide_banner", "-ss", fmt.Sprintf("%.2f", sl.start), "-t", fmt.Sprintf("%.2f", sl.length)}
		args = append(args, globalArgs(enc, false, s.vaapiDev(ctx))...) // sample = software decode (short, keep it simple/robust)
		args = append(args, "-i", src)
		args = append(args, compileOutputArgs(enc, mi, plan, false, s.cpuCores(ctx), s.noNumaPools)...)
		args = append(args, dst)
		cmd := exec.CommandContext(ctx, s.ffmpeg, args...)
		lowPriority(cmd)
		if err := cmd.Start(); err != nil {
			return err
		}
		applyNice(cmd.Process.Pid, encodeNice)
		return cmd.Wait()
	}

	enc := s.encoderFor(plan.VideoCodec)
	scratch := s.activeScratch(ctx)
	var sampledBytes int64
	var sampledSec float64
	for i, sl := range slices {
		dst := filepath.Join(scratch, fmt.Sprintf("sample-%d-%d%s", movieID, i, ext))
		if err := encodeSlice(enc, sl, dst); err != nil {
			if enc.Hardware { // retry this slice on CPU
				err = encodeSlice(cpuEncoder(plan.VideoCodec), sl, dst)
			}
			if err != nil {
				os.Remove(dst)
				return SampleResult{}, fmt.Errorf("sample encode failed: %w", err)
			}
		}
		sampledBytes += fileSize(dst)
		sampledSec += sl.length
		os.Remove(dst)
	}
	if sampledSec <= 0 {
		return SampleResult{}, fmt.Errorf("sample produced no output")
	}
	estFull := int64(float64(sampledBytes) * dur / sampledSec)
	percent := 0
	if mi.SizeBytes > 0 {
		percent = int((1 - float64(estFull)/float64(mi.SizeBytes)) * 100)
	}
	return SampleResult{MovieID: movieID, Title: m.Title, SrcBytes: mi.SizeBytes, EstBytes: estFull, Percent: percent, SampleSec: int(sampledSec)}, nil
}

// Sweep is the scheduled auto-conversion: if auto-convert is on and we're inside the schedule
// window, queue every file that isn't already the target codec. (No rules — one focused job.)
func (s *Service) Sweep(ctx context.Context) {
	if !s.settings.GetBool(ctx, keyAuto, false) {
		return
	}
	if !s.inSweepWindow(ctx) {
		s.log.Info("convert: outside the schedule window, skipping")
		return
	}
	if r, _ := s.ConvertAll(ctx); r.Queued > 0 {
		s.log.Info("convert: scheduled conversion queued",
			"queued", r.Queued, "movies", r.Movies, "episodes", r.Episodes, "blocklisted", r.Blocklisted)
	}
}

// ConvertAll queues every library file (movies + TV episodes) that isn't already the target
// codec, skipping anything that has failed too many times. The manual "Convert all" button and
// the schedule both use it. Queueing is not converting: the workers still hold each job until
// the encode window opens.
// ConvertAllResult breaks down what a "Convert all" actually did. The button used to show a
// movies-only count while this queued TV as well, so the number on screen was far lower than
// the work it started.
type ConvertAllResult struct {
	Movies      int `json:"movies"`
	Episodes    int `json:"episodes"`
	Queued      int `json:"queued"`
	Blocklisted int `json:"blocklisted"` // skipped: failed too many times already
}

func (s *Service) ConvertAll(ctx context.Context) (ConvertAllResult, error) {
	plan := s.defaultPlan(ctx)
	maxFail := s.maxFailures(ctx)
	var res ConvertAllResult

	cands, err := s.Library(ctx)
	if err != nil {
		return res, err
	}
	for _, c := range cands {
		if !c.Candidate {
			continue
		}
		if s.failures.blocklisted(ctx, movieKey(c.MovieID), maxFail) {
			res.Blocklisted++
			continue
		}
		if _, err := s.queueMovie(ctx, c.MovieID, plan); err == nil {
			res.Movies++
		}
	}

	tv, err := s.LibraryTV(ctx, 0)
	if err == nil {
		for _, c := range tv {
			if !c.Candidate {
				continue
			}
			if s.failures.blocklisted(ctx, episodeKey(c.SeriesID, c.Season, c.Episode), maxFail) {
				res.Blocklisted++
				continue
			}
			if _, err := s.enqueueEpisodeIndexed(ctx, c, plan); err == nil {
				res.Episodes++
			}
		}
	}
	res.Queued = res.Movies + res.Episodes
	return res, nil
}

// waitForWindow blocks until the encode window opens, parking the job in the queue with a
// note so the UI can explain why nothing is running. Returns false if ctx was cancelled.
func (s *Service) waitForWindow(ctx context.Context, job *Job) bool {
	if s.inSweepWindow(ctx) {
		return true
	}
	start := s.settings.Get(ctx, keySweepStart, "")
	end := s.settings.Get(ctx, keySweepEnd, "")
	s.update(job, func(j *Job) { j.Note = fmt.Sprintf("waiting for the %s–%s window", start, end) })
	s.log.Info("convert: job waiting for the encode window",
		"title", job.Title, "window", start+"-"+end)
	for {
		select {
		case <-ctx.Done():
			return false
		case <-time.After(time.Minute):
			if s.inSweepWindow(ctx) {
				s.update(job, func(j *Job) { j.Note = "" })
				return true
			}
		}
	}
}

// inSweepWindow reports whether now falls inside the global auto-sweep window (a master
// quiet-hours gate in Settings). An unset window means "always". Per-rule windows narrow it
// further (see Sweep).
func (s *Service) inSweepWindow(ctx context.Context) bool {
	return windowAllows(s.settings.Get(ctx, keySweepStart, ""), s.settings.Get(ctx, keySweepEnd, ""))
}

// windowAllows reports whether the current time falls within an "HH:MM"–"HH:MM" window. An empty
// or unparseable window means "always". Windows may wrap past midnight (e.g. 22:00–06:00).
func windowAllows(start, end string) bool {
	if start == "" || end == "" {
		return true
	}
	s0, ok1 := parseHM(start)
	e0, ok2 := parseHM(end)
	if !ok1 || !ok2 || s0 == e0 {
		return true
	}
	now := time.Now()
	cur := now.Hour()*60 + now.Minute()
	if s0 < e0 {
		return cur >= s0 && cur < e0
	}
	return cur >= s0 || cur < e0 // wraps past midnight
}

// parseHM parses "HH:MM" into minutes-since-midnight.
func parseHM(s string) (int, bool) {
	p := strings.SplitN(s, ":", 2)
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

func (s *Service) update(job *Job, fn func(*Job)) {
	s.mu.Lock()
	fn(job)
	s.mu.Unlock()
}

// runJob wraps process() with per-job cancellation, and drops a job that was cancelled while
// it sat in the queue.
func (s *Service) runJob(ctx context.Context, job *Job) {
	jobCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	s.mu.Lock()
	if job.cancelled {
		s.mu.Unlock()
		if pendingState(job.State) {
			s.finish(job, StateCancelled, "cancelled")
		}
		return
	}
	job.cancel = cancel
	s.mu.Unlock()

	s.process(jobCtx, job)

	s.mu.Lock()
	job.cancel = nil
	wasCancelled := job.cancelled
	state := job.State
	s.mu.Unlock()
	// process() reports a killed ffmpeg as a failure; relabel it so the user sees their own
	// action rather than an error they didn't cause.
	if wasCancelled && state == StateFailed {
		s.update(job, func(j *Job) { j.State = StateCancelled; j.Note = "cancelled" })
	}
}

func (s *Service) process(ctx context.Context, job *Job) {
	src, title, ok := s.resolveSource(ctx, job)
	if !ok {
		s.finish(job, StateFailed, "source file is gone")
		return
	}
	mi, err := probe(ctx, s.ffprobe, src)
	if err != nil {
		s.finish(job, StateFailed, "could not analyze file: "+err.Error())
		return
	}
	s.update(job, func(j *Job) { j.SrcBytes = mi.SizeBytes; j.DurationSec = mi.DurationSec })

	plan := job.plan // the compiled plan this job runs (a rule's, or the save-space default)

	// Health check is a read-only corruption scan (no transcode) — report and return (R5).
	if plan.HealthCheck && plan.VideoCodec == "" {
		s.runHealthCheck(ctx, job, src, mi)
		return
	}
	if wouldBeNoOp(mi, plan) {
		s.finishSkip(job, SkipAlreadyTarget, "already "+strings.ToUpper(mi.VideoCodec)+" — nothing to do")
		return
	}
	// Encoder routing — ordered, first match wins. See CONVERT-QUALITY-PLAN.md.
	enc := s.pickEncoder(ctx, job, mi, plan)

	// Whatever still can't be preserved after routing (Dolby Vision or HDR10+ with an AV1
	// target, or a missing dovi_tool / hdr10plus_tool) is skipped rather than silently
	// flattened. The format cards in Settings warn about this before the user commits.
	if isHDR(mi.HDR) && !s.canPreserveHDR(mi, plan, enc) {
		s.finishSkip(job, SkipHDRUnsupported, skipReasonHDR(mi.HDR, plan.VideoCodec))
		return
	}

	// Seeding-safety: skip hardlinked files by default so we don't duplicate a seeding copy.
	if s.settings.GetBool(ctx, keySkipHardlinked, true) && fileLinks(src) > 1 {
		s.finishSkip(job, SkipHardlinked, "file is hardlinked (likely still seeding) — skipped")
		return
	}
	// The transcode working dir (fast/SSD when configured) — heavy encode I/O lands here.
	scratch := s.activeScratch(ctx)
	// Disk-space guard: need room for a worst-case same-size output on the scratch volume.
	if free := freeBytes(scratch); free > 0 && int64(free) < mi.SizeBytes+(256<<20) {
		s.finish(job, StateFailed, "not enough scratch space to convert safely")
		return
	}

	// HDR10+ dynamic metadata: any HDR10 file may carry it (ffprobe won't reliably say so), and
	// the bundled x265 can't re-embed it, so extract it up front — a successful extract means the
	// file has HDR10+ and we route to the inject pipeline. Absence is normal and silent.
	h10pJSON := ""
	if mi.HDR != "Dolby Vision" && (mi.HDR == "HDR10" || mi.HDR == "HDR10+") && s.hdr10plusTool != "" && plan.VideoCodec == "hevc" {
		jf := filepath.Join(scratch, fmt.Sprintf("h10p-%d.json", job.ID))
		err := s.extractHDR10Plus(ctx, src, jf)
		switch {
		case err == nil:
			h10pJSON = jf
			defer os.Remove(jf)
		case mi.HDR == "HDR10+":
			// This file is KNOWN to carry HDR10+. Continuing would encode it with static
			// HDR10 only and quietly lose the dynamic metadata, so stop instead.
			_ = os.Remove(jf)
			s.finish(job, StateSkipped,
				"HDR10+ metadata could not be extracted — kept the original rather than re-encoding without it")
			return
		default:
			// Labelled plain HDR10: a failed extract just means it has no HDR10+. Normal.
			_ = os.Remove(jf)
		}
	}

	// Output extension follows the plan's container, except the Dolby Vision and HDR10+ inject
	// pipelines always produce MKV (DV in MP4 needs finicky dvh1 tagging; both re-mux a raw ES).
	ext := ".mkv"
	if plan.Container == "mp4" && mi.HDR != "Dolby Vision" && h10pJSON == "" {
		ext = ".mp4"
	}
	dst := filepath.Join(scratch, fmt.Sprintf("convert-%d%s", job.ID, ext))
	defer os.Remove(dst)

	// Encode, then (if the quality gate is on for a real transcode) score the result and
	// re-encode at a higher quality until it passes or we run out of attempts.
	// Default true, matching the settings API (httpapi/settings.go) — they disagreed, so a
	// fresh install showed the gate as ON in the UI while encoding with it OFF.
	gate := plan.VideoCodec != "" && s.settings.GetBool(ctx, keyQualityGate, true)
	minSSIM := parseFloatDefault(s.settings.Get(ctx, keyMinSSIM, defaultMinSSIM), defaultMinSSIMValue)
	s.update(job, func(j *Job) { j.State = StateEncoding; j.Encoder = enc.Label })
	s.event("info", fmt.Sprintf("%s — source: %s · %s", title, mediaSpec(mi), humanBytes(mi.SizeBytes)))
	s.event("info", fmt.Sprintf("Encoding %s → %s on %s (%s source)", title, strings.ToUpper(plan.VideoCodec), enc.Label, humanBytes(mi.SizeBytes)))
	for attempt := 0; ; attempt++ {
		if err := s.runEncode(ctx, job, src, dst, mi, enc, plan, h10pJSON); err != nil {
			s.finish(job, StateFailed, "encode failed: "+err.Error())
			return
		}
		if !gate {
			break
		}
		s.update(job, func(j *Job) { j.State = StateVerifying })
		s.event("info", fmt.Sprintf("Verifying %s (SSIM vs source)…", title))
		// Cap the verify: SSIM decodes both files, which is slow on a long movie and must
		// never hang the whole queue.
		sctx, cancel := context.WithTimeout(ctx, 25*time.Minute)
		score, err := s.computeSSIM(sctx, dst, src)
		cancel()
		if err != nil {
			// FAIL CLOSED. This used to accept the encode and recycle the original when the
			// comparison couldn't be made — which is backwards for the one mechanism
			// enforcing "you won't see a difference". An unmeasurable result is not a pass,
			// and it happens most often on exactly the long, complex files most likely to
			// have encoded badly. Keep the original instead.
			s.log.Warn("convert: quality gate could not measure SSIM — keeping the original", "err", err)
			s.finishSkip(job, SkipQualityGate,
				"couldn't verify the encode matched the source — kept the original")
			return
		}
		s.update(job, func(j *Job) { j.SSIM = score })
		if score >= minSSIM {
			s.event("info", fmt.Sprintf("%s: quality gate passed (SSIM %.4f ≥ %.2f)", title, score, minSSIM))
			break
		}
		if attempt >= qualityRetries {
			s.finishAfterEncode(job, SkipQualityGate, fmt.Sprintf("quality gate failed — SSIM %.4f < %.4f after %d attempts; kept the original", score, minSSIM, attempt+1))
			return
		}
		plan.Quality = higherQuality(plan)
		s.event("warn", fmt.Sprintf("%s: SSIM %.4f < %.2f — re-encoding at higher quality (attempt %d)", title, score, minSSIM, attempt+2))
		s.update(job, func(j *Job) { j.State = StateEncoding; j.Progress = 0 })
	}

	s.finalizeOutput(ctx, job, src, dst, ext, mi, plan, title)
}

// resolveSource re-resolves a job's current source file path + display title,
// covering both movie and TV-episode jobs. ok is false when the file is gone.
func (s *Service) resolveSource(ctx context.Context, job *Job) (src, title string, ok bool) {
	if job.Kind == "episode" {
		if s.series == nil {
			return "", "", false
		}
		path, _ := s.series.EpisodeFilePath(ctx, job.SeriesID, job.Season, job.Episode)
		if path == "" {
			return "", "", false
		}
		return path, job.Title, true
	}
	m, err := s.movies.Get(ctx, job.MovieID)
	if err != nil || !m.HasFile || m.MovieFilePath == "" {
		return "", "", false
	}
	return m.MovieFilePath, m.Title, true
}

// markConverted records a finished conversion against the right library record
// (movie or episode), re-tagging its file path.
func (s *Service) markConverted(ctx context.Context, job *Job, src, finalPath, tag string) error {
	if job.Kind == "episode" {
		if s.series == nil {
			return fmt.Errorf("series module not available")
		}
		size := fileSize(finalPath)
		// One file can serve several episodes — a double-length "S03E01E02" is a single
		// file with two episode rows. Repoint by PATH so they all follow the conversion;
		// updating only this job's episode left the others pointing at the old path, which
		// no longer exists whenever the container changed.
		if src != "" {
			if n, err := s.series.RepointEpisodeFile(ctx, job.SeriesID, src, finalPath, size); err == nil && n > 0 {
				if n > 1 {
					s.log.Info("convert: file serves multiple episodes — all repointed",
						"title", job.Title, "episodes", n)
				}
				return nil
			}
		}
		return s.series.MarkEpisodeImported(ctx, job.SeriesID, job.Season, job.Episode, finalPath, size)
	}
	return s.movies.MarkImported(ctx, job.MovieID, finalPath, tag)
}

// runEncode dispatches to the right pipeline (standard, Dolby Vision, or HDR10+) and produces
// dst, with a one-time CPU fallback if a hardware encoder fails. Returns an error the caller
// turns into a job failure.
func (s *Service) runEncode(ctx context.Context, job *Job, src, dst string, mi *MediaInfo, enc Encoder, plan Plan, h10pJSON string) error {
	switch {
	case mi.HDR == "Dolby Vision":
		s.update(job, func(j *Job) { j.Encoder = "CPU (x265) + Dolby Vision" })
		return s.encodeDolbyVision(ctx, job, src, dst, mi, plan)
	case h10pJSON != "":
		s.update(job, func(j *Job) { j.Encoder = "CPU (x265) + HDR10+" })
		return s.encodeHDR10Plus(ctx, job, src, dst, mi, plan, h10pJSON)
	default:
		// VAAPI: first try a full-GPU pipeline (hardware decode → GPU encode) so the CPU
		// isn't stuck doing software decode. If the source can't be hardware-decoded (odd
		// codec / unsupported profile) fall back to software decode + GPU encode, then CPU.
		if enc.Kind == "vaapi" {
			if err := s.encode(ctx, job, src, dst, mi, enc, plan, true); err == nil {
				return nil
			} else {
				s.log.Warn("convert: hardware decode failed, retrying with software decode", "err", err)
				s.update(job, func(j *Job) { j.Progress = 0 })
			}
		}
		err := s.encode(ctx, job, src, dst, mi, enc, plan, false)
		if err != nil && enc.Hardware { // hardware encoder failed → fall back to CPU once
			cpu := cpuEncoder(plan.VideoCodec)
			s.markHardwareBroken(enc.Name, err.Error())
			s.update(job, func(j *Job) { j.Encoder = cpu.Label; j.Progress = 0 })
			err = s.encode(ctx, job, src, dst, mi, cpu, plan, false)
		}
		return err
	}
}

// finalizeOutput verifies a freshly-encoded file, then safely replaces the original: recycle the
// source, move the new file into place, re-tag the library record, and record the space saved.
// Shared by the standard and Dolby Vision encode paths.
func (s *Service) finalizeOutput(ctx context.Context, job *Job, src, dst, ext string, mi *MediaInfo, plan Plan, title string) {
	s.update(job, func(j *Job) { j.State = StateVerifying; j.Progress = 1 })
	outInfo, err := probe(ctx, s.ffprobe, dst)
	if err != nil || outInfo.VideoCodec == "" || outInfo.DurationSec < mi.DurationSec*0.90 {
		s.finish(job, StateFailed, "output failed verification — kept the original")
		return
	}
	outSize := fileSize(dst)
	if outSize <= 0 {
		s.finish(job, StateFailed, "output was empty — kept the original")
		return
	}
	// Save-space safety only applies to transcodes; a remux/container change is about the
	// container, not size, so reject it only if it grew unreasonably.
	if plan.VideoCodec != "" && outSize >= mi.SizeBytes {
		s.finishAfterEncode(job, SkipNotSmaller, "converted file wasn't smaller — kept the original")
		return
	}
	if plan.VideoCodec == "" && outSize > mi.SizeBytes*12/10 {
		s.finishSkip(job, SkipNotSmaller, "remuxed file was unexpectedly larger — kept the original")
		return
	}
	pct := 0
	if mi.SizeBytes > 0 {
		pct = int(100 - outSize*100/mi.SizeBytes)
	}
	s.event("info", fmt.Sprintf("%s — output: %s · %s (%d%% smaller)", title, mediaSpec(outInfo), humanBytes(outSize), pct))

	// Safe replace. Ordering matters enormously here: the original must remain on disk and
	// intact until the converted file is completely written to the destination volume.
	//
	// This used to recycle the original FIRST and then move the new file in. Those two steps
	// fail together — a full array breaks both — and the deferred scratch cleanup then removed
	// the encode, destroying the only copy. Worse, the move is a cross-volume COPY on every
	// job (scratch deliberately lives on separate fast storage), so an interrupted copy left a
	// truncated file at the real library path.
	//
	// Now: copy to a sibling .arrpart file, and only once that succeeds do we retire the
	// original and swap the part file in with a same-directory rename, which is atomic.
	s.update(job, func(j *Job) { j.State = StateReplacing })
	finalPath := strings.TrimSuffix(src, filepath.Ext(src)) + ext
	part := finalPath + ".arrpart"
	_ = os.Remove(part) // a leftover from an interrupted job
	if err := moveFile(dst, part); err != nil {
		_ = os.Remove(part)
		s.finish(job, StateFailed, "could not stage the converted file: "+err.Error()+" — kept the original")
		return
	}

	// The original is only retired once the replacement is safely on the same volume.
	if err := s.retire(src); err != nil {
		_ = os.Remove(part)
		s.finish(job, StateFailed, "could not move the original to the recycle bin: "+err.Error()+" — kept the original")
		return
	}
	if finalPath != src {
		if _, e := os.Stat(finalPath); e == nil {
			if err := s.retire(finalPath); err != nil {
				s.log.Warn("convert: could not retire the file being replaced", "path", finalPath, "err", err)
			}
		}
	}
	// Same-directory rename: atomic, so the library never sees a partial file.
	if err := os.Rename(part, finalPath); err != nil {
		s.log.Error("convert: converted file is staged but could not be swapped in",
			"part", part, "final", finalPath, "err", err)
		s.finish(job, StateFailed, "converted file is staged at "+filepath.Base(part)+
			" but could not replace the original — the original is in the recycle bin")
		return
	}
	if err := s.markConverted(ctx, job, src, finalPath, "arrmada-convert:"+codecTag(plan)); err != nil {
		s.log.Warn("convert: mark imported failed", "title", title, "err", err)
	}
	// Refresh this item in the library index. Without it a converted file keeps its OLD codec
	// in the index: it shows as convertible forever, inflates "reclaimable", and gets re-queued
	// by every sweep. Episodes self-heal on the next size comparison, but movies never did —
	// nothing else calls IndexMovie.
	s.reindexConverted(ctx, job)
	s.update(job, func(j *Job) { j.OutBytes = outSize })
	s.addReclaimed(ctx, mi.SizeBytes-outSize)
	s.finish(job, StateDone, "")
	// "title", not "movie" — this path handles episodes too, and labelling one as a movie
	// makes the logs quietly misleading.
	s.log.Info("convert: done", "title", title, "src_mb", mi.SizeBytes>>20, "out_mb", outSize>>20, "saved_mb", (mi.SizeBytes-outSize)>>20)
}

// runHealthCheck decodes the whole file looking for decode errors (a corruption scan, like
// Tdarr's health check) and reports the result on the job without touching the file (R5).
func (s *Service) runHealthCheck(ctx context.Context, job *Job, src string, mi *MediaInfo) {
	s.update(job, func(j *Job) { j.State = StateVerifying; j.Encoder = "Health scan"; j.SrcBytes = mi.SizeBytes })
	cmd := exec.CommandContext(ctx, s.ffmpeg, "-v", "error", "-i", src, "-f", "null", "-")
	var buf strings.Builder
	cmd.Stderr = &buf
	runErr := cmd.Run()
	issues := 0
	for _, ln := range strings.Split(buf.String(), "\n") {
		if strings.TrimSpace(ln) != "" {
			issues++
		}
	}
	s.update(job, func(j *Job) { j.Progress = 1 })
	switch {
	case runErr != nil && issues == 0:
		s.finish(job, StateFailed, "health scan could not run: "+runErr.Error())
	case issues == 0:
		s.finish(job, StateDone, "healthy — no decode errors")
	default:
		s.finish(job, StateFailed, fmt.Sprintf("%d decode issue(s) found — file may be corrupt", issues))
	}
}

// wouldBeNoOp reports whether running this plan on this file would change nothing (already the
// target codec, no downscale, same container) — so the worker can skip it cleanly.
func wouldBeNoOp(mi *MediaInfo, plan Plan) bool {
	if plan.VideoCodec == "" { // remux / container / track work still happens
		return false
	}
	if !strings.EqualFold(mi.VideoCodec, plan.VideoCodec) {
		return false
	}
	if plan.ScaleHeight > 0 && mi.Height > plan.ScaleHeight {
		return false
	}
	if plan.Container != "" && !strings.EqualFold(mi.Container, plan.Container) {
		return false
	}
	return true
}

// canPreserveHDR reports whether this plan can convert an HDR file without losing its metadata.
// A remux/copy keeps the stream (and metadata) intact. Re-encoding preserves HDR only on the
// CPU/HEVC path: HDR10 static metadata is re-passed to x265; HDR10+ dynamic metadata and Dolby
// Vision RPU each require their bundled tool. When none applies the caller skips the file rather
// than silently degrading it.
func (s *Service) canPreserveHDR(mi *MediaInfo, plan Plan, enc Encoder) bool {
	if plan.VideoCodec == "" { // remux / container work copies the video stream as-is
		return true
	}

	// AV1. The format itself handles all of these — Netflix ships AV1-HDR10+ at scale — but
	// our bundled tools can't write the metadata into an AV1 bitstream: dovi_tool 2.3.3 and
	// hdr10plus_tool 1.7.2 both read and write HEVC only. So what AV1 can carry here is
	// whatever lives in the bitstream headers, not what needs injecting afterwards.
	if plan.VideoCodec == "av1" {
		switch mi.HDR {
		case "HLG":
			return true // transfer curve only — nothing to inject
		case "HDR10":
			// Verified against the bundled ffmpeg: SVT-AV1 accepts mastering-display and
			// content-light via -svtav1-params, and the values round-trip into the output
			// as Mastering display / Content light level side data.
			return true
		}
		return false // HDR10+ and Dolby Vision: no injection path for AV1
	}

	// HEVC. Everything is preservable, but only on the x265 path — hdr10Params emits
	// -x265-params, and the HDR10+ / DV pipelines encode an x265 elementary stream before
	// re-injecting. Hardware encoders have no equivalent, so HDR routes to CPU (see process).
	if plan.VideoCodec != "hevc" || enc.Name != "libx265" {
		return false
	}
	if !cpuWorks("hevc", s.encoders) {
		return false // x265 doesn't run here, so nothing can be preserved through it
	}
	switch mi.HDR {
	case "HDR10", "HLG":
		// Colour tags survive a re-encode; mastering-display / max-cll are re-passed for PQ.
		return true
	case "HDR10+":
		// x265 here isn't built with dhdr10-info, so the dynamic metadata is injected after
		// the encode. Without the tool it would silently degrade to static HDR10.
		return s.hdr10plusTool != ""
	case "Dolby Vision":
		return s.doviTool != ""
	}
	return false
}

// pickEncoder applies the ordered routing rules:
//
//  1. HDR of any kind → CPU. Forced: every HDR metadata path is software-only. hdr10Params
//     emits -x265-params, and the HDR10+ / Dolby Vision pipelines encode an x265 elementary
//     stream before re-injecting. This is checked BEFORE resolution, which is why a 1080p
//     HDR file goes to CPU — resolution never enters into it.
//  2. Height >= threshold (default 2160) → CPU. A preference, not a requirement: 4K files
//     are few and large, so the efficiency gain is tens of GB each.
//  3. Otherwise → hardware, falling back to CPU if none is available.
func (s *Service) pickEncoder(ctx context.Context, job *Job, mi *MediaInfo, plan Plan) Encoder {
	hw := s.encoderFor(plan.VideoCodec)
	cpu := cpuEncoder(plan.VideoCodec)
	// The CPU encoder is normally the safe harbour, but it isn't guaranteed — a broken
	// libx265 build fails every file. When it doesn't work there's nothing to fall back
	// to, so stay on hardware and let the HDR check below decide what's preservable.
	cpuOK := cpuWorks(plan.VideoCodec, s.encoders)
	if hw.Hardware && s.hardwareIsBroken(hw.Name) && cpuOK {
		return cpu // proven not to work on this machine; don't waste an attempt on it
	}
	if !cpuOK {
		return hw
	}

	reason := ""
	switch {
	case isHDR(mi.HDR):
		reason = mi.HDR + " metadata can only be preserved on the CPU encoder"
	case plan.VideoCodec != "" && mi.Height > 0 && mi.Height >= s.cpuAboveHeight(ctx):
		reason = "high resolution — CPU gives a better result per byte"
	default:
		return hw
	}
	if hw.Name == cpu.Name {
		return cpu // already on CPU; nothing to say
	}
	s.update(job, func(j *Job) { j.Encoder = cpu.Label }) // don't claim GPU in the UI
	s.log.Info("convert: routing to the CPU encoder", "title", job.Title, "reason", reason,
		"instead_of", hw.Name)
	return cpu
}

// cpuAboveHeight is the resolution at or above which conversions use the CPU encoder.
// 0 disables the rule (hardware for everything that isn't forced to CPU).
func (s *Service) cpuAboveHeight(ctx context.Context) int {
	n, err := strconv.Atoi(s.settings.Get(ctx, keyCPUAboveHeight, "2160"))
	if err != nil || n < 0 {
		return 2160
	}
	if n == 0 {
		return 1 << 30 // effectively never
	}
	return n
}

// skipReasonHDR explains, in the user's terms, why a file can't be converted to this format.
func skipReasonHDR(hdr, target string) string {
	if target == "av1" {
		return hdr + " can't be converted to AV1 — the metadata can't be carried, so this file was left as it is"
	}
	return hdr + " — skipped (metadata passthrough not available for this target)"
}

// isHDR reports whether a probed HDR label means the file carries HDR metadata.
func isHDR(hdr string) bool { return hdr != "" && hdr != "SDR" }

// codecTag is the source-release marker written to the library record after a conversion.
func codecTag(plan Plan) string {
	if plan.VideoCodec == "" {
		return "remux"
	}
	return plan.VideoCodec
}

func (s *Service) finish(job *Job, state JobState, note string) {
	s.update(job, func(j *Job) {
		j.State = state
		j.Note = note
		if state == StateDone {
			j.Progress = 1
		}
	})
	// Mirror the outcome to the console log.
	switch state {
	case StateDone:
		saved := job.SrcBytes - job.OutBytes
		s.event("info", fmt.Sprintf("✓ Done %s — %s → %s (saved %s)", job.Title, humanBytes(job.SrcBytes), humanBytes(job.OutBytes), humanBytes(saved)))
	case StateFailed:
		s.event("error", fmt.Sprintf("✗ Failed %s — %s", job.Title, note))
	case StateSkipped:
		s.event("info", fmt.Sprintf("Skipped %s — %s", job.Title, note))
	}
	// Track hard failures for the quarantine blocklist; a success clears the record.
	// (Skips are intentional outcomes, not failures, so they don't count.) Keyed per
	// item, so episodes quarantine exactly like movies — without this a file that
	// always fails to encode is re-queued by every sweep, forever.
	key := jobKey(job)
	s.releasePending(key, job) // the file can be queued again once this job is over
	switch state {
	case StateFailed:
		s.log.Warn("convert: job failed", "title", job.Title, "note", note)
		s.failures.recordFailure(context.Background(), key, note)
	case StateDone:
		s.failures.clearFailures(context.Background(), key)
		s.skips.clear(context.Background(), key) // it converted after all
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

// String returns the retained lines, most useful last. Lines that are plainly metadata
// rather than diagnostics are dropped, since they crowd out the actual error.
func (t *lineTail) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.lines) == 0 {
		// A crash can kill ffmpeg before it says anything. Saying so plainly is more use
		// than dredging up whatever text happened to be last.
		return "no output from ffmpeg"
	}
	return strings.Join(t.lines, " | ")
}

// encode runs ffmpeg for one job, parsing live progress from the -progress pipe. hwDecode
// asks the GPU to decode too (VAAPI only) — set for the full-GPU attempt, cleared for the
// software-decode fallback.
func (s *Service) encode(ctx context.Context, job *Job, src, dst string, mi *MediaInfo, enc Encoder, plan Plan, hwDecode bool) error {
	cores := s.cpuCores(ctx)
	// -loglevel warning suppresses ffmpeg's input stream dump. That dump is emitted at
	// info level and floods stderr, so the retained tail filled up with "Side data:" and
	// "title:" lines and the actual diagnostic — if there was one — scrolled away. Warnings
	// and errors still come through, which is all the tail is for.
	args := []string{"-y", "-hide_banner", "-nostats", "-loglevel", "warning",
		"-progress", "pipe:1", "-threads", strconv.Itoa(cores)}
	args = append(args, globalArgs(enc, hwDecode, s.vaapiDev(ctx))...) // device / hwaccel init must precede the input
	args = append(args, "-i", src)
	args = append(args, compileOutputArgs(enc, mi, plan, hwDecode, cores, s.noNumaPools)...)
	args = append(args, dst)

	err := s.runWithProgress(ctx, job, args, mi.DurationSec)
	if err == nil || ctx.Err() != nil {
		return err
	}
	// Safe-mode retry. The quality-tuning parameters are a far larger surface than a plain
	// preset/CRF encode, and they vary with the machine (core counts, driver, ffmpeg build)
	// in ways that can't all be verified up front. A failure there shouldn't cost the user
	// the conversion when the simple form would have worked — so try once without them
	// before giving up, and say so.
	simple := stripTuningParams(args)
	if len(simple) == len(args) {
		return err // nothing to strip; the failure is something else
	}
	s.log.Warn("convert: encode failed with tuned settings — retrying with plain preset/CRF",
		"title", job.Title, "err", err)
	s.update(job, func(j *Job) { j.Progress = 0 })
	if err2 := s.runWithProgress(ctx, job, simple, mi.DurationSec); err2 != nil {
		return err // report the ORIGINAL failure; the retry is a bonus, not the diagnosis
	}
	s.event("warn", job.Title+": converted with default encoder settings — the tuned ones failed on this machine")
	return nil
}

// runWithProgress runs an ffmpeg command whose stdout is a -progress stream, updating the job
// live and returning any error with a tail of stderr for diagnosis.
func (s *Service) runWithProgress(ctx context.Context, job *Job, args []string, durationSec float64) error {
	cmd := exec.CommandContext(ctx, s.ffmpeg, args...)
	lowPriority(cmd) // own process group, so the whole encode can be niced and signalled together
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	// Keep the LAST few stderr lines, not just one. ffmpeg's final line is usually stream
	// metadata, so a one-line tail reported things like "title: English (SDH)" as the cause
	// of a segfault. The mutex matters too: this goroutine outlives Wait() in principle, and
	// the old unsynchronized Builder was a genuine data race.
	tail := &lineTail{max: 12}
	if errPipe, e := cmd.StderrPipe(); e == nil {
		go func() {
			sc := bufio.NewScanner(errPipe)
			for sc.Scan() {
				tail.add(sc.Text())
			}
		}()
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	applyNice(cmd.Process.Pid, encodeNice) // bulk work — yield to anything interactive
	s.readProgress(job, stdout, durationSec)
	if err := cmd.Wait(); err != nil {
		return fmt.Errorf("%v: %s", err, tail.String())
	}
	return nil
}

// readProgress consumes ffmpeg's -progress key=value stream and updates the job live.
func (s *Service) readProgress(job *Job, r io.Reader, durationSec float64) {
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		line := sc.Text()
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		switch k {
		case "out_time_ms":
			if us, err := strconv.ParseFloat(v, 64); err == nil && durationSec > 0 {
				p := (us / 1e6) / durationSec
				if p > 1 {
					p = 1
				}
				s.update(job, func(j *Job) { j.Progress = p })
			}
		case "fps":
			if f, err := strconv.ParseFloat(v, 64); err == nil {
				s.update(job, func(j *Job) { j.FPS = f })
			}
		case "speed":
			if sp, err := strconv.ParseFloat(strings.TrimSuffix(strings.TrimSpace(v), "x"), 64); err == nil {
				s.update(job, func(j *Job) { j.SpeedX = sp })
			}
		}
	}
}

func fileSize(path string) int64 {
	if fi, err := os.Stat(path); err == nil {
		return fi.Size()
	}
	return 0
}

// humanBytes renders a byte count for the console log.
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

// moveFile renames src→dst, falling back to copy+remove across filesystems (scratch is
// often a different volume from the library).
// retire moves a library file out of the way before it's replaced: into the recycle bin, or
// deleted outright if the admin switched the bin off. It never silently relocates a file —
// see library.ErrRecycleDisabled.
func (s *Service) retire(path string) error {
	dst, err := library.RecycleFile(s.recycleDir, path)
	switch {
	case errors.Is(err, library.ErrRecycleDisabled):
		// Deliberate: the user turned the bin off, so deletion is the configured behaviour.
		if rerr := os.Remove(path); rerr != nil && !os.IsNotExist(rerr) {
			return rerr
		}
		s.log.Info("convert: original deleted (recycle bin is off)", "path", path)
		return nil
	case err != nil:
		return err
	}
	s.log.Info("convert: original recycled", "to", dst)
	return nil
}

// moveFile moves src to dst, falling back to copy+remove across filesystems. The copy is
// fsynced before the source is removed: without that, a crash between the copy and the
// filesystem flushing it leaves a zero-length or partial destination while the source is
// already gone. Callers must pass a temporary dst and rename it into place.
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
	if err := out.Sync(); err != nil { // durable before we drop the only other copy
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Remove(src)
}
