package subtitles

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// LangStatus is one kept-language's coverage for a file: whether an external SRT exists, and if
// not, the first source the ladder will try and what it falls back to.
type LangStatus struct {
	Lang   string `json:"lang"`
	Have   bool   `json:"have"`             // an external .srt for this language sits next to the file
	Source string `json:"source,omitempty"` // when !Have: extract | ocr | download | ai
	// Fallback is "ai" when the first source is extract or download and the local AI
	// could still make this language if that source comes up empty.
	Fallback string `json:"fallback,omitempty"`
	// Orphan is set when the language is missing but a subtitle in the folder that pairs
	// with no video (an old name, most likely) covers it. Plex won't show it for this file.
	Orphan bool `json:"orphan,omitempty"`
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
	Embedded    []SubTrack   `json:"embedded"`          // embedded subtitle tracks (for badges/filters)
	External    []string     `json:"external"`          // kept languages that already have a sidecar
	Languages   []LangStatus `json:"languages"`         // per-kept-language coverage + best source
	Health      *SubHealth   `json:"health,omitempty"`  // Tier-1 sync/health score (nil until scored)
	Missing     int          `json:"missing"`           // count of kept languages still without an SRT
	Orphans     []Orphan     `json:"orphans,omitempty"` // movies: subtitles in the folder paired with no video
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
						Title: fmt.Sprintf("%s - S%02dE%02d", full.Title, e.SeasonNumber, e.EpisodeNumber),
						Year:  full.Year, PosterURL: full.PosterURL, Path: e.FilePath,
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
	if mi, err := s.probeFile(ctx, fs.Path); err == nil && mi != nil {
		fs.DurationSec = mi.DurationSec
		fs.AudioLangs = mi.AudioLangs
		fs.Embedded = mi.Subs
	}
	if fs.Embedded == nil {
		fs.Embedded = []SubTrack{} // never nil → JSON emits [] not null (frontend iterates it)
	}
	sc := scanSidecars(fs.Path, langs, kindOf(fs.Kind))
	present := sc.Present // kept languages with a paired sidecar already
	fs.Orphans = sc.Orphans
	orphaned := orphanCovered(langs, present, sc.Orphans)
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
		ls := LangStatus{Lang: l, Have: false, Source: bestSource(fs.Embedded, l, canDownload), Orphan: orphaned[strings.ToLower(l)]}
		if (ls.Source == "extract" || ls.Source == "download") && s.aiCanMake(fs.AudioLangs, l) {
			ls.Fallback = "ai"
		}
		fs.Languages = append(fs.Languages, ls)
	}
}

// aiCanMake reports whether the local AI could produce lang from audio in audioLangs:
// a model is installed, whisper can go in that direction, and the model for it is there.
func (s *Service) aiCanMake(audioLangs []string, lang string) bool {
	ai := s.aiGen()
	if !ai.available() {
		return false
	}
	plan := aiPlan(audioLangs, lang)
	return plan != "" && ai.canRun(plan == "translate")
}

// bestSource names the FIRST rung the ladder in process() will try for a missing language: an
// embedded text track (extract), else a provider download when one is configured, else AI.
// It is only the starting point — a rung that comes up empty falls through to the next one, so
// "download" means "download, then AI" (LangStatus.Fallback says whether AI can actually act).
func bestSource(embedded []SubTrack, lang string, canDownload bool) string {
	if _, ok := pickFullTrack(embedded, lang); ok {
		return "extract"
	}
	// An embedded IMAGE track (PGS/VobSub) would be the next-best source — but OCR isn't
	// implemented, and routing to it sent every PGS-only file to "pending" without ever
	// trying a download. That is precisely the file that most needs a text subtitle: an
	// image track is what makes Plex burn subtitles in and transcode the video. So fall
	// through to the sources that exist. When OCR ships it slots in here.
	if canDownload {
		return "download"
	}
	return "ai"
}

