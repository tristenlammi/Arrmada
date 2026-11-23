package convert

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/tristenlammi/arrmada/internal/toolrun"
)

// Resumable encoding (the convert_resumable setting, off by default while it's proven). A
// 4K film takes the better part of a day on a CPU and the encode hours may be five a night,
// so a conversion spans several nights. Pausing between them is free (the encoder is frozen
// in place), but a restart — an update, a reboot — kills the frozen encoder, and a
// single-pass encode then starts again from frame one.
//
// So with the setting on, a long film is encoded in pieces of a few minutes, each kept in a
// resume folder in the scratch space as soon as it's complete. After a restart the same film
// is picked first and carries on from the first missing piece: at most one piece of work is
// lost. The folder's manifest.json holds what the first run settled — format, quality, crop,
// encoder, pixel format, the HDR10+ metadata — so the format test and the test encodes
// aren't run again, and can't land on a different quality and orphan the pieces.
//
// The joins are frame-exact. Each piece is cut by frame number (an accurate seek to just
// before its first frame, then an exact frame count), every finished piece's frame count
// is checked, and the pieces are joined with their exact lengths stated — joined by their
// own container timestamps instead, each seam drifted ~20 ms, a second over a long film,
// which would have pulled the sound off the picture. The audio, subtitles and chapters
// come from the original in the final mux, with the video's original start offset
// restored, so sync is exactly the original's. The joined file then goes through the same
// checks as any other encode: the SSIM gate, then finalizeOutput's full-decode check of
// every frame, then the journalled swap.
//
// Only constant-frame-rate, progressive films encoded on the CPU are pieced: frame numbers
// and times map one-to-one there, a deinterlacer needs the frames either side of a seam,
// and hardware encodes are quick enough that a restart costs little. Anything else, and
// anything short, is encoded in one go — as is a film whose pieced encode fails for any
// reason other than its size cap.

// keyResumable is the setting. Off unless you turned it on.
const keyResumable = "convert_resumable"

// chunkSeconds is how much film each piece holds. Five minutes of 4K is about half an
// hour of encoding on a fast CPU — the most a restart can cost. (A var for tests.)
var chunkSeconds = 300.0

// resumeMaxAge is how long an untouched resume folder is kept: a film nobody has come back
// to in a month has been replaced, removed, or skipped.
const resumeMaxAge = 30 * 24 * time.Hour

// The names inside a resume folder.
const (
	resumeManifestName = "manifest.json"
	resumeItemName     = "item"           // the item key, for the folder listing
	resumeHDR10Plus    = "hdr10plus.json" // the HDR10+ metadata read on the first run
)

// chunkSpan is one piece: its first frame and how many frames (0 = to the end).
type chunkSpan struct{ start, frames int }

// frameDuration is one frame's length in seconds from the exact rational rate, or 0.
func frameDuration(mi *MediaInfo) float64 {
	if !validRate(mi.FrameRateRat) {
		return 0
	}
	num, den, _ := strings.Cut(mi.FrameRateRat, "/")
	n, _ := strconv.ParseFloat(num, 64)
	d, _ := strconv.ParseFloat(den, 64)
	return d / n
}

// chunkPlan splits a film into pieces, or returns nil when it should be encoded in one go.
func chunkPlan(mi *MediaInfo) []chunkSpan {
	fd := frameDuration(mi)
	if fd <= 0 || mi.VFR || mi.Interlaced || mi.DurationSec < 2*chunkSeconds {
		return nil
	}
	per := int(math.Round(chunkSeconds / fd))
	n := int(math.Round((mi.DurationSec - mi.VideoStartSec) / fd / float64(per)))
	if n < 2 {
		return nil
	}
	out := make([]chunkSpan, n)
	for i := range out {
		out[i] = chunkSpan{start: i * per, frames: per}
	}
	out[n-1].frames = 0 // the last piece runs to the end, whatever the exact count
	return out
}

// resumeRun is what a job running with the setting on carries (in its ledger record, which
// only its own goroutine touches).
type resumeRun struct {
	cropOn    bool    // the crop setting the plan was settled under
	h10pJSON  string  // the HDR10+ metadata read for this film, "" = none
	attempt   int     // quality-gate tries so far
	priorSecs float64 // encoding time spent before a restart, for the ledger and the forecast
	failed    bool    // a pieced encode failed this run: the rest of the job runs in one go
}

