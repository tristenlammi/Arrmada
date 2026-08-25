// Package audiobook merges a folder of chapter files (e.g. 100+ MP3s) into a single
// chapterized .m4b using ffmpeg. Each source file becomes one chapter, titled from its
// filename. Requires ffmpeg + ffprobe on PATH (bundled in the Docker image).
//
// The point of the merge is ONE FILE, not a smaller one. Where the sources can be carried
// into an MP4 container untouched they are copied, and where they can't the encode is
// sized from the source rather than from a fixed number — a GraphicAudio production is
// full-cast with score and effects, and squeezing that through a 64k encoder is exactly
// the loss this is meant to avoid.
package audiobook

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// Available reports whether ffmpeg and ffprobe are on PATH.
func Available() bool {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		return false
	}
	_, err := exec.LookPath("ffprobe")
	return err == nil
}

// audioInfo is what a source file tells us about itself.
type audioInfo struct {
	Codec      string
	BitrateBPS int64
	SampleRate int
	Channels   int
	DurationMS int64
}

// copyableCodecs are the codecs an MP4/M4B container carries natively, so a merge of them
// is a remux: the audio is byte-for-byte what it was.
//
// MP3 is deliberately absent. MP4 can technically hold it, but players handle it so
// inconsistently that a "successful" merge would produce a file some devices refuse — and
// the sources are deleted afterwards. An MP3 set is re-encoded instead, generously.
var copyableCodecs = map[string]bool{"aac": true, "alac": true}

