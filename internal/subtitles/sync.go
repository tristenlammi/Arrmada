package subtitles

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"math/cmplx"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/tristenlammi/arrmada/internal/toolrun"
)

// Sync checking: does a subtitle line up with the speech in the file's audio, and if not,
// by how much is it off? Both sides become a 10 ms on/off envelope — speech from the
// audio's energy, dialogue from the cue spans — and the envelopes are cross-correlated.
// A frame-rate mismatch (a subtitle timed for the 25 fps PAL release on a 23.976 fps
// file) stretches the whole timeline, so the known frame-rate ratios are tried as well,
// and eight windows along the film check that one correction fits all of it.
//
// The caller's flow (SUB-19 downloads, the drawer's check):
//
//	extractWAV(ctx, ffmpeg, video, stream, wav)      // once per job, shared by languages
//	track, _ := loadSpeechTrack(wav)
//	res := track.Check(srtBytes)                    // per candidate
//	if res.Confidence >= 70 && res.NeedsFix(300) { writeShifted(path, res.ShiftMs(), res.Scale) }

const (
	syncFrame      = 10 * time.Millisecond // envelope resolution
	syncCoarse     = 10                    // coarse search runs on 100 ms frames
	syncMaxLag     = 120 * time.Second     // furthest constant offset searched
	syncHangover   = 20                    // frames (200 ms) speech stays "on" after the energy drops
	syncFineRadius = 15                    // ±150 ms refinement around the coarse peak
	syncWinWide    = 1000                  // ±10 s per window when looking for a drift
	syncWinRadius  = 300                   // ±3 s per window when confirming the fit
	syncWindows    = 8
	syncMinCues    = 20 // fewer cues than this can't be judged
)

// knownRatios are the frame-rate stretches a subtitle can be off by: cue times are
// multiplied by the ratio to fit the file. 23.976 is really 24000/1001; 30/29.97 is the
// same 1.001 as 24/23.976.
var knownRatios = func() []float64 {
	ntsc := 24000.0 / 1001
	return []float64{1, 25 / ntsc, ntsc / 25, 24 / ntsc, ntsc / 24, 25.0 / 24, 24.0 / 25}
}()

// SyncResult is how a subtitle lines up with the speech. The subtitle's cues relate to
// the speech as
//
//	speech time = cue time × Scale − OffsetMs
//
// so OffsetMs > 0 means the subtitle runs late (its cues come after the words), and
// fixing it means shifting by ShiftMs() = −OffsetMs after stretching by Scale.
type SyncResult struct {
	OffsetMs   int     `json:"offset_ms"`  // how late the cues run, after Scale; + = late
	Scale      float64 `json:"scale"`      // stretch that undoes frame-rate drift; 1 = none
	Confidence int     `json:"confidence"` // 0-100; below ~50 the subtitle doesn't fit this audio
	Coverage   float64 `json:"coverage"`   // share of corrected cue time that lands on speech
}

// ShiftMs is the shift to apply (with Scale) to put the subtitle in sync.
func (r SyncResult) ShiftMs() int { return -r.OffsetMs }

// NeedsFix reports whether applying the correction would move anything by more than
// tolMs: an offset beyond the tolerance, or any frame-rate stretch.
func (r SyncResult) NeedsFix(tolMs int) bool {
	off := r.OffsetMs
	if off < 0 {
		off = -off
	}
	return off > tolMs || (r.Scale != 0 && math.Abs(r.Scale-1) > 1e-6)
}

// Fix returns the cues moved into sync.
func (r SyncResult) Fix(cues []cue) []cue { return shiftCues(cues, r.ShiftMs(), r.scale()) }

func (r SyncResult) scale() float64 {
	if r.Scale == 0 {
		return 1
	}
	return r.Scale
}

// extractWAVArgs is ffmpeg's command line for extractWAV: 16 kHz mono 16-bit PCM — what
// whisper expects, and what the speech envelope reads.
func extractWAVArgs(video string, audioStream int, dst string) []string {
	args := []string{"-y", "-hide_banner", "-i", video}
	if audioStream >= 0 {
		args = append(args, "-map", fmt.Sprintf("0:a:%d", audioStream))
	}
	return append(args, "-vn", "-ac", "1", "-ar", "16000", "-c:a", "pcm_s16le", dst)
}

