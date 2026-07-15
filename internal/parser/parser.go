// Package parser turns a scene/p2p release name into structured attributes
// (resolution, source, codec, dynamic range, audio, edition, group, and — for
// TV — season/episodes). It's the atom the acquisition pipeline is built on:
// indexer results are ranked from it, the quality engine scores it, and the
// importer identifies files with it. Pure, offline, and heavily tested.
package parser

import (
	"regexp"
	"strconv"
	"strings"
)

// Resolution is a normalized vertical resolution tier.
type Resolution string

const (
	ResUnknown Resolution = ""
	Res2160p   Resolution = "2160p"
	Res1080p   Resolution = "1080p"
	Res720p    Resolution = "720p"
	Res576p    Resolution = "576p"
	Res480p    Resolution = "480p"
)

// Source is the normalized origin of a release.
type Source string

const (
	SourceUnknown Source = ""
	SourceRemux   Source = "Remux"
	SourceBluray  Source = "BluRay"
	SourceWebDL   Source = "WEB-DL"
	SourceWebRip  Source = "WEBRip"
	SourceHDTV    Source = "HDTV"
	SourceDVD     Source = "DVD"
	SourceCAM     Source = "CAM"
)

// Codec is the normalized video codec.
type Codec string

const (
	CodecUnknown Codec = ""
	CodecX265    Codec = "x265"
	CodecX264    Codec = "x264"
	CodecXvid    Codec = "XviD"
	CodecVC1     Codec = "VC-1"
)

// Release is the structured result of parsing a release name.
type Release struct {
	Title      string     `json:"title"`
	Year       int        `json:"year,omitempty"`
	Resolution Resolution `json:"resolution,omitempty"`
	Source     Source     `json:"source,omitempty"`
	Codec      Codec      `json:"codec,omitempty"`
	HDR        []string   `json:"hdr,omitempty"`   // e.g. ["DV","HDR10"]
	Audio      []string   `json:"audio,omitempty"` // e.g. ["Atmos","TrueHD"]
	Edition    string     `json:"edition,omitempty"`
	Group      string     `json:"group,omitempty"`
	Proper     bool       `json:"proper,omitempty"`
	Repack     bool       `json:"repack,omitempty"`

	// TV-specific (zero for movies).
	Season   int   `json:"season,omitempty"`
	Episodes []int `json:"episodes,omitempty"`
	// Seasons lists every season a pack covers (e.g. S01-S03 → [1,2,3]); a single
	// season pack is [Season]. Complete marks a full-series pack ("Complete Series").
	Seasons  []int `json:"seasons,omitempty"`
	Complete bool  `json:"complete,omitempty"`
}

// Kind classifies a TV release by the breadth it covers.
type Kind int

const (
	KindMovie        Kind = iota
	KindEpisode           // one or more specific episodes (SxxExx)
	KindSeasonPack        // a whole single season
	KindMultiSeason       // a range/several seasons
	KindCompleteShow      // the entire series
)

// Kind returns how much a release covers.
func (r Release) Kind() Kind {
	switch {
	case !r.IsTV():
		return KindMovie
	case r.Complete:
		return KindCompleteShow
	case len(r.Seasons) > 1:
		return KindMultiSeason
	case len(r.Episodes) == 0 && r.Season > 0:
		return KindSeasonPack
	default:
		return KindEpisode
	}
}

// CoversSeason reports whether the release includes the given season number.
func (r Release) CoversSeason(season int) bool {
	if r.Complete {
		return true
	}
	for _, s := range r.Seasons {
		if s == season {
			return true
		}
	}
	return r.Season == season
}

// HasHDR reports whether any dynamic-range tag was detected.
func (r Release) HasHDR() bool { return len(r.HDR) > 0 }

// HasAudio reports whether the release carries the given audio tag.
func (r Release) HasAudio(tag string) bool {
	for _, a := range r.Audio {
		if a == tag {
			return true
		}
	}
	return false
}

// IsTV reports whether a season/episode/series marker was found.
func (r Release) IsTV() bool {
	return r.Season > 0 || len(r.Episodes) > 0 || len(r.Seasons) > 0 || r.Complete
}

var (
	reYear    = regexp.MustCompile(`\b(19\d{2}|20\d{2})\b`)
	reGroup   = regexp.MustCompile(`-([A-Za-z0-9]{2,})$`)
	reSxxExx  = regexp.MustCompile(`(?i)\bS(\d{1,2})(?:E(\d{1,3}))(?:[-\s.]?E?(\d{1,3}))*`)
	reSeason  = regexp.MustCompile(`(?i)\bS(\d{1,2})\b`)
	reEpRange = regexp.MustCompile(`(?i)E(\d{1,3})`)
	// Multi-season ranges: "S01-S03", "S1 - S5", or "Seasons 1-5".
	reSeasonRange = regexp.MustCompile(`(?i)\bS(\d{1,2})\s*-\s*S(\d{1,2})\b`)
	reSeasonWord  = regexp.MustCompile(`(?i)\bseasons?\s*(\d{1,2})\s*-\s*(\d{1,2})\b`)
)

