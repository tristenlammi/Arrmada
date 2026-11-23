//go:build linux

package convert

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestGPUHDRProbeIT runs the HDR startup test's machinery with real ffmpeg: the HDR10 and HLG
// clips are made and read back, a CPU encoder standing in for a GPU one passes through the
// same bare-stream check (HEVC and AV1), and an encode that drops the metadata fails it.
// There's no GPU here, so no hardware encoder runs. Synthetic media only (lavfi); it needs
// the app image's ffmpeg, so it runs only with ARRMADA_FFMPEG_IT set.
func TestGPUHDRProbeIT(t *testing.T) {
	if os.Getenv("ARRMADA_FFMPEG_IT") == "" {
		t.Skip("set ARRMADA_FFMPEG_IT=1 inside the app image to run real encodes")
	}
	ctx := context.Background()
	dir := t.TempDir()
	encs := workingCPUEncoders()
	samples := map[string]*hdrSample{}
	for _, kind := range []string{"HDR10", "HLG"} {
		s, err := makeHDRSample(ctx, "ffmpeg", "ffprobe", dir, kind, encs)
		if err != nil {
			t.Fatalf("%s clip: %v", kind, err)
		}
		if s.mi.EncodeHDR() != kind || !s.mi.TenBit {
			t.Fatalf("%s clip probed as %s (10-bit %v)", kind, s.mi.EncodeHDR(), s.mi.TenBit)
		}
		samples[kind] = s
	}
	if m := samples["HDR10"].mi.HDR10; m == nil || m.MasterDisplay != probeMasterDisplay || m.MaxCLL != probeMaxCLL {
		t.Fatalf("HDR10 clip metadata: %+v", m)
	}

	// The CPU encoders keep it, through the same check a GPU encoder gets — including AV1's
	// bare stream (OBU), whose mastering display comes back in other fractions.
	for _, codec := range []string{"hevc", "av1"} {
		for kind, s := range samples {
			if err := probeEncoderHDR(ctx, "ffmpeg", "ffprobe", dir, cpuEncoder(codec), s); err != nil {
				t.Errorf("%s %s: %v", codec, kind, err)
			}
		}
	}

	// An encode that doesn't re-pass the metadata loses it, and the check says so.
	ff := func(args ...string) {
		t.Helper()
		out, err := exec.Command("ffmpeg", append([]string{"-y", "-hide_banner", "-loglevel", "error"}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("ffmpeg %v: %v\n%s", args, err, out)
		}
	}
	// ffmpeg 7's libx265 copies the frames' metadata by itself, so it's taken off the frames
	// first: an encoder that ignores it.
	dropHDR := "sidedata=mode=delete:type=MASTERING_DISPLAY_METADATA,sidedata=mode=delete:type=CONTENT_LIGHT_LEVEL"
	plain := filepath.Join(dir, "plain.hevc")
	ff("-i", samples["HDR10"].path, "-vf", dropHDR, "-c:v", "libx265", "-preset", "ultrafast", "-x265-params", "log-level=error:pools=none",
		"-pix_fmt", pixFmt10, "-color_primaries", "bt2020", "-color_trc", "smpte2084", "-colorspace", "bt2020nc", "-f", "hevc", plain)
	got, err := probe(ctx, "ffprobe", plain)
	if err != nil {
		t.Fatal(err)
	}
	if why := hdrLost(samples["HDR10"].mi, got); why == "" {
		t.Errorf("a bare stream without the mastering display passed: %+v", got.HDR10)
	} else {
		t.Logf("bare stream without metadata: %s", why)
	}
	// An encoder that ignores the metadata altogether (FFV1 has nowhere to put it) still gets
	// a Matroska file with it: ffmpeg 7 hands the frames' metadata to the muxer, which writes
	// it in the container. So a Matroska file can show HDR its video stream doesn't carry —
	// which is why the startup test reads a bare stream. The container's own fields come back
	// as reduced fractions ("53/200"), and are still read in x265's units.
	ffv1 := filepath.Join(dir, "ffv1.mkv")
	ff("-i", samples["HDR10"].path, "-c:v", "ffv1", "-color_primaries", "bt2020", "-color_trc", "smpte2084", "-colorspace", "bt2020nc", ffv1)
	if got, err := probe(ctx, "ffprobe", ffv1); err != nil || got.HDR10 == nil || got.HDR10.MasterDisplay != probeMasterDisplay || got.HDR10.MaxCLL != probeMaxCLL {
		t.Errorf("container-only metadata: %+v (err %v)", got, err)
	}
	// With the metadata taken off the frames, the container has none either.
	mkv := filepath.Join(dir, "plain.mkv")
	ff("-i", samples["HDR10"].path, "-vf", dropHDR, "-c:v", "libx265", "-preset", "ultrafast", "-x265-params", "log-level=error:pools=none",
		"-pix_fmt", pixFmt10, "-color_primaries", "bt2020", "-color_trc", "smpte2084", "-colorspace", "bt2020nc", mkv)
	if got, err := probe(ctx, "ffprobe", mkv); err != nil || hdrLost(samples["HDR10"].mi, got) == "" {
		t.Errorf("Matroska with no metadata anywhere passed: %+v (err %v)", got, err)
	}

	// The check of a GPU output's video stream on its own, with the real tools: the HDR10
	// clip's stream keeps it, the stripped encode's doesn't.
	s := &Service{ffmpeg: "ffmpeg", ffprobe: "ffprobe"}
	if why := s.streamHDRLost(ctx, samples["HDR10"].path, samples["HDR10"].mi, "hevc"); why != "" {
		t.Errorf("the HDR10 clip's own stream: %s", why)
	}
	if why := s.streamHDRLost(ctx, mkv, samples["HDR10"].mi, "hevc"); why == "" {
		t.Error("a stream without the metadata passed the stream check")
	}
	av1 := filepath.Join(dir, "av1.mkv")
	ff(hdrSampleArgs("libsvtav1", "HDR10", av1)[4:]...) // without the -y/-hide_banner/-loglevel ff adds
	if why := s.streamHDRLost(ctx, av1, sampleSpec("HDR10"), "av1"); why != "" {
		t.Errorf("an AV1 HDR10 stream: %s", why)
	}
}
