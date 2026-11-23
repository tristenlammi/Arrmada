package convert

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/tristenlammi/arrmada/internal/toolrun"
)

// HDR on the GPU. Static HDR — HDR10 (the PQ curve plus the mastering-display and light-level
// numbers) and HLG (a curve alone) — is a handful of tags and two small blocks of metadata,
// and ffmpeg 7's hardware encoders can write them: Quick Sync (HEVC and AV1) and VAAPI (HEVC
// and AV1) copy the mastering display and light level from the decoded frames, and so does
// NVENC HEVC in jellyfin-ffmpeg (a patch of theirs; plain ffmpeg 7.1's NVENC writes neither).
// The colour tags go in from -color_primaries / -color_trc / -colorspace on all of them.
//
// Whether a particular GPU, driver and ffmpeg build really does it is another matter, so it
// isn't assumed: at startup every hardware encoder that writes 10-bit encodes a tiny HDR10
// clip and a tiny HLG clip, with the very options a real conversion uses, into a bare video
// stream (no container that could hold the metadata for it), and the result is read back.
// Only an encoder whose output comes back as the same HDR, with the same numbers, takes HDR
// files; every other HDR file goes to the CPU as before. HDR10+ and Dolby Vision always go
// to the CPU: their metadata changes scene by scene, and no hardware encoder carries it.
//
// And every conversion is still checked after the encode (hdrLost): a file that came out
// without its HDR never replaces the original, whichever encoder wrote it.

// The mastering display and light level the startup clip carries: a typical 1000-nit P3 grade.
const (
	probeMasterDisplay = "G(13250,34500)B(7500,3000)R(34000,16000)WP(15635,16450)L(10000000,50)"
	probeMaxCLL        = "1000,400"
)

// carriesHDR reports whether an encoder proved at startup that it writes this kind of static
// HDR ("HDR10" or "HLG"). Nothing else is ever carried by a hardware encoder.
func (e Encoder) carriesHDR(hdr string) bool {
	switch hdr {
	case "HDR10":
		return e.HDR10
	case "HLG":
		return e.HLG
	}
	return false
}

// gpuTakesHDR reports whether a file with this HDR may be encoded on hardware encoder hw.
// hdr is the file's own format (MediaInfo.HDR): plain HDR10 and HLG only, and only on an
// encoder that passed the startup test for it. Dolby Vision (even with its layer dropped)
// and HDR10+ — labelled so, or found by hdr10plus_tool (hdr10plus) — stay on the CPU.
func gpuTakesHDR(hw Encoder, hdr string, hdr10plus bool) bool {
	if !isHDR(hdr) {
		return true
	}
	if hdr10plus {
		return false
	}
	return hw.carriesHDR(hdr)
}

// gpuKeepsHDR is gpuTakesHDR for this run: an encoder whose real output lost a kind of HDR
// (revokeGPUHDR) no longer takes that kind, whatever the startup test said.
func (s *Service) gpuKeepsHDR(hw Encoder, hdr string, hdr10plus bool) bool {
	if !gpuTakesHDR(hw, hdr, hdr10plus) {
		return false
	}
	if !isHDR(hdr) {
		return true
	}
	s.hwBrokenMu.Lock()
	defer s.hwBrokenMu.Unlock()
	return !s.hdrRevoked[hw.Name+"|"+hdr]
}

// revokeGPUHDR stops sending a kind of HDR to a hardware encoder for the rest of this run,
// after a real conversion on it came out without its HDR (the check before the swap refused
// it, so the original is untouched). The file itself is tried again later — on the CPU.
func (s *Service) revokeGPUHDR(name, hdr, why string) {
	s.hwBrokenMu.Lock()
	if s.hdrRevoked == nil {
		s.hdrRevoked = map[string]bool{}
	}
	first := !s.hdrRevoked[name+"|"+hdr]
	s.hdrRevoked[name+"|"+hdr] = true
	s.hwBrokenMu.Unlock()
	if first {
		s.log.Warn("convert: a GPU conversion lost its HDR — "+hdr+" files go to the CPU for the rest of this run", "encoder", name, "why", why)
		s.event("warn", fmt.Sprintf("%s didn't keep the HDR of a %s film (%s) — %s films convert on the CPU from now on", name, hdr, why, hdr))
	}
}

// extractVideoFn copies the first few frames of a file's video stream into a bare stream (a
// seam for tests).
var extractVideoFn = func(ctx context.Context, ffmpeg, src, dst, format string) error {
	out, err := toolrun.Command(ctx, toolrun.Normal, ffmpeg, "-y", "-hide_banner", "-loglevel", "error",
		"-i", src, "-map", "0:v:0", "-c", "copy", "-frames:v", "3", "-f", format, dst).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%v (%s)", err, tailStr(out))
	}
	return nil
}

