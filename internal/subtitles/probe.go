package subtitles

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

// SubTrack is one embedded subtitle stream in a media file.
type SubTrack struct {
	Index  int    `json:"index"`  // position among subtitle streams (ffmpeg's 0:s:N)
	Codec  string `json:"codec"`  // e.g. subrip, ass, hdmv_pgs_subtitle, dvd_subtitle
	Lang   string `json:"lang"`   // ISO code, lower-case ("" / "und" = unknown)
	Text   bool   `json:"text"`   // true = extractable to SRT; false = image sub (PGS/VOBSUB) → needs OCR
	Forced bool   `json:"forced,omitempty"`
}

// mediaInfo is the subtitle-relevant probe of a file: its runtime, spoken-audio languages, and
// embedded subtitle tracks. That's everything the coverage engine needs to decide, per language,
// whether to extract / OCR / download / AI-generate.
type mediaInfo struct {
	DurationSec float64    `json:"duration_sec"`
	AudioLangs  []string   `json:"audio_langs,omitempty"`
	Subs        []SubTrack `json:"subs,omitempty"`
}

// textSubCodecs are subtitle codecs we can extract straight to SRT (everything else — PGS, VOBSUB —
// is image-based and needs OCR).
var textSubCodecs = map[string]bool{
	"subrip": true, "srt": true, "ass": true, "ssa": true, "mov_text": true, "webvtt": true, "text": true,
}

// probeSubs runs ffprobe and parses a file's runtime, audio languages, and subtitle tracks.
func probeSubs(ctx context.Context, ffprobe, path string) (*mediaInfo, error) {
	out, err := exec.CommandContext(ctx, ffprobe,
		"-v", "quiet", "-print_format", "json", "-show_format", "-show_streams", path).Output()
	if err != nil {
		return nil, err
	}
	var raw struct {
		Streams []struct {
			CodecType   string `json:"codec_type"`
			CodecName   string `json:"codec_name"`
			Disposition struct {
				Forced int `json:"forced"`
			} `json:"disposition"`
			Tags struct {
				Language string `json:"language"`
			} `json:"tags"`
		} `json:"streams"`
		Format struct {
			Duration string `json:"duration"`
		} `json:"format"`
	}
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, err
	}
	mi := &mediaInfo{}
	mi.DurationSec, _ = strconv.ParseFloat(raw.Format.Duration, 64)
	subIdx := 0
	for _, st := range raw.Streams {
		switch st.CodecType {
		case "audio":
			if l := strings.ToLower(strings.TrimSpace(st.Tags.Language)); l != "" && l != "und" {
				mi.AudioLangs = append(mi.AudioLangs, l)
			}
		case "subtitle":
			mi.Subs = append(mi.Subs, SubTrack{
				Index:  subIdx,
				Codec:  st.CodecName,
				Lang:   strings.ToLower(strings.TrimSpace(st.Tags.Language)),
				Text:   textSubCodecs[st.CodecName],
				Forced: st.Disposition.Forced == 1,
			})
			subIdx++
		}
	}
	return mi, nil
}

// probeCache persists ffprobe results (subtitle_probe_cache table) so the library scan doesn't
// re-analyze every file on each page load — valid while the file's size + mtime are unchanged.
type probeCache struct{ db *sql.DB }

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
		 VALUES (?, ?, ?, ?, datetime('now'))
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
		return mi, nil
	}
	mi, err := probeSubs(ctx, s.ffprobe, path)
	if err != nil {
		return nil, err
	}
	s.cache.put(ctx, path, size, mtime, mi)
	return mi, nil
}
