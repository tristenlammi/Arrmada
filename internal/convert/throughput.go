package convert

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"time"
)

// The time forecast. Up next used to show only the space a file would free, never how long
// it would take — and a 4K film on the CPU is about a day of encoding, four nights at
// 01:00–07:00. Nothing said the backlog was months of nights, or that an update right now
// would throw away twenty hours of work.
//
// The speed model is this machine's own history: throughput in pixel-seconds per second of
// encoding — (runtime × width × height) / encode time — the median of the last
// throughputSamples finished conversions of the same encoder class and resolution bucket.
// Until a few have run, it starts from the measured default below, scaled by the CPU cores
// a conversion may use. Watching pauses aren't modelled, so everything is "about".
//
// The API other parts build on (the plan view, and wave 2's resumable encoding):
//   - (*Service).FileForecast(ctx, mi, plan) Forecast — one file's estimate;
//   - Nights(sec, start, end) — nights of the encode window that much work needs;
//   - Status carries eta_sec/nights per Up next item, backlog_sec/backlog_nights over the
//     whole remaining list, and in_flight_secs (encoding the running conversions have done
//     that a restart would lose).

// Forecast is how long converting one file is expected to take.
type Forecast struct {
	Seconds int64 `json:"eta_sec"`
	Nights  int   `json:"nights"` // nights of the encode window (days of 24 h with none)
}

// Encoder classes the model keeps apart: they run at very different speeds.
const (
	classCPUHEVC          = "cpu-hevc"
	classCPUAV1           = "cpu-av1"
	classCPUHEVCHDR10Plus = "cpu-hevc-hdr10plus"
	classGPUHEVC          = "gpu-hevc"
	classGPUAV1           = "gpu-av1"
)

// Resolution buckets.
const (
	bucketSD = "sd"
	bucketHD = "hd"
	bucket4K = "4k"
)

// The default speed, before this machine has history: x265 slow did 3.8 fps on a 4K 23.976
// film with 16 threads (the owner's LOTR remux, 2026-10-02). Per pixel that holds up well
// for 1080p too (~17 fps). The other classes are relative to it.
const (
	defaultCPUPixelRate = 3.8 / 23.976 * 3840 * 2160 // pixel-seconds per second, 16 cores
	defaultCPUCores     = 16
)

var classSpeed = map[string]float64{
	classCPUHEVC:          1,
	classCPUAV1:           1,   // SVT-AV1 preset 5 is about x265 slow's pace
	classCPUHEVCHDR10Plus: 0.9, // the HDR10+ pipeline extracts and re-injects around the encode
	classGPUHEVC:          8,   // hardware encoders aren't held back by the core budget
	classGPUAV1:           8,
}

// throughputSamples is how many recent conversions the median is taken over, and
// throughputMinRows how many of a kind it takes before they replace the default.
const (
	throughputSamples = 20
	throughputMinRows = 3
)

// Overheads around the full encode, in seconds of work.
const (
	rehearsalContentSecs = 90    // the test encode: four 15 s clips, sometimes twice
	trialContentSecs     = 60    // the HEVC-vs-AV1 test: three 10 s clips in each format
	verifySecs           = 120   // the quality check's ten scenes, and the full-length probe
	verifySecs4K         = 240   //   … on a 4K film
	copyBytesPerSec      = 150e6 // staging the result next to the original, or a tracks-only rewrite
)

// throughputCache holds the model, re-read at most every throughputTTL.
type throughputCache struct {
	mu    sync.Mutex
	at    time.Time
	rates map[string][]float64 // class|bucket → recent rates, newest first
}

const throughputTTL = 10 * time.Minute

// pixelBucket buckets a frame size the way the model groups history.
func pixelBucket(w, h int) string {
	switch px := w * h; {
	case px >= 3200*1600:
		return bucket4K
	case px >= 1100*600:
		return bucketHD
	}
	return bucketSD
}

// encoderClass is the class a file's encode runs in, chosen the way encoderChoice picks:
// HDR on the CPU (HDR10+ through its own pipeline) unless it's HDR10 or HLG the GPU keeps,
// the GPU when asked for and able. hdr is the file's own format (MediaInfo.HDR), encHDR
// what the conversion carries (MediaInfo.EncodeHDR).
func (s *Service) encoderClass(codec, hdr, encHDR string, hdr10plus, useGPU bool) string {
	gpu, cpu := classGPUHEVC, classCPUHEVC
	if codec == "av1" {
		gpu, cpu = classGPUAV1, classCPUAV1
	}
	hw := s.gpuCan(codec)
	switch {
	case !cpuWorks(codec, s.encoders) && hw:
		return gpu
	case codec == "hevc" && hdr10plus:
		return classCPUHEVCHDR10Plus
	case isHDR(encHDR) && !s.encoderChoiceHDR(&MediaInfo{HDR: hdr, DVBase: encHDR}, codec, useGPU, hdr10plus).Hardware:
		return cpu
	case useGPU && hw:
		return gpu
	}
	return cpu
}

