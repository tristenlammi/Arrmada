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

// extractHDR10Plus pulls the HDR10+ dynamic metadata from a file's HEVC stream into a JSON file
// that x265 consumes via dhdr10-info. Returns an error (and writes nothing usable) if the file
// carries no HDR10+ metadata.
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
	stem := filepath.Join(s.scratchDir, fmt.Sprintf("dv-%d", job.ID))
	rpu, encoded, injected := stem+".rpu.bin", stem+".hevc", stem+".inj.hevc"
	defer os.Remove(rpu)
	defer os.Remove(encoded)
	defer os.Remove(injected)

	// 1) Extract the RPU untouched (mode 0) so we can read its profile and pick the right
	//    conversion — profile 5 needs mode 3, everything else (7 dual-layer, 8.x) mode 2.
	if err := s.extractDoviRPU(ctx, src, rpu); err != nil {
		return fmt.Errorf("extract RPU: %w", err)
	}
	mode := "2"
	if s.doviProfile(ctx, rpu) == 5 {
		mode = "3" // profile 5 (IPT-PQ-c2) → 8.1, else the picture tints green/pink
	}
	// 2) Encode the video to a raw HEVC 10-bit elementary stream (the slow, progress-tracked step).
	if err := s.encodeHEVCStream(ctx, job, src, encoded, mi, plan); err != nil {
		return fmt.Errorf("encode: %w", err)
	}
	// 3) Interleave the RPU back into the encoded stream, converting it to profile 8.1 (broadly
	//    compatible: plays as HDR10 on non-DV displays, DV where supported).
	if out, err := exec.CommandContext(ctx, s.doviTool, "-m", mode, "inject-rpu", "-i", encoded, "--rpu-in", rpu, "-o", injected).CombinedOutput(); err != nil {
		return fmt.Errorf("inject RPU: %v (%s)", err, tailStr(out))
	}
	// 4) Remux the DV video with the original audio/subtitles into the final container.
	if err := s.remuxVideoStream(ctx, injected, src, dst, mi.FrameRate); err != nil {
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
	if err := s.remuxVideoStream(ctx, injected, src, dst, mi.FrameRate); err != nil {
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
func (s *Service) encodeHEVCStream(ctx context.Context, job *Job, src, dst string, mi *MediaInfo, plan Plan) error {
	crf := plan.Quality
	if crf <= 0 {
		crf = crfDefault("hevc")
	}
	args := []string{"-y", "-hide_banner", "-nostats", "-progress", "pipe:1", "-i", src, "-map", "0:v:0", "-an", "-sn"}
	// A raw Annex-B stream carries no timestamps, so the remux stamps it at one constant rate.
	// A VFR source therefore has to be flattened HERE, or its variable timing is reinterpreted
	// as constant while the audio keeps real timing — progressive A/V drift. The normal path
	// does this in compileOutputArgs; this path was missing it entirely.
	if plan.VFRToCFR && mi.VFR {
		args = append(args, "-fps_mode", "cfr")
	}
	if plan.ScaleHeight > 0 && mi.Height > plan.ScaleHeight {
		args = append(args, "-vf", scaleCPU(plan.ScaleHeight))
	}
	args = append(args, "-c:v", "libx265", "-preset", "medium", "-crf", strconv.Itoa(crf), "-pix_fmt", "yuv420p10le")
	args = append(args, hdr10Args(mi.HDR10)...) // BT.2020/PQ + mastering (dynamic metadata re-added later)
	args = append(args, "-f", "hevc", dst)
	return s.runWithProgress(ctx, job, args, mi.DurationSec)
}

// remuxVideoStream muxes a processed raw HEVC elementary stream (with re-injected HDR metadata)
// together with the original file's audio and subtitles into the final MKV. It goes via a
// temporary MP4 because this ffmpeg build won't ingest a raw HEVC ES straight into Matroska (no
// timestamps) — MP4 accepts it with an input frame rate, and the resulting MP4 then remuxes
// cleanly into MKV. Shared by the Dolby Vision and HDR10+ pipelines.
func (s *Service) remuxVideoStream(ctx context.Context, video, src, dst string, frameRate float64) error {
	r := frameRate
	if r <= 0 {
		r = 24
	}
	tmp := video + ".mp4"
	defer os.Remove(tmp)
	// 1) Raw HEVC ES → video-only MP4 (the -r generates timestamps for the timestamp-less ES).
	if out, err := exec.CommandContext(ctx, s.ffmpeg, "-y", "-hide_banner", "-loglevel", "error",
		"-r", fmt.Sprintf("%.6g", r), "-i", video, "-c", "copy", "-tag:v", "hvc1", tmp).CombinedOutput(); err != nil {
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