// extractWAV writes one audio stream of a video (0:a:N, or ffmpeg's default for -1) to
// dst as 16 kHz mono PCM. Shared by whisper and the sync check, so a job that needs both
// reads the video once.
func extractWAV(ctx context.Context, ffmpeg, video string, audioStream int, dst string) error {
	if out, err := toolrun.Command(ctx, toolrun.Background, ffmpeg, extractWAVArgs(video, audioStream, dst)...).CombinedOutput(); err != nil {
		return fmt.Errorf("extract audio: %w: %s", err, tailStr(out, 300))
	}
	return nil
}

// speechTrack is a file's speech envelope, ready to check any number of subtitles
// against. Loading it once lets one extraction serve every language and candidate.
type speechTrack struct {
	env    []float32 // 10 ms frames, 1 = speech
	prefix []float64 // prefix[i] = sum of env[:i]
	coarse []float64 // 100 ms frames, mean of env, zero-meaned
	norm   float64   // ‖coarse‖

	once sync.Once
	n    int          // FFT size
	spec []complex128 // FFT of coarse, padded to n
}

// loadSpeechTrack reads a 16-bit PCM WAV (as extractWAV writes it) into a speechTrack.
func loadSpeechTrack(wavPath string) (*speechTrack, error) {
	env, err := speechEnvelope(wavPath)
	if err != nil {
		return nil, err
	}
	return newSpeechTrack(env), nil
}

func newSpeechTrack(env []float32) *speechTrack {
	t := &speechTrack{env: env, prefix: make([]float64, len(env)+1)}
	for i, v := range env {
		t.prefix[i+1] = t.prefix[i] + float64(v)
	}
	t.coarse = downsample(env, syncCoarse)
	t.norm = zeroMean(t.coarse)
	return t
}

// Check measures how an SRT lines up with this track's speech.
func (t *speechTrack) Check(srt []byte) SyncResult {
	res, _ := t.CheckJudged(srt)
	return res
}

// CheckJudged is Check, plus whether there was enough to judge at all: a subtitle with
// too few lines, or audio with no speech the envelope can find, comes back with
// confidence 0 and judged false — "can't tell", not "out of sync".
func (t *speechTrack) CheckJudged(srt []byte) (SyncResult, bool) {
	cues := parseSRT(string(srt))
	// Size the cue envelope to the audio (plus room for a slow frame rate and the largest
	// offset), not to the last cue: a stray "99:59:59" cue mustn't allocate hours of frames.
	frames := len(t.env) + len(t.env)/10 + int(syncMaxLag/syncFrame)
	env := cueEnvelope(cues, frames)
	judged := len(runsOf(env)) >= syncMinCues && len(t.env) > 0 && t.norm > 0
	return t.align(env), judged
}

// checkSRTSync is the one-shot form: one WAV, one subtitle.
func checkSRTSync(wavPath string, srt []byte) (SyncResult, error) {
	t, err := loadSpeechTrack(wavPath)
	if err != nil {
		return SyncResult{Scale: 1}, err
	}
	return t.Check(srt), nil
}