// resumeManifest is a resume folder's manifest.json: the plan the first run settled and the
// source it was settled for.
type resumeManifest struct {
	Version   int     `json:"version"`
	Key       string  `json:"key"`
	Src       string  `json:"src"`
	Size      int64   `json:"size"`     // the source's size on disk
	MtimeNs   int64   `json:"mtime_ns"` // and its modification time
	Kind      string  `json:"kind"`     // "mkv" (the standard encode) | "hevc" (the HDR10+ pipeline's stream)
	Codec     string  `json:"codec"`
	CRF       int     `json:"crf"` // after the test encodes
	Crop      *Crop   `json:"crop,omitempty"`
	CropOn    bool    `json:"crop_on"` // the crop setting then; turning it off discards the pieces
	Encoder   string  `json:"encoder"`
	PixFmt    string  `json:"pix_fmt"`
	HDR10Plus bool    `json:"hdr10plus"` // hdr10plus.json beside it holds the metadata
	Pieces    int     `json:"pieces"`
	Seconds   float64 `json:"chunk_seconds"`
	Attempt   int     `json:"attempt"`     // quality-gate tries so far
	ActiveSec float64 `json:"active_secs"` // encoding time spent so far, pauses excluded

	h10pJSON string // set on load: the job's copy of the HDR10+ metadata
}

// resumeDir is the folder one encode's pieces live in. Its name is a fingerprint of
// everything that decides the pieces' content — the item, the file, the format, the
// quality, the crop, the encoder, the pixel format, the pipeline — so a resumed encode can
// only ever pick up pieces of the same encode. The core count isn't in it: it changes how
// fast x265 works, not what a decoder needs, and each piece is a whole stream of its own.
func resumeDir(scratch, key, src string, mi *MediaInfo, enc Encoder, plan Plan, kind string) string {
	pix := outputPixFmt(mi, plan)
	if kind == "hevc" {
		pix = pixFmt10 // the HDR10+ pipeline is always 10-bit
	}
	h := sha256.New()
	fmt.Fprintf(h, "v2|%s|%s|%d|%s|%d|%s|%s|%s|%s|%g", key, src, mi.SizeBytes, plan.VideoCodec, plan.Quality,
		plan.Crop.filter(), enc.Name, pix, kind, chunkSeconds)
	return filepath.Join(scratch, "resume-"+hex.EncodeToString(h.Sum(nil))[:16])
}

// resumeFolders lists the resume folders in the scratch space (the configured folder and
// the default one) with the item each belongs to ("" when it has no marker).
func (s *Service) resumeFolders(ctx context.Context) map[string]string {
	out := map[string]string{} // dir → item key
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
		for _, e := range entries {
			if !e.IsDir() || !strings.HasPrefix(e.Name(), "resume-") {
				continue
			}
			p := filepath.Join(dir, e.Name())
			if b, err := os.ReadFile(filepath.Join(p, resumeItemName)); err == nil {
				out[p] = strings.TrimSpace(string(b))
			} else {
				out[p] = ""
			}
		}
	}
	return out
}

// resumableKeys is the set of items with an encode in progress on disk.
func (s *Service) resumableKeys(ctx context.Context) map[string]bool {
	out := map[string]bool{}
	for _, key := range s.resumeFolders(ctx) {
		if key != "" {
			out[key] = true
		}
	}
	return out
}

// resumeBytes is how much each item's finished pieces take up in the scratch space —
// space the encode has already claimed, so the space checks don't ask for it twice.
func (s *Service) resumeBytes(ctx context.Context) map[string]int64 {
	out := map[string]int64{}
	for dir, key := range s.resumeFolders(ctx) {
		if key != "" {
			out[key] += piecesBytes(dir)
		}
	}
	return out
}

