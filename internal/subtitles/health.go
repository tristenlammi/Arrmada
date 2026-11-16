package subtitles

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// Health is how well a present subtitle lines up with its video's speech, from the last
// sync check (syncgate.go) — stored on its provenance row, never computed by a library
// pass: checking means reading the whole soundtrack.

// Health states.
const (
	HealthOK        = "ok"        // confidence ≥ syncKeep: it fits the speech
	HealthUncertain = "uncertain" // between syncReject and syncKeep
	HealthBad       = "bad"       // below syncReject: it doesn't fit this audio
	HealthUnknown   = "unknown"   // checked, but there was too little to measure
	HealthUnchecked = "unchecked" // never checked
)

// LangHealth is one present language's Health.
type LangHealth struct {
	State     string `json:"state"`
	Score     int    `json:"score,omitempty"`      // sync confidence 0-100, when checked
	OffsetMs  int    `json:"offset_ms,omitempty"`  // how late it ran when checked (+ = late)
	ShiftMs   int    `json:"shift_ms,omitempty"`   // the shift Arrmada applied to it
	Stretched bool   `json:"stretched,omitempty"`  // a frame-rate correction was applied too
	CheckedAt int64  `json:"checked_at,omitempty"` // unix
	Source    string `json:"source,omitempty"`     // the sidecar's provenance, when Arrmada knows it
	// Note says it in words: "shifted −3.0 s · checked 12 Oct 2026", "not checked".
	Note string `json:"note"`
}

// healthOf reads a provenance row's Health.
func healthOf(f SubtitleFile) LangHealth {
	h := LangHealth{State: HealthUnchecked, Source: f.Source}
	if f.SyncConfidence == nil {
		h.Note = "not checked"
		switch f.Source {
		case SourceEmbedded:
			h.Note += " · from the file's own track"
		case SourceAITranscribe, SourceAITranslate:
			h.Note += " · AI-made from this audio"
		case SourceOpenSubtitles:
			if f.HashMatch {
				h.Note += " · hash-matched to this file"
			}
		}
		return h
	}
	h.Score, h.CheckedAt = *f.SyncConfidence, f.SyncCheckedAt
	if f.SyncOffsetMs != nil {
		h.OffsetMs = *f.SyncOffsetMs
	}
	if f.SyncShiftMs != nil {
		h.ShiftMs = *f.SyncShiftMs
	}
	h.Stretched = f.SyncScale != nil && *f.SyncScale != 1
	switch {
	case h.Score >= syncKeep:
		h.State = HealthOK
	case h.Score >= syncReject:
		h.State = HealthUncertain
	default:
		h.State = HealthBad
	}
	var parts []string
	switch {
	case h.ShiftMs != 0 || h.Stretched:
		scale := 1.0
		if h.Stretched {
			scale = *f.SyncScale
		}
		parts = append(parts, shiftWords(h.ShiftMs, scale))
	case h.State == HealthBad:
		parts = append(parts, "doesn't match the speech")
	case abs(h.OffsetMs) > syncTolMs:
		parts = append(parts, "runs "+lateWords(h.OffsetMs))
	default:
		parts = append(parts, "in sync")
	}
	if h.State == HealthUncertain {
		parts = append(parts, "uncertain")
	}
	if h.CheckedAt > 0 {
		parts = append(parts, "checked "+time.Unix(h.CheckedAt, 0).Format("2006-01-02"))
	}
	h.Note = strings.Join(parts, " · ")
	return h
}

// lateWords says which way a subtitle is off: "3.0 s late", "1.5 s early".
func lateWords(offsetMs int) string {
	if offsetMs < 0 {
		return fmt.Sprintf("%.1f s early", float64(-offsetMs)/1000)
	}
	return fmt.Sprintf("%.1f s late", float64(offsetMs)/1000)
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// healthRank orders states worst first, for a file's roll-up.
var healthRank = map[string]int{HealthBad: 0, HealthUncertain: 1, HealthUnknown: 2, HealthUnchecked: 3, HealthOK: 4}

// applyHealth fills each present language's Health from the file's live provenance rows,
// and the file's Health as its worst language. A row written for another video file (the
// movie was upgraded) or a language without a row reads "not checked".
func applyHealth(fs *FileSubs, rows []SubtitleFile) {
	var worst *LangHealth
	for i := range fs.Languages {
		ls := &fs.Languages[i]
		if !ls.Have || ls.Variant == VariantForced { // a forced want's file has no sync check of its own
			continue
		}
		h := LangHealth{State: HealthUnchecked, Note: "not checked"}
		best := -1
		for j, f := range rows {
			if f.VideoPath != fs.Path || !coversFull(f.Variant) || !langMatches(f.Lang, ls.Lang) {
				continue
			}
			// The full sidecar's row over an SDH one; the newest when there are several.
			if best < 0 || (rows[best].Variant != VariantFull && f.Variant == VariantFull) ||
				(rows[best].Variant == f.Variant && f.CreatedAt >= rows[best].CreatedAt) {
				best = j
			}
		}
		if best >= 0 {
			h = healthOf(rows[best])
		}
		ls.Health = &h
		if worst == nil || healthRank[h.State] < healthRank[worst.State] ||
			(h.State == worst.State && h.Score < worst.Score) {
			hh := h
			worst = &hh
		}
	}
	if worst == nil {
		fs.Health = nil
		return
	}
	fs.Health = &SubHealth{State: worst.State, Score: worst.Score}
	if worst.Note != "" {
		fs.Health.Notes = []string{worst.Note}
	}
}

// liveFilesByKey reads every live provenance row in one query, grouped by media key — a
// library pass's whole Health view. An error just means Health reads "not checked" this
// time.
func (s *Service) liveFilesByKey(ctx context.Context) map[string][]SubtitleFile {
	rows, err := s.ledger.LiveFiles(ctx)
	if err != nil {
		s.log.Warn("subtitles: could not read subtitle health for the library", "err", err)
		return nil
	}
	out := map[string][]SubtitleFile{}
	for _, f := range rows {
		out[f.MediaKey] = append(out[f.MediaKey], f)
	}
	return out
}

// mediaKeyOf is a FileSubs entry's media key (Job.key() format).
func mediaKeyOf(fs *FileSubs) string {
	if fs.Kind == "episode" {
		return fmt.Sprintf("e:%d:%d:%d", fs.SeriesID, fs.Season, fs.Episode)
	}
	return fmt.Sprintf("m:%d", fs.MovieID)
}

// fillHealth is applyHealth for one file, reading its rows itself (a patched movie, one
// show's episodes).
func (s *Service) fillHealth(ctx context.Context, fs *FileSubs) {
	rows, err := s.ledger.FilesFor(ctx, mediaKeyOf(fs))
	if err != nil {
		s.log.Warn("subtitles: could not read subtitle health", "media_key", mediaKeyOf(fs), "err", err)
	}
	applyHealth(fs, rows)
}
