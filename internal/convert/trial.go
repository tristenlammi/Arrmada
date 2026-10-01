package convert

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// The side-by-side test: encode the same few clips of a file as HEVC and as AV1, score each
// against the original, and pick the smaller one at the same quality. It's how a file's
// format is decided when AV1 is allowed, and — keeping the clips — what the Compare button
// shows, so the choice can be checked by eye on your own footage.

// trialClip is one stretch of the source the test encodes.
type trialClip struct{ start, dur float64 }

// trialClips spreads a few short clips across the runtime, skipping the very start and end
// (logos, credits). A short file is tested whole.
func trialClips(dur float64) []trialClip {
	const clip = 10.0
	if dur <= 0 {
		return nil
	}
	if dur <= 180 {
		return []trialClip{{0, minf(dur, 60)}}
	}
	out := make([]trialClip, 0, 3)
	for _, f := range []float64{0.2, 0.5, 0.8} {
		out = append(out, trialClip{dur * f, clip})
	}
	return out
}

// TrialSide is one format's result.
type TrialSide struct {
	Codec    string  `json:"codec"`
	Encoder  string  `json:"encoder"`
	Bytes    int64   `json:"bytes"`     // all clips together
	SSIM     float64 `json:"ssim"`      // mean score vs the original
	EstBytes int64   `json:"est_bytes"` // the whole file, extrapolated (video only)
}

// TrialResult is the outcome of a side-by-side test.
type TrialResult struct {
	Key      string    `json:"key"`
	Title    string    `json:"title"`
	SrcBytes int64     `json:"src_bytes"`
	Clips    int       `json:"clips"`
	Seconds  float64   `json:"seconds"`
	HEVC     TrialSide `json:"hevc"`
	AV1      TrialSide `json:"av1"`
	Pick     string    `json:"pick"`
	Why      string    `json:"why"`
	Files    []string  `json:"files,omitempty"` // kept clips, for Compare
}

// AV1 has to beat HEVC clearly to be chosen: at least 5% smaller, and no more than a hair
// worse on the quality score. Anything closer isn't worth giving up HEVC's wider playback.
const (
	av1MinSaving  = 0.95  // AV1 bytes must be ≤ 95% of HEVC's
	av1MaxSSIMGap = 0.002 // and its score within this of HEVC's
)

func pickCodec(hevc, av1 TrialSide) (string, string) {
	if hevc.Bytes <= 0 {
		return "av1", "HEVC couldn't be tested — using AV1"
	}
	if av1.Bytes <= 0 {
		return "hevc", "AV1 couldn't be tested — using HEVC"
	}
	ratio := float64(av1.Bytes) / float64(hevc.Bytes)
	pct := int((1 - ratio) * 100)
	switch {
	case av1.SSIM < hevc.SSIM-av1MaxSSIMGap:
		return "hevc", fmt.Sprintf("HEVC looked better (SSIM %.4f vs AV1 %.4f) — converting to HEVC", hevc.SSIM, av1.SSIM)
	case ratio > av1MinSaving:
		if pct > 0 {
			return "hevc", fmt.Sprintf("AV1 was only %d%% smaller at the same quality — converting to HEVC", pct)
		}
		return "hevc", fmt.Sprintf("AV1 came out %d%% bigger at the same quality — converting to HEVC", -pct)
	}
	return "av1", fmt.Sprintf("AV1 was %d%% smaller at the same quality (SSIM %.4f vs %.4f) — converting to AV1", pct, av1.SSIM, hevc.SSIM)
}

