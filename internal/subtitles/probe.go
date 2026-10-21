package subtitles

import (
	"context"
	"encoding/json"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/tristenlammi/arrmada/internal/langs"
	"github.com/tristenlammi/arrmada/internal/store"
	"github.com/tristenlammi/arrmada/internal/toolrun"
)

// SubTrack is one embedded subtitle stream in a media file.
type SubTrack struct {
	Index int    `json:"index"` // position among subtitle streams (ffmpeg's 0:s:N)
	Codec string `json:"codec"` // e.g. subrip, ass, hdmv_pgs_subtitle, dvd_subtitle
	Lang  string `json:"lang"`  // ISO code, lower-case ("" / "und" = unknown)
	Text  bool   `json:"text"`  // true = extractable to SRT; false = image sub (PGS/VOBSUB) → needs OCR
	Title string `json:"title,omitempty"`
	// Forced is a foreign-parts-only track (signs, the odd line of another language):
	// a handful of cues, never the full subtitle. SDH carries the full dialogue plus
	// sound cues, so it still counts as the full subtitle, just a second choice.
	Forced  bool `json:"forced,omitempty"`
	SDH     bool `json:"sdh,omitempty"`
	Default bool `json:"default,omitempty"`
}

// probeVersion is what a fresh probe writes into mediaInfo.ProbeVersion. probeCached
// re-probes cache rows older than this once, so new fields reach files probed before them.
//
//	2: subtitle titles, forced/SDH detection from titles, default flags, video FPS.
const probeVersion = 2

// Release groups often mark forced and SDH tracks only in the title, with no disposition
// flag. Word-bounded and applied to subtitle streams only, so a title like "Forcedly"
// or an audio track called "CC" can't trip them.
var (
	forcedTitleRe = regexp.MustCompile(`(?i)\b(forced|signs( ?(&|and) ?songs)?|foreign parts?)\b`)
	sdhTitleRe    = regexp.MustCompile(`(?i)\b(sdh|cc|hearing.impaired)\b`)
)

// mediaInfo is the subtitle-relevant probe of a file: its runtime, spoken-audio languages, and
// embedded subtitle tracks. That's everything the coverage engine needs to decide, per language,
// whether to extract / OCR / download / AI-generate.
type mediaInfo struct {
	ProbeVersion int          `json:"probe_version,omitempty"` // see probeVersion; 0 = before it existed
	DurationSec  float64      `json:"duration_sec"`
	FPS          float64      `json:"fps,omitempty"` // first video stream's frame rate; 0 = unknown
	AudioLangs   []string     `json:"audio_langs,omitempty"`
	Audio        []AudioTrack `json:"audio,omitempty"` // every audio stream, in ffmpeg's 0:a:N order
	Subs         []SubTrack   `json:"subs,omitempty"`
	// Detected is the language whisper heard on each untagged audio stream (by 0:a:N
	// index), filled in when the AI rung first needed it and kept with the probe: it
	// holds as long as the file's size and mtime do.
	Detected map[int]DetectedLang `json:"detected,omitempty"`
}

// untagged reports whether an audio track carries no language the table knows ("",
// "und", "zxx", nonsense) — the ones detection listens to.
func (t AudioTrack) untagged() bool {
	_, ok := langs.Lookup(t.Lang)
	return !ok
}

// withDetected is the probe as the AI should see it: each untagged track that detection
// heard confidently (≥ detectMinProb) takes that language, in Audio and AudioLangs. The
// receiver isn't changed; nil stays nil.
func (mi *mediaInfo) withDetected() *mediaInfo {
	if mi == nil || len(mi.Detected) == 0 {
		return mi
	}
	cp := *mi
	cp.Audio = append([]AudioTrack(nil), mi.Audio...)
	cp.AudioLangs = append([]string(nil), mi.AudioLangs...)
	for i, t := range cp.Audio {
		d, ok := mi.Detected[t.Index]
		if !ok || !t.untagged() || d.Lang == "" || d.Prob < detectMinProb {
			continue
		}
		cp.Audio[i].Lang = d.Lang
		cp.AudioLangs = append(cp.AudioLangs, d.Lang)
	}
	return &cp
}

