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

// ResolutionRank orders resolutions from best (higher) to worst, with unknown lowest,
// so the importer can decide whether a candidate is an upgrade over what's on disk.
func ResolutionRank(r Resolution) int {
	switch r {
	case Res2160p:
		return 5
	case Res1080p:
		return 4
	case Res720p:
		return 3
	case Res576p:
		return 2
	case Res480p:
		return 1
	default:
		return 0
	}
}

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
	// AbsoluteEpisodes holds anime-style absolute episode numbers ("[Group] Show - 137"),
	// numbered 1..N across the whole run. Only consulted for series flagged as anime.
	AbsoluteEpisodes []int `json:"absolute_episodes,omitempty"`
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
	return r.Season > 0 || len(r.Episodes) > 0 || len(r.Seasons) > 0 || r.Complete || len(r.AbsoluteEpisodes) > 0
}

var (
	reYear  = regexp.MustCompile(`\b(19\d{2}|20\d{2})\b`)
	reGroup = regexp.MustCompile(`-([A-Za-z0-9]{2,})$`)
	// reSxxExx captures the season (1), the first episode (2), and any continuation
	// (3) — a multi-episode file's extra parts: "S03E21-E22", "S03E21-22", "S01E01E02".
	// The continuation only accepts an explicit "E<n>" or a bare "-<n>" (never a
	// space/dot before a number, so ".1080p"/" 720p" stay out of it).
	reSxxExx = regexp.MustCompile(`(?i)\bS(\d{1,2})E(\d{1,3})((?:[-\s.]?E\d{1,3}|-\d{1,3})*)`)
	reSeason = regexp.MustCompile(`(?i)\bS(\d{1,2})\b`)
	// Spelled-out single season: "Season 3" / "Season 3 Complete" (scene/WEBRip packs
	// that don't use the "S03" form). A range like "Season 1-3" is handled separately.
	reSeasonSingleWord = regexp.MustCompile(`(?i)\bseason\s+(\d{1,2})\b`)
	reEpPart           = regexp.MustCompile(`(?i)E(\d{1,3})|-(\d{1,3})`)
	// Multi-season ranges: "S01-S03", "S1 - S5", or "Seasons 1-5".
	reSeasonRange = regexp.MustCompile(`(?i)\bS(\d{1,2})\s*-\s*S(\d{1,2})\b`)
	reSeasonWord  = regexp.MustCompile(`(?i)\bseasons?\s*(\d{1,2})\s*-\s*(\d{1,2})\b`)
	// "S01-07" — a season range where the second bound drops the "S" (TorrentLeech
	// box sets: "Elementary S01-07 Complete").
	reSeasonRangeShort = regexp.MustCompile(`(?i)\bS(\d{1,2})\s*-\s*(\d{1,2})\b`)
	// Anime fansub tag: a leading single-token "[Group]" (SubsPlease, Erai-raws…).
	// Space inside the brackets excludes site tags like "[ Some Site ]".
	reAnimeGroup = regexp.MustCompile(`^\s*\[([^\]\s]+)\]\s*`)
	// Anime absolute episode: "Title - 137", "Title - 12v2", or a batch "Title - 01-12".
	reAbsEp = regexp.MustCompile(`(?i)\s-\s(\d{1,4})(?:v\d+)?(?:\s*[-~]\s*(\d{1,4}))?(?:\s|$|[.\[(])`)
	// Anime "S2 29" — a fansub season followed by the absolute episode number. Some
	// groups (No0bSubs, …) number episodes absolutely across seasons, so "S2 29" is the
	// 29th episode overall. The trailing \b keeps a resolution like "S2 1080p" out.
	reAnimeSeasonAbs = regexp.MustCompile(`(?i)\bS(\d{1,2})\s+(\d{1,4})\b`)
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

	// Anime fansub tag: a leading "[Group]". titleStart trims it off the title.
	titleStart := 0
	if m := reAnimeGroup.FindStringSubmatchIndex(name); m != nil {
		if r.Group == "" {
			r.Group = name[m[2]:m[3]]
		}
		titleStart = m[1]
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
		r.Episodes = parseEpisodeList(m[2], m[3])
	} else if m := reSeason.FindStringSubmatch(name); m != nil {
		// Season pack (no episode markers).
		r.Season, _ = strconv.Atoi(m[1])
	} else if m := reSeasonSingleWord.FindStringSubmatch(name); m != nil {
		// Spelled-out "Season 3" pack (e.g. "Ben 10 2016 Season 3 Complete").
		r.Season, _ = strconv.Atoi(m[1])
	}

	// Multi-season / complete-series packs. Require a series context so a movie
	// "Complete Collection" box set isn't misread as a TV pack.
	if contains(lc, "complete") && (contains(lc, "series") || strings.Contains(lc, "season") || r.Season > 0) {
		r.Complete = true
	}
	// Anime absolute episode ("[Group] Show - 137" / batch "... - 01-12"). Gated on a
	// leading fansub tag so scene TV and movies are never misread; only when no SxxExx.
	absCut := len(name)
	if len(r.Episodes) == 0 {
		// "[Group] Title - 137" — dash-absolute. Gated on a leading fansub tag because a
		// bare "- <n>" is ambiguous (could be part of a movie title).
		if titleStart > 0 && r.Season == 0 {
			if m := reAbsEp.FindStringSubmatchIndex(name); m != nil {
				r.AbsoluteEpisodes = absoluteRange(name[m[2]:m[3]], subIdx(name, m, 4))
				if len(r.AbsoluteEpisodes) > 0 {
					absCut = m[0]
				}
			}
		}
		// "Title S2 29" — a (fansub) season followed by the absolute episode number. This
		// form is distinctive enough (a season with a space then a plain number, and no
		// SxxExx) that it needs no fansub tag — so it also works on renamed library files.
		// Clears the season, since the number IS the absolute episode, not a season pack.
		if len(r.AbsoluteEpisodes) == 0 {
			if m := reAnimeSeasonAbs.FindStringSubmatchIndex(name); m != nil {
				if eps := absoluteRange(name[m[4]:m[5]], ""); len(eps) > 0 {
					r.AbsoluteEpisodes = eps
					r.Season = 0
					absCut = m[0]
				}
			}
		}
	}

	if rng := reSeasonRange.FindStringSubmatch(name); rng != nil {
		r.Seasons = seasonRange(rng[1], rng[2])
	} else if rng := reSeasonWord.FindStringSubmatch(name); rng != nil {
		r.Seasons = seasonRange(rng[1], rng[2])
	} else if rng := reSeasonRangeShort.FindStringSubmatch(name); rng != nil && len(r.Episodes) == 0 {
		r.Seasons = seasonRange(rng[1], rng[2]) // "S01-07" box set
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
	if absCut < cut {
		cut = absCut
	}
	if titleStart > cut {
		titleStart = 0
	}
	r.Title = cleanTitle(name[titleStart:cut])

	return r
}

// subIdx returns the i-th submatch string from a FindStringSubmatchIndex result
// (i is the group number; group g occupies indices [2g, 2g+1]). "" when unset.
func subIdx(s string, m []int, g int) string {
	if g+1 >= len(m) || m[g] < 0 {
		return ""
	}
	return s[m[g]:m[g+1]]
}

// absoluteRange expands an anime absolute-episode match into episode numbers: a
// single "137", or a batch "01"-"12" → 1..12. A number that looks like a year
// (1900-2099) is rejected, since "Show - 2011" is a year, not episode 2011.
func absoluteRange(start, end string) []int {
	a, err := strconv.Atoi(start)
	if err != nil || a <= 0 || (a >= 1900 && a <= 2099) {
		return nil
	}
	if end == "" {
		return []int{a}
	}
	b, err := strconv.Atoi(end)
	if err != nil || b <= a || b-a >= 500 {
		return []int{a}
	}
	out := make([]int, 0, b-a+1)
	for i := a; i <= b; i++ {
		out = append(out, i)
	}
	return out
}

// seasonRange expands "a" and "b" into an inclusive list of season numbers.
// parseEpisodeList turns an SxxExx match into its episode numbers. first is the
// E<n> right after the season; tail is the continuation ("-22", "E02E03", "-E22").
// A bare "-<n>" counts as a range end only when it's a small step up from the
// previous episode — so "S01E01-720p" reads as episode 1, not episodes 1 and 720.
func parseEpisodeList(first, tail string) []int {
	n0, err := strconv.Atoi(first)
	if err != nil {
		return nil
	}
	eps := []int{n0}
	prev := n0
	for _, em := range reEpPart.FindAllStringSubmatch(tail, -1) {
		var n int
		switch {
		case em[1] != "": // explicit "E<n>" — always an episode
			n, _ = strconv.Atoi(em[1])
		case em[2] != "": // bare "-<n>" — a range end only if consecutive-ish
			if v, _ := strconv.Atoi(em[2]); v > prev && v-prev <= 50 {
				n = v
			}
		}
		if n > 0 && n != prev {
			eps = append(eps, n)
			prev = n
		}
	}
	return eps
}

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

// packWords are scene words that describe the RELEASE, not the show. They're normally
// harmless because they sit after the season marker ("Scorpion S01-S04 complete web ..."),
// which is where the title is cut. But when a group puts them BEFORE it — "The Expanse
// complete S01-S06 web ..." — they land inside the title, the release stops matching any
// library series, and the download is retried forever while the searcher re-grabs it.
var packWords = map[string]bool{
	"complete": true, "collection": true, "boxset": true, "box": true, "set": true,
	"pack": true, "series": true, "duology": true, "trilogy": true, "quadrilogy": true,
	"anthology": true,
}

func cleanTitle(s string) string {
	s = strings.NewReplacer(".", " ", "_", " ").Replace(s)
	// Trim trailing separators and a dangling "(" from a parenthesized year,
	// e.g. "The Matrix (1999)" → title "The Matrix (" → "The Matrix".
	s = strings.Trim(s, " -([")
	fields := strings.Fields(s)
	// Strip pack words from the END only. Leading/middle occurrences are part of real
	// titles ("The Complete Sherlock Holmes", "Band of Brothers"), and never stripping
	// the last remaining word keeps a show genuinely called "Complete" intact.
	for len(fields) > 1 && packWords[strings.ToLower(fields[len(fields)-1])] {
		fields = fields[:len(fields)-1]
	}
	return strings.Join(fields, " ")
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