// resumePieces is how many of an item's pieces are finished, of how many (0, 0 when it has
// no encode in progress).
func (s *Service) resumePieces(ctx context.Context, key string) (done, total int) {
	for dir, k := range s.resumeFolders(ctx) {
		if k != key {
			continue
		}
		m, err := readManifest(dir)
		if err != nil {
			continue
		}
		files, _ := filepath.Glob(filepath.Join(dir, "chunk-*"))
		for _, f := range files {
			if !strings.HasSuffix(f, ".part") {
				done++
			}
		}
		return done, m.Pieces
	}
	return 0, 0
}

// piecesBytes is the size of the finished pieces in a resume folder.
func piecesBytes(dir string) int64 {
	var n int64
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "chunk-") && !strings.HasSuffix(e.Name(), ".part") {
			if fi, err := e.Info(); err == nil {
				n += fi.Size()
			}
		}
	}
	return n
}

// dropResume deletes an item's resume folders, except keep (the one in use, or "").
func (s *Service) dropResume(ctx context.Context, key, keep string) {
	if key == "" {
		return
	}
	for dir, k := range s.resumeFolders(ctx) {
		if k == key && dir != keep {
			_ = os.RemoveAll(dir)
		}
	}
}

// pruneResume runs at startup, before the workers: it removes resume folders nobody has
// touched in resumeMaxAge, any that never got their marker or manifest (debris of a folder
// that was never set up), any whose item no longer has a file in the library — and all of
// them while the setting is off, since nothing would ever pick them up.
func (s *Service) pruneResume(ctx context.Context) {
	on := s.settings.GetBool(ctx, keyResumable, false)
	for dir, key := range s.resumeFolders(ctx) {
		why := ""
		fi, err := os.Stat(dir)
		_, merr := readManifest(dir)
		it, keyOK := parseKey(key)
		switch {
		case !on:
			why = "resumable encoding is off"
		case key == "" || merr != nil || !keyOK:
			why = "it was never set up"
		case err != nil || time.Since(fi.ModTime()) > resumeMaxAge:
			why = "untouched for 30 days"
		case !s.itemHasFile(ctx, it):
			why = "the file is no longer in the library"
		}
		if why == "" {
			continue
		}
		if os.RemoveAll(dir) == nil && s.log != nil {
			s.log.Info("convert: removed a resume folder", "dir", dir, "item", key, "why", why)
		}
	}
}

// itemHasFile reports whether an item still has a library file. Without the module that
// owns it (a test rig) it's assumed to.
func (s *Service) itemHasFile(ctx context.Context, it item) bool {
	if (it.Kind == "episode" && s.series == nil) || (it.Kind != "episode" && s.movies == nil) {
		return true
	}
	_, _, _, ok := s.resolveSource(ctx, it.job())
	return ok
}

func readManifest(dir string) (*resumeManifest, error) {
	b, err := os.ReadFile(filepath.Join(dir, resumeManifestName))
	if err != nil {
		return nil, err
	}
	var m resumeManifest
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	if m.Version != 1 || m.Key == "" {
		return nil, fmt.Errorf("unknown manifest")
	}
	return &m, nil
}

// writeManifest replaces the manifest in one step, so a restart mid-write never leaves half
// of one.
func writeManifest(dir string, m *resumeManifest) error {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(dir, resumeManifestName+".tmp")
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, resumeManifestName))
}

// loadResume looks for an encode of this item that a restart interrupted. It returns the
// manifest when the folder's pieces can be carried on with: the same file, unchanged, and
// the same settled plan under today's settings — the same fingerprint. Anything else is
// discarded (and said so). The HDR10+ metadata is copied out for the job, as the first
// run's read would have left it.
func (s *Service) loadResume(ctx context.Context, job *Job, src, scratch string, mi *MediaInfo, plan Plan, p prefs) *resumeManifest {
	var dir string
	for d, k := range s.resumeFolders(ctx) {
		if k == job.Key {
			dir = d
			break
		}
	}
	if dir == "" {
		return nil
	}
	m, why := s.validResume(dir, job, src, scratch, mi, plan, p)
	if why == "" && m.HDR10Plus {
		jf := filepath.Join(scratch, fmt.Sprintf("h10p-%d.json", job.ID))
		if err := copyFile(filepath.Join(dir, resumeHDR10Plus), jf); err != nil {
			why = "its HDR10+ metadata couldn't be read back"
		} else {
			m.h10pJSON = jf
		}
	}
	if why != "" {
		done, _ := s.resumePieces(ctx, job.Key)
		s.dropResume(ctx, job.Key, "")
		if done > 0 {
			s.event("info", fmt.Sprintf("%s: discarded %d encoded piece%s from before — %s", job.Title, done, plural(done), why))
		}
		return nil
	}
	return m
}