// AudioTrack is one audio stream: what the AI transcribes. A MULTI release carries
// several, and ffmpeg's default pick (the first, or the one flagged default) is as
// likely to be the dub as the original — which is how an "English" AI subtitle came
// out in French.
type AudioTrack struct {
	Index   int    `json:"index"` // position among audio streams (ffmpeg's 0:a:N)
	Lang    string `json:"lang,omitempty"`
	Title   string `json:"title,omitempty"`
	Default bool   `json:"default,omitempty"`
}

// textSubCodecs are subtitle codecs we can extract straight to SRT (everything else — PGS, VOBSUB —
// is image-based and needs OCR).
var textSubCodecs = map[string]bool{
	"subrip": true, "srt": true, "ass": true, "ssa": true, "mov_text": true, "webvtt": true, "text": true,
}

// probeSubs runs ffprobe and parses a file's runtime, audio languages, and subtitle tracks.
func probeSubs(ctx context.Context, ffprobe, path string) (*mediaInfo, error) {
	out, err := toolrun.Command(ctx, toolrun.Normal, ffprobe,
		"-v", "quiet", "-print_format", "json", "-show_format", "-show_streams", path).Output()
	if err != nil {
		return nil, err
	}
	return parseProbe(out)
}

// parseProbe turns ffprobe's JSON into a mediaInfo. Split from probeSubs so the parsing
// is testable on a fixture without any real media.
func parseProbe(out []byte) (*mediaInfo, error) {
	var raw struct {
		Streams []struct {
			CodecType    string `json:"codec_type"`
			CodecName    string `json:"codec_name"`
			RFrameRate   string `json:"r_frame_rate"`
			AvgFrameRate string `json:"avg_frame_rate"`
			Disposition  struct {
				Forced          int `json:"forced"`
				Default         int `json:"default"`
				HearingImpaired int `json:"hearing_impaired"`
				AttachedPic     int `json:"attached_pic"`
			} `json:"disposition"`
			Tags struct {
				Language string `json:"language"`
				Title    string `json:"title"`
			} `json:"tags"`
		} `json:"streams"`
		Format struct {
			Duration string `json:"duration"`
		} `json:"format"`
	}
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, err
	}
	mi := &mediaInfo{ProbeVersion: probeVersion}
	mi.DurationSec, _ = strconv.ParseFloat(raw.Format.Duration, 64)
	subIdx, audioIdx := 0, 0
	for _, st := range raw.Streams {
		switch st.CodecType {
		case "video":
			// Cover art is a "video" stream too; the frame rate belongs to the real one.
			if mi.FPS == 0 && st.Disposition.AttachedPic == 0 {
				if mi.FPS = parseRate(st.RFrameRate); mi.FPS == 0 {
					mi.FPS = parseRate(st.AvgFrameRate)
				}
			}
		case "audio":
			l := strings.ToLower(strings.TrimSpace(st.Tags.Language))
			if l != "" && l != "und" {
				mi.AudioLangs = append(mi.AudioLangs, l)
			}
			mi.Audio = append(mi.Audio, AudioTrack{
				Index:   audioIdx,
				Lang:    l,
				Title:   strings.TrimSpace(st.Tags.Title),
				Default: st.Disposition.Default == 1,
			})
			audioIdx++
		case "subtitle":
			title := strings.TrimSpace(st.Tags.Title)
			mi.Subs = append(mi.Subs, SubTrack{
				Index:   subIdx,
				Codec:   st.CodecName,
				Lang:    strings.ToLower(strings.TrimSpace(st.Tags.Language)),
				Text:    textSubCodecs[st.CodecName],
				Title:   title,
				Forced:  st.Disposition.Forced == 1 || forcedTitleRe.MatchString(title),
				SDH:     st.Disposition.HearingImpaired == 1 || sdhTitleRe.MatchString(title),
				Default: st.Disposition.Default == 1,
			})
			subIdx++
		}
	}
	return mi, nil
}