// speechEnvelope reads a 16-bit mono PCM WAV and marks each 10 ms frame as speech or
// not: energy in the 300-3400 Hz voice band, against a threshold 10 dB above the file's
// own noise floor, with a 200 ms hangover so the gaps between words don't break an
// utterance up.
func speechEnvelope(wavPath string) ([]float32, error) {
	f, err := os.Open(wavPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return speechEnvelopeFrom(bufio.NewReaderSize(f, 1<<20))
}

func speechEnvelopeFrom(r io.Reader) ([]float32, error) {
	rate, data, err := wavData(r)
	if err != nil {
		return nil, err
	}
	per := rate / 100 // samples per 10 ms frame
	if per <= 0 {
		return nil, fmt.Errorf("wav: sample rate %d", rate)
	}
	hp := newBiquad(rate, 300, true)
	lp := newBiquad(rate, 3400, false)
	var db []float64
	buf := make([]byte, per*2*512)
	var sum float64
	n := 0
	for {
		k, err := io.ReadFull(data, buf)
		k -= k % 2
		for i := 0; i < k; i += 2 {
			x := float64(int16(binary.LittleEndian.Uint16(buf[i:]))) / 32768
			y := lp.step(hp.step(x))
			sum += y * y
			if n++; n == per {
				db = append(db, 10*math.Log10(sum/float64(per)+1e-12))
				sum, n = 0, 0
			}
		}
		if err == io.EOF || errors.Is(err, io.ErrUnexpectedEOF) {
			break
		}
		if err != nil {
			return nil, err
		}
	}
	return gateEnergy(db), nil
}

// gateEnergy turns per-frame energy (dB) into the on/off speech envelope. The noise floor
// is the 10th percentile of frames above near-digital silence, so a silent intro or
// credits pause doesn't drag the threshold down to nothing.
func gateEnergy(db []float64) []float32 {
	env := make([]float32, len(db))
	var live []float64
	for _, d := range db {
		if d > -90 {
			live = append(live, d)
		}
	}
	if len(live) == 0 {
		return env
	}
	sort.Float64s(live)
	thresh := live[len(live)/10] + 10
	hold := 0
	for i, d := range db {
		if d > thresh {
			hold = syncHangover + 1
		}
		if hold > 0 {
			env[i] = 1
			hold--
		}
	}
	return env
}

// wavData walks a RIFF/WAVE header to the PCM samples, returning the sample rate and a
// reader over the data chunk. Only 16-bit mono PCM is accepted (what extractWAV writes).
func wavData(r io.Reader) (int, io.Reader, error) {
	var hdr [12]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return 0, nil, fmt.Errorf("wav: %w", err)
	}
	if string(hdr[0:4]) != "RIFF" || string(hdr[8:12]) != "WAVE" {
		return 0, nil, errors.New("wav: not a RIFF/WAVE file")
	}
	rate := 0
	for {
		var ch [8]byte
		if _, err := io.ReadFull(r, ch[:]); err != nil {
			return 0, nil, fmt.Errorf("wav: no data chunk: %w", err)
		}
		id, size := string(ch[0:4]), binary.LittleEndian.Uint32(ch[4:8])
		switch id {
		case "fmt ":
			if size < 16 || size > 1<<16 {
				return 0, nil, errors.New("wav: bad fmt chunk")
			}
			b := make([]byte, size+size%2)
			if _, err := io.ReadFull(r, b); err != nil {
				return 0, nil, fmt.Errorf("wav: %w", err)
			}
			format, chans := binary.LittleEndian.Uint16(b[0:2]), binary.LittleEndian.Uint16(b[2:4])
			bits := binary.LittleEndian.Uint16(b[14:16])
			if format != 1 || chans != 1 || bits != 16 {
				return 0, nil, fmt.Errorf("wav: need 16-bit mono PCM, got format %d, %d channel(s), %d bits", format, chans, bits)
			}
			rate = int(binary.LittleEndian.Uint32(b[4:8]))
		case "data":
			if rate == 0 {
				return 0, nil, errors.New("wav: data before fmt")
			}
			// ffmpeg writes a placeholder size when it can't seek back; read to the end then.
			if size == 0 || size == 0xFFFFFFFF {
				return rate, r, nil
			}
			return rate, io.LimitReader(r, int64(size)), nil
		default:
			if _, err := io.CopyN(io.Discard, r, int64(size)+int64(size%2)); err != nil {
				return 0, nil, fmt.Errorf("wav: %w", err)
			}
		}
	}
}

// biquad is an RBJ-cookbook second-order filter (Butterworth Q).
type biquad struct{ b0, b1, b2, a1, a2, x1, x2, y1, y2 float64 }