// validResume checks a resume folder against the job, returning why it can't be used.
func (s *Service) validResume(dir string, job *Job, src, scratch string, mi *MediaInfo, plan Plan, p prefs) (*resumeManifest, string) {
	m, err := readManifest(dir)
	if err != nil {
		return nil, "its record of the encode is missing"
	}
	fi, err := os.Stat(src)
	if err != nil || m.Key != job.Key || m.Src != src || m.Size != fi.Size() || m.MtimeNs != fi.ModTime().UnixNano() {
		return nil, "the file has changed since"
	}
	plan.VideoCodec, plan.Quality, plan.Crop = m.Codec, m.CRF, m.Crop
	// The crop setting, AV1 being allowed, and image subtitles now kept (they're placed on
	// the full frame, so a film keeping them keeps its bars) all change what a fresh run
	// would settle on.
	if m.CropOn != p.crop || (m.Codec == "av1" && !p.allowAV1) || (m.Crop != nil && keepsImageSubs(mi, plan)) {
		return nil, "the conversion settings have changed since"
	}
	enc := cpuEncoder(m.Codec)
	// An HDR film the CPU started stays on the CPU, even now the GPU may take HDR (gpuhdr.go):
	// its pieces are the CPU's, and process.go finishes it there.
	if m.Kind != "hevc" && !(isHDR(mi.EncodeHDR()) && m.Encoder == enc.Name) {
		enc = s.encoderChoice(mi, m.Codec, p.useGPU)
	}
	if enc.Kind != "cpu" || enc.Name != m.Encoder || resumeDir(filepath.Dir(dir), job.Key, src, mi, enc, plan, m.Kind) != dir {
		return nil, "the conversion settings have changed since"
	}
	if filepath.Dir(dir) != scratch {
		return nil, "the transcode folder has changed since"
	}
	return m, ""
}

// startResumeDir sets up (or takes up again) an encode's resume folder: the item marker,
// the manifest and, for the HDR10+ pipeline, the metadata.
func (s *Service) startResumeDir(job *Job, dir, src string, mi *MediaInfo, enc Encoder, plan Plan, kind string, pieces int) error {
	rr := s.record(job).resume
	if rr == nil {
		return fmt.Errorf("not a resumable encode")
	}
	fi, err := os.Stat(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, resumeItemName), []byte(job.Key), 0o644); err != nil {
		return err
	}
	pix := outputPixFmt(mi, plan)
	if kind == "hevc" {
		pix = pixFmt10
		if rr.h10pJSON == "" {
			return fmt.Errorf("the HDR10+ metadata is missing")
		}
		if err := copyFile(rr.h10pJSON, filepath.Join(dir, resumeHDR10Plus)); err != nil {
			return err
		}
	}
	m := &resumeManifest{Version: 1, Key: job.Key, Src: src, Size: fi.Size(), MtimeNs: fi.ModTime().UnixNano(),
		Kind: kind, Codec: plan.VideoCodec, CRF: plan.Quality, Crop: plan.Crop, CropOn: rr.cropOn,
		Encoder: enc.Name, PixFmt: pix, HDR10Plus: kind == "hevc", Pieces: pieces, Seconds: chunkSeconds,
		Attempt: rr.attempt, ActiveSec: s.encodedSecs(job)}
	if old, err := readManifest(dir); err == nil {
		m.ActiveSec = max(m.ActiveSec, old.ActiveSec)
	}
	if err := writeManifest(dir, m); err != nil {
		return err
	}
	now := time.Now()
	_ = os.Chtimes(dir, now, now) // in use: not stale
	return nil
}

