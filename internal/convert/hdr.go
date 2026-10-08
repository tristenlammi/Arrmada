package convert

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// The HDR10+ pipeline. HDR10+ is dynamic metadata — per-scene brightness instructions — and
// the bundled x265 isn't built to embed it during an encode, so it's carried around the
// encode instead: extract it from the source, encode the picture (with its static HDR10
// base), inject the metadata back into the new stream, then mux it with the kept tracks.
// HEVC only: hdr10plus_tool reads and writes HEVC, which is why HDR10+ files always
// convert to HEVC rather than AV1.
//
// Dolby Vision is not carried at all — only its HDR10/HLG base layer — so there is no
// Dolby Vision pipeline; those files go through the standard encode.

// Pulling the HDR10+ dynamic metadata from a file's HEVC stream into a JSON file is two
// steps. It looks before it reads: hasHDR10Plus checks the first 100 frames, and only then
// does readHDR10Plus read the whole stream for the full metadata. Without that, checking a
// 60 GB remux meant streaming the entire file off the array just to learn it had none. The
// caller checks scratch space in between, so the whole-file read never runs for a file the
// HDR10+ pipeline couldn't fit.

// hasHDR10Plus returns an error (and writes nothing usable) if the file carries no HDR10+.
func (s *Service) hasHDR10Plus(ctx context.Context, src, jsonOut string) error {
	return s.extractHDR10Plus(ctx, src, jsonOut, 100)
}

// readHDR10Plus reads the metadata for the whole film into jsonOut.
func (s *Service) readHDR10Plus(ctx context.Context, src, jsonOut string) error {
	return s.extractHDR10Plus(ctx, src, jsonOut, 0)
}

// extractHDR10Plus extracts up to limit frames' metadata (0 = all of them).
func (s *Service) extractHDR10Plus(ctx context.Context, src, jsonOut string, limit int) error {
	ff := exec.CommandContext(ctx, s.ffmpeg, "-loglevel", "error", "-i", src,
		"-map", "0:v:0", "-c", "copy", "-bsf:v", "hevc_mp4toannexb", "-f", "hevc", "-")
	args := []string{"extract", "-o", jsonOut}
	if limit > 0 {
		args = append(args, "--limit", strconv.Itoa(limit))
	}
	h10 := exec.CommandContext(ctx, s.hdr10plusTool, append(args, "-")...)
	// With a frame limit the tool stops reading early on purpose, so ffmpeg then hits a
	// broken pipe — expected, not a failure. Reading everything, ffmpeg must succeed:
	// a stream cut short would leave metadata for only part of the film.
	if err := pipeCommands(ff, h10, limit > 0); err != nil {
		return err
	}
	if fi, err := os.Stat(jsonOut); err != nil || fi.Size() < 8 {
		return fmt.Errorf("no HDR10+ metadata found")
	}
	return nil
}

// encodeHDR10Plus runs the HDR10+ pipeline into dst: encode the video to a raw HEVC stream
// (carrying the HDR10 static base), interleave the extracted dynamic metadata back in, and
// mux it with the original's kept audio/subtitles.
func (s *Service) encodeHDR10Plus(ctx context.Context, job *Job, src, dst, scratch string, mi *MediaInfo, plan Plan, jsonPath string) error {
	if s.hdr10plusTool == "" {
		return fmt.Errorf("hdr10plus_tool not available")
	}
	stem := filepath.Join(scratch, fmt.Sprintf("h10p-enc-%d", job.ID))
	encoded, injected := stem+".hevc", stem+".inj.hevc"
	defer os.Remove(encoded)
	defer os.Remove(injected)

	// The video stream gets what's left of the size cap after the copied audio. Hitting it
	// leaves a truncated stream, which is never injected or muxed.
	videoCap := int64(0)
	if limit := sizeCap(mi, plan); limit > 0 {
		videoCap = max(limit-keptAudioBytes(mi, plan), 1<<20)
	}
	if err := s.encodeHEVCStream(ctx, job, src, encoded, mi, plan, videoCap); err != nil {
		return fmt.Errorf("encode: %w", err)
	}
	if videoCap > 0 && fileSize(encoded) >= videoCap {
		return errTooBig
	}
	if out, err := exec.CommandContext(ctx, s.hdr10plusTool, "inject", "-i", encoded, "-j", jsonPath, "-o", injected).CombinedOutput(); err != nil {
		return fmt.Errorf("inject HDR10+: %v (%s)", err, tailStr(out))
	}
	_ = os.Remove(encoded) // each stage frees the last: a 4K stream is tens of gigabytes
	if err := s.remuxVideoStream(ctx, injected, src, dst, mi, plan); err != nil {
		return fmt.Errorf("remux: %w", err)
	}
	return nil
}