func newBiquad(rate int, freq float64, highpass bool) *biquad {
	w := 2 * math.Pi * freq / float64(rate)
	alpha := math.Sin(w) / math.Sqrt2 // sin(w)/(2Q) with Q = 1/√2
	cw := math.Cos(w)
	a0 := 1 + alpha
	q := &biquad{a1: -2 * cw / a0, a2: (1 - alpha) / a0}
	if highpass {
		q.b0, q.b1, q.b2 = (1+cw)/2/a0, -(1+cw)/a0, (1+cw)/2/a0
	} else {
		q.b0, q.b1, q.b2 = (1-cw)/2/a0, (1-cw)/a0, (1-cw)/2/a0
	}
	return q
}

func (q *biquad) step(x float64) float64 {
	y := q.b0*x + q.b1*q.x1 + q.b2*q.x2 - q.a1*q.y1 - q.a2*q.y2
	q.x2, q.x1, q.y2, q.y1 = q.x1, x, q.y1, y
	return y
}

// cueEnvelope marks the 10 ms frames inside any cue. frames 0 sizes it to the last cue.
func cueEnvelope(cues []cue, frames int) []float32 {
	if frames <= 0 {
		for _, c := range cues {
			if e := int(c.end / syncFrame); e > frames {
				frames = e
			}
		}
	}
	env := make([]float32, frames)
	for _, c := range cues {
		a, b := int(c.start/syncFrame), int(c.end/syncFrame)
		if a < 0 {
			a = 0
		}
		if b > frames {
			b = frames
		}
		for i := a; i < b; i++ {
			env[i] = 1
		}
	}
	return env
}

// align measures how a cue envelope lines up with a speech envelope (both 10 ms frames).
func align(speech, cueEnv []float32) SyncResult {
	return newSpeechTrack(speech).align(cueEnv)
}

// span is a run of cue frames [a, b) on the 10 ms grid.
type span struct{ a, b float64 }

func (t *speechTrack) align(cueEnv []float32) SyncResult {
	none := SyncResult{Scale: 1}
	spans := runsOf(cueEnv)
	if len(spans) < syncMinCues || len(t.env) == 0 || t.norm == 0 {
		return none
	}

	// 1. Coarse: normalized cross-correlation on 100 ms frames, once per known frame-rate
	// ratio. Stay with no stretch unless another ratio fits clearly better.
	maxLag := int(syncMaxLag / syncFrame / syncCoarse)
	type peak struct {
		r, p, q float64
		lag     int // 100 ms frames; cue frame t matches speech frame t+lag
	}
	var best peak
	for i, r := range knownRatios {
		lag, p, q := t.coarsePeak(spans, r, maxLag)
		if i == 0 || p > best.p*1.05 {
			best = peak{r, p, q, lag}
		}
	}
	if best.p <= 0 {
		return none
	}

	// 2. Fine: ±150 ms around the coarse peak on the 10 ms grid.
	scaled := make([]span, len(spans))
	for i, s := range spans {
		scaled[i] = span{s.a * best.r, s.b * best.r}
	}
	shift := float64(best.lag*syncCoarse) + t.bestLag(scaled, float64(best.lag*syncCoarse), syncFineRadius)
	scale := best.r

	// 3. Windows: the local offset in eight stretches of the film. They should all agree
	// with one line; a drift the ratios didn't cover shows up as a slope and is folded
	// into the correction. A wide pass finds such a drift; a narrow one confirms the fit.
	corrected := moved(spans, scale, shift)
	agree := 0.5 // too few windows to say either way
	for _, radius := range []int{syncWinWide, syncWinRadius} {
		pts := t.windowOffsets(corrected, radius)
		if len(pts) < 3 {
			break
		}
		alpha, beta, in := fitLine(pts, 15)
		// Any two windows fit some line, so agreement counts the ones beyond a pair.
		agree = float64(max(in-2, 0)) / float64(len(pts)-2)
		first, last := corrected[0].a, corrected[len(corrected)-1].b
		// Only a line most windows sit on is a drift; a few chance agreements are not.
		if in >= 4 && 2*in > len(pts) && math.Abs(beta)*(last-first) > 20 {
			scale *= 1 + beta
			shift = shift*(1+beta) + alpha
			corrected = moved(spans, scale, shift)
		}
	}

	// 4. Confidence: a high, sharp correlation peak that every part of the film agrees with.
	pS := clamp01((best.p - 0.08) / 0.22)
	ratio := 10.0
	if best.q > 1e-9 {
		ratio = best.p / best.q
	}
	sS := clamp01((ratio - 1.1) / 0.6)
	conf := 100 * (0.35*pS + 0.25*sS + 0.40*agree)

	var cueLen, hit float64
	for _, s := range corrected {
		cueLen += s.b - s.a
		hit += t.overlap(s.a, s.b)
	}
	cov := 0.0
	if cueLen > 0 {
		cov = hit / cueLen
	}
	return SyncResult{
		OffsetMs:   -int(math.Round(shift * float64(syncFrame/time.Millisecond))),
		Scale:      math.Round(scale*1e6) / 1e6,
		Confidence: int(math.Round(conf)),
		Coverage:   math.Round(cov*1000) / 1000,
	}
}

