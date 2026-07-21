package convert

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// This file holds the dynamic-HDR pipelines (R6): HDR10+ (hdr10plus_tool → x265 dhdr10-info) and
// Dolby Vision (dovi_tool extract-RPU → encode → inject-RPU → remux, converting to profile 8.1).
// Both are HEVC-only and run on the CPU (libx265) path; when a tool or the metadata is missing,
// callers fall back to the static-HDR10 base or skip, never silently degrading a file.

// errDVProfile5 is the refusal returned when the Dolby Vision pipeline is asked to convert a
// profile 5 source. Kept as a sentinel so the router's skip reason and the pipeline's error
// can never drift apart.
var errDVProfile5 = fmt.Errorf("Dolby Vision profile 5 (IPT-PQ-c2) can't be converted faithfully — " +
	"its base layer isn't HDR10-compatible even after an RPU conversion to 8.1; keeping the original")

// extractHDR10Plus pulls the HDR10+ dynamic metadata from a file's HEVC stream into a JSON file
// that x265 consumes via dhdr10-info. Returns an error (and writes nothing usable) if the file
// carries no HDR10+ metadata. NOTE: the pipeline (this bitstream filter, and hdr10plus_tool
// itself) is HEVC-only — the caller's gate must ensure the SOURCE is HEVC before extracting.
func (s *Service) extractHDR10Plus(ctx context.Context, src, jsonOut string) error {
	// Feed the raw HEVC elementary stream (Annex-B) into hdr10plus_tool.
	ff := exec.CommandContext(ctx, s.ffmpeg, "-loglevel", "error", "-i", src,
		"-map", "0:v:0", "-c", "copy", "-bsf:v", "hevc_mp4toannexb", "-f", "hevc", "-")
	h10 := exec.CommandContext(ctx, s.hdr10plusTool, "extract", "-o", jsonOut, "-")
	if err := pipeCommands(ff, h10); err != nil {
		return err
	}
	if fi, err := os.Stat(jsonOut); err != nil || fi.Size() < 8 {
		return fmt.Errorf("no HDR10+ metadata found")
	}
	return nil
}

// encodeDolbyVision runs the full Dolby Vision pipeline into dst: extract the RPU (converting to
// profile 8.1 so the result is single-layer and HDR10-compatible), re-encode the video to a raw
// HEVC stream, interleave the RPU back in, then remux with the original audio/subtitles.
func (s *Service) encodeDolbyVision(ctx context.Context, job *Job, src, dst string, mi *MediaInfo, plan Plan) error {
	if s.doviTool == "" {
		return fmt.Errorf("dovi_tool not available")
	}
	// Profile 5 (IPT-PQ-c2) is refused outright. Converting its RPU to 8.1 does NOT convert
	// the base-layer PIXELS, which stay in IPT-PQ-c2 — the "HDR10-compatible" output renders
	// wrong colors on every non-DV display. The router (canPreserveHDR) skips P5 before a job
	// gets here; this is defence in depth for anything that slips past it.
	if mi.DVProfile == 5 {
		return errDVProfile5
	}
	stem := filepath.Join(s.scratchDir, fmt.Sprintf("dv-%d", job.ID))
	rpu, encoded, injected := stem+".rpu.bin", stem+".hevc", stem+".inj.hevc"
	defer os.Remove(rpu)
	defer os.Remove(encoded)
	defer os.Remove(injected)

	// 1) Extract the RPU untouched (mode 0) so we can read its profile — the stream-level
	//    probe can miss it (some sources carry no DOVI configuration record).
	if err := s.extractDoviRPU(ctx, src, rpu); err != nil {
		return fmt.Errorf("extract RPU: %w", err)
	}
	if s.doviProfile(ctx, rpu) == 5 {
		return errDVProfile5
	}
	// 2) Encode the video to a raw HEVC 10-bit elementary stream (the slow, progress-tracked step).
	if err := s.encodeHEVCStream(ctx, job, src, encoded, mi, plan); err != nil {
		return fmt.Errorf("encode: %w", err)
	}
	// 3) Interleave the RPU back into the encoded stream, converting it to profile 8.1 (broadly
	//    compatible: plays as HDR10 on non-DV displays, DV where supported). Mode 2 handles the
	//    dual-layer (7) and 8.x profiles; profile 5 never reaches this point.
	if out, err := exec.CommandContext(ctx, s.doviTool, "-m", "2", "inject-rpu", "-i", encoded, "--rpu-in", rpu, "-o", injected).CombinedOutput(); err != nil {
		return fmt.Errorf("inject RPU: %v (%s)", err, tailStr(out))
	}
	// 4) Remux the DV video with the original audio/subtitles into the final container.
	if err := s.remuxVideoStream(ctx, injected, src, dst, mi.FrameRateRat); err != nil {
		return fmt.Errorf("remux: %w", err)
	}
	return nil
}