// trial runs the side-by-side test. keepDir, when set, keeps the encoded clips (plus a copy
// of each original clip) there for Compare; otherwise they're deleted as it goes.
func (s *Service) trial(ctx context.Context, job *Job, src string, mi *MediaInfo, plan Plan, p prefs, keepDir string) (TrialResult, error) {
	res := TrialResult{SrcBytes: mi.SizeBytes}
	clips := trialClips(mi.DurationSec)
	if len(clips) == 0 {
		return res, fmt.Errorf("the file's length is unknown")
	}
	dir := keepDir
	if dir == "" {
		dir = filepath.Join(s.activeScratch(ctx), fmt.Sprintf("trial-%d", time.Now().UnixNano()))
		defer os.RemoveAll(dir)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return res, err
	}
	cores := s.cpuCores(ctx)
	total := float64(len(clips) * 2)
	step := 0.0
	run := func(codec string) TrialSide {
		side := TrialSide{Codec: codec}
		enc := s.encoderChoice(mi, codec, p.useGPU)
		side.Encoder = enc.Label
		cp := plan
		cp.VideoCodec, cp.Quality = codec, maxQualityCRF(codec)
		var scores []float64
		for i, c := range clips {
			out := filepath.Join(dir, fmt.Sprintf("%s-%d.mkv", codec, i+1))
			if err := s.encodeClip(ctx, job, src, out, mi, enc, cp, c, cores); err != nil {
				if enc.Hardware && ctx.Err() == nil { // one go on the CPU before giving up
					enc = cpuEncoder(codec)
					side.Encoder = enc.Label
					err = s.encodeClip(ctx, job, src, out, mi, enc, cp, c, cores)
				}
				if err != nil {
					step++
					continue
				}
			}
			side.Bytes += fileSize(out)
			if di, err := probe(ctx, s.ffprobe, out); err == nil && di.Width > 0 {
				if sc, err := s.ssimWindow(ctx, out, src, 0, c.start, c.dur, di.Width, di.Height, di.FrameRateRat); err == nil {
					scores = append(scores, sc)
				}
			}
			if keepDir == "" {
				_ = os.Remove(out)
			}
			step++
			if job != nil {
				pr := step / total
				s.update(job, func(j *Job) { j.Progress = pr })
			}
		}
		if len(scores) > 0 {
			var sum float64
			for _, v := range scores {
				sum += v
			}
			side.SSIM = sum / float64(len(scores))
		}
		return side
	}
	res.HEVC = run("hevc")
	if ctx.Err() != nil {
		return res, ctx.Err()
	}
	res.AV1 = run("av1")
	if ctx.Err() != nil {
		return res, ctx.Err()
	}
	for _, c := range clips {
		res.Seconds += c.dur
	}
	res.Clips = len(clips)
	if res.HEVC.Bytes <= 0 && res.AV1.Bytes <= 0 {
		return res, errNoClip
	}
	scale := mi.DurationSec / res.Seconds
	res.HEVC.EstBytes = int64(float64(res.HEVC.Bytes) * scale)
	res.AV1.EstBytes = int64(float64(res.AV1.Bytes) * scale)
	res.Pick, res.Why = pickCodec(res.HEVC, res.AV1)
	if keepDir != "" {
		// The original clips, for comparing by eye. A stream copy starts on the nearest
		// keyframe, so it may begin a moment before the encoded clip.
		for i, c := range clips {
			out := filepath.Join(dir, fmt.Sprintf("original-%d.mkv", i+1))
			_ = exec.CommandContext(ctx, s.ffmpeg, "-y", "-hide_banner", "-loglevel", "error",
				"-ss", strconv.FormatFloat(c.start, 'f', 2, 64), "-t", strconv.FormatFloat(c.dur, 'f', 2, 64),
				"-i", src, "-map", fmt.Sprintf("0:v:%d", mi.VideoIndex), "-map", "0:a:0?", "-c", "copy", out).Run()
		}
		entries, _ := os.ReadDir(dir)
		for _, e := range entries {
			res.Files = append(res.Files, e.Name())
		}
		sort.Strings(res.Files)
	}
	return res, nil
}

// encodeClip encodes one test clip — video only, the exact settings a real conversion uses.
func (s *Service) encodeClip(ctx context.Context, job *Job, src, out string, mi *MediaInfo, enc Encoder, plan Plan, c trialClip, cores int) error {
	args := []string{"-y", "-hide_banner", "-nostats", "-loglevel", "error", "-threads", strconv.Itoa(cores)}
	args = append(args, globalArgs(enc, false, s.vaapiDev(ctx))...)
	if c.start > 0 {
		args = append(args, "-ss", strconv.FormatFloat(c.start, 'f', 3, 64))
	}
	args = append(args, "-t", strconv.FormatFloat(c.dur, 'f', 3, 64), "-i", src,
		"-map", fmt.Sprintf("0:v:%d", mi.VideoIndex), "-an", "-sn")
	args = append(args, videoArgs(enc, mi, plan, false, cores, s.noNumaPools)...)
	args = append(args, out)
	cmd := exec.CommandContext(ctx, s.ffmpeg, args...)
	lowPriority(cmd)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return err
	}
	pid := cmd.Process.Pid
	applyNice(pid, encodeNice)
	s.trackProc(job, pid)
	defer s.untrackProc(job, pid)
	if err := cmd.Wait(); err != nil {
		return fmt.Errorf("%v: %s", err, tailStr([]byte(stderr.String())))
	}
	return nil
}

func minf(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}

// --- remembered choices ---------------------------------------------------------------

// choiceStore remembers each file's test outcome (convert_choices), keyed by path + size so
// a replaced file is tested again.
type choiceStore struct{ db *sql.DB }