// coarsePeak cross-correlates the spans, stretched by r, with the speech on 100 ms frames
// through an FFT, and returns the best lag within ±maxLag, its normalized correlation, and
// the best correlation more than 2 s away from it (how sharp the peak is).
func (t *speechTrack) coarsePeak(spans []span, r float64, maxLag int) (int, float64, float64) {
	t.once.Do(func() {
		t.n = 1
		for t.n < 2*len(t.coarse)+2*maxLag+1 {
			t.n <<= 1
		}
		t.spec = make([]complex128, t.n)
		for i, v := range t.coarse {
			t.spec[i] = complex(v, 0)
		}
		fft(t.spec, false)
	})
	// The cue raster stops where a lag could still put it on the audio.
	length := len(t.coarse) + maxLag
	c := make([]float64, length)
	unit := float64(syncCoarse)
	for _, s := range spans {
		a, b := s.a*r/unit, s.b*r/unit
		for i := int(a); i < length && float64(i) < b; i++ {
			lo, hi := math.Max(a, float64(i)), math.Min(b, float64(i+1))
			if hi > lo {
				c[i] += hi - lo
			}
		}
	}
	cn := zeroMean(c)
	if cn == 0 {
		return 0, 0, 0
	}
	buf := make([]complex128, t.n)
	for i, v := range c {
		buf[i] = complex(v, 0)
	}
	fft(buf, false)
	for i := range buf {
		buf[i] = cmplx.Conj(buf[i]) * t.spec[i]
	}
	fft(buf, true)
	at := func(lag int) float64 {
		if lag < 0 {
			lag += t.n
		}
		return real(buf[lag]) / (cn * t.norm)
	}
	bestLag, p := 0, math.Inf(-1)
	for lag := -maxLag; lag <= maxLag; lag++ {
		if v := at(lag); v > p {
			bestLag, p = lag, v
		}
	}
	q := 0.0
	for lag := -maxLag; lag <= maxLag; lag++ {
		if d := lag - bestLag; d > 20 || d < -20 {
			q = math.Max(q, at(lag))
		}
	}
	return bestLag, p, q
}

// bestLag finds the shift within ±radius frames of center that puts the most cue time on
// speech. When a range of shifts ties (a cue sitting inside a longer speech run), it takes
// the middle of that plateau rather than its edge.
func (t *speechTrack) bestLag(spans []span, center float64, radius int) float64 {
	scores := make([]float64, 2*radius+1)
	top := 0
	for i := range scores {
		d := center + float64(i-radius)
		for _, s := range spans {
			scores[i] += t.overlap(s.a+d, s.b+d)
		}
		if scores[i] > scores[top] {
			top = i
		}
	}
	tol := 0.5 + 0.0005*scores[top]
	lo, hi := top, top
	for lo > 0 && scores[lo-1] >= scores[top]-tol {
		lo--
	}
	for hi < len(scores)-1 && scores[hi+1] >= scores[top]-tol {
		hi++
	}
	return float64(lo+hi)/2 - float64(radius)
}