// historyClass reads the class back from what the ledger recorded (the encoder's label and
// the target codec).
func historyClass(label, codec string) string {
	switch {
	case strings.Contains(label, "HDR10+"):
		return classCPUHEVCHDR10Plus
	case strings.HasPrefix(label, "CPU"):
		if codec == "av1" {
			return classCPUAV1
		}
		return classCPUHEVC
	case codec == "av1":
		return classGPUAV1
	}
	return classGPUHEVC
}

// rates returns the model's recent rates by class|bucket, from the ledger.
func (s *Service) rates(ctx context.Context) map[string][]float64 {
	c := &s.tput
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.rates != nil && time.Since(c.at) < throughputTTL {
		return c.rates
	}
	out := map[string][]float64{}
	if s.history != nil && s.history.db != nil {
		rows, err := s.history.db.QueryContext(ctx,
			`SELECT encoder, codec, src_info_json, encode_secs FROM convert_history
			  WHERE outcome = ? AND codec <> '' AND encode_secs > 0 AND src_info_json <> ''
			  ORDER BY finished_at DESC, id DESC LIMIT 500`, OutcomeDone)
		if err == nil {
			for rows.Next() {
				var label, codec, info string
				var secs float64
				if rows.Scan(&label, &codec, &info, &secs) != nil {
					continue
				}
				var mi MediaInfo
				if json.Unmarshal([]byte(info), &mi) != nil || mi.DurationSec <= 0 || mi.Width <= 0 || mi.Height <= 0 {
					continue
				}
				k := historyClass(label, codec) + "|" + pixelBucket(mi.Width, mi.Height)
				if len(out[k]) < throughputSamples {
					out[k] = append(out[k], mi.DurationSec*float64(mi.Width*mi.Height)/secs)
				}
			}
			rows.Close()
		}
	}
	c.rates, c.at = out, time.Now()
	return out
}

// pixelRate is the expected throughput, in pixel-seconds per second, for one class and
// frame size: the median of this machine's recent conversions of the kind, else the
// default scaled by the cores a CPU encode may use.
func (s *Service) pixelRate(ctx context.Context, class string, w, h int) float64 {
	if r := s.rates(ctx)[class+"|"+pixelBucket(w, h)]; len(r) >= throughputMinRows {
		return median(r)
	}
	rate := defaultCPUPixelRate * classSpeed[class]
	if strings.HasPrefix(class, "cpu") {
		rate *= float64(s.cpuCores(ctx)) / defaultCPUCores
	}
	return rate
}

func median(v []float64) float64 {
	c := append([]float64(nil), v...)
	sort.Float64s(c)
	n := len(c)
	if n%2 == 1 {
		return c[n/2]
	}
	return (c[n/2-1] + c[n/2]) / 2
}

// estimator estimates any number of files under one set of settings, working out each
// encoder class and frame-size bucket's rate once — the backlog sums thousands of files on
// every Status read.
type estimator struct {
	s    *Service
	ctx  context.Context
	p    prefs
	memo map[string]float64
}

func (s *Service) newEstimator(ctx context.Context, p prefs) *estimator {
	return &estimator{s: s, ctx: ctx, p: p, memo: map[string]float64{}}
}

func (e *estimator) rate(f forecastFacts, w, h int) float64 {
	k := fmt.Sprintf("%s|%s|%s|%v|%s", f.codec, f.hdr, f.encHDR, f.hdr10plus, pixelBucket(w, h))
	if r, ok := e.memo[k]; ok {
		return r
	}
	r := e.s.pixelRate(e.ctx, e.s.encoderClass(f.codec, f.hdr, f.encHDR, f.hdr10plus, e.p.useGPU), w, h)
	e.memo[k] = r
	return r
}

// estimateSeconds estimates one file (see estimator.seconds).
func (s *Service) estimateSeconds(ctx context.Context, p prefs, f forecastFacts) int64 {
	return s.newEstimator(ctx, p).seconds(f)
}

