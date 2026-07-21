// Package parser turns a scene/p2p release name into structured attributes
// (resolution, source, codec, dynamic range, audio, edition, group, and — for
// TV — season/episodes). It's the atom the acquisition pipeline is built on:
// indexer results are ranked from it, the quality engine scores it, and the
// importer identifies files with it. Pure, offline, and heavily tested.
package parser

import (
	"path/filepath"
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
	CodecAV1     Codec = "AV1"
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
	// An optional dot/space between Sxx and Exx accepts the "S01.E05" spaced form,
	// which otherwise misparsed as a full season pack.
	reSxxExx = regexp.MustCompile(`(?i)\bS(\d{1,2})[. ]?E(\d{1,3})((?:[-\s.]?E\d{1,3}|-\d{1,3})*)`)
	reSeason = regexp.MustCompile(`(?i)\bS(\d{1,2})\b`)
	// "1x01" — the other common episode form, used by a lot of pack releasers and by
	// anyone who renamed their library that way. Nothing understood it, so a 122-file
	// "Parks and Recreation S01-S07" pack imported nothing at all and was blocklisted as
	// junk. Captures multi-episode continuations too: "6x01 & 6x02", "7x12 & 7x13".
	//
	// The episode needs 2+ digits so an aspect ratio ("16x9") or a dimension ("4x4")
	// can't be read as an episode. That does mean a genuine "1x1" is missed; releases
	// using the form pad to two digits in practice.
	reNxNN = regexp.MustCompile(`(?i)\b(\d{1,2})x(\d{2,3})((?:\s*[&+]\s*\d{1,2}x\d{2,3}|\s*-\s*\d{1,2}x\d{2,3})*)`)
	// Spelled-out single season: "Season 3" / "Season 3 Complete" (scene/WEBRip packs
	// that don't use the "S03" form). A range like "Season 1-3" is handled separately.
	reSeasonSingleWord = regexp.MustCompile(`(?i)\bseason\s+(\d{1,2})\b`)
	// Continuation parts of a multi-episode file. Group 1 captures the separator before
	// an "E<n>" so a hyphen ("E62-E65", a RANGE) can be told apart from juxtaposition
	// ("E01E02", a LIST) — without it, a range's middle episodes were dropped.
	reEpPart = regexp.MustCompile(`(?i)([-\s.]?)E(\d{1,3})|-(\d{1,3})`)
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
	// Exclude the trailing release-group token from HDR detection: a group
	// literally named "DV" ("...x265-DV") is a tag, not Dolby Vision.
	hdrHay := lc
	if r.Group != "" && strings.HasSuffix(name, "-"+r.Group) {
		hdrHay = normalize(strings.TrimSuffix(name, "-"+r.Group))
	}
	r.HDR = detectHDR(hdrHay)
	r.Audio = detectAudio(lc)
	r.Edition = detectEdition(lc)
	r.Proper = contains(lc, "proper")
	r.Repack = contains(lc, "repack")

	// TV season/episodes.
	if m := reSxxExx.FindStringSubmatch(name); m != nil {
		r.Season, _ = strconv.Atoi(m[1])
		r.Episodes = parseEpisodeList(m[2], m[3])
	} else if m := reNxNN.FindStringSubmatch(name); m != nil {
		// Checked before the season-only forms on purpose: "Flu Season 2" in an episode
		// title would otherwise win and report season 2 for a file that is plainly 6x19.
		r.Season, _ = strconv.Atoi(m[1])
		r.Episodes = parseNxNNList(m[2], m[3])
	} else if m := reSeason.FindStringSubmatch(name); m != nil {
		// Season pack (no episode markers).
		r.Season, _ = strconv.Atoi(m[1])
	} else if m := reSeasonSingleWord.FindStringSubmatch(name); m != nil {
		// Spelled-out "Season 3" pack (e.g. "Ben 10 2016 Season 3 Complete").
		r.Season, _ = strconv.Atoi(m[1])
	}

	// Multi-season / complete-series packs. Require a series context so a movie
	// "Complete Collection" box set isn't misread as a TV pack. And a single
	// specific season ("The.Wire.S02.COMPLETE") is a complete SEASON — a season
	// pack covering only that season — never a complete-show marker.
	hasSeasonRange := reSeasonRange.MatchString(name) || reSeasonWord.MatchString(name) ||
		(len(r.Episodes) == 0 && reSeasonRangeShort.MatchString(name))
	if contains(lc, "complete") {
		switch {
		case contains(lc, "series"), hasSeasonRange:
			r.Complete = true
		case r.Season > 0:
			// "Sxx COMPLETE" / "Season N Complete": one full season, not the show.
		case strings.Contains(lc, "season"):
			r.Complete = true
		}
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
		} else if loc := reNxNN.FindStringIndex(name); loc != nil && loc[0] < cut {
			cut = loc[0] // "Parks and Recreation - 1x01 - Make My Pit a Park"
		}
	}
	if absCut < cut {
		cut = absCut
	}
	if titleStart > cut {
		titleStart = 0
	}
	// Pack words are only stripped from the title when the release shows actual
	// pack evidence; otherwise "The.Box.2009" would lose "Box" to the list.
	packCtx := r.Complete || len(r.Seasons) > 0 || contains(lc, "complete") || contains(lc, "collection")
	r.Title = cleanTitle(name[titleStart:cut], packCtx)

	// Year-titled shows ("1923", "1984"): when the only year token opens the name,
	// the year-cut lands at position 0 and leaves no title — the token IS the
	// title. Fall back to cutting at the season/episode/quality marker instead,
	// and drop the year (a second distinct year, "2012.2009.1080p", never gets
	// here because the last-year cut already leaves "2012" as the title).
	if r.Title == "" && r.Year > 0 {
		r.Year = 0
		cut = len(name)
		if r.IsTV() {
			if loc := reSeason.FindStringIndex(name); loc != nil && loc[0] > 0 && loc[0] < cut {
				cut = loc[0]
			} else if loc := reSxxExx.FindStringIndex(name); loc != nil && loc[0] > 0 && loc[0] < cut {
				cut = loc[0]
			} else if loc := reNxNN.FindStringIndex(name); loc != nil && loc[0] > 0 && loc[0] < cut {
				cut = loc[0]
			}
		}
		if absCut > 0 && absCut < cut {
			cut = absCut
		}
		if q := reQualityStart.FindStringIndex(name); q != nil && q[0] > 0 && q[0] < cut {
			cut = q[0]
		}
		if titleStart > cut {
			titleStart = 0
		}
		r.Title = cleanTitle(name[titleStart:cut], packCtx)
	}

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
		var isRange bool
		switch {
		case em[2] != "": // explicit "E<n>" — always an episode
			n, _ = strconv.Atoi(em[2])
			isRange = em[1] == "-" // "E62-E65" spans; "E01E02" just lists two
		case em[3] != "": // bare "-<n>" — a range end only if consecutive-ish
			if v, _ := strconv.Atoi(em[3]); v > prev && v-prev <= 50 {
				n = v
				isRange = true
			}
		}
		if n <= 0 || n == prev {
			continue
		}
		// A hyphen means every episode in between is in this file too. Peppa Pig's
		// "S07E62-E65.Cruise.Ship.Holiday...mkv" is one file holding four episodes;
		// recording only the endpoints left 63 and 64 permanently "missing", so the
		// searcher hunted forever for episodes already sitting on disk.
		if isRange && n-prev <= maxEpisodeSpan {
			for e := prev + 1; e < n; e++ {
				eps = append(eps, e)
			}
		}
		eps = append(eps, n)
		prev = n
	}
	return eps
}