// Parse extracts structured attributes from a release name.
func Parse(name string) Release {
	name = strings.TrimSpace(name)
	// Strip a trailing container extension if present.
	name = strings.TrimSuffix(name, ".mkv")
	name = strings.TrimSuffix(name, ".mp4")

	var r Release

	// Release group: trailing "-GROUP" (before normalization eats the dash).
	if m := reGroup.FindStringSubmatch(name); m != nil {
		r.Group = m[1]
	}

	// Lowercased, space-separated copy for keyword matching.
	lc := normalize(name)

	r.Resolution = detectResolution(lc)
	r.Source = detectSource(lc)
	r.Codec = detectCodec(lc)
	r.HDR = detectHDR(lc)
	r.Audio = detectAudio(lc)
	r.Edition = detectEdition(lc)
	r.Proper = contains(lc, "proper")
	r.Repack = contains(lc, "repack")

	// TV season/episodes.
	if m := reSxxExx.FindStringSubmatch(name); m != nil {
		r.Season, _ = strconv.Atoi(m[1])
		for _, em := range reEpRange.FindAllStringSubmatch(m[0], -1) {
			if n, err := strconv.Atoi(em[1]); err == nil {
				r.Episodes = append(r.Episodes, n)
			}
		}
	} else if m := reSeason.FindStringSubmatch(name); m != nil {
		// Season pack (no episode markers).
		r.Season, _ = strconv.Atoi(m[1])
	}

	// Multi-season / complete-series packs. Require a series context so a movie
	// "Complete Collection" box set isn't misread as a TV pack.
	if contains(lc, "complete") && (contains(lc, "series") || strings.Contains(lc, "season") || r.Season > 0) {
		r.Complete = true
	}
	if rng := reSeasonRange.FindStringSubmatch(name); rng != nil {
		r.Seasons = seasonRange(rng[1], rng[2])
	} else if rng := reSeasonWord.FindStringSubmatch(name); rng != nil {
		r.Seasons = seasonRange(rng[1], rng[2])
	} else if r.Season > 0 && len(r.Episodes) == 0 {
		r.Seasons = []int{r.Season} // single-season pack
	}
	if len(r.Seasons) > 0 && r.Season == 0 {
		r.Season = r.Seasons[0]
	}

	// Year + title. The title is whatever precedes the first year (movies) or
	// the season marker (TV).
	cut := len(name)
	// Use the LAST year token: a title may itself contain a year
	// (e.g. "Blade Runner 2049"), while the release year comes after it.
	if locs := reYear.FindAllStringIndex(name, -1); len(locs) > 0 {
		last := locs[len(locs)-1]
		if y, err := strconv.Atoi(name[last[0]:last[1]]); err == nil {
			r.Year = y
			cut = last[0]
		}
	}
	if r.IsTV() {
		if loc := reSeason.FindStringIndex(name); loc != nil && loc[0] < cut {
			cut = loc[0]
		} else if loc := reSxxExx.FindStringIndex(name); loc != nil && loc[0] < cut {
			cut = loc[0]
		}
	}
	r.Title = cleanTitle(name[:cut])

	return r
}

// seasonRange expands "a" and "b" into an inclusive list of season numbers.
func seasonRange(a, b string) []int {
	lo, _ := strconv.Atoi(a)
	hi, _ := strconv.Atoi(b)
	if lo > hi {
		lo, hi = hi, lo
	}
	var out []int
	for s := lo; s <= hi; s++ {
		out = append(out, s)
	}
	return out
}

func normalize(s string) string {
	s = strings.ToLower(s)
	repl := strings.NewReplacer(".", " ", "_", " ", "-", " ")
	return " " + repl.Replace(s) + " "
}

func cleanTitle(s string) string {
	s = strings.NewReplacer(".", " ", "_", " ").Replace(s)
	// Trim trailing separators and a dangling "(" from a parenthesized year,
	// e.g. "The Matrix (1999)" → title "The Matrix (" → "The Matrix".
	s = strings.Trim(s, " -([")
	return strings.Join(strings.Fields(s), " ")
}

func contains(hay, needle string) bool {
	return strings.Contains(hay, " "+needle+" ") || strings.Contains(hay, " "+needle)
}

