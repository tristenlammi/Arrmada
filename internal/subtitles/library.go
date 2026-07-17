package subtitles

import (
	"context"
	"fmt"
	"strings"
)

// LangStatus is one kept-language's coverage for a file: whether an external SRT exists, and if
// not, the best available source to create one (the "best-source → AI" priority).
type LangStatus struct {
	Lang   string `json:"lang"`
	Have   bool   `json:"have"`             // an external .srt for this language sits next to the file
	Source string `json:"source,omitempty"` // when !Have: extract | ocr | download | ai
}

// SubHealth is the Tier-1 sync/health score for a file's subtitle (0-100). Nil until the scoring
// phase lands — the field is here now so the model and UI don't need retrofitting.
type SubHealth struct {
	Score int      `json:"score"`
	Notes []string `json:"notes,omitempty"`
}

// FileSubs is one media file's subtitle picture for the Library tab.
type FileSubs struct {
	Kind        string       `json:"kind"` // "movie" | "episode"
	MovieID     int64        `json:"movie_id,omitempty"`
	SeriesID    int64        `json:"series_id,omitempty"`
	Season      int          `json:"season,omitempty"`
	Episode     int          `json:"episode,omitempty"`
	Title       string       `json:"title"`
	Year        int          `json:"year,omitempty"`
	PosterURL   string       `json:"poster_url,omitempty"`
	Path        string       `json:"path"`
	DurationSec float64      `json:"duration_sec,omitempty"`
	AudioLangs  []string     `json:"audio_langs,omitempty"`
	Embedded    []SubTrack   `json:"embedded"`            // embedded subtitle tracks (for badges/filters)
	External    []string     `json:"external"`            // kept languages that already have a sidecar
	Languages   []LangStatus `json:"languages"`           // per-kept-language coverage + best source
	Health      *SubHealth   `json:"health,omitempty"`    // Tier-1 sync/health score (nil until scored)
	Missing     int          `json:"missing"`             // count of kept languages still without an SRT
}

// Library probes every downloaded movie (media="movies") or episode (media="tv") and returns its
// subtitle coverage against the kept languages. Cached probes keep it fast after the first scan.
func (s *Service) Library(ctx context.Context, media string) ([]FileSubs, error) {
	langs := s.languages(ctx)
	canDownload := s.provider != nil && s.provider.CanDownload()
	var out []FileSubs

	if media == "tv" {
		list, err := s.series.List(ctx)
		if err != nil {
			return nil, err
		}
		for _, sm := range list {
			full, err := s.series.Get(ctx, sm.ID)
			if err != nil {
				continue
			}
			for _, sn := range full.Seasons {
				for _, e := range sn.Episodes {
					if !e.HasFile || e.FilePath == "" {
						continue
					}
					fs := FileSubs{
						Kind: "episode", SeriesID: full.ID, Season: e.SeasonNumber, Episode: e.EpisodeNumber,
						Title:     fmt.Sprintf("%s - S%02dE%02d", full.Title, e.SeasonNumber, e.EpisodeNumber),
						Year:      full.Year, PosterURL: full.PosterURL, Path: e.FilePath,
					}
					s.fillCoverage(ctx, &fs, langs, canDownload)
					out = append(out, fs)
				}
			}
		}
		return out, nil
	}

	list, err := s.movies.List(ctx)
	if err != nil {
		return nil, err
	}
	for _, m := range list {
		if !m.HasFile || m.MovieFilePath == "" {
			continue
		}
		fs := FileSubs{
			Kind: "movie", MovieID: m.ID, Title: m.Title, Year: m.Year,
			PosterURL: m.PosterURL, Path: m.MovieFilePath,
		}
		s.fillCoverage(ctx, &fs, langs, canDownload)
		out = append(out, fs)
	}
	return out, nil
}

// fillCoverage probes the file and computes its per-kept-language coverage. Best-effort: a file
// that can't be probed still reports its sidecar coverage (embedded tracks just come back empty).
func (s *Service) fillCoverage(ctx context.Context, fs *FileSubs, langs []string, canDownload bool) {
	if mi, err := s.probeCached(ctx, fs.Path); err == nil {
		fs.DurationSec = mi.DurationSec
		fs.AudioLangs = mi.AudioLangs
		fs.Embedded = mi.Subs
	}
	if fs.Embedded == nil {
		fs.Embedded = []SubTrack{} // never nil → JSON emits [] not null (frontend iterates it)
	}
	present := presentLanguages(fs.Path, langs, fs.Kind != "episode") // kept languages with a sidecar already
	have := make(map[string]bool, len(present))
	for _, p := range present {
		have[strings.ToLower(p)] = true
	}
	fs.External = nonNil(present)
	fs.Languages = make([]LangStatus, 0, len(langs))
	for _, l := range langs {
		if have[strings.ToLower(l)] {
			fs.Languages = append(fs.Languages, LangStatus{Lang: l, Have: true})
			continue
		}
		fs.Missing++
		fs.Languages = append(fs.Languages, LangStatus{Lang: l, Have: false, Source: bestSource(fs.Embedded, l, canDownload)})
	}
}

// bestSource picks the highest-priority way to produce a missing-language SRT: an embedded text
// track (extract) beats an embedded image track (OCR) beats a provider download beats AI generation
// (the always-available fallback). Mirrors the module's "best-source → AI" pipeline.
func bestSource(embedded []SubTrack, lang string, canDownload bool) string {
	for _, t := range embedded {
		if t.Text && langMatches(t.Lang, lang) {
			return "extract"
		}
	}
	for _, t := range embedded {
		if !t.Text && langMatches(t.Lang, lang) {
			return "ocr"
		}
	}
	if canDownload {
		return "download"
	}
	return "ai"
}

// twoToThree maps common ISO 639-1 codes to 639-2/T so a wanted "en" matches an "eng" track.
var twoToThree = map[string]string{
	"en": "eng", "es": "spa", "fr": "fre", "de": "ger", "it": "ita", "pt": "por",
	"nl": "dut", "sv": "swe", "pl": "pol", "ru": "rus", "tr": "tur", "ar": "ara",
	"hi": "hin", "ja": "jpn", "ko": "kor", "zh": "chi",
}

// langMatches reports whether a track's language tag matches a wanted code, tolerating 2- vs
// 3-letter forms (en ≡ eng). An untagged track never matches a specific wanted language.
func langMatches(track, wanted string) bool {
	t := strings.ToLower(strings.TrimSpace(track))
	w := strings.ToLower(strings.TrimSpace(wanted))
	if t == "" || t == "und" || w == "" {
		return false
	}
	return t == w || twoToThree[w] == t || twoToThree[t] == w
}