// maxEpisodeSpan bounds how many episodes one hyphenated range may expand to. Genuine
// multi-episode files hold a handful; anything wider is far more likely a misparse, and
// inventing 40 episode numbers from one would mark a whole season present in error. Past
// the cap we keep just the endpoints, which is what the parser always did.
const maxEpisodeSpan = 12

// reNxNNPart pulls each continuation episode out of "& 6x02" / "- 7x13".
var reNxNNPart = regexp.MustCompile(`(?i)\d{1,2}x(\d{2,3})`)

// parseNxNNList expands "6x01 & 6x02" into [1, 2]. Only the episode numbers matter — the
// season is taken from the first pair, and a file spanning two seasons isn't a thing.
func parseNxNNList(first, tail string) []int {
	n0, err := strconv.Atoi(first)
	if err != nil {
		return nil
	}
	eps := []int{n0}
	prev := n0
	for _, m := range reNxNNPart.FindAllStringSubmatch(tail, -1) {
		n, err := strconv.Atoi(m[1])
		if err != nil || n == prev {
			continue
		}
		// A hyphen between them is a range ("6x01-6x04"), so fill the gap — same rule as
		// the SxxExx form, and capped identically against a misparse inventing a season.
		if n > prev+1 && n-prev <= maxEpisodeSpan && strings.Contains(tail, "-") {
			for e := prev + 1; e < n; e++ {
				eps = append(eps, e)
			}
		}
		eps = append(eps, n)
		prev = n
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

// cleanTitle turns a raw title segment into the display title. packCtx reports
// whether the release shows pack evidence (Complete flag, a Seasons range, or a
// "complete"/"collection" word); only then are trailing pack words stripped —
// otherwise a movie genuinely titled "The Box" or "The Pack" loses its last word.
func cleanTitle(s string, packCtx bool) string {
	s = strings.NewReplacer(".", " ", "_", " ").Replace(s)
	// Trim trailing separators and a dangling "(" from a parenthesized year,
	// e.g. "The Matrix (1999)" → title "The Matrix (" → "The Matrix".
	s = strings.Trim(s, " -([")
	fields := strings.Fields(s)
	// Strip pack words from the END only. Leading/middle occurrences are part of real
	// titles ("The Complete Sherlock Holmes", "Band of Brothers"), and never stripping
	// the last remaining word keeps a show genuinely called "Complete" intact.
	for packCtx && len(fields) > 1 && packWords[strings.ToLower(fields[len(fields)-1])] {
		// "Series" is the one pack word that is routinely part of the real title —
		// "ARK: The Animated Series", "Batman: The Animated Series", "Star Trek: The
		// Animated Series". Stripping it unconditionally left "ARK The Animated", which
		// matched no library show, so a perfectly good pack was held for review on every
		// sweep. Scene releases spell the pack sense as "Complete Series", so only treat
		// it as one when another pack word sits immediately before it.
		if strings.EqualFold(fields[len(fields)-1], "series") &&
			!(len(fields) > 1 && packWords[strings.ToLower(fields[len(fields)-2])]) {
			break
		}
		fields = fields[:len(fields)-1]
	}
	return strings.Join(fields, " ")
}

// contains reports whether needle appears in hay as a whole token: preceded by a
// space, and followed by a space, the end of the string, or a digit. The digit
// keeps scene tags like "REPACK2"/"PROPER2" matching, while "Property" no longer
// matches "proper".
func contains(hay, needle string) bool {
	for from := 0; ; {
		i := strings.Index(hay[from:], " "+needle)
		if i < 0 {
			return false
		}
		end := from + i + 1 + len(needle)
		if end >= len(hay) {
			return true
		}
		if c := hay[end]; c == ' ' || ('0' <= c && c <= '9') {
			return true
		}
		from = from + i + 1
	}
}

func detectResolution(lc string) Resolution {
	// An explicit "<n>p"/"<n>i" token always wins: "Hybrid.1080p.UHD.BluRay" is a
	// 1080p encode OF a UHD source, not 2160p. "uhd"/"4k" only infer 2160p when no
	// explicit token exists.
	switch {
	case strings.Contains(lc, "2160p"):
		return Res2160p
	case strings.Contains(lc, "1080p") || strings.Contains(lc, "1080i"):
		return Res1080p
	case strings.Contains(lc, "720p"):
		return Res720p
	case strings.Contains(lc, "576p"):
		return Res576p
	case strings.Contains(lc, "480p"):
		return Res480p
	case strings.Contains(lc, " 4k ") || strings.Contains(lc, " uhd "):
		return Res2160p
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
	case strings.Contains(lc, "webrip"), strings.Contains(lc, "web rip"):
		return SourceWebRip
	case strings.Contains(lc, "hdtv"), strings.Contains(lc, "pdtv"):
		return SourceHDTV
	case strings.Contains(lc, "dvdrip"), strings.Contains(lc, " dvd "):
		return SourceDVD
	case strings.Contains(lc, "hdcam"), strings.Contains(lc, " cam "),
		strings.Contains(lc, "telesync"), strings.Contains(lc, " ts "):
		return SourceCAM
	case strings.Contains(lc, " web "):
		// The bare " web " token LAST: it's a word in real titles ("Charlottes
		// Web", "Web of Lies"), so it only counts when nothing explicit matched.
		return SourceWebRip
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
	case strings.Contains(lc, " av1"), strings.HasPrefix(lc, "av1"):
		return CodecAV1
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
// bounded needles must match as whole tokens (see contains) — "aac"/"flac" are
// short enough to hide inside real words ("Isaac", "Flack").
var audioTags = []struct {
	label   string
	needles []string
	bounded bool
}{
	{"Atmos", []string{"atmos"}, false},
	{"TrueHD", []string{"truehd", "true hd"}, false},
	{"DTS-HD", []string{"dts hd", "dtshd", "dts x", "dts:x"}, false},
	{"DTS", []string{" dts "}, false},
	{"DDP", []string{"ddp", "dd+", "eac3", "e ac 3", "e ac3"}, false},
	{"DD", []string{" dd ", "ac3", "dd5 1", "dd2 0"}, false},
	{"AAC", []string{"aac"}, true},
	{"FLAC", []string{"flac"}, true},
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
			ok := strings.Contains(lc, n)
			if t.bounded {
				ok = contains(lc, n)
			}
			if ok && !seen[t.label] {
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

// reQualityStart marks where a release's technical tokens begin, so anything before them
// (after the episode marker) is the episode title.
var reQualityStart = regexp.MustCompile(`(?i)\b(\d{3,4}[pi]|web[-. ]?dl|webrip|bluray|blu-ray|hdtv|remux|dvdrip|x26[45]|h[-. ]?26[45]|hevc|avc|xvid|aac|ac3|eac3|dts|ddp|dd\+|truehd|atmos|10bit|8bit|amzn|nf|hmax|dsnp|atvp|repack|proper|internal|complete)\b`)

// EpisodeTitleFrom extracts the episode title a filename carries, if any:
// "Show - 6x03 - The Pawnee-Eagleton Tip-Off Classic.mkv" → "The Pawnee-Eagleton Tip-Off Classic".
//
// Returns "" when the name has no episode marker or nothing readable after it — most
// scene releases carry no title at all, and a guess would be worse than silence.
//
// Worth having because a title is an independent check on the NUMBER. When a pack says
// 6x03 is one episode and the metadata says that slot is another, the numbering schemes
// disagree, and the file gets filed (and renamed) as the wrong episode.
// StripBracketed removes parenthesised, bracketed and braced segments — the alternate
// titles and tags that shouldn't defeat a title match. "My Hero Academia (Boku no Hero
// Academia)" becomes "My Hero Academia", so a release carrying the romaji alt-title still
// resolves to the show. Nested and unbalanced groups are handled defensively.
func StripBracketed(s string) string {
	var b strings.Builder
	depth := 0
	for _, r := range s {
		switch r {
		case '(', '[', '{':
			depth++
		case ')', ']', '}':
			if depth > 0 {
				depth--
			}
		default:
			if depth == 0 {
				b.WriteRune(r)
			}
		}
	}
	return strings.TrimSpace(b.String())
}

func EpisodeTitleFrom(name string) string {
	name = strings.TrimSuffix(name, filepath.Ext(name))
	loc := reSxxExx.FindStringIndex(name)
	if loc == nil {
		loc = reNxNN.FindStringIndex(name)
	}
	if loc == nil {
		return ""
	}
	rest := name[loc[1]:]
	if q := reQualityStart.FindStringIndex(rest); q != nil {
		rest = rest[:q[0]]
	}
	rest = strings.NewReplacer(".", " ", "_", " ").Replace(rest)
	rest = strings.Trim(rest, " -[]()")
	if len(rest) < 2 {
		return "" // nothing meaningful — don't invent a title
	}
	return strings.Join(strings.Fields(rest), " ")
}