// parseRate reads an ffprobe rate like "24000/1001" or "25/1"; 0 when absent or "0/0".
func parseRate(r string) float64 {
	num, den, ok := strings.Cut(strings.TrimSpace(r), "/")
	if !ok {
		f, _ := strconv.ParseFloat(num, 64)
		return f
	}
	n, err1 := strconv.ParseFloat(num, 64)
	d, err2 := strconv.ParseFloat(den, 64)
	if err1 != nil || err2 != nil || d == 0 {
		return 0
	}
	return n / d
}

// probeCache persists ffprobe results (subtitle_probe_cache table) so the library scan doesn't
// re-analyze every file on each page load — valid while the file's size + mtime are unchanged.
type probeCache struct{ db store.Pool }

func (c *probeCache) get(ctx context.Context, path string, size, mtime int64) (*mediaInfo, bool) {
	if c == nil || c.db == nil {
		return nil, false
	}
	var infoJSON string
	err := c.db.QueryRowContext(ctx,
		`SELECT info_json FROM subtitle_probe_cache WHERE path = ? AND size_bytes = ? AND mtime_unix = ?`,
		path, size, mtime).Scan(&infoJSON)
	if err != nil {
		return nil, false
	}
	var mi mediaInfo
	if json.Unmarshal([]byte(infoJSON), &mi) != nil {
		return nil, false
	}
	return &mi, true
}

func (c *probeCache) put(ctx context.Context, path string, size, mtime int64, mi *mediaInfo) {
	if c == nil || c.db == nil {
		return
	}
	b, err := json.Marshal(mi)
	if err != nil {
		return
	}
	_, _ = c.db.ExecContext(ctx,
		`INSERT INTO subtitle_probe_cache (path, size_bytes, mtime_unix, info_json, probed_at)
		 VALUES (?, ?, ?, ?, `+store.UTCNowText(c.db, "")+`)
		 ON CONFLICT(path) DO UPDATE SET
		   size_bytes = excluded.size_bytes,
		   mtime_unix = excluded.mtime_unix,
		   info_json  = excluded.info_json,
		   probed_at  = excluded.probed_at`,
		path, size, mtime, string(b))
}

// probeCached returns a file's subtitle probe from the cache when the file is unchanged, otherwise
// runs ffprobe once and stores the result.
func (s *Service) probeCached(ctx context.Context, path string) (*mediaInfo, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	size, mtime := fi.Size(), fi.ModTime().Unix()
	if mi, ok := s.cache.get(ctx, path, size, mtime); ok {
		// Entries from an older probe are missing fields the pipeline now relies on
		// (audio streams, subtitle titles and flags, FPS): probe again, once.
		if mi.ProbeVersion >= probeVersion {
			return mi, nil
		}
	}
	run := s.ffprobeRun
	if run == nil {
		run = probeSubs
	}
	mi, err := run(ctx, s.ffprobe, path)
	if err != nil {
		return nil, err
	}
	s.cache.put(ctx, path, size, mtime, mi)
	return mi, nil
}

// cachedProbe is a file's probe from the cache alone — nil when it was never probed, has
// changed since, or was probed before the current version. It never runs ffprobe, so the
// passes that only list directories can use it.
func (s *Service) cachedProbe(ctx context.Context, path string) *mediaInfo {
	fi, err := os.Stat(path)
	if err != nil {
		return nil
	}
	mi, ok := s.cache.get(ctx, path, fi.Size(), fi.ModTime().Unix())
	if !ok || mi.ProbeVersion < probeVersion {
		return nil
	}
	return mi
}

// saveProbe writes a probe back to the cache — after detection added to it — keyed by
// the file's current size and mtime, as probeCached would have.
func (s *Service) saveProbe(ctx context.Context, path string, mi *mediaInfo) {
	fi, err := os.Stat(path)
	if err != nil {
		return
	}
	s.cache.put(context.WithoutCancel(ctx), path, fi.Size(), fi.ModTime().Unix(), mi)
}
