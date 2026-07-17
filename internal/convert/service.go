package convert

import (
	"bufio"
	"context"
	"database/sql"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
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
	keyExtractSubs    = "convert_extract_subs"
	keyKeepAudioLangs = "convert_keep_audio_langs" // CSV; empty = keep all audio
	keyAddStereo      = "convert_add_stereo"       // add an AAC 2.0 downmix beside surround
	keyLoudnorm       = "convert_loudnorm"         // EBU R128 loudness normalize
	keyTargetCodec    = "convert_target_codec"     // "hevc" | "av1" — what the library is converted to
	keyAuto           = "convert_auto"             // auto-convert the library on the schedule
	keyQualityGate    = "convert_quality_gate"     // reject/retry an encode that scores too low (SSIM)
	keyMinSSIM        = "convert_min_ssim"         // minimum acceptable SSIM (default 0.95)
	keyWorkers        = "convert_workers"          // number of concurrent encode workers (default 1)
	keySweepStart     = "convert_sweep_start"      // auto-sweep window start "HH:MM" (empty = always)
	keySweepEnd       = "convert_sweep_end"        // auto-sweep window end   "HH:MM"
	keyMaxFailures    = "convert_max_failures"     // blocklist a movie after this many convert failures
	keyScratchDir     = "convert_scratch_dir"      // transcode working dir override (empty = the startup default)
	keyVaapiDevice    = "convert_vaapi_device"     // which /dev/dri/renderD* VAAPI encodes on (empty = default)
)

// qualityRetries is how many times the quality gate re-encodes at a higher quality before
// giving up and keeping the original.
const qualityRetries = 2

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
)

// Job is one conversion of one library file — a movie or a TV episode.
type Job struct {
	ID       int64    `json:"id"`
	Kind     string   `json:"kind"` // "movie" | "episode" (empty = movie, for back-compat)
	MovieID  int64    `json:"movie_id,omitempty"`
	SeriesID int64    `json:"series_id,omitempty"`
	Season   int      `json:"season,omitempty"`
	Episode  int      `json:"episode,omitempty"`
	Title    string   `json:"title"`
	State    JobState `json:"state"`
	Progress    float64 `json:"progress"`     // 0..1
	FPS         float64 `json:"fps"`
	SpeedX      float64 `json:"speed_x"`      // × realtime
	DurationSec float64 `json:"duration_sec"` // source runtime (for the UI's ETA)
	Encoder     string  `json:"encoder"`
	SrcBytes    int64   `json:"src_bytes"`
	OutBytes int64    `json:"out_bytes"`
	SSIM     float64  `json:"ssim,omitempty"` // quality-gate score vs the source (0 = not measured)
	Note     string   `json:"note,omitempty"` // skip reason / error
	plan     Plan     // the compiled plan this job runs (not serialized)
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

	encoders []Encoder
	encoder  Encoder
	failures *failureStore // quarantine blocklist (repeated-failure tracking)
	cache    *probeCache   // persisted ffprobe results (avoids re-analyzing on restart)

	mu       sync.Mutex
	reclaimMu sync.Mutex // guards the reclaimed-bytes read-modify-write across workers
	jobs     []*Job
	nextID   int64
	queue    chan *Job

	logMu  sync.Mutex
	logBuf []LogLine // recent human-readable convert events, for the UI console
}

// LogLine is one entry in the Convert activity console.
type LogLine struct {
	At    int64  `json:"at"`    // unix seconds
	Level string `json:"level"` // "info" | "warn" | "error"
	Msg   string `json:"msg"`
}

// event appends a human-readable line to the convert console (kept to the last 300)
// and mirrors it to the structured log.
func (s *Service) event(level, msg string) {
	s.logMu.Lock()
	s.logBuf = append(s.logBuf, LogLine{At: time.Now().Unix(), Level: level, Msg: msg})
	if len(s.logBuf) > 300 {
		s.logBuf = s.logBuf[len(s.logBuf)-300:]
	}
	s.logMu.Unlock()
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
		cache: &probeCache{db: db},
		queue: make(chan *Job, 256),
	}
	s.doviTool, _ = exec.LookPath("dovi_tool")
	s.hdr10plusTool, _ = exec.LookPath("hdr10plus_tool")
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
					s.process(ctx, job)
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
			if strings.HasPrefix(n, "convert-") || strings.HasPrefix(n, "sample-") || strings.HasPrefix(n, "h10p-") {
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
	Season    int        `json:"season,omitempty"`
	Episode   int        `json:"episode,omitempty"`
	Title     string     `json:"title"`
	Year      int        `json:"year,omitempty"`
	PosterURL string     `json:"poster_url,omitempty"`
	Path         string     `json:"path"`
	Info         *MediaInfo `json:"info,omitempty"`
	Candidate    bool       `json:"candidate"`     // would Save space convert it
	EstBytes     int64      `json:"est_bytes"`     // rough estimate of converted size
	ExternalSubs int        `json:"external_subs"` // count of .srt sidecars next to the file
}