// windowOffsets splits the corrected cues into syncWindows equal stretches of time and
// finds each stretch's own best shift within ±radius frames. Stretches with fewer than
// four cues are left out.
func (t *speechTrack) windowOffsets(spans []span, radius int) [][2]float64 {
	first, last := spans[0].a, spans[len(spans)-1].b
	width := (last - first) / syncWindows
	if width <= 0 {
		return nil
	}
	var pts [][2]float64
	for w := 0; w < syncWindows; w++ {
		lo, hi := first+float64(w)*width, first+float64(w+1)*width
		var in []span
		for _, s := range spans {
			if mid := (s.a + s.b) / 2; mid >= lo && (mid < hi || w == syncWindows-1) {
				in = append(in, s)
			}
		}
		if len(in) < 4 {
			continue
		}
		pts = append(pts, [2]float64{(lo + hi) / 2, t.bestLag(in, 0, radius)})
	}
	return pts
}

// fitLine fits offset = alpha + beta·time to the window points robustly: every pair of
// points proposes a line (none steeper than a 1% drift, which no real subtitle has), the
// one most points sit within tol of wins (the flatter on a tie), and a least-squares fit
// over its inliers refines it.
func fitLine(pts [][2]float64, tol float64) (alpha, beta float64, inliers int) {
	bestIn := -1
	var bestA, bestB float64
	for i := range pts {
		for j := i + 1; j < len(pts); j++ {
			dx := pts[j][0] - pts[i][0]
			if dx == 0 {
				continue
			}
			b := (pts[j][1] - pts[i][1]) / dx
			if math.Abs(b) > 0.01 {
				continue
			}
			a := pts[i][1] - b*pts[i][0]
			n := 0
			for _, p := range pts {
				if math.Abs(a+b*p[0]-p[1]) <= tol {
					n++
				}
			}
			if n > bestIn || (n == bestIn && math.Abs(b) < math.Abs(bestB)) {
				bestIn, bestA, bestB = n, a, b
			}
		}
	}
	if bestIn < 0 {
		return 0, 0, 0
	}
	var sx, sy, sxx, sxy, k float64
	for _, p := range pts {
		if math.Abs(bestA+bestB*p[0]-p[1]) <= tol {
			sx, sy, sxx, sxy, k = sx+p[0], sy+p[1], sxx+p[0]*p[0], sxy+p[0]*p[1], k+1
		}
	}
	if den := k*sxx - sx*sx; k >= 2 && den != 0 {
		bestB = (k*sxy - sx*sy) / den
		bestA = (sy - bestB*sx) / k
	}
	return bestA, bestB, bestIn
}

// overlap is the speech inside frames [a, b), interpolating partial frames.
func (t *speechTrack) overlap(a, b float64) float64 {
	return t.cum(b) - t.cum(a)
}

func (t *speechTrack) cum(x float64) float64 {
	n := len(t.env)
	if x <= 0 {
		return 0
	}
	if x >= float64(n) {
		return t.prefix[n]
	}
	i := int(x)
	return t.prefix[i] + (x-float64(i))*float64(t.env[i])
}

func runsOf(env []float32) []span {
	var out []span
	start := -1
	for i, v := range env {
		switch {
		case v >= 0.5 && start < 0:
			start = i
		case v < 0.5 && start >= 0:
			out = append(out, span{float64(start), float64(i)})
			start = -1
		}
	}
	if start >= 0 {
		out = append(out, span{float64(start), float64(len(env))})
	}
	return out
}

func moved(spans []span, scale, shift float64) []span {
	out := make([]span, len(spans))
	for i, s := range spans {
		out[i] = span{s.a*scale + shift, s.b*scale + shift}
	}
	return out
}

func downsample(env []float32, k int) []float64 {
	out := make([]float64, (len(env)+k-1)/k)
	for i, v := range env {
		out[i/k] += float64(v) / float64(k)
	}
	return out
}

