package convert

import (
	"context"
	"encoding/json"
	"strings"
	"time"
)

// The Switch-on summary. Switching automatic conversion on starts in-place replacements
// across the whole library, so before it does, the page shows what that means in this
// library's own numbers: how many files, how much space, and how many lose something —
// Dolby Vision, closed captions, image subtitles, audio tracks. The same summary, narrowed
// to one show or season, confirms a bulk Convert-now. It reads the runner's cached
// candidate list, so it is exactly what the runner would work through (give or take the
// minutes the cache may lag a settings change — the page says "about").

// candEffects is what converting one candidate costs it, worked out alongside its plan.
type candEffects struct {
	seriesID int64
	season   int
	estBytes int64

	dv              bool // the Dolby Vision layer goes (the HDR10/HLG base stays)
	cc              bool // embedded closed captions go with the re-encode
	containerChange bool // MP4, AVI, M2TS… become Matroska
	audioDropped    int  // audio tracks the filters remove
	imageSubs       int  // image subtitle tracks removed
	forcedLost      int  // forced subtitle tracks removed with nothing to replace them
}

// effectsOf works out a candidate's effects from the plan the runner would run.
func effectsOf(mi *MediaInfo, plan Plan, n Needs, size, seriesID int64, season int) candEffects {
	e := candEffects{seriesID: seriesID, season: season, estBytes: size - n.Save}
	reencode := plan.VideoCodec != ""
	e.dv = reencode && mi.HDR == "Dolby Vision" && !plan.KeepDV // kept as 8.1 under "keep"
	e.cc = reencode && mi.HasCC
	if c := strings.ToUpper(mi.Container); c != "" && c != "MKV" {
		e.containerChange = true
	}
	e.audioDropped = len(mi.Audio) - len(keptAudio(mi, plan))
	kept := map[int]bool{}
	for _, s := range keptSubs(mi, plan) {
		kept[s.SubIndex] = true
	}
	for _, s := range mi.Subs {
		if !s.Text && !kept[s.SubIndex] {
			e.imageSubs++
		}
	}
	e.forcedLost = forcedLost(mi, plan)
	return e
}

// AutoPreview is the summary: how many files would convert, their size now and after, and
// how many of them lose what.
type AutoPreview struct {
	Files    int   `json:"files"`
	Reencode int   `json:"reencode"`  // of those, re-encodes (the rest only rewrite tracks)
	Bytes    int64 `json:"bytes"`     // their size now
	EstBytes int64 `json:"est_bytes"` // their estimated size after
	DVFiles  int   `json:"dv_files"`  // lose the Dolby Vision layer
	// DVLeftAlone counts the Dolby Vision files whose picture is left alone by the Dolby
	// Vision choice (new installs' default), so the summary says why they aren't in it.
	DVLeftAlone int   `json:"dv_left_alone"`
	CCFiles     int   `json:"cc_files"` // lose embedded closed captions
	ImageSubs   int   `json:"image_sub_tracks"`
	AudioDrops  int   `json:"filtered_audio_tracks"`
	Containers  int   `json:"container_changes"`
	ForcedLost  int   `json:"forced_tracks"`
	AsOf        int64 `json:"as_of"`
}

// PreviewFilter narrows the summary to one show (SeriesID > 0) or one of its seasons
// (Season >= 0); the zero SeriesID means the whole library.
type PreviewFilter struct {
	SeriesID int64
	Season   int
}

// AutoPreview sums what converting the candidates would do. Files the runner won't pick —
// repeated failures, and skips that won't resolve by waiting — are left out, the same way
// the bulk request leaves them out.
func (s *Service) AutoPreview(ctx context.Context, f PreviewFilter) AutoPreview {
	out := AutoPreview{AsOf: time.Now().Unix()}
	permanent := s.skips.permanentKeys(ctx)
	blocked := s.failures.blockedKeys(ctx, maxFailures)
	p := s.prefs(ctx)
	if p.dolbyVision == DVSkip {
		out.DVLeftAlone = s.dvLeftAlone(ctx, f)
	}
	for _, c := range s.autoCandidates(ctx, p) {
		if permanent[c.Key] || blocked[c.Key] {
			continue
		}
		e := c.effects
		if f.SeriesID > 0 && (c.Kind != "episode" || e.seriesID != f.SeriesID || (f.Season >= 0 && e.season != f.Season)) {
			continue
		}
		out.Files++
		if c.Video {
			out.Reencode++
		}
		out.Bytes += c.Size
		out.EstBytes += e.estBytes
		if e.dv {
			out.DVFiles++
		}
		if e.cc {
			out.CCFiles++
		}
		if e.containerChange {
			out.Containers++
		}
		out.ImageSubs += e.imageSubs
		out.AudioDrops += e.audioDropped
		out.ForcedLost += e.forcedLost
	}
	return out
}

// dvLeftAlone counts the indexed Dolby Vision files (narrowed by f) whose picture would be
// worth converting but is left alone because of the Dolby Vision choice. Profile 5 never
// converts whatever the choice, so it isn't counted.
func (s *Service) dvLeftAlone(ctx context.Context, f PreviewFilter) int {
	if s.index == nil {
		return 0
	}
	q := `SELECT info_json FROM convert_library WHERE info_json LIKE '%"hdr":"Dolby Vision"%'`
	var args []any
	if f.SeriesID > 0 {
		q += ` AND media_type = 'episode' AND series_id = ?`
		args = append(args, f.SeriesID)
		if f.Season >= 0 {
			q += ` AND season = ?`
			args = append(args, f.Season)
		}
	}
	rows, err := s.index.db.QueryContext(ctx, q, args...)
	if err != nil {
		return 0
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var infoJSON string
		var mi MediaInfo
		if rows.Scan(&infoJSON) != nil || json.Unmarshal([]byte(infoJSON), &mi) != nil {
			continue
		}
		if worth, _ := videoWorth(&mi); worth && mi.HDR == "Dolby Vision" {
			n++
		}
	}
	return n
}