// encodedSecs is the job's encoding time including any from before a restart.
func (s *Service) encodedSecs(job *Job) float64 {
	prior := 0.0
	if rr := s.record(job).resume; rr != nil {
		prior = rr.priorSecs
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return prior + job.activeSecs
}

// piecedSpans is the pieces this encode is cut into, or nil when it runs in one go: the
// setting is off for this job, it's a copy or a hardware encode, or the film can't be cut
// frame-exactly.
func (s *Service) piecedSpans(job *Job, mi *MediaInfo, enc Encoder, plan Plan) []chunkSpan {
	// Keeping Dolby Vision encodes in one go: its RPU goes back in frame for frame, and that
	// pipeline isn't proven across joined pieces (dv.go).
	if job == nil || enc.Kind != "cpu" || plan.VideoCodec == "" || plan.KeepDV {
		return nil
	}
	if rr := s.record(job).resume; rr == nil || rr.failed {
		return nil
	}
	return chunkPlan(mi)
}

// waitsForRoom reports whether a job ended only to wait for space — scratch, the library
// disk or the originals hold. Its finished pieces are kept for when there's room; every
// other ending discards them.
func waitsForRoom(state JobState, skipKind string) bool {
	if state != StateSkipped {
		return false
	}
	switch skipKind {
	case SkipNoScratch, SkipLibraryFull, SkipHoldFull:
		return true
	}
	return false
}

// piecesFailed gives up on a pieced encode that failed for a reason other than its size
// cap or a stop: its pieces go, and the caller encodes the film in one go instead.
func (s *Service) piecesFailed(ctx context.Context, job *Job, err error) {
	s.log.Warn("convert: pieced encode failed — encoding in one go", "title", job.Title, "err", err)
	s.event("warn", fmt.Sprintf("%s: encoding in pieces failed (%v) — encoding it in one go instead", job.Title, err))
	s.dropResume(ctx, job.Key, "")
	if rr := s.record(job).resume; rr != nil {
		rr.failed = true // and any quality-gate retry too: it would fail the same way
	}
	s.update(job, func(j *Job) { j.Progress = 0 })
}

// chunkFile is one finished piece.
type chunkFile struct {
	path   string
	frames int // exact; 0 for the last piece (it needs no stated length)
}

// encodeChunks encodes the pieces of a film into dir, skipping the ones already there.
// vargs are the video encode options, format/ext the pieces' container. videoCap > 0 stops
// the encode with errTooBig once the pieces together reach it.
func (s *Service) encodeChunks(ctx context.Context, job *Job, src, dir string, mi *MediaInfo, spans []chunkSpan,
	vargs []string, format, ext string, videoCap int64) ([]chunkFile, error) {
	fd := frameDuration(mi)
	totalSec := mi.DurationSec - mi.VideoStartSec
	cores := s.cpuCores(ctx)
	vargs = withStartPTS(vargs)
	var files []chunkFile
	var bytes int64
	onDisk, encoded := 0, 0
	for i, sp := range spans {
		final := filepath.Join(dir, fmt.Sprintf("chunk-%04d%s", i+1, ext))
		cf := chunkFile{path: final, frames: sp.frames}
		if fi, err := os.Stat(final); err == nil {
			// Finished before a restart: a piece only gets its name once its frames are
			// counted, so one that's there is whole.
			bytes += fi.Size()
			files = append(files, cf)
			onDisk++
			if videoCap > 0 && bytes >= videoCap {
				return nil, errTooBig
			}
			continue
		}
		if encoded == 0 && onDisk > 0 {
			s.event("info", fmt.Sprintf("%s: resuming from piece %d of %d — %d already encoded before the restart", job.Title, i+1, len(spans), onDisk))
		}
		if !s.waitAllowed(ctx, job) {
			return nil, ctx.Err()
		}
		part := final + ".part"
		args := []string{"-y", "-hide_banner", "-nostats", "-loglevel", "warning", "-progress", "pipe:1", "-threads", strconv.Itoa(cores)}
		if sp.start > 0 {
			// Half a frame before the piece's first frame: the accurate seek then starts
			// exactly on it, whatever the rounding of the timestamps.
			at := mi.VideoStartSec + (float64(sp.start)-0.5)*fd
			args = append(args, "-ss", strconv.FormatFloat(at, 'f', 6, 64))
		}
		// Every decoded frame once, as it is: left to choose, ffmpeg holds a raw HEVC piece to
		// a constant rate from time zero, so a film whose video starts a frame after its audio
		// got its first frame doubled — the whole first piece a frame late (measured: SSIM
		// 0.925 there, 0.996 everywhere else). Passed through as they are, though, a seeked
		// piece's frames sit half a frame off the encoder's clock and round two to a tick, so
		// each piece's clock starts at its first frame (withStartPTS). The pieces' timing is
		// set at the join anyway.
		args = append(args, "-i", src, "-map", fmt.Sprintf("0:v:%d", mi.VideoIndex), "-an", "-sn", "-dn", "-fps_mode", "passthrough")
		secs := totalSec - float64(sp.start)*fd
		if sp.frames > 0 {
			args = append(args, "-frames:v", strconv.Itoa(sp.frames))
			secs = float64(sp.frames) * fd
		}
		args = append(args, vargs...)
		args = append(args, "-f", format, part)
		base := float64(sp.start) * fd / totalSec
		s.update(job, func(j *Job) { j.Progress = base })
		if err := s.runWithProgressSpan(ctx, job, args, secs, base, secs/totalSec); err != nil {
			_ = os.Remove(part)
			return nil, fmt.Errorf("piece %d of %d: %w", i+1, len(spans), err)
		}
		if sp.frames > 0 {
			if got := s.countFrames(ctx, part); got != sp.frames {
				_ = os.Remove(part)
				return nil, fmt.Errorf("piece %d came out %d frames instead of %d", i+1, got, sp.frames)
			}
		}
		if err := os.Rename(part, final); err != nil {
			return nil, err
		}
		encoded++
		bytes += fileSize(final)
		files = append(files, cf)
		s.notePieceDone(job, dir)
		if videoCap > 0 && bytes >= videoCap {
			return nil, errTooBig
		}
	}
	if encoded == 0 {
		s.event("info", fmt.Sprintf("%s: all %d pieces were already encoded before the restart — joining them", job.Title, len(spans)))
	}
	return files, nil
}

// notePieceDone records the encoding time so far in the manifest after each piece, so a
// resumed job's ledger row (and the forecast learning from it) counts the whole encode.
func (s *Service) notePieceDone(job *Job, dir string) {
	m, err := readManifest(dir)
	if err != nil {
		return
	}
	m.ActiveSec = max(m.ActiveSec, s.encodedSecs(job))
	_ = writeManifest(dir, m)
}

// withStartPTS starts a piece's clock at its first frame: setpts=PTS-STARTPTS at the head
// of the video filter chain (added to the options' -vf, or as one).
func withStartPTS(vargs []string) []string {
	const f = "setpts=PTS-STARTPTS"
	out := append([]string(nil), vargs...)
	for i := 0; i+1 < len(out); i++ {
		if out[i] == "-vf" {
			out[i+1] = f + "," + out[i+1]
			return out
		}
	}
	return append([]string{"-vf", f}, out...)
}

// countFrames counts a file's video frames from its packets — no decoding, so it's quick.
func (s *Service) countFrames(ctx context.Context, path string) int {
	out, err := toolrun.Command(ctx, toolrun.Normal, s.ffprobe, "-v", "error", "-count_packets", "-select_streams", "v:0",
		"-show_entries", "stream=nb_read_packets", "-of", "csv=p=0", path).Output()
	if err != nil {
		return -1
	}
	n, err := strconv.Atoi(strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(string(out)), ",")))
	if err != nil {
		return -1
	}
	return n
}