// zeroMean subtracts the mean in place and returns the vector's norm.
func zeroMean(v []float64) float64 {
	if len(v) == 0 {
		return 0
	}
	var m float64
	for _, x := range v {
		m += x
	}
	m /= float64(len(v))
	var ss float64
	for i := range v {
		v[i] -= m
		ss += v[i] * v[i]
	}
	return math.Sqrt(ss)
}

func clamp01(x float64) float64 { return math.Max(0, math.Min(1, x)) }

// fft is an in-place iterative radix-2 transform; len(a) must be a power of two. The
// inverse is scaled by 1/n.
func fft(a []complex128, inverse bool) {
	n := len(a)
	for i, j := 1, 0; i < n; i++ {
		bit := n >> 1
		for ; j&bit != 0; bit >>= 1 {
			j ^= bit
		}
		j ^= bit
		if i < j {
			a[i], a[j] = a[j], a[i]
		}
	}
	sign := -1.0
	if inverse {
		sign = 1
	}
	for size := 2; size <= n; size <<= 1 {
		ang := sign * 2 * math.Pi / float64(size)
		half := size / 2
		tw := make([]complex128, half)
		for k := range tw {
			tw[k] = cmplx.Rect(1, ang*float64(k))
		}
		for i := 0; i < n; i += size {
			for k := 0; k < half; k++ {
				u, v := a[i+k], a[i+k+half]*tw[k]
				a[i+k], a[i+k+half] = u+v, u-v
			}
		}
	}
	if inverse {
		inv := complex(1/float64(n), 0)
		for i := range a {
			a[i] *= inv
		}
	}
}

// shiftCues moves cues by scale then shiftMs: new = old × scale + shift. Cues that would
// end before the film starts are dropped; one straddling zero starts at zero.
func shiftCues(cues []cue, shiftMs int, scale float64) []cue {
	out := make([]cue, 0, len(cues))
	for _, c := range cues {
		s, e := shiftTime(c.start, shiftMs, scale), shiftTime(c.end, shiftMs, scale)
		if e <= 0 {
			continue
		}
		out = append(out, cue{start: max(s, 0), end: e, text: c.text})
	}
	return out
}

func shiftTime(d time.Duration, shiftMs int, scale float64) time.Duration {
	ms := math.Round(float64(d.Milliseconds())*scale) + float64(shiftMs)
	return time.Duration(ms) * time.Millisecond
}

// shiftSRT applies the same move to SRT text, rewriting only the timing lines so the
// file's line breaks, styling tags and numbering stay exactly as the uploader wrote
// them (parseSRT/formatSRT would flatten each cue to one line). Times before zero clamp
// to zero.
func shiftSRT(text string, shiftMs int, scale float64) string {
	lines := strings.Split(text, "\n")
	for i, ln := range lines {
		m := srtTimeRe.FindStringSubmatchIndex(ln)
		if m == nil {
			continue
		}
		start := srtTime([]string{ln[m[2]:m[3]], ln[m[4]:m[5]], ln[m[6]:m[7]], ln[m[8]:m[9]]})
		end := srtTime([]string{ln[m[10]:m[11]], ln[m[12]:m[13]], ln[m[14]:m[15]], ln[m[16]:m[17]]})
		timing := fmtSRTTime(shiftTime(start, shiftMs, scale)) + " --> " + fmtSRTTime(shiftTime(end, shiftMs, scale))
		lines[i] = ln[:m[0]] + timing + ln[m[1]:]
	}
	return strings.Join(lines, "\n")
}

// writeShifted rewrites a subtitle file in place, moved by scale then shiftMs. It writes
// a temp file beside it and renames it over, so a player never reads half a file.
func writeShifted(path string, shiftMs int, scale float64) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".arrmada-shift-*.srt")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if _, err := tmp.WriteString(shiftSRT(string(raw), shiftMs, scale)); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return err
	}
	_ = os.Chmod(name, 0o644)
	if err := os.Rename(name, path); err != nil {
		os.Remove(name)
		return err
	}
	return nil
}