func detectResolution(lc string) Resolution {
	switch {
	case strings.Contains(lc, "2160p") || strings.Contains(lc, " 4k ") || strings.Contains(lc, " uhd "):
		return Res2160p
	case strings.Contains(lc, "1080p") || strings.Contains(lc, "1080i"):
		return Res1080p
	case strings.Contains(lc, "720p"):
		return Res720p
	case strings.Contains(lc, "576p"):
		return Res576p
	case strings.Contains(lc, "480p"):
		return Res480p
	}
	return ResUnknown
}

func detectSource(lc string) Source {
	switch {
	case strings.Contains(lc, "remux"):
		return SourceRemux
	case strings.Contains(lc, "bluray"), strings.Contains(lc, "blu ray"),
		strings.Contains(lc, "bdrip"), strings.Contains(lc, "brrip"), strings.Contains(lc, "bdremux"):
		return SourceBluray
	case strings.Contains(lc, "web dl"), strings.Contains(lc, "webdl"):
		return SourceWebDL
	case strings.Contains(lc, "webrip"), strings.Contains(lc, "web rip"), strings.Contains(lc, " web "):
		return SourceWebRip
	case strings.Contains(lc, "hdtv"), strings.Contains(lc, "pdtv"):
		return SourceHDTV
	case strings.Contains(lc, "dvdrip"), strings.Contains(lc, " dvd "):
		return SourceDVD
	case strings.Contains(lc, "hdcam"), strings.Contains(lc, " cam "),
		strings.Contains(lc, "telesync"), strings.Contains(lc, " ts "):
		return SourceCAM
	}
	return SourceUnknown
}

func detectCodec(lc string) Codec {
	switch {
	case strings.Contains(lc, "x265"), strings.Contains(lc, "h265"),
		strings.Contains(lc, "h 265"), strings.Contains(lc, "hevc"):
		return CodecX265
	case strings.Contains(lc, "x264"), strings.Contains(lc, "h264"),
		strings.Contains(lc, "h 264"), strings.Contains(lc, "avc"):
		return CodecX264
	case strings.Contains(lc, "xvid"), strings.Contains(lc, "divx"):
		return CodecXvid
	case strings.Contains(lc, "vc 1"), strings.Contains(lc, "vc1"):
		return CodecVC1
	}
	return CodecUnknown
}

func detectHDR(lc string) []string {
	var out []string
	if strings.Contains(lc, "dolby vision") || strings.Contains(lc, " dovi ") || strings.Contains(lc, " dv ") {
		out = append(out, "DV")
	}
	switch {
	case strings.Contains(lc, "hdr10+") || strings.Contains(lc, "hdr10plus"):
		// HDR10+ is a superset of HDR10 — tag both so an "HDR10" preference matches.
		out = append(out, "HDR10+", "HDR10")
	case strings.Contains(lc, "hdr10") || strings.Contains(lc, " hdr "):
		out = append(out, "HDR10")
	}
	return out
}

// audioTags is checked in priority order; the first-listed matching tags win.
var audioTags = []struct {
	label   string
	needles []string
}{
	{"Atmos", []string{"atmos"}},
	{"TrueHD", []string{"truehd", "true hd"}},
	{"DTS-HD", []string{"dts hd", "dtshd", "dts x", "dts:x"}},
	{"DTS", []string{" dts "}},
	{"DDP", []string{"ddp", "dd+", "eac3", "e ac 3", "e ac3"}},
	{"DD", []string{" dd ", "ac3", "dd5 1", "dd2 0"}},
	{"AAC", []string{"aac"}},
	{"FLAC", []string{"flac"}},
}

func detectAudio(lc string) []string {
	var out []string
	seen := map[string]bool{}
	for _, t := range audioTags {
		// "DTS" is a substring of "DTS-HD"; don't double-report the base codec.
		if t.label == "DTS" && seen["DTS-HD"] {
			continue
		}
		for _, n := range t.needles {
			if strings.Contains(lc, n) && !seen[t.label] {
				out = append(out, t.label)
				seen[t.label] = true
				break
			}
		}
	}
	return out
}

var editions = []struct {
	label   string
	needles []string
}{
	{"Director's Cut", []string{"director s cut", "directors cut", "director cut"}},
	{"Extended", []string{"extended"}},
	{"Theatrical", []string{"theatrical"}},
	{"IMAX", []string{"imax"}},
	{"Unrated", []string{"unrated"}},
	{"Remastered", []string{"remastered", "remaster"}},
	{"Final Cut", []string{"final cut"}},
}

func detectEdition(lc string) string {
	for _, e := range editions {
		for _, n := range e.needles {
			if strings.Contains(lc, n) {
				return e.label
			}
		}
	}
	return ""
}
