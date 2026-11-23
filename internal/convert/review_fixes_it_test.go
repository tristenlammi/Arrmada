package convert

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Real-encode checks for the Convert review fixes. Synthetic clips only (lavfi); they need
// ffmpeg/ffprobe with libx265, so they run only with ARRMADA_FFMPEG_IT set.

func itFFmpeg(t *testing.T) func(args ...string) {
	t.Helper()
	if os.Getenv("ARRMADA_FFMPEG_IT") == "" {
		t.Skip("set ARRMADA_FFMPEG_IT=1 inside the app image to run real encodes")
	}
	return func(args ...string) {
		t.Helper()
		out, err := exec.Command("ffmpeg", append([]string{"-y", "-hide_banner", "-loglevel", "error"}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("ffmpeg %v: %v\n%s", args, err, out)
		}
	}
}

// convertIT runs the real encode command a conversion builds (compileOutputArgs) on src.
func convertIT(t *testing.T, ff func(...string), src, dst string, mi *MediaInfo, plan Plan) {
	t.Helper()
	args := append([]string{"-i", src}, compileOutputArgs(cpuEncoder(plan.VideoCodec), mi, plan, false, 2, numaPoolsBlocked(context.Background(), "ffmpeg"))...)
	ff(append(args, dst)...)
}

// An interlaced source passes the quality check at the normal target once the reference
// is deinterlaced the way the encode was; compared with its combed fields it couldn't.
// A progressive source is measured exactly as before.
func TestInterlacedQualityGateIT(t *testing.T) {
	ff := itFFmpeg(t)
	ctx := context.Background()
	dir := t.TempDir()
	s := newTestService(t)
	s.ffmpeg, s.ffprobe = "ffmpeg", "ffprobe"

	// PAL 576i: 50 progressive frames a second woven into 25 interlaced ones, top field first.
	src := filepath.Join(dir, "576i.mkv")
	ff("-f", "lavfi", "-i", "testsrc2=s=720x576:r=50:d=12", "-vf", "tinterlace=mode=interleave_top,setfield=tff",
		"-c:v", "libx264", "-preset", "veryfast", "-crf", "12", "-flags", "+ildct+ilme", "-x264opts", "tff=1",
		"-pix_fmt", "yuv420p", src)
	mi, err := probe(ctx, "ffprobe", src)
	if err != nil {
		t.Fatal(err)
	}
	if !mi.Interlaced {
		t.Fatalf("the synthetic source should probe as interlaced: %+v", mi)
	}
	plan := Plan{VideoCodec: "hevc", Quality: maxQualityCRF("hevc", mi)}
	out := filepath.Join(dir, "out.mkv")
	convertIT(t, ff, src, out, mi, plan)

	now, err := s.computeSSIM(ctx, out, src, refLegOf(mi, plan))
	if err != nil {
		t.Fatal(err)
	}
	before, err := s.computeSSIM(ctx, out, src, refLeg{index: mi.VideoIndex, filter: plan.Crop.filter()})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("576i at CRF %d: SSIM %.4f against the deinterlaced source, %.4f against the combed one (the old check)", plan.Quality, now.Mean, before.Mean)
	if !now.passes() {
		t.Errorf("an interlaced source must pass at CRF %d: %s", plan.Quality, now.summary())
	}
	if now.Mean <= before.Mean {
		t.Errorf("deinterlacing the reference should raise the score: %.4f vs %.4f", now.Mean, before.Mean)
	}

	// Progressive: no deinterlace, same score as the old check.
	psrc := filepath.Join(dir, "1080p.mkv")
	ff("-f", "lavfi", "-i", "testsrc2=s=1280x720:r=24:d=12", "-c:v", "libx264", "-preset", "veryfast", "-crf", "12", "-pix_fmt", "yuv420p", psrc)
	pmi, err := probe(ctx, "ffprobe", psrc)
	if err != nil {
		t.Fatal(err)
	}
	if pmi.Interlaced {
		t.Fatal("the progressive source probed as interlaced")
	}
	pplan := Plan{VideoCodec: "hevc", Quality: maxQualityCRF("hevc", pmi)}
	pout := filepath.Join(dir, "pout.mkv")
	convertIT(t, ff, psrc, pout, pmi, pplan)
	pnow, err := s.computeSSIM(ctx, pout, psrc, refLegOf(pmi, pplan))
	if err != nil {
		t.Fatal(err)
	}
	pold, err := s.computeSSIM(ctx, pout, psrc, refLeg{})
	if err != nil {
		t.Fatal(err)
	}
	if !pnow.passes() || pnow.Mean != pold.Mean {
		t.Errorf("progressive: %.4f now vs %.4f before — must pass and be unchanged", pnow.Mean, pold.Mean)
	}
}

// A copy whose audio was cut short — with full-length video, and the original's stale
// DURATION-eng tag carried along — fails the check before the swap; a whole copy passes.
func TestTruncatedAudioCopyIT(t *testing.T) {
	ff := itFFmpeg(t)
	ctx := context.Background()
	dir := t.TempDir()
	src := filepath.Join(dir, "src.mkv")
	ff("-f", "lavfi", "-i", "testsrc2=s=320x240:r=24:d=30", "-f", "lavfi", "-i", "sine=f=440:d=30",
		"-c:v", "libx264", "-preset", "ultrafast", "-c:a", "aac", "-metadata:s:a:0", "DURATION-eng=00:00:30.000000000", src)
	mi, err := probe(ctx, "ffprobe", src)
	if err != nil {
		t.Fatal(err)
	}
	if len(mi.Audio) != 1 || mi.Audio[0].Sec < 29 {
		t.Fatalf("source audio length: %+v", mi.Audio)
	}
	whole := filepath.Join(dir, "whole.mkv")
	ff("-i", src, "-map", "0", "-c", "copy", whole)
	cut := filepath.Join(dir, "cut.mkv")
	ff("-i", src, "-t", "12", "-i", src, "-map", "0:v", "-map", "1:a", "-c", "copy", cut)

	for _, c := range []struct {
		path string
		ok   bool
	}{{whole, true}, {cut, false}} {
		out, err := probe(ctx, "ffprobe", c.path)
		if err != nil {
			t.Fatal(err)
		}
		_, why := verifyOutput(mi, out, 1, Plan{})
		t.Logf("%s: runs %.1fs, audio %.1fs → %q", filepath.Base(c.path), out.DurationSec, out.Audio[0].Sec, why)
		if c.ok != (why == "") {
			t.Errorf("%s: verdict %q", filepath.Base(c.path), why)
		}
		if !c.ok && !strings.Contains(why, "audio track 1") {
			t.Errorf("the reason should name the short track: %q", why)
		}
	}
}

// The HDR10+ read pulls the film's own stream when another video stream comes first: the
// HEVC bitstream filter fails on the picture in front, and the film comes out as Annex-B.
func TestHDR10PlusExtractSkipsLeadingPictureIT(t *testing.T) {
	ff := itFFmpeg(t)
	ctx := context.Background()
	dir := t.TempDir()
	src := filepath.Join(dir, "covered.mkv")
	ff("-f", "lavfi", "-i", "color=c=red:s=320x320:d=2:r=1", "-f", "lavfi", "-i", "testsrc2=s=320x240:r=24:d=2",
		"-map", "0", "-map", "1", "-c:v:0", "mjpeg", "-c:v:1", "libx265", "-x265-params", "log-level=error", src)
	mi, err := probe(ctx, "ffprobe", src)
	if err != nil {
		t.Fatal(err)
	}
	// This picture isn't flagged as cover art, so the probe can't skip it; the film's index
	// is what a cover-art-first file probes to.
	mi.VideoIndex = 1
	read := func(idx int) ([]byte, error) {
		args := hdr10PlusExtractArgs(src, idx)
		out := filepath.Join(dir, "stream.hevc")
		args[len(args)-1] = out
		_ = os.Remove(out)
		if o, err := exec.Command("ffmpeg", append([]string{"-y"}, args...)...).CombinedOutput(); err != nil {
			return o, err
		}
		return os.ReadFile(out)
	}
	if _, err := read(0); err == nil {
		t.Error("reading stream 0 (the picture) should fail — that's how HDR10+ went undetected")
	}
	b, err := read(mi.VideoIndex)
	if err != nil {
		t.Fatalf("reading the film's stream: %v\n%s", err, b)
	}
	if !bytes.HasPrefix(b, []byte{0, 0, 0, 1}) && !bytes.HasPrefix(b, []byte{0, 0, 1}) {
		t.Errorf("not an Annex-B HEVC stream: % x", b[:min(len(b), 8)])
	}
}

// The SVT-AV1 version is read from the encoder itself, so the core budget is passed the
// way that version understands.
func TestSVTAV1VersionIT(t *testing.T) {
	itFFmpeg(t)
	major, minor := svtAV1Version(context.Background(), "ffmpeg")
	legacy := svtLegacyVersion(major, minor)
	t.Logf("SVT-AV1 %d.%d (legacy lp form: %v)", major, minor, legacy)
	if major == 0 {
		t.Fatal("the SVT-AV1 version couldn't be read from this ffmpeg")
	}
	// The core budget it gets is accepted by this SVT-AV1 without a word about it.
	was := svtLegacyLP.Load()
	t.Cleanup(func() { svtLegacyLP.Store(was) })
	svtLegacyLP.Store(legacy)
	args := append([]string{"-f", "lavfi", "-i", "testsrc2=s=320x240:r=24:d=1"}, cpuVideoArgs("libsvtav1", "av1", 30, 2, "", false)...)
	args = append([]string{"-hide_banner", "-loglevel", "info", "-y"}, append(args, "-f", "null", "-")...)
	out, err := exec.Command("ffmpeg", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("encode: %v\n%s", err, out)
	}
	// (Debian's 2.3 doesn't know luminance-qp-bias — that complaint is about another option.)
	for _, bad := range []string{"Error parsing option pin", "Error parsing option lp", "Level of parallelism supports"} {
		if strings.Contains(string(out), bad) {
			t.Errorf("SVT-AV1 complained about the core budget (%q):\n%s", bad, out)
		}
	}
}