// encodeHDR10Plus runs the HDR10+ pipeline into dst: re-encode the video to a raw HEVC stream
// (carrying the HDR10 static base), then interleave the extracted HDR10+ dynamic metadata back in
// with hdr10plus_tool, and remux with the original audio/subtitles. Used because the bundled x265
// isn't built with dhdr10-info support, so the metadata is re-embedded post-encode.
func (s *Service) encodeHDR10Plus(ctx context.Context, job *Job, src, dst string, mi *MediaInfo, plan Plan, jsonPath string) error {
	if s.hdr10plusTool == "" {
		return fmt.Errorf("hdr10plus_tool not available")
	}
	stem := filepath.Join(s.scratchDir, fmt.Sprintf("h10p-enc-%d", job.ID))
	encoded, injected := stem+".hevc", stem+".inj.hevc"
	defer os.Remove(encoded)
	defer os.Remove(injected)

	if err := s.encodeHEVCStream(ctx, job, src, encoded, mi, plan); err != nil {
		return fmt.Errorf("encode: %w", err)
	}
	if out, err := exec.CommandContext(ctx, s.hdr10plusTool, "inject", "-i", encoded, "-j", jsonPath, "-o", injected).CombinedOutput(); err != nil {
		return fmt.Errorf("inject HDR10+: %v (%s)", err, tailStr(out))
	}
	if err := s.remuxVideoStream(ctx, injected, src, dst, mi.FrameRateRat); err != nil {
		return fmt.Errorf("remux: %w", err)
	}
	return nil
}

// extractDoviRPU pipes the source's HEVC elementary stream into dovi_tool, extracting the RPU
// untouched (mode 0) — the conversion to profile 8.1 happens at inject time, once we know the
// source profile.
func (s *Service) extractDoviRPU(ctx context.Context, src, rpuOut string) error {
	ff := exec.CommandContext(ctx, s.ffmpeg, "-loglevel", "error", "-i", src,
		"-map", "0:v:0", "-c", "copy", "-bsf:v", "hevc_mp4toannexb", "-f", "hevc", "-")
	dv := exec.CommandContext(ctx, s.doviTool, "extract-rpu", "-o", rpuOut, "-")
	if err := pipeCommands(ff, dv); err != nil {
		return err
	}
	if fi, err := os.Stat(rpuOut); err != nil || fi.Size() < 8 {
		return fmt.Errorf("no Dolby Vision RPU found")
	}
	return nil
}

// doviProfile reads the Dolby Vision profile from an extracted RPU (dovi_tool info prints a
// preamble line then JSON). Returns 0 if it can't be determined.
func (s *Service) doviProfile(ctx context.Context, rpu string) int {
	out, err := exec.CommandContext(ctx, s.doviTool, "info", "-i", rpu, "-f", "0").Output()
	if err != nil {
		return 0
	}
	i := strings.IndexByte(string(out), '{')
	if i < 0 {
		return 0
	}
	var info struct {
		DoviProfile int `json:"dovi_profile"`
	}
	if json.Unmarshal(out[i:], &info) != nil {
		return 0
	}
	return info.DoviProfile
}