func (c *choiceStore) get(ctx context.Context, path string, size int64) (string, bool) {
	var codec string
	err := c.db.QueryRowContext(ctx, `SELECT codec FROM convert_choices WHERE path = ? AND size_bytes = ?`, path, size).Scan(&codec)
	return codec, err == nil && (codec == "hevc" || codec == "av1")
}

func (c *choiceStore) put(ctx context.Context, path string, size int64, codec, detail string) {
	_, _ = c.db.ExecContext(ctx,
		`INSERT INTO convert_choices (path, size_bytes, codec, detail, decided_at) VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT(path) DO UPDATE SET size_bytes = excluded.size_bytes, codec = excluded.codec,
		   detail = excluded.detail, decided_at = excluded.decided_at`,
		path, size, codec, detail, time.Now().Unix())
}

// --- Compare ------------------------------------------------------------------------------

// compareState is the one Compare that may run at a time.
type compareState struct {
	mu      sync.Mutex
	running bool
	key     string
	title   string
	started int64
	result  *TrialResult
	err     string
	dir     string
}

// CompareStatus is what the Compare panel polls.
type CompareStatus struct {
	Running bool         `json:"running"`
	Key     string       `json:"key,omitempty"`
	Title   string       `json:"title,omitempty"`
	Started int64        `json:"started,omitempty"`
	Result  *TrialResult `json:"result,omitempty"`
	Error   string       `json:"error,omitempty"`
}

// StartCompare runs the side-by-side test on one file in the background, keeping the clips.
// Only one runs at a time.
func (s *Service) StartCompare(ctx context.Context, key string) error {
	it, ok := parseKey(key)
	if !ok {
		return fmt.Errorf("invalid item")
	}
	probeJob := &Job{Kind: it.Kind, MovieID: it.MovieID, SeriesID: it.SeriesID, Season: it.Season, Episode: it.Episode}
	src, title, origLang, ok := s.resolveSource(ctx, probeJob)
	if !ok {
		return fmt.Errorf("this title has no file")
	}
	if it.Kind == "episode" {
		title = s.episodeTitle(ctx, it)
	}
	s.compare.mu.Lock()
	if s.compare.running {
		s.compare.mu.Unlock()
		return fmt.Errorf("a comparison is already running")
	}
	dir := filepath.Join(s.activeScratch(ctx), "compare")
	_ = os.RemoveAll(dir)
	s.compare.running, s.compare.key, s.compare.title = true, key, title
	s.compare.started, s.compare.result, s.compare.err, s.compare.dir = time.Now().Unix(), nil, "", dir
	s.compare.mu.Unlock()

	go func() {
		bg := context.WithoutCancel(ctx)
		bg, cancel := context.WithTimeout(bg, 3*time.Hour)
		defer cancel()
		var res TrialResult
		mi, err := probe(bg, s.ffprobe, src)
		if err == nil {
			p := s.prefs(bg)
			plan, _ := p.planFor(mi, src, origLang, nil)
			res, err = s.trial(bg, nil, src, mi, plan, p, dir)
			res.Key, res.Title = key, title
			if err == nil && mi.EncodeHDR() == "HDR10+" {
				res.Why += " · this file carries HDR10+, so it will convert to HEVC either way"
			}
		}
		s.compare.mu.Lock()
		s.compare.running = false
		if err != nil {
			s.compare.err = err.Error()
		} else {
			s.compare.result = &res
		}
		s.compare.mu.Unlock()
		if err == nil {
			s.event("info", fmt.Sprintf("Compared HEVC and AV1 on %s: %s", title, res.Why))
		}
	}()
	return nil
}

// CompareStatus reports the current or last comparison.
func (s *Service) CompareStatus() CompareStatus {
	s.compare.mu.Lock()
	defer s.compare.mu.Unlock()
	return CompareStatus{Running: s.compare.running, Key: s.compare.key, Title: s.compare.title,
		Started: s.compare.started, Result: s.compare.result, Error: s.compare.err}
}

// CompareFile resolves a kept comparison clip by name (no paths — just a file in the folder).
func (s *Service) CompareFile(name string) (string, bool) {
	s.compare.mu.Lock()
	dir := s.compare.dir
	s.compare.mu.Unlock()
	if dir == "" || name == "" || name != filepath.Base(name) || strings.HasPrefix(name, ".") {
		return "", false
	}
	path := filepath.Join(dir, name)
	if fi, err := os.Stat(path); err != nil || fi.IsDir() {
		return "", false
	}
	return path, true
}