// streamHDRLost is hdrLost for a converted file's video stream on its own. ffmpeg 7 writes
// the source's mastering display and light level into the Matroska container whatever the
// encoder did with them, so a file whose GPU encoder dropped them still looks like HDR10
// from the outside — and a player, or a later remux, that goes by the video stream wouldn't
// see them. So a GPU's HDR output is also read without its container: the first frames,
// copied out as a bare stream, must carry it too. Fails closed: a stream that can't be read
// counts as lost.
func (s *Service) streamHDRLost(ctx context.Context, dst string, mi *MediaInfo, codec string) string {
	format, ext := rawVideoFormat(codec)
	raw := dst + ".hdrcheck." + ext
	defer os.Remove(raw)
	if err := extractVideoFn(ctx, s.ffmpeg, dst, raw, format); err != nil {
		return "couldn't read the converted video stream for its HDR: " + err.Error()
	}
	got, err := probeFn(ctx, s.ffprobe, raw)
	if err != nil {
		return "couldn't read the converted video stream for its HDR: " + err.Error()
	}
	if why := hdrLost(mi, got); why != "" {
		return strings.Replace(why, "the converted file", "the converted video stream", 1)
	}
	return ""
}

// encodedOnGPU reports the hardware encoder a job's video was encoded on, if it was (a
// hardware encode that failed and fell back to the CPU counts as the CPU's).
func (s *Service) encodedOnGPU(job *Job, codec string) (Encoder, bool) {
	s.mu.Lock()
	kind := job.encKind
	s.mu.Unlock()
	if kind == "" || kind == "cpu" {
		return Encoder{}, false
	}
	for _, e := range s.encoders {
		if e.Codec == codec && e.Kind == kind && e.Hardware {
			return e, true
		}
	}
	return Encoder{}, false
}

// gpuHDRKinds is hdrKinds for this run (revokeGPUHDR).
func (s *Service) gpuHDRKinds(e Encoder) []string {
	kinds := []string{}
	for _, k := range hdrKinds(e) {
		if s.gpuKeepsHDR(e, k, false) {
			kinds = append(kinds, k)
		}
	}
	return kinds
}

// hdrKinds lists the HDR formats an encoder carries, for Settings ("HDR10", "HLG").
func hdrKinds(e Encoder) []string {
	kinds := []string{}
	for _, k := range []string{"HDR10", "HLG"} {
		if e.carriesHDR(k) {
			kinds = append(kinds, k)
		}
	}
	return kinds
}

// hdrLost says what HDR a re-encode lost, or "" when it kept all of it: the same format
// (HDR10 may come out as HDR10+ — the HDR10+ pipeline found what ffprobe didn't label), the
// BT.2020 colour tags, and — for a PQ grade — the source's mastering display and light level,
// to within the rounding a format's own fractions cause. It applies to every encoder, CPU or
// GPU, so an encode that dropped the metadata never replaces the original.
func hdrLost(src, out *MediaInfo) string {
	want := src.EncodeHDR()
	if !isHDR(want) {
		return ""
	}
	if out == nil {
		return "the converted file couldn't be read for its HDR"
	}
	got := out.EncodeHDR()
	switch {
	case got == want, want == "HDR10" && got == "HDR10+":
	case !isHDR(got):
		return fmt.Sprintf("the converted file lost its %s (it came out as SDR)", want)
	default:
		return fmt.Sprintf("the converted file came out as %s instead of %s", got, want)
	}
	// The colour tags are held to what the source had: a BT.2020 source must come out BT.2020.
	// (A source that never said doesn't fail its conversion over it.)
	if src.ColorPrimaries == "bt2020" && out.ColorPrimaries != "bt2020" {
		return fmt.Sprintf("the converted file's colours are tagged %s instead of BT.2020", tagOrNone(out.ColorPrimaries))
	}
	if strings.HasPrefix(src.ColorSpace, "bt2020") && !strings.HasPrefix(out.ColorSpace, "bt2020") {
		return fmt.Sprintf("the converted file's colour matrix is tagged %s instead of BT.2020", tagOrNone(out.ColorSpace))
	}
	// HLG is relative and self-describing: it carries no mastering numbers (hdr10Params).
	if want == "HLG" || src.HDR10 == nil {
		return ""
	}
	if md := src.HDR10.MasterDisplay; masteringSet(md) {
		if out.HDR10 == nil || !sameMastering(md, out.HDR10.MasterDisplay) {
			return "the converted file lost its HDR mastering-display information"
		}
	}
	if cll := src.HDR10.MaxCLL; cll != "" && cll != "0,0" {
		if out.HDR10 == nil || out.HDR10.MaxCLL != cll {
			return "the converted file lost its HDR light-level information (MaxCLL " + cll + ")"
		}
	}
	return ""
}