// Merge concatenates the given audio files (in order) into a single .m4b at outPath, with
// one chapter per input file. It does not delete the sources — the caller decides.
func Merge(ctx context.Context, files []string, outPath string) error {
	if !Available() {
		return fmt.Errorf("audiobook merge needs ffmpeg — not installed")
	}
	if len(files) < 2 {
		return fmt.Errorf("need at least two files to merge")
	}

	tmp, err := os.MkdirTemp("", "arrmada-merge-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)

	infos := make([]audioInfo, len(files))
	for i, f := range files {
		infos[i] = probeAudio(ctx, f)
	}

	// 1) concat list for ffmpeg's concat demuxer.
	var list strings.Builder
	for _, f := range files {
		// The concat demuxer needs single-quotes escaped as '\''.
		list.WriteString("file '" + strings.ReplaceAll(f, "'", `'\''`) + "'\n")
	}
	listPath := filepath.Join(tmp, "list.txt")
	if err := os.WriteFile(listPath, []byte(list.String()), 0o644); err != nil {
		return err
	}

	// 2) chapter metadata (ffmetadata) — cumulative durations, title = filename.
	var meta strings.Builder
	meta.WriteString(";FFMETADATA1\n")
	var startMs int64
	for i, f := range files {
		endMs := startMs + infos[i].DurationMS
		title := strings.TrimSuffix(filepath.Base(f), filepath.Ext(f))
		meta.WriteString("[CHAPTER]\nTIMEBASE=1/1000\n")
		meta.WriteString("START=" + strconv.FormatInt(startMs, 10) + "\n")
		meta.WriteString("END=" + strconv.FormatInt(endMs, 10) + "\n")
		meta.WriteString("title=" + sanitizeMeta(title) + "\n")
		startMs = endMs
	}
	metaPath := filepath.Join(tmp, "chapters.ffmeta")
	if err := os.WriteFile(metaPath, []byte(meta.String()), 0o644); err != nil {
		return err
	}

	// 3) mux into a single .m4b, copying the audio when the container can carry it.
	args := []string{
		"-y", "-hide_banner", "-loglevel", "error",
		"-f", "concat", "-safe", "0", "-i", listPath,
		"-i", metaPath, "-map_metadata", "1",
		"-map", "0:a",
	}
	args = append(args, encodeArgs(infos)...)
	args = append(args, "-movflags", "+faststart", outPath)

	cmd := exec.CommandContext(ctx, "ffmpeg", args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("ffmpeg merge failed: %v — %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// Plan describes how a set of files would be merged, so the caller can say so in the log
// before spending an hour on it.
type Plan struct {
	Copy       bool  // true = remux, the audio is untouched
	BitrateBPS int64 // encode target when Copy is false
	SampleRate int
	Channels   int
}

// PlanFor reports how these files would be merged. Exported so a caller can log the
// decision; Merge makes the same one internally.
func PlanFor(ctx context.Context, files []string) Plan {
	infos := make([]audioInfo, len(files))
	for i, f := range files {
		infos[i] = probeAudio(ctx, f)
	}
	return planOf(infos)
}

// planOf decides between a remux and an encode.
//
// Copying needs every source to agree: same codec, and one the container carries, at the
// same sample rate and channel count. A mid-stream change of any of those produces a file
// that plays wrong from that point on, and since the sources are deleted afterwards there
// is no second chance — so anything less than unanimous falls back to encoding.
func planOf(infos []audioInfo) Plan {
	if len(infos) == 0 {
		return Plan{}
	}
	first := infos[0]
	uniform := copyableCodecs[first.Codec] && first.SampleRate > 0 && first.Channels > 0
	for _, in := range infos[1:] {
		if in.Codec != first.Codec || in.SampleRate != first.SampleRate || in.Channels != first.Channels {
			uniform = false
			break
		}
	}
	if uniform {
		return Plan{Copy: true, SampleRate: first.SampleRate, Channels: first.Channels}
	}
	return Plan{BitrateBPS: encodeBitrate(infos), SampleRate: maxSampleRate(infos), Channels: maxChannels(infos)}
}

// encodeArgs turns the plan into ffmpeg flags.
func encodeArgs(infos []audioInfo) []string {
	p := planOf(infos)
	if p.Copy {
		return []string{"-c:a", "copy"}
	}
	args := []string{"-c:a", "aac", "-b:a", strconv.FormatInt(p.BitrateBPS, 10)}
	if p.SampleRate > 0 {
		args = append(args, "-ar", strconv.Itoa(p.SampleRate))
	}
	if p.Channels > 0 {
		args = append(args, "-ac", strconv.Itoa(p.Channels))
	}
	// Sources that disagree on sample rate or channel layout are exactly why we're
	// encoding rather than copying. Resampling explicitly makes the join seamless instead
	// of letting the decoder re-initialise mid-stream and drift the chapter timings.
	return append(args, "-af", "aresample=async=1:first_pts=0")
}

const (
	// minEncodeBPS is the floor for a re-encode. Below this even spoken word audibly
	// suffers, and the merge exists to make one file rather than a smaller one.
	minEncodeBPS int64 = 128_000
	// maxEncodeBPS caps it. Past this AAC has nothing left to give a lossy source, and
	// the file is just bigger.
	maxEncodeBPS int64 = 320_000
	// bitrateHeadroom over the source. A second lossy generation loses a little whatever
	// you do; encoding at the source's own bitrate rather than below it keeps that
	// difference inaudible, and the extra quarter buys back the transcode's overhead.
	bitrateHeadroom = 1.25
)

// encodeBitrate sizes the encode from the loudest source rather than a fixed number.
//
// The old fixed 64k halved a 128k MP3 and gutted a GraphicAudio production — full cast,
// score and effects, which is nothing like the single narrator that number assumes.
func encodeBitrate(infos []audioInfo) int64 {
	var peak int64
	for _, in := range infos {
		if in.BitrateBPS > peak {
			peak = in.BitrateBPS
		}
	}
	target := int64(float64(peak) * bitrateHeadroom)
	if target < minEncodeBPS {
		target = minEncodeBPS
	}
	if target > maxEncodeBPS {
		target = maxEncodeBPS
	}
	return target
}

func maxSampleRate(infos []audioInfo) int {
	best := 0
	for _, in := range infos {
		if in.SampleRate > best {
			best = in.SampleRate
		}
	}
	return best
}

func maxChannels(infos []audioInfo) int {
	best := 0
	for _, in := range infos {
		if in.Channels > best {
			best = in.Channels
		}
	}
	return best
}

// probeAudio reads a file's codec, bitrate, sample rate, channels and duration. Anything
// unreadable comes back zeroed, which planOf treats as "can't copy" — the safe answer.
func probeAudio(ctx context.Context, path string) audioInfo {
	out, err := exec.CommandContext(ctx, "ffprobe", "-v", "quiet",
		"-select_streams", "a:0",
		"-show_entries", "stream=codec_name,bit_rate,sample_rate,channels:format=duration,bit_rate",
		"-of", "default=noprint_wrappers=1", path).Output()
	if err != nil {
		return audioInfo{}
	}
	var info audioInfo
	var formatBitrate int64
	for _, line := range strings.Split(string(out), "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok || v == "" || v == "N/A" {
			continue
		}
		switch k {
		case "codec_name":
			info.Codec = strings.ToLower(v)
		case "sample_rate":
			info.SampleRate, _ = strconv.Atoi(v)
		case "channels":
			info.Channels, _ = strconv.Atoi(v)
		case "bit_rate":
			// Printed twice — once for the stream, once for the format. The stream's is
			// the accurate one; keep the larger so a stream that reports nothing still
			// gets the container's figure.
			if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > formatBitrate {
				formatBitrate = n
			}
		case "duration":
			if secs, err := strconv.ParseFloat(v, 64); err == nil {
				info.DurationMS = int64(secs * 1000)
			}
		}
	}
	info.BitrateBPS = formatBitrate
	return info
}

func sanitizeMeta(s string) string {
	// ffmetadata treats =, ;, #, \ and newlines specially — escape with a backslash.
	repl := strings.NewReplacer("\\", "\\\\", "=", "\\=", ";", "\\;", "#", "\\#", "\n", " ")
	return repl.Replace(s)
}
