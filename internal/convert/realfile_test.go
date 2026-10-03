//go:build linux

package convert

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/tristenlammi/arrmada/internal/settings"
	"github.com/tristenlammi/arrmada/internal/store"
)

// TestRealFile runs Convert's decisions and its rehearsal on one real film, read-only, and
// optionally encodes before/after samples to watch. Inside the app image:
//
//	ARRMADA_REAL_FILE=/media/film.mkv   the film (mount it read-only)
//	ARRMADA_REAL_OUT=/out               where samples go
//	ARRMADA_REAL_SAMPLES=900,8100       sample start times, seconds (optional)
//	ARRMADA_REAL_SAMPLE_LEN=120         sample length, seconds (default 120)
//
// The film itself is never written: samples are encoded from it into ARRMADA_REAL_OUT.
func TestRealFile(t *testing.T) {
	src := os.Getenv("ARRMADA_REAL_FILE")
	if src == "" {
		t.Skip("set ARRMADA_REAL_FILE to a film inside the app image")
	}
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "db"))
	must(t, err)
	defer st.Close()
	scratch := filepath.Join(dir, "scratch")
	s := NewService(st.DB(), nil, nil, settings.NewService(st.DB()), "ffmpeg", "ffprobe", scratch, filepath.Join(dir, "recycle"), testLogger())

	mi, err := probe(ctx, s.ffprobe, src)
	must(t, err)
	t.Logf("source: %s · %s", mediaSpec(mi), humanBytes(mi.SizeBytes))
	t.Logf("HDR %s (DV profile %d, base %q) → carries %s", mi.HDR, mi.DVProfile, mi.DVBase, mi.EncodeHDR())
	bpp, kbps := videoBitsPerPixel(mi)
	t.Logf("video ≈ %.1f Mb/s · %.3f bits/pixel/frame", float64(kbps)/1000, bpp)

	p := s.prefs(ctx)
	plan, needs := p.planFor(mi, src, "", nil)
	t.Logf("needs: %+v", needs)
	t.Logf("estimate: −%s (%d%%) → ~%s", humanBytes(needs.Save), savedPct(mi.SizeBytes, mi.SizeBytes-needs.Save), humanBytes(mi.SizeBytes-needs.Save))
	if ts := trackSummary(mi, plan); ts != "" {
		t.Logf("tracks: %s", ts)
	}
	if !needs.Video {
		t.Logf("the picture would be left alone: %s", needs.Why)
		return
	}

	job := &Job{ID: 1, Title: filepath.Base(src)}
	h10p := false
	if codecClass(mi.VideoCodec) == "hevc" && isHDR(mi.EncodeHDR()) && s.hdr10plusTool != "" {
		start := time.Now()
		jf := filepath.Join(dir, "h10p.json")
		_ = os.MkdirAll(dir, 0o755)
		err := s.extractHDR10Plus(ctx, src, jf)
		h10p = err == nil
		t.Logf("HDR10+ check: present=%v (%v, %s)", h10p, err, time.Since(start).Round(time.Second))
	}
	codec := s.chooseCodec(ctx, job, src, mi, plan, p, h10p)
	plan.VideoCodec, plan.Quality = codec, maxQualityCRF(codec, mi)
	cs := time.Now()
	plan = s.withCrop(ctx, src, mi, plan, p)
	t.Logf("black bars (%s): %+v", time.Since(cs).Round(time.Second), plan.Crop)
	enc := s.pickEncoder(job, mi, codec, p.useGPU)
	t.Logf("plan: %s CRF %d on %s · keeps HDR: %v · cores %d", codec, plan.Quality, enc.Label,
		s.canPreserveHDR(mi, plan, enc, h10p), s.cpuCores(ctx))

	start := time.Now()
	v, err := s.preflight(ctx, job, src, mi, enc, plan, h10p)
	must(t, err)
	t.Logf("rehearsal (%s): CRF %d · SSIM %.4f · predicts %s → ~%s (%d%% smaller) · skip=%q %s",
		time.Since(start).Round(time.Second), v.quality, v.ssim, humanBytes(mi.SizeBytes), humanBytes(v.projected),
		savedPct(mi.SizeBytes, v.projected), v.skipKind, v.reason)
	for _, l := range s.Logs() {
		t.Logf("  log: %s", l.Msg)
	}
	plan.Quality = v.quality

	out := os.Getenv("ARRMADA_REAL_OUT")
	starts := splitCSV(os.Getenv("ARRMADA_REAL_SAMPLES"))
	if out == "" || len(starts) == 0 {
		return
	}
	length := 120.0
	if l, err := strconv.ParseFloat(os.Getenv("ARRMADA_REAL_SAMPLE_LEN"), 64); err == nil && l > 0 {
		length = l
	}
	must(t, os.MkdirAll(out, 0o755))
	for i, st := range starts {
		at, err := strconv.ParseFloat(st, 64)
		must(t, err)
		orig := filepath.Join(out, fmt.Sprintf("sample%d-original.mkv", i+1))
		conv := filepath.Join(out, fmt.Sprintf("sample%d-converted.mkv", i+1))
		seg := []string{"-ss", strconv.FormatFloat(at, 'f', 3, 64), "-t", strconv.FormatFloat(length, 'f', 3, 64)}
		// The original: a straight copy of the stretch (starts on the nearest keyframe).
		run := func(args ...string) {
			t.Helper()
			if o, err := exec.CommandContext(ctx, s.ffmpeg, append([]string{"-y", "-hide_banner", "-loglevel", "error"}, args...)...).CombinedOutput(); err != nil {
				t.Fatalf("ffmpeg %s: %v\n%s", strings.Join(args, " "), err, o)
			}
		}
		run(append(append(seg, "-i", src), "-map", "0", "-c", "copy", orig)...)
		// The converted: the exact production command, run on the copied stretch so both
		// samples hold the same frames.
		cores := s.cpuCores(ctx)
		args := []string{"-y", "-hide_banner", "-loglevel", "error", "-threads", strconv.Itoa(cores), "-i", orig}
		smi, err := probe(ctx, s.ffprobe, orig)
		must(t, err)
		smi.HDR, smi.DVProfile, smi.DVBase, smi.HDR10 = mi.HDR, mi.DVProfile, mi.DVBase, mi.HDR10
		args = append(args, compileOutputArgs(enc, smi, plan, false, cores, s.noNumaPools)...)
		args = append(args, conv)
		t0 := time.Now()
		if o, err := exec.CommandContext(ctx, s.ffmpeg, args...).CombinedOutput(); err != nil {
			t.Fatalf("encode: %v\n%s", err, o)
		}
		took := time.Since(t0)
		cmi, err := probe(ctx, s.ffprobe, conv)
		must(t, err)
		sc, _ := s.computeSSIM(ctx, conv, orig, plan.Crop.filter())
		fps := smi.FrameRate * smi.DurationSec / took.Seconds()
		t.Logf("sample %d @ %s: %s → %s (%d%% smaller) · SSIM %.4f · %.2f fps (%s for %s of film) · out: %s",
			i+1, clock(at), humanBytes(fileSize(orig)), humanBytes(fileSize(conv)), savedPct(fileSize(orig), fileSize(conv)),
			sc, fps, took.Round(time.Second), clock(smi.DurationSec), mediaSpec(cmi))
	}
}