// pickFullTrack chooses the embedded text track to extract as a language's full subtitle:
// a plain track first, then an SDH one (full dialogue plus sound cues), with the track
// flagged default winning a tie and stream order after that. A forced track is never
// picked — it only carries the foreign-language lines — so a language whose only text
// track is forced falls through to download or AI.
func pickFullTrack(subs []SubTrack, lang string) (SubTrack, bool) {
	best, found := SubTrack{}, false
	rank := func(t SubTrack) int {
		r := 0
		if t.SDH {
			r += 2
		}
		if !t.Default {
			r++
		}
		return r
	}
	for _, t := range subs {
		if !t.Text || t.Forced || !langMatches(t.Lang, lang) {
			continue
		}
		if !found || rank(t) < rank(best) {
			best, found = t, true
		}
	}
	return best, found
}

// pickForcedTrack is the first forced text track in a language, if any.
func pickForcedTrack(subs []SubTrack, lang string) (SubTrack, bool) {
	for _, t := range subs {
		if t.Text && t.Forced && langMatches(t.Lang, lang) {
			return t, true
		}
	}
	return SubTrack{}, false
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

// SeriesGroup is one show's subtitle coverage, rolled up.
//
// A flat list of every episode is unusable at library scale — 23,000 rows, each of
// which has to be probed before the page can render. Showing shows first means one row
// per series and probing only the show you open.
type SeriesGroup struct {
	SeriesID  int64  `json:"series_id"`
	Title     string `json:"title"`
	Year      int    `json:"year,omitempty"`
	PosterURL string `json:"poster_url,omitempty"`
	Episodes  int    `json:"episodes"` // episodes with a file on disk
	Missing   int    `json:"missing"`  // episodes still missing at least one kept language
	Covered   int    `json:"covered"`  // episodes with every kept language present
	Seasons   int    `json:"seasons"`
}

// SeriesGroups returns one row per show with a file on disk, for the Library list.
//
// Coverage here is counted from the SIDECARS on disk, deliberately without probing the
// video files. Probing is what makes the flat list slow, and the roll-up only needs to
// answer "does this show need attention?" — the per-episode view probes properly once
// you open one show.
func (s *Service) SeriesGroups(ctx context.Context) ([]SeriesGroup, error) {
	if s.series == nil {
		return []SeriesGroup{}, nil
	}
	langs := s.languages(ctx)
	list, err := s.series.List(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]SeriesGroup, 0, len(list))
	for _, sm := range list {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		g, ok := s.groupFor(ctx, sm.ID, langs)
		if !ok {
			continue
		}
		out = append(out, g)
	}
	// Shows needing the most work first: that's what the page is for.
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Missing != out[j].Missing {
			return out[i].Missing > out[j].Missing
		}
		return strings.ToLower(out[i].Title) < strings.ToLower(out[j].Title)
	})
	return out, nil
}

// SeriesEpisodes returns the full per-episode coverage for ONE show — the expanded view.
// Probing is bounded to that show, which is what makes it affordable.
func (s *Service) SeriesEpisodes(ctx context.Context, seriesID int64) ([]FileSubs, error) {
	if s.series == nil {
		return []FileSubs{}, nil
	}
	langs := s.languages(ctx)
	canDownload := s.provider != nil && s.provider.CanDownload()
	full, err := s.series.Get(ctx, seriesID)
	if err != nil {
		return nil, err
	}
	out := []FileSubs{}
	for _, sn := range full.Seasons {
		for _, e := range sn.Episodes {
			if !e.HasFile || e.FilePath == "" {
				continue
			}
			fs := FileSubs{
				Kind: "episode", SeriesID: full.ID, Season: e.SeasonNumber, Episode: e.EpisodeNumber,
				Title: fmt.Sprintf("%s - S%02dE%02d", full.Title, e.SeasonNumber, e.EpisodeNumber),
				Year:  full.Year, PosterURL: full.PosterURL, Path: e.FilePath,
			}
			s.fillCoverage(ctx, &fs, langs, canDownload)
			out = append(out, fs)
		}
	}
	return out, nil
}