// seconds is the whole job: the encode, the rehearsal and the format test when they run,
// the quality check, and copying the result into place. A tracks-only rewrite is the copy
// alone.
func (e *estimator) seconds(f forecastFacts) int64 {
	p := e.p
	if !f.video || f.codec == "" {
		return int64(float64(f.size)/copyBytesPerSec) + 30
	}
	w, h, dur := f.w, f.h, f.dur
	if w <= 0 || h <= 0 {
		w, h = 1920, 1080
	}
	if dur <= 0 {
		dur = 2 * 3600
	}
	rate := e.rate(f, w, h)
	px := float64(w * h)
	content := dur
	if dur >= preflightMinDuration {
		content += rehearsalContentSecs
	}
	if p.allowAV1 && !f.hdr10plus {
		content += trialContentSecs
	}
	secs := content * px / rate
	if pixelBucket(w, h) == bucket4K {
		secs += verifySecs4K
	} else {
		secs += verifySecs
	}
	secs += float64(f.size) / 2 / copyBytesPerSec // the result is about half the original
	return int64(math.Ceil(secs))
}

// forecastFacts is what the estimate reads about a file — kept on each candidate so the
// backlog can be summed without unmarshalling the index again.
type forecastFacts struct {
	video     bool
	codec     string
	size      int64
	dur       float64
	w, h      int
	hdr       string // the file's own HDR format (MediaInfo.HDR)
	encHDR    string
	hdr10plus bool
}

func factsFor(mi *MediaInfo, plan Plan, size int64) forecastFacts {
	f := forecastFacts{video: plan.VideoCodec != "", codec: plan.VideoCodec, size: size}
	if mi != nil {
		if f.size <= 0 {
			f.size = mi.SizeBytes
		}
		f.dur, f.w, f.h, f.hdr, f.encHDR = mi.DurationSec, mi.Width, mi.Height, mi.HDR, mi.EncodeHDR()
		f.hdr10plus = f.encHDR == "HDR10+"
	}
	return f
}

// FileForecast estimates one file's conversion under the current settings: time, and the
// nights of the encode window it needs.
func (s *Service) FileForecast(ctx context.Context, mi *MediaInfo, plan Plan) Forecast {
	p := s.prefs(ctx)
	sec := s.estimateSeconds(ctx, p, factsFor(mi, plan, 0))
	return Forecast{Seconds: sec, Nights: Nights(sec, p.start, p.end)}
}

// windowSeconds is how long the encode window is each day; any time (empty, unreadable or
// start == end) is the whole day.
func windowSeconds(start, end string) int64 {
	s0, ok1 := parseHM(start)
	e0, ok2 := parseHM(end)
	if start == "" || end == "" || !ok1 || !ok2 || s0 == e0 {
		return 24 * 3600
	}
	mins := e0 - s0
	if mins < 0 {
		mins += 24 * 60 // wraps past midnight (22:00–06:00)
	}
	return int64(mins) * 60
}

// Nights is how many nights of the start–end encode window sec seconds of work needs
// (rounded up; 0 for none). With no window, each day counts as one.
func Nights(sec int64, start, end string) int {
	if sec <= 0 {
		return 0
	}
	w := windowSeconds(start, end)
	return int((sec + w - 1) / w)
}

// forecastStatus fills the forecast fields of a Status: an estimate per Up next file, the
// backlog over every remaining file (from the cached candidates — never a re-probe), spread
// over the conversions that run at once, and the encoding a restart would lose now.
func (s *Service) forecastStatus(ctx context.Context, st *Status, p prefs, remaining []autoCand) {
	est := s.newEstimator(ctx, p)
	for i := range st.UpNext {
		c := &st.UpNext[i]
		c.ETASec = est.seconds(c.facts)
		c.Nights = Nights(c.ETASec, p.start, p.end)
	}
	var total int64
	for _, c := range remaining {
		total += est.seconds(c.facts)
	}
	if w := int64(s.workerCount(ctx)); w > 1 {
		total = (total + w - 1) / w
	}
	st.BacklogSec = total
	st.BacklogNights = Nights(total, p.start, p.end)
	st.InFlightSecs = s.inFlightSecs()
}

// inFlightSecs is the most encoding any running conversion has done — what a restart now
// would throw away (pauses excluded: they cost nothing to lose).
func (s *Service) inFlightSecs() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	var most float64
	for _, j := range s.jobs {
		if activeState(j.State) && j.activeSecs > most {
			most = j.activeSecs
		}
	}
	return int64(most)
}

// activeGap is the longest pause between two progress lines that still counts as encoding:
// ffmpeg reports every half second, and a frozen encode (outside the hours, or someone
// watching) reports nothing until it's resumed.
const activeGap = 30 * time.Second

// tickActive is called for every progress line of a full encode: it adds the time since the
// last line to the job's encoding time, unless the gap says the encode was paused.
func (s *Service) tickActive(job *Job) {
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	if !job.lastTick.IsZero() {
		if gap := now.Sub(job.lastTick); gap > 0 && gap <= activeGap {
			job.activeSecs += gap.Seconds()
		}
	}
	job.lastTick = now
}