func tagOrNone(v string) string {
	if v == "" || v == "unknown" {
		return "nothing"
	}
	return v
}

// masteringValues reads an x265-form mastering display ("G(x,y)B(x,y)R(x,y)WP(x,y)L(max,min)")
// into its ten numbers, in that order.
func masteringValues(md string) ([10]float64, bool) {
	var v [10]float64
	i := 0
	rest := md
	for _, label := range []string{"G", "B", "R", "WP", "L"} {
		if !strings.HasPrefix(rest, label+"(") {
			return v, false
		}
		end := strings.IndexByte(rest, ')')
		if end < 0 {
			return v, false
		}
		x, y, ok := strings.Cut(rest[len(label)+1:end], ",")
		if !ok {
			return v, false
		}
		a, err1 := strconv.ParseFloat(strings.TrimSpace(x), 64)
		b, err2 := strconv.ParseFloat(strings.TrimSpace(y), 64)
		if err1 != nil || err2 != nil {
			return v, false
		}
		v[i], v[i+1] = a, b
		i += 2
		rest = rest[end+1:]
	}
	return v, rest == ""
}

// masteringSet reports a mastering display worth keeping: readable, with a peak brightness.
func masteringSet(md string) bool {
	v, ok := masteringValues(md)
	return ok && v[8] > 0
}

// sameMastering compares two mastering displays to within rounding. The units are x265's
// (0.00002 for colour coordinates, 0.0001 cd/m² for brightness); AV1 stores finer or coarser
// fractions (a peak in 1/256 cd/m²) and some encoders truncate instead of rounding, so each
// value may be off by 2 units or 0.1%, whichever is more — far below anything visible, far
// above nothing at all.
func sameMastering(want, got string) bool {
	a, ok1 := masteringValues(want)
	b, ok2 := masteringValues(got)
	if !ok1 || !ok2 {
		return false
	}
	for i := range a {
		if math.Abs(a[i]-b[i]) > math.Max(2, math.Abs(a[i])*0.001) {
			return false
		}
	}
	return true
}

// hdrSample is a tiny synthetic HDR clip made at startup, and what it is.
type hdrSample struct {
	path string
	mi   *MediaInfo
}

// sampleSpec is the MediaInfo a startup clip of this HDR kind is made to.
func sampleSpec(kind string) *MediaInfo {
	trc := "smpte2084"
	if kind == "HLG" {
		trc = "arib-std-b67"
	}
	mi := &MediaInfo{HDR: kind, TenBit: true, Width: 320, Height: 240,
		ColorPrimaries: "bt2020", ColorTransfer: trc, ColorSpace: "bt2020nc"}
	if kind == "HDR10" {
		mi.HDR10 = &HDR10Meta{MasterDisplay: probeMasterDisplay, MaxCLL: probeMaxCLL}
	}
	return mi
}

