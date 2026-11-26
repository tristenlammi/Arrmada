package automation

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/tristenlammi/arrmada/internal/mediainfo"
	"github.com/tristenlammi/arrmada/internal/music"
	"github.com/tristenlammi/arrmada/internal/toolrun"
)

// MusicTranscodePrefs is the optional post-import music transcode (issue #140). Off by
// default. Transcoding happens on a staging copy only; downloads/seeding sources are never
// touched.
type MusicTranscodePrefs struct {
	Enabled      bool
	Format       string // "mp3"
	Quality      string // v0, v2, 320, 256, 192
	LosslessOnly bool   // don't re-encode lossy sources by default
}

func normalizeMusicTranscode(p MusicTranscodePrefs) MusicTranscodePrefs {
	p.Format = strings.ToLower(strings.TrimSpace(p.Format))
	if p.Format == "" {
		p.Format = "mp3"
	}
	p.Quality = strings.ToLower(strings.TrimSpace(p.Quality))
	switch p.Quality {
	case "v0", "v2", "320", "256", "192":
	default:
		p.Quality = "v0"
	}
	return p
}

// musicTranscodeTarget reports the target tier and whether src (at its held tier) should be
// transcoded. It never re-encodes into the same or lower tier.
func musicTranscodeTarget(src music.Quality, p MusicTranscodePrefs) (music.Quality, bool) {
	p = normalizeMusicTranscode(p)
	if !p.Enabled || p.Format != "mp3" {
		return music.QualityUnknown, false
	}
	if p.LosslessOnly && !music.IsLosslessQuality(src) {
		return music.QualityUnknown, false
	}
	target, ok := musicTranscodeQualityTier(p.Quality)
	if !ok {
		return music.QualityUnknown, false
	}
	if src == music.QualityUnknown {
		// Unknown lossy source: don't guess; avoid potential quality loss.
		if p.LosslessOnly {
			return target, true
		}
		return music.QualityUnknown, false
	}
	if music.QualityRank(src) <= music.QualityRank(target) {
		return music.QualityUnknown, false
	}
	return target, true
}

func musicTranscodeQualityTier(q string) (music.Quality, bool) {
	switch strings.ToLower(strings.TrimSpace(q)) {
	case "v0":
		return music.QualityMP3V0, true
	case "v2":
		return music.QualityMP3V2, true
	case "320":
		return music.QualityMP3320, true
	case "256":
		return music.QualityMP3256, true
	case "192":
		return music.QualityMP3192, true
	default:
		return music.QualityUnknown, false
	}
}

func musicTranscodeArgs(src, dst, quality string) ([]string, error) {
	args := []string{"-hide_banner", "-loglevel", "error", "-nostdin", "-y", "-i", src, "-vn", "-map_metadata", "0", "-map", "0:a:0"}
	switch strings.ToLower(strings.TrimSpace(quality)) {
	case "v0":
		args = append(args, "-c:a", "libmp3lame", "-q:a", "0")
	case "v2":
		args = append(args, "-c:a", "libmp3lame", "-q:a", "2")
	case "320":
		args = append(args, "-c:a", "libmp3lame", "-b:a", "320k")
	case "256":
		args = append(args, "-c:a", "libmp3lame", "-b:a", "256k")
	case "192":
		args = append(args, "-c:a", "libmp3lame", "-b:a", "192k")
	default:
		return nil, fmt.Errorf("unsupported mp3 quality %q", quality)
	}
	args = append(args, "-id3v2_version", "3", dst)
	return args, nil
}

// transcodeAudioToMP3 writes a new MP3 next to the staging file. It never touches src.
func (c *Coordinator) transcodeAudioToMP3(ctx context.Context, src, quality string) (string, error) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		return "", mediainfo.ErrNoFFmpeg
	}
	dir := filepath.Dir(src)
	tmp, err := os.CreateTemp(dir, ".arrmada-music-transcode-*.mp3")
	if err != nil {
		return "", err
	}
	dst := tmp.Name()
	_ = tmp.Close()
	_ = os.Remove(dst)
	args, err := musicTranscodeArgs(src, dst, quality)
	if err != nil {
		_ = os.Remove(dst)
		return "", err
	}
	cmd := toolrun.Command(ctx, toolrun.Normal, "ffmpeg", args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		_ = os.Remove(dst)
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("ffmpeg mp3 transcode: %s", msg)
	}
	return dst, nil
}

func replaceExt(path, ext string) string {
	if ext == "" || ext[0] != '.' {
		ext = "." + ext
	}
	return strings.TrimSuffix(path, filepath.Ext(path)) + ext
}

func (c *Coordinator) maybeTranscodePlan(ctx context.Context, src string, tier music.Quality, bitrate int, probe *mediainfo.AudioInfo, p MusicTranscodePrefs) (outTier music.Quality, outBitrate int, outProbe *mediainfo.AudioInfo, outExt string, transcode MusicTranscodePrefs, err error) {
	outTier, outBitrate, outProbe, outExt = tier, bitrate, probe, filepath.Ext(src)
	target, ok := musicTranscodeTarget(tier, p)
	if !ok {
		return
	}
	if _, lookErr := exec.LookPath("ffmpeg"); lookErr != nil {
		c.log.Warn("music import: ffmpeg not available, skipping transcode", "file", filepath.Base(src))
		return
	}
	transcode = normalizeMusicTranscode(p)
	outTier = target
	outBitrate = targetBitrateKbps(target)
	outProbe = nil // the encoded file will be probed after transcode
	outExt = ".mp3"
	_ = ctx
	return
}

func targetBitrateKbps(q music.Quality) int {
	switch q {
	case music.QualityMP3320:
		return 320
	case music.QualityMP3256:
		return 256
	case music.QualityMP3192:
		return 192
	case music.QualityMP3V0:
		return 245
	case music.QualityMP3V2:
		return 190
	default:
		return 0
	}
}

// prepareTranscodes creates transcoded staging copies for placements that requested it.
// It never touches the download/source files. Cleanup removes created scratch files.
func (c *Coordinator) prepareTranscodes(ctx context.Context, al music.Album, plan []placement, res *albumImport) ([]placement, func(), bool) {
	var scratch []string
	cleanup := func() {
		for _, p := range scratch {
			_ = os.Remove(p)
		}
	}
	out := make([]placement, 0, len(plan))
	for _, p := range plan {
		if !p.transcode.Enabled {
			out = append(out, p)
			continue
		}
		dst, err := c.transcodeAudioToMP3(ctx, p.f.Path, p.transcode.Quality)
		if err != nil {
			c.log.Warn("music import: transcoding failed, placing original", "album", al.Title, "track", p.t.Title, "err", err)
			orig := p
			orig.transcode = MusicTranscodePrefs{}
			orig.tier = p.origTier
			orig.bitrate = p.origBitrate
			orig.probe = p.origProbe
			orig.target = p.origTarget
			orig.albumDir = p.origAlbumDir
			out = append(out, orig)
			continue
		}
		scratch = append(scratch, dst)
		np := p
		np.f = music.AudioFile{Path: dst, Track: p.f.Track, Disc: p.f.Disc}
		np.from = p.source()
		np.scratch = true
		if probe, perr := mediainfo.ProbeAudio(ctx, dst); perr == nil {
			np.probe = &probe
			if q := music.TierFromProbe(probe); q != music.QualityUnknown {
				np.tier = q
			}
			if probe.BitrateKbps > 0 {
				np.bitrate = probe.BitrateKbps
			}
		}
		out = append(out, np)
	}
	if len(out) == 0 {
		return nil, cleanup, false
	}
	return out, cleanup, true
}
