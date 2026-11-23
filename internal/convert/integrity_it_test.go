//go:build linux

package convert

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestIntegrityCheckRealFiles runs the full-decode check with the real ffmpeg on synthetic
// files only (lavfi test patterns, never library files): a clean encode passes; one with
// damaged bytes mid-file, one with frames cut out and one cut short (which keeps its full
// running time in the Matroska header, so the duration check alone passes it) are each
// rejected. Needs ffmpeg with libx265, so it runs only with ARRMADA_FFMPEG_IT set.
func TestIntegrityCheckRealFiles(t *testing.T) {
	if os.Getenv("ARRMADA_FFMPEG_IT") == "" {
		t.Skip("set ARRMADA_FFMPEG_IT=1 inside the app image to run real decodes")
	}
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("no ffmpeg")
	}
	ctx := context.Background()
	dir := t.TempDir()
	ff := func(args ...string) {
		t.Helper()
		run(t, "ffmpeg", append([]string{"-y", "-hide_banner", "-loglevel", "error"}, args...)...)
	}
	s := newTestService(t)
	s.ffmpeg, s.ffprobe = "ffmpeg", "ffprobe"
	plan := Plan{VideoCodec: "hevc"}
	check := func(path string) string {
		t.Helper()
		mi := mustProbe(t, filepath.Join(dir, "src.mkv"))
		out, err := probe(ctx, "ffprobe", path)
		if err != nil {
			return "unreadable: " + err.Error()
		}
		_, reason := s.integrityCheck(ctx, &Job{ID: 1, Title: filepath.Base(path)}, path, mi, out, plan)
		return reason
	}

	// A 20-second 24 fps source: 480 frames.
	src := filepath.Join(dir, "src.mkv")
	ff("-f", "lavfi", "-i", "testsrc2=s=640x360:r=24:d=20,noise=alls=3:allf=u", "-f", "lavfi", "-i", "sine=f=440:d=20",
		"-c:v", "libx264", "-preset", "veryfast", "-crf", "16", "-pix_fmt", "yuv420p", "-c:a", "aac", src)
	if n, _ := expectedFrames(mustProbe(t, src), nil, plan); n < 479 || n > 481 {
		t.Fatalf("source expects %v frames, want 480", n)
	}

	clean := filepath.Join(dir, "clean.mkv")
	ff("-i", src, "-map", "0", "-c:v", "libx265", "-preset", "ultrafast", "-x265-params", "log-level=0:pools=none",
		"-c:a", "copy", clean)
	if r := check(clean); r != "" {
		t.Fatalf("a clean encode failed the check: %s", r)
	}

	// Damaged bytes in the middle of the stream.
	corrupt := filepath.Join(dir, "corrupt.mkv")
	b, err := os.ReadFile(clean)
	must(t, err)
	for i := len(b) * 2 / 5; i < len(b)*3/5; i += 97 {
		b[i] ^= 0x5a
	}
	must(t, os.WriteFile(corrupt, b, 0o644))
	if r := check(corrupt); r == "" {
		t.Fatal("a file with damaged bytes mid-stream passed")
	} else {
		t.Logf("damaged: %s", r)
	}

	// Sixty frames cut out of the middle, timestamps kept: the running time still matches.
	dropped := filepath.Join(dir, "dropped.mkv")
	ff("-i", src, "-map", "0", "-vf", `select='not(between(n\,200\,259))'`, "-fps_mode", "passthrough",
		"-c:v", "libx265", "-preset", "ultrafast", "-x265-params", "log-level=0:pools=none", "-c:a", "copy", dropped)
	if r := check(dropped); !strings.Contains(r, "missing") {
		t.Fatalf("dropped frames: %q, want a missing-frames rejection", r)
	} else {
		t.Logf("dropped: %s", r)
	}

	// Cut short: the header still claims the whole running time.
	short := filepath.Join(dir, "short.mkv")
	b, err = os.ReadFile(clean)
	must(t, err)
	must(t, os.WriteFile(short, b[:len(b)*3/5], 0o644))
	if out, err := probe(ctx, "ffprobe", short); err == nil {
		t.Logf("truncated file probes as %.1f s", out.DurationSec)
	}
	if r := check(short); r == "" {
		t.Fatal("a truncated file passed")
	} else {
		t.Logf("truncated: %s", r)
	}

	// VMAF, where this ffmpeg has it: a clean encode scores like one.
	if !hasLibVMAF(ctx, "ffmpeg") {
		t.Log("no libvmaf in this ffmpeg — VMAF not exercised")
		return
	}
	out := mustProbe(t, clean)
	v, err := s.vmafWindow(ctx, clean, src, 0, 0, 10, out.Width, out.Height, out.FrameRateRat, refLeg{})
	if err != nil || v < 80 {
		t.Fatalf("VMAF of a clean encode: %v, %v", v, err)
	}
	t.Logf("VMAF %.2f", v)
}