// hdrSampleArgs is the ffmpeg command that writes a startup clip of this kind with a CPU
// encoder (libx265 or libsvtav1), using the same HDR parameters a CPU conversion does.
func hdrSampleArgs(cpu, kind, out string) []string {
	spec := sampleSpec(kind)
	// The frames are tagged too, as a real HDR film's decoded frames are: ffmpeg 7 takes an
	// encode's primaries and curve from its frames, and the -color_* options alone left the
	// SVT-AV1 clip untagged.
	tag := fmt.Sprintf("setparams=color_primaries=%s:color_trc=%s:colorspace=%s", spec.ColorPrimaries, spec.ColorTransfer, spec.ColorSpace)
	args := []string{"-y", "-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "testsrc2=d=0.5:s=320x240:r=10", "-vf", tag, "-pix_fmt", pixFmt10, "-c:v", cpu}
	var tags []string
	if cpu == "libsvtav1" {
		var params string
		params, tags = av1HDRParams(spec)
		args = append(args, "-preset", "12")
		if params != "" {
			args = append(args, "-svtav1-params", params)
		}
	} else {
		var params string
		params, tags = hdr10Params(spec)
		args = append(args, "-preset", "ultrafast", "-x265-params", "log-level=error:pools=none:"+params)
	}
	args = append(args, tags...)
	return append(args, out)
}

// makeHDRSample writes the startup clip of one HDR kind with the first CPU encoder that
// works, and reads it back: it's only any use if it really is that HDR.
func makeHDRSample(ctx context.Context, ffmpeg, ffprobe, dir, kind string, encs []Encoder) (*hdrSample, error) {
	var last error = errors.New("no working CPU encoder to make the HDR test clip with")
	for _, codec := range []string{"hevc", "av1"} {
		if !cpuWorks(codec, encs) {
			continue
		}
		cpu := cpuEncoder(codec).Name
		path := filepath.Join(dir, fmt.Sprintf("sample-%s-%s.mkv", strings.ToLower(kind), codec))
		if out, err := toolrun.Command(ctx, toolrun.Normal, ffmpeg, hdrSampleArgs(cpu, kind, path)...).CombinedOutput(); err != nil {
			last = fmt.Errorf("%s: %v: %s", cpu, err, firstLine(strings.TrimSpace(string(out))))
			continue
		}
		mi, err := probe(ctx, ffprobe, path)
		if err != nil {
			last = err
			continue
		}
		if why := hdrLost(sampleSpec(kind), mi); why != "" {
			last = fmt.Errorf("%s test clip from %s: %s", kind, cpu, why)
			continue
		}
		return &hdrSample{path: path, mi: mi}, nil
	}
	return nil, last
}

// rawVideoFormat is the bare-stream muxer for a codec: the probe's output has no container,
// so a container's own HDR fields can't stand in for metadata the encoder didn't write.
func rawVideoFormat(codec string) (format, ext string) {
	if codec == "av1" {
		return "obu", "obu"
	}
	return "hevc", "hevc"
}

// probeEncoderHDR encodes the startup clip with a hardware encoder — the same video options
// a real conversion of that clip would use (videoArgs), and the plain-settings retry a real
// conversion gets — and checks the bare stream it wrote kept the clip's HDR.
func probeEncoderHDR(ctx context.Context, ffmpeg, ffprobe, dir string, e Encoder, sample *hdrSample) error {
	format, ext := rawVideoFormat(e.Codec)
	out := filepath.Join(dir, fmt.Sprintf("%s-%s.%s", e.Name, strings.ToLower(sample.mi.EncodeHDR()), ext))
	vargs := videoArgs(e, sample.mi, Plan{VideoCodec: e.Codec, Quality: 24}, false, 2, true)
	run := func(v []string) error {
		args := []string{"-y", "-hide_banner", "-loglevel", "error"}
		args = append(args, globalArgs(e, false, "")...)
		args = append(args, "-i", sample.path, "-map", "0:v:0", "-an", "-sn")
		args = append(args, v...)
		args = append(args, "-f", format, out)
		cmd := toolrun.Command(ctx, toolrun.Normal, ffmpeg, args...)
		cmd.Env = libvaEnv(args)
		if b, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("%v: %s", err, firstLine(strings.TrimSpace(string(b))))
		}
		return nil
	}
	err := run(vargs)
	if plain := stripTuningParams(vargs); err != nil && !slices.Equal(plain, vargs) {
		err = run(plain)
	}
	if err != nil {
		return err
	}
	got, err := probe(ctx, ffprobe, out)
	if err != nil {
		return err
	}
	if why := hdrLost(sample.mi, got); why != "" {
		return errors.New(why)
	}
	return nil
}

// verifyEncodersHDR runs the HDR startup test on every working hardware encoder that writes
// 10-bit (HDR is always 10-bit), and marks the CPU encoders as carrying both kinds. It never
// touches Available: an encoder that fails here still encodes SDR as before.
func verifyEncodersHDR(ctx context.Context, ffmpeg, ffprobe string, encs []Encoder) {
	var hw []*Encoder
	for i := range encs {
		e := &encs[i]
		switch {
		case !e.Available:
		case !e.Hardware:
			e.HDR10, e.HLG = true, true
		case e.TenBit:
			hw = append(hw, e)
		}
	}
	if len(hw) == 0 || ffprobe == "" {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	dir, err := os.MkdirTemp("", "arrmada-hdrprobe-")
	if err != nil {
		return
	}
	defer os.RemoveAll(dir)
	for _, kind := range []string{"HDR10", "HLG"} {
		sample, err := makeHDRSample(ctx, ffmpeg, ffprobe, dir, kind, encs)
		if err != nil {
			for _, e := range hw {
				e.HDRNote = appendNote(e.HDRNote, kind+": "+firstLine(err.Error()))
			}
			continue
		}
		var wg sync.WaitGroup
		var mu sync.Mutex
		for _, e := range hw {
			wg.Add(1)
			go func(e *Encoder) {
				defer wg.Done()
				err := probeEncoderHDR(ctx, ffmpeg, ffprobe, dir, *e, sample)
				mu.Lock()
				defer mu.Unlock()
				if err != nil {
					e.HDRNote = appendNote(e.HDRNote, kind+": "+firstLine(err.Error()))
					return
				}
				switch kind {
				case "HDR10":
					e.HDR10 = true
				case "HLG":
					e.HLG = true
				}
			}(e)
		}
		wg.Wait()
	}
}

func appendNote(note, more string) string {
	if note == "" {
		return more
	}
	return note + "; " + more
}