// concatList writes the join list for the pieces, stating each one's exact length (all but
// the last, which needs none). Names are relative: the list sits beside the pieces.
func concatList(dir string, files []chunkFile, fd float64) (string, error) {
	var b strings.Builder
	for i, f := range files {
		fmt.Fprintf(&b, "file '%s'\n", filepath.Base(f.path))
		if i < len(files)-1 {
			fmt.Fprintf(&b, "duration %.9f\n", float64(f.frames)*fd)
		}
	}
	list := filepath.Join(dir, "join.txt")
	return list, os.WriteFile(list, []byte(b.String()), 0o644)
}

// encodeResumable is the pieced form of the standard encode: video in pieces, then one mux
// of the joined pieces with the original's kept tracks, metadata and chapters.
func (s *Service) encodeResumable(ctx context.Context, job *Job, src, dst string, mi *MediaInfo, enc Encoder, plan Plan, spans []chunkSpan) error {
	dir := resumeDir(filepath.Dir(dst), job.Key, src, mi, enc, plan, "mkv")
	s.dropResume(ctx, job.Key, dir) // an earlier attempt at other settings is no use now
	if err := s.startResumeDir(job, dir, src, mi, enc, plan, "mkv", len(spans)); err != nil {
		return err
	}
	cores := s.cpuCores(ctx)
	videoCap := int64(0)
	if limit := sizeCap(mi, plan); limit > 0 {
		videoCap = max(limit-keptAudioBytes(mi, plan), 1<<20)
	}
	files, err := s.encodeChunks(ctx, job, src, dir, mi, spans, videoArgs(enc, mi, plan, false, cores, s.noNumaPools), "matroska", ".mkv", videoCap)
	if err != nil {
		if errors.Is(err, errTooBig) {
			_ = os.RemoveAll(dir)
		}
		return err
	}
	list, err := concatList(dir, files, frameDuration(mi))
	if err != nil {
		return err
	}
	args := []string{"-y", "-hide_banner", "-loglevel", "error"}
	args = append(args, startOffset(mi)...)
	args = append(args, "-f", "concat", "-safe", "0", "-i", list, "-i", src, "-map", "0:v:0")
	args = append(args, trackArgs(mi, plan, 1)...)
	args = append(args, "-map_metadata", "1", "-map_chapters", "1", "-c:v", "copy")
	args = append(args, clearStatsTags("v:0")...)
	args = append(args, dst)
	if out, err := s.tool(ctx, job, toolrun.Background, s.ffmpeg, args...).CombinedOutput(); err != nil {
		return fmt.Errorf("joining the pieces: %v (%s)", err, tailStr(out))
	}
	// The pieces stay until the job finishes: a restart during the quality check then
	// rebuilds the film from them in minutes rather than encoding it again.
	return nil
}