// encodeHEVCStream re-encodes only the video to a raw HEVC 10-bit Annex-B stream, carrying
// the HDR10 base metadata. Live progress comes from the -progress pipe.
//
// NO frame-rate flattening here, deliberately: the HDR10+ JSON is extracted from the SOURCE
// frame for frame, and -fps_mode cfr changes the frame count, so every subsequent frame's
// metadata would land on the wrong picture. (The remux stamps the source's exact average
// rate, so timing still lines up.)
//
// And NO B-frames. A raw HEVC stream carries no timestamps, so the remux has to invent them
// from the frame rate — in DECODE order. With B-frames, decode order isn't display order:
// the result plays frames out of sequence and drops a couple (measured: 478 of 480 frames,
// SSIM 0.88), so no HDR10+ file could ever pass the quality check. Without them the stream
// is in display order and comes out exact; it costs roughly 15% in size, on HDR10+ titles
// only.
func (s *Service) encodeHEVCStream(ctx context.Context, job *Job, src, dst string, mi *MediaInfo, plan Plan, sizeLimit int64) error {
	crf := plan.Quality
	if crf <= 0 {
		crf = maxQualityCRF("hevc", mi)
	}
	cores := s.cpuCores(ctx)
	args := []string{"-y", "-hide_banner", "-nostats", "-loglevel", "warning", "-progress", "pipe:1",
		"-threads", strconv.Itoa(cores),
		"-i", src, "-map", fmt.Sprintf("0:v:%d", mi.VideoIndex), "-an", "-sn"}
	if vf := swFilterChain(mi, plan); vf != "" {
		args = append(args, "-vf", vf)
	}
	hdrParams, colourTags := hdr10Params(mi)
	args = append(args, cpuVideoArgs("libx265", "hevc", crf, cores, hdrParams+":bframes=0", s.noNumaPools)...)
	args = append(args, colourTags...)
	if sizeLimit > 0 {
		args = append(args, "-fs", strconv.FormatInt(sizeLimit, 10))
	}
	args = append(args, "-f", "hevc", dst)
	return s.runWithProgress(ctx, job, args, mi.DurationSec)
}

// remuxVideoStream muxes a processed raw HEVC elementary stream together with the original
// file's kept audio, subtitles and attachments into the final MKV. It goes via a temporary
// MP4 because this ffmpeg won't ingest a raw HEVC ES straight into Matroska (no timestamps)
// — MP4 accepts it with an input frame rate, and that MP4 then remuxes cleanly.
//
// The frame rate is ffprobe's EXACT RATIONAL (e.g. "24000/1001"). Stamping a rounded float
// instead re-times every frame slightly, and over a feature the video drifts out of sync.
func (s *Service) remuxVideoStream(ctx context.Context, video, src, dst string, mi *MediaInfo, plan Plan) error {
	r := mi.FrameRateRat
	if r == "" || r == "0/0" || strings.HasPrefix(r, "0/") {
		r = "24"
	}
	tmp := video + ".mp4"
	defer os.Remove(tmp)
	// 1) Raw HEVC ES → video-only MP4 (the -r generates timestamps for the timestamp-less ES).
	if out, err := exec.CommandContext(ctx, s.ffmpeg, "-y", "-hide_banner", "-loglevel", "error",
		"-r", r, "-i", video, "-c", "copy", "-tag:v", "hvc1", tmp).CombinedOutput(); err != nil {
		return fmt.Errorf("package video: %v (%s)", err, tailStr(out))
	}
	_ = os.Remove(video)
	// 2) MP4 video + the original's kept tracks → final MKV. Metadata and chapters come from
	// the original (input 1), not the throwaway video-only temp file.
	args := []string{"-y", "-hide_banner", "-loglevel", "error", "-i", tmp, "-i", src, "-map", "0:v:0"}
	args = append(args, trackArgs(mi, plan, 1)...)
	args = append(args, "-map_metadata", "1", "-map_chapters", "1", "-c:v", "copy", dst)
	if out, err := exec.CommandContext(ctx, s.ffmpeg, args...).CombinedOutput(); err != nil {
		return fmt.Errorf("%v (%s)", err, tailStr(out))
	}
	return nil
}

// errSourceStream marks a pipe whose producer failed reading the source while the consumer
// was fine: usually the disk, not the file.
var errSourceStream = errors.New("source stream failed")

// pipeCommands runs producer | consumer and returns the consumer's error if it failed (the
// meaningful one — a producer that then can't write is just a consequence), else the
// producer's — unless consumerMayStopEarly, when a producer cut off by a consumer that
// finished successfully is fine.
//
// The pipe is made here and BOTH of our copies of its ends are closed once the children
// have their own. Holding the read end open (as exec's StdoutPipe does until Wait) means a
// consumer that exits early — hdr10plus_tool on a file with no HDR10+ — never breaks the
// pipe: the producer blocks on a full pipe forever, and the conversion with it.
func pipeCommands(producer, consumer *exec.Cmd, consumerMayStopEarly bool) error {
	r, w, err := os.Pipe()
	if err != nil {
		return err
	}
	producer.Stdout = w
	consumer.Stdin = r
	var cerr strings.Builder
	consumer.Stderr = &cerr
	if err := consumer.Start(); err != nil {
		r.Close()
		w.Close()
		return err
	}
	r.Close()
	if err := producer.Start(); err != nil {
		w.Close()
		_ = consumer.Wait()
		return err
	}
	w.Close()
	perr := producer.Wait()
	if err := consumer.Wait(); err != nil {
		return fmt.Errorf("%v (%s)", err, tailStr([]byte(cerr.String())))
	}
	if perr != nil && !consumerMayStopEarly {
		return fmt.Errorf("%w: %w", errSourceStream, perr)
	}
	return nil
}

// tailStr returns the last line/snippet of command output for error messages.
func tailStr(b []byte) string {
	s := strings.TrimSpace(string(b))
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		s = s[i+1:]
	}
	if len(s) > 200 {
		s = s[len(s)-200:]
	}
	return s
}