// encodeHEVCStream re-encodes only the video to a raw HEVC 10-bit Annex-B stream (no audio/subs),
// carrying the HDR10 base metadata so the profile-8.1 result is HDR10-correct. Live progress is
// parsed from the -progress pipe.
//
// NO frame-rate flattening here, deliberately: the DV RPU / HDR10+ JSON is extracted from the
// SOURCE frame-for-frame, and -fps_mode cfr changes the frame count, so every subsequent frame's
// dynamic metadata lands on the wrong picture after inject. Frame counts must match exactly.
// (The remux stamps the stream at the source's exact average rate, so timing still lines up.)
func (s *Service) encodeHEVCStream(ctx context.Context, job *Job, src, dst string, mi *MediaInfo, plan Plan) error {
	crf := plan.Quality
	if crf <= 0 {
		crf = crfDefault("hevc")
	}
	cores := s.cpuCores(ctx)
	args := []string{"-y", "-hide_banner", "-nostats", "-progress", "pipe:1",
		"-threads", strconv.Itoa(cores),
		"-i", src, "-map", fmt.Sprintf("0:v:%d", mi.VideoIndex), "-an", "-sn"}
	scale := plan.ScaleHeight > 0 && mi.Height > plan.ScaleHeight
	if vf := swFilterChain(mi, scale, plan.ScaleHeight); vf != "" {
		args = append(args, "-vf", vf)
	}
	// Same quality tuning as the standard CPU path — preset slow, the tuned x265 params, the
	// HDR10 args merged in, and pools bounded (or disabled under the NUMA workaround). This
	// path used a bare "-preset medium", so DV/HDR10+ files — the premium content — were the
	// only ones encoded BELOW the module's quality bar, and blocked-NUMA machines crashed here.
	hdrParams, colourTags := hdr10Params(mi)
	args = append(args, cpuVideoArgs("libx265", "hevc", crf, true, cores, hdrParams, s.noNumaPools)...)
	args = append(args, colourTags...)
	args = append(args, "-f", "hevc", dst)
	return s.runWithProgress(ctx, job, args, mi.DurationSec)
}

// remuxVideoStream muxes a processed raw HEVC elementary stream (with re-injected HDR metadata)
// together with the original file's audio and subtitles into the final MKV. It goes via a
// temporary MP4 because this ffmpeg build won't ingest a raw HEVC ES straight into Matroska (no
// timestamps) — MP4 accepts it with an input frame rate, and the resulting MP4 then remuxes
// cleanly into MKV. Shared by the Dolby Vision and HDR10+ pipelines.
//
// frameRate is ffprobe's EXACT RATIONAL (e.g. "24000/1001", from avg_frame_rate). Stamping a
// %.6g float of 23.976… instead re-times every frame slightly, and over a feature the video
// drifts audibly out of sync with the copied audio.
func (s *Service) remuxVideoStream(ctx context.Context, video, src, dst, frameRate string) error {
	r := frameRate
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
	// 2) MP4 video + original audio/subtitles → final MKV.
	// Input 1 is the ORIGINAL: audio, subtitles, attachments, chapters and global metadata all
	// come from there. Without the explicit -map_metadata/-map_chapters, ffmpeg defaults to
	// input 0 — the throwaway video-only temp file — so the title, tags and chapters were taken
	// from an empty container and the original's were discarded. Attachments were dropped too.
	args := []string{"-y", "-hide_banner", "-loglevel", "error",
		"-i", tmp, "-i", src,
		"-map", "0:v:0", "-map", "1:a?", "-map", "1:s?", "-map", "1:t?",
		"-map_metadata", "1", "-map_chapters", "1",
		"-c", "copy", "-tag:v", "hvc1", dst}
	if out, err := exec.CommandContext(ctx, s.ffmpeg, args...).CombinedOutput(); err != nil {
		return fmt.Errorf("%v (%s)", err, tailStr(out))
	}
	return nil
}

// pipeCommands wires producer.stdout → consumer.stdin, runs both, and returns the first error.
func pipeCommands(producer, consumer *exec.Cmd) error {
	pipe, err := producer.StdoutPipe()
	if err != nil {
		return err
	}
	consumer.Stdin = pipe
	var cerr strings.Builder
	consumer.Stderr = &cerr
	if err := consumer.Start(); err != nil {
		return err
	}
	if err := producer.Run(); err != nil {
		_ = consumer.Wait()
		return fmt.Errorf("source stream failed: %w", err)
	}
	if err := consumer.Wait(); err != nil {
		return fmt.Errorf("%v (%s)", err, tailStr([]byte(cerr.String())))
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