// Library probes every downloaded movie and returns its spec + convert candidacy.
func (s *Service) Library(ctx context.Context) ([]Candidate, error) {
	list, err := s.movies.List(ctx)
	if err != nil {
		return nil, err
	}
	dp := s.defaultPlan(ctx)
	target := s.targetCodec(ctx)
	var out []Candidate
	for _, m := range list {
		if !m.HasFile || m.MovieFilePath == "" {
			continue
		}
		c := Candidate{Kind: "movie", MovieID: m.ID, Title: m.Title, Year: m.Year, PosterURL: m.PosterURL, Path: m.MovieFilePath}
		c.ExternalSubs = countSidecarSubs(m.MovieFilePath)
		if mi, err := s.probeCached(ctx, m.MovieFilePath); err == nil {
			c.Info = mi
			c.Candidate = isCandidateFor(mi, target)
			if c.Candidate {
				c.EstBytes = estimatePlanSize(mi, dp) // plan-aware estimate
			}
		}
		out = append(out, c)
	}
	return out, nil
}

// LibraryTV probes every downloaded TV episode and returns its spec + convert
// candidacy — the TV Shows tab of Convert → Library. Uses the same cached probe
// path as the movie scan, so it only re-analyzes new or changed files.
func (s *Service) LibraryTV(ctx context.Context) ([]Candidate, error) {
	if s.series == nil {
		return nil, nil
	}
	list, err := s.series.List(ctx)
	if err != nil {
		return nil, err
	}
	dp := s.defaultPlan(ctx)
	target := s.targetCodec(ctx)
	var out []Candidate
	for _, sm := range list {
		full, err := s.series.Get(ctx, sm.ID)
		if err != nil {
			continue
		}
		for _, sn := range full.Seasons {
			for _, e := range sn.Episodes {
				if !e.HasFile || e.FilePath == "" {
					continue
				}
				c := Candidate{
					Kind: "episode", SeriesID: full.ID, Season: e.SeasonNumber, Episode: e.EpisodeNumber,
					Title:     fmt.Sprintf("%s - S%02dE%02d", full.Title, e.SeasonNumber, e.EpisodeNumber),
					Year:      full.Year,
					PosterURL: full.PosterURL,
					Path:      e.FilePath,
				}
				c.ExternalSubs = countSidecarSubs(e.FilePath)
				if mi, err := s.probeCached(ctx, e.FilePath); err == nil {
					c.Info = mi
					c.Candidate = isCandidateFor(mi, target)
					if c.Candidate {
						c.EstBytes = estimatePlanSize(mi, dp)
					}
				}
				out = append(out, c)
			}
		}
	}
	return out, nil
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

// QueueExtractMovie enqueues a subtitle-only extraction for a movie (text subs → SRT sidecars,
// video left untouched).
func (s *Service) QueueExtractMovie(ctx context.Context, movieID int64) (*Job, error) {
	return s.queueMovie(ctx, movieID, extractPlan())
}

// QueueExtractEpisode enqueues a subtitle-only extraction for one TV episode.
func (s *Service) QueueExtractEpisode(ctx context.Context, seriesID int64, season, episode int) (*Job, error) {
	return s.queueEpisode(ctx, seriesID, season, episode, extractPlan())
}

// extractPlan is the plan for a subtitle-only extraction: no transcode, no remux, just pull the
// text subs out to sidecars (ExtractOnly short-circuits the encode pipeline in process).
func extractPlan() Plan {
	return Plan{Container: "mkv", ExtractOnly: true, Subs: SubPlan{ExtractText: true, ExtractCC: true}}
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
	s.mu.Lock()
	s.nextID++
	job := &Job{ID: s.nextID, Kind: "episode", SeriesID: seriesID, Season: season, Episode: episode,
		Title: title, State: StateQueued, Encoder: s.encoder.Label, plan: plan}
	s.jobs = append([]*Job{job}, s.jobs...)
	if len(s.jobs) > 200 {
		s.jobs = s.jobs[:200]
	}
	s.mu.Unlock()
	s.event("info", "Queued "+job.Title)
	s.queue <- job
	return job, nil
}

// defaultPlan is the single conversion plan derived from the global settings: transcode to the
// chosen codec at maximum-retention quality, keep audio untouched, extract subtitles per the
// toggle, MKV container. There is no per-rule plan any more — Convert is one focused job.
func (s *Service) defaultPlan(ctx context.Context) Plan {
	codec := s.targetCodec(ctx)
	subs := s.settings.GetBool(ctx, keyExtractSubs, true)
	return Plan{
		VideoCodec: codec,
		Quality:    maxQualityCRF(codec),
		VFRToCFR:   true,
		Container:  "mkv",
		Subs:       SubPlan{ExtractText: subs, ExtractCC: subs},
	}
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
	return mi.VideoCodec != "" && !strings.EqualFold(mi.VideoCodec, target)
}

func (s *Service) queueMovie(ctx context.Context, movieID int64, plan Plan) (*Job, error) {
	m, err := s.movies.Get(ctx, movieID)
	if err != nil {
		return nil, err
	}
	if !m.HasFile || m.MovieFilePath == "" {
		return nil, fmt.Errorf("movie has no file to convert")
	}
	s.mu.Lock()
	s.nextID++
	job := &Job{ID: s.nextID, MovieID: movieID, Title: m.Title, State: StateQueued, Encoder: s.encoder.Label, plan: plan}
	s.jobs = append([]*Job{job}, s.jobs...) // newest first
	if len(s.jobs) > 200 {
		s.jobs = s.jobs[:200]
	}
	s.mu.Unlock()
	s.event("info", "Queued "+job.Title)
	s.queue <- job
	return job, nil
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
		args = append(args, compileOutputArgs(enc, mi, plan, false)...)
		args = append(args, dst)
		return exec.CommandContext(ctx, s.ffmpeg, args...).Run()
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
	if n, _ := s.ConvertAll(ctx); n > 0 {
		s.log.Info("convert: scheduled conversion queued", "queued", n)
	}
}

// ConvertAll queues every library file (movies + TV episodes) that isn't already the target
// codec, skipping movies that have failed too many times. The manual "Convert all now" button
// and the schedule both use it.
func (s *Service) ConvertAll(ctx context.Context) (int, error) {
	plan := s.defaultPlan(ctx)
	maxFail := s.maxFailures(ctx)
	queued := 0

	cands, err := s.Library(ctx)
	if err != nil {
		return 0, err
	}
	for _, c := range cands {
		if !c.Candidate {
			continue
		}
		if s.failures.failureCount(ctx, c.MovieID) >= maxFail {
			continue // blocklisted after repeated failures
		}
		if _, err := s.queueMovie(ctx, c.MovieID, plan); err == nil {
			queued++
		}
	}

	tv, err := s.LibraryTV(ctx)
	if err == nil {
		for _, c := range tv {
			if !c.Candidate {
				continue
			}
			if _, err := s.QueueEpisode(ctx, c.SeriesID, c.Season, c.Episode); err == nil {
				queued++
			}
		}
	}
	return queued, nil
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
	// Extract-only: write text subs → SRT sidecars, leave the video untouched (no remux/replace),
	// so it's safe on hardlinked/seeding files. Report and return.
	if plan.ExtractOnly {
		s.runExtractOnly(ctx, job, src, mi, title)
		return
	}
	if wouldBeNoOp(mi, plan) {
		s.finish(job, StateSkipped, "already "+strings.ToUpper(mi.VideoCodec)+" — nothing to do")
		return
	}
	// Pick the encoder for this plan's target codec (hardware if available, else CPU).
	enc := s.encoderFor(plan.VideoCodec)

	// HDR fail-safe: re-encoding HDR without preserving its metadata degrades the picture. HDR10
	// static metadata is re-passed on the CPU/HEVC path, and a plain remux/copy keeps the stream
	// (and its metadata) intact — both are safe. HDR10+ / Dolby Vision are still skipped until
	// their dynamic-metadata tools (hdr10plus_tool / dovi_tool) land.
	if mi.HDR != "" && mi.HDR != "SDR" && !s.canPreserveHDR(mi, plan, enc) {
		s.finish(job, StateSkipped, mi.HDR+" — skipped (metadata passthrough not yet available for this target)")
		return
	}
	// Seeding-safety: skip hardlinked files by default so we don't duplicate a seeding copy.
	if s.settings.GetBool(ctx, keySkipHardlinked, true) && fileLinks(src) > 1 {
		s.finish(job, StateSkipped, "file is hardlinked (likely still seeding) — skipped")
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
		if err := s.extractHDR10Plus(ctx, src, jf); err == nil {
			h10pJSON = jf
			defer os.Remove(jf)
		} else {
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
	gate := plan.VideoCodec != "" && s.settings.GetBool(ctx, keyQualityGate, false)
	minSSIM := parseFloatDefault(s.settings.Get(ctx, keyMinSSIM, "0.95"), 0.95)
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
		// Cap the verify: SSIM decodes both files, which is slow on a long movie and
		// must never hang the whole queue. On timeout we accept the encode.
		sctx, cancel := context.WithTimeout(ctx, 25*time.Minute)
		score, err := s.computeSSIM(sctx, dst, src)
		cancel()
		if err != nil {
			s.log.Warn("convert: quality gate could not measure SSIM, accepting output", "err", err)
			s.event("warn", fmt.Sprintf("%s: couldn't measure SSIM — accepting the encode", title))
			break
		}
		s.update(job, func(j *Job) { j.SSIM = score })
		if score >= minSSIM {
			s.event("info", fmt.Sprintf("%s: quality gate passed (SSIM %.4f ≥ %.2f)", title, score, minSSIM))
			break
		}
		if attempt >= qualityRetries {
			s.finish(job, StateSkipped, fmt.Sprintf("quality gate failed — SSIM %.4f < %.4f after %d attempts; kept the original", score, minSSIM, attempt+1))
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
func (s *Service) markConverted(ctx context.Context, job *Job, finalPath, tag string) error {
	if job.Kind == "episode" {
		if s.series == nil {
			return fmt.Errorf("series module not available")
		}
		return s.series.MarkEpisodeImported(ctx, job.SeriesID, job.Season, job.Episode, finalPath, fileSize(finalPath))
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
			s.log.Warn("convert: hardware encode failed, retrying on CPU", "err", err)
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
		s.finish(job, StateSkipped, "converted file wasn't smaller — kept the original")
		return
	}
	if plan.VideoCodec == "" && outSize > mi.SizeBytes*12/10 {
		s.finish(job, StateSkipped, "remuxed file was unexpectedly larger — kept the original")
		return
	}
	pct := 0
	if mi.SizeBytes > 0 {
		pct = int(100 - outSize*100/mi.SizeBytes)
	}
	s.event("info", fmt.Sprintf("%s — output: %s · %s (%d%% smaller)", title, mediaSpec(outInfo), humanBytes(outSize), pct))

	// Safe replace: recycle the original, then move the new file into place. The container may
	// change (→ MKV or MP4), so the extension may change.
	s.update(job, func(j *Job) { j.State = StateReplacing })
	finalPath := strings.TrimSuffix(src, filepath.Ext(src)) + ext
	if plan.Subs.ExtractText {
		s.extractTextSubs(ctx, src, finalPath, mi)
	}
	if plan.Subs.ExtractCC && mi.HasCC {
		s.extractCC(ctx, src, finalPath) // embedded 608/708 captions → sidecar (best-effort)
	}
	if dst2, rerr := library.RecycleFile(s.recycleDir, src); rerr != nil {
		s.log.Warn("convert: recycle original failed, hard-deleting", "path", src, "err", rerr)
		_ = os.Remove(src)
	} else {
		s.log.Info("convert: original recycled", "to", dst2)
	}
	if finalPath != src {
		if _, e := os.Stat(finalPath); e == nil {
			_, _ = library.RecycleFile(s.recycleDir, finalPath)
		}
	}
	if err := moveFile(dst, finalPath); err != nil {
		s.finish(job, StateFailed, "could not move converted file into place: "+err.Error())
		return
	}
	if err := s.markConverted(ctx, job, finalPath, "arrmada-convert:"+codecTag(plan)); err != nil {
		s.log.Warn("convert: mark imported failed", "title", title, "err", err)
	}
	s.update(job, func(j *Job) { j.OutBytes = outSize })
	s.addReclaimed(ctx, mi.SizeBytes-outSize)
	s.finish(job, StateDone, "")
	s.log.Info("convert: done", "movie", title, "src_mb", mi.SizeBytes>>20, "out_mb", outSize>>20, "saved_mb", (mi.SizeBytes-outSize)>>20)
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

// runExtractOnly pulls the embedded text subtitles (and any closed captions) out to SRT sidecars
// next to the original file, without re-muxing or replacing the video. Image subs (PGS/VOBSUB)
// can't be turned into text without OCR, so they're left in place and reported.
func (s *Service) runExtractOnly(ctx context.Context, job *Job, src string, mi *MediaInfo, title string) {
	s.update(job, func(j *Job) { j.State = StateEncoding; j.Encoder = "Subtitle extract"; j.SrcBytes = mi.SizeBytes; j.Progress = 0.2 })
	text, image := 0, 0
	for _, sub := range mi.Subs {
		if sub.Text {
			text++
		} else {
			image++
		}
	}
	if text == 0 && !mi.HasCC {
		note := "no embedded text subtitles to extract"
		if image > 0 {
			note = fmt.Sprintf("only %d image subtitle(s) (PGS/VOBSUB) — need OCR, coming soon", image)
		}
		s.finish(job, StateSkipped, note)
		return
	}
	s.event("info", fmt.Sprintf("Extracting %d text subtitle track(s) from %s → SRT sidecar(s)", text, title))
	s.extractTextSubs(ctx, src, src, mi) // finalPath == src → sidecars land next to the original
	if mi.HasCC {
		s.extractCC(ctx, src, src)
	}
	s.update(job, func(j *Job) { j.Progress = 1 })
	left := ""
	if image > 0 {
		left = fmt.Sprintf(" · left %d image sub(s) in place (need OCR)", image)
	}
	s.event("info", fmt.Sprintf("✓ Extracted subtitles from %s → SRT%s", title, left))
	s.finish(job, StateDone, fmt.Sprintf("extracted %d subtitle track(s) → SRT sidecar(s)%s", text, left))
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
	if plan.VideoCodec == "" { // remux / container / track work copies the video stream as-is
		return true
	}
	if plan.VideoCodec != "hevc" || enc.Name != "libx265" {
		return false // dynamic/static HDR passthrough is implemented only on the x265/HEVC path
	}
	switch mi.HDR {
	case "HDR10", "HDR10+":
		// Colour tags (BT.2020/PQ) always survive a libx265 re-encode; mastering-display /
		// max-cll are re-passed when present, and HDR10+ dynamic metadata is extracted and fed
		// back via dhdr10-info when the file has it and the tool is bundled.
		return true
	case "Dolby Vision":
		return s.doviTool != ""
	}
	return false
}

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
	// (Skips are intentional outcomes, not failures, so they don't count.) The
	// blocklist is keyed by movie id, so it only applies to movie jobs today;
	// episode jobs simply aren't quarantined.
	if job.Kind == "episode" {
		return
	}
	switch state {
	case StateFailed:
		s.log.Warn("convert: job failed", "title", job.Title, "note", note)
		s.failures.recordFailure(context.Background(), job.MovieID, note)
	case StateDone:
		s.failures.clearFailures(context.Background(), job.MovieID)
	}
}

// extractTextSubs pulls each embedded text subtitle track out to an SRT sidecar next to
// the final file (image subs — PGS/VOBSUB — are left in the container; they need OCR).
func (s *Service) extractTextSubs(ctx context.Context, src, finalPath string, mi *MediaInfo) {
	dir := filepath.Dir(finalPath)
	base := strings.TrimSuffix(filepath.Base(finalPath), filepath.Ext(finalPath))
	for _, sub := range mi.Subs {
		if !sub.Text {
			continue
		}
		lang := sub.Lang
		if lang == "" {
			lang = "und"
		}
		out := filepath.Join(dir, base+"."+lang+".srt")
		if _, err := os.Stat(out); err == nil { // second track in the same language
			out = filepath.Join(dir, fmt.Sprintf("%s.%s.%d.srt", base, lang, sub.SubIndex))
		}
		cmd := exec.CommandContext(ctx, s.ffmpeg, "-y", "-hide_banner", "-i", src,
			"-map", fmt.Sprintf("0:s:%d", sub.SubIndex), "-c:s", "srt", out)
		if err := cmd.Run(); err != nil {
			s.log.Warn("convert: subtitle extract failed", "sub", sub.SubIndex, "err", err)
			continue
		}
		s.log.Info("convert: extracted subtitle → SRT", "to", out, "lang", lang)
	}
}

// extractCC pulls embedded EIA/CEA-608/708 closed captions (which live inside the video
// stream, not as a track, and are lost on re-encode) out to an SRT sidecar. Best-effort:
// any failure is logged and ignored so it never fails the conversion.
func (s *Service) extractCC(ctx context.Context, src, finalPath string) {
	dir := filepath.Dir(finalPath)
	base := strings.TrimSuffix(filepath.Base(finalPath), filepath.Ext(finalPath))
	out := filepath.Join(dir, base+".cc.srt")
	if _, err := os.Stat(out); err == nil {
		return
	}
	// The lavfi "movie" filter exposes captions via the subcc pad; single-quote the path so
	// spaces/colons/parentheses are literal.
	graph := "movie=filename=" + lavfiSingleQuote(src) + "[out0+subcc]"
	cmd := exec.CommandContext(ctx, s.ffmpeg, "-y", "-hide_banner", "-f", "lavfi", "-i", graph, "-map", "0:1", "-c:s", "srt", out)
	if err := cmd.Run(); err != nil {
		s.log.Warn("convert: closed-caption extract failed (best-effort)", "err", err)
		_ = os.Remove(out)
		return
	}
	if fi, e := os.Stat(out); e != nil || fi.Size() < 10 { // no captions actually decoded
		_ = os.Remove(out)
		return
	}
	s.log.Info("convert: extracted closed captions → SRT", "to", out)
}

// countSidecarSubs counts external .srt files sitting next to a video (named after it), e.g.
// "Movie.en.srt". Used to power the "external subs" filter in the Convert library.
func countSidecarSubs(videoPath string) int {
	if videoPath == "" {
		return 0
	}
	base := strings.ToLower(strings.TrimSuffix(filepath.Base(videoPath), filepath.Ext(videoPath)))
	entries, err := os.ReadDir(filepath.Dir(videoPath))
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := strings.ToLower(e.Name())
		if strings.HasSuffix(name, ".srt") && strings.HasPrefix(name, base) {
			n++
		}
	}
	return n
}

// lavfiSingleQuote wraps a path for safe use inside a lavfi filtergraph.
func lavfiSingleQuote(p string) string {
	return "'" + strings.ReplaceAll(p, "'", `'\''`) + "'"
}

// encode runs ffmpeg for one job, parsing live progress from the -progress pipe. hwDecode
// asks the GPU to decode too (VAAPI only) — set for the full-GPU attempt, cleared for the
// software-decode fallback.
func (s *Service) encode(ctx context.Context, job *Job, src, dst string, mi *MediaInfo, enc Encoder, plan Plan, hwDecode bool) error {
	args := []string{"-y", "-hide_banner", "-nostats", "-progress", "pipe:1"}
	args = append(args, globalArgs(enc, hwDecode, s.vaapiDev(ctx))...) // device / hwaccel init must precede the input
	args = append(args, "-i", src)
	args = append(args, compileOutputArgs(enc, mi, plan, hwDecode)...)
	args = append(args, dst)
	return s.runWithProgress(ctx, job, args, mi.DurationSec)
}

// runWithProgress runs an ffmpeg command whose stdout is a -progress stream, updating the job
// live and returning any error with a tail of stderr for diagnosis.
func (s *Service) runWithProgress(ctx context.Context, job *Job, args []string, durationSec float64) error {
	cmd := exec.CommandContext(ctx, s.ffmpeg, args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	var tail strings.Builder
	if errPipe, e := cmd.StderrPipe(); e == nil {
		go func() {
			sc := bufio.NewScanner(errPipe)
			for sc.Scan() {
				tail.Reset()
				tail.WriteString(sc.Text())
			}
		}()
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	s.readProgress(job, stdout, durationSec)
	if err := cmd.Wait(); err != nil {
		return fmt.Errorf("%v (%s)", err, tail.String())
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

// csv splits a comma-separated setting into trimmed, non-empty values.
func csv(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
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
func moveFile(src, dst string) error {
	if err := os.Rename(src, dst); err == nil {
		return nil
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		in.Close()
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		in.Close()
		out.Close()
		return err
	}
	in.Close()
	if err := out.Close(); err != nil {
		return err
	}
	return os.Remove(src)
}