// encodeHEVCPieces is the pieced form of the HDR10+ pipeline's stream encode: raw HEVC
// pieces joined by plain concatenation into dst. Without B-frames each piece is in display
// order, frame for frame, and starts with its own parameter sets and a keyframe.
func (s *Service) encodeHEVCPieces(ctx context.Context, job *Job, src, dst string, mi *MediaInfo, plan Plan, vargs []string, spans []chunkSpan, sizeLimit int64) error {
	enc := cpuEncoder("hevc")
	dir := resumeDir(filepath.Dir(dst), job.Key, src, mi, enc, plan, "hevc")
	s.dropResume(ctx, job.Key, dir)
	if err := s.startResumeDir(job, dir, src, mi, enc, plan, "hevc", len(spans)); err != nil {
		return err
	}
	files, err := s.encodeChunks(ctx, job, src, dir, mi, spans, vargs, "hevc", ".hevc", sizeLimit)
	if err != nil {
		if errors.Is(err, errTooBig) {
			_ = os.RemoveAll(dir)
		}
		return err
	}
	if err := concatFiles(dst, files); err != nil {
		return err
	}
	// Joined: the pieces are freed now — the injection and mux that follow take minutes,
	// and holding three copies of a 4K stream would need too much space.
	return os.RemoveAll(dir)
}

// startOffset restores the original's video start offset on a rebuilt video input.
func startOffset(mi *MediaInfo) []string {
	if mi.VideoStartSec <= 0 {
		return nil
	}
	return []string{"-itsoffset", strconv.FormatFloat(mi.VideoStartSec, 'f', 6, 64)}
}

// concatFiles appends the pieces, in order, into one file.
func concatFiles(dst string, files []chunkFile) error {
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	for _, f := range files {
		in, err := os.Open(f.path)
		if err != nil {
			out.Close()
			return err
		}
		_, err = io.Copy(out, in)
		in.Close()
		if err != nil {
			out.Close()
			return err
		}
	}
	return out.Close()
}

// copyFile copies a small file (the HDR10+ metadata) in one step.
func copyFile(from, to string) error {
	b, err := os.ReadFile(from)
	if err != nil {
		return err
	}
	tmp := to + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, to)
}
