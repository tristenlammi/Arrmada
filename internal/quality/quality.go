// Package quality decides which release to grab. It scores parsed releases
// against a profile — a quality ladder (resolution × source), custom-format
// preferences, a size ceiling — and ranks candidates into a decision with
// plain-language reasons. This is the engine behind the "what you'll get"
// experience; the Simple UI hides the numbers, the Advanced UI exposes them.
package quality

import (
	"fmt"
	"sort"
	"strings"

	"github.com/tristenlammi/arrmada/internal/parser"
)

// --- Quality ladder -------------------------------------------------------

var resBase = map[parser.Resolution]int{
	parser.Res2160p: 400,
	parser.Res1080p: 250,
	parser.Res720p:  90,
	parser.Res576p:  60,
	parser.Res480p:  40,
}

var sourceBonus = map[parser.Source]int{
	parser.SourceRemux:  130,
	parser.SourceBluray: 95,
	parser.SourceWebDL:  70,
	parser.SourceWebRip: 35,
	parser.SourceDVD:    20,
	parser.SourceHDTV:   10,
	parser.SourceCAM:    -50,
}

var sourceRank = map[parser.Source]int{
	parser.SourceRemux: 6, parser.SourceBluray: 5, parser.SourceWebDL: 4,
	parser.SourceWebRip: 3, parser.SourceDVD: 2, parser.SourceHDTV: 1, parser.SourceCAM: 0,
}

var resRank = map[parser.Resolution]int{
	parser.Res2160p: 5, parser.Res1080p: 4, parser.Res720p: 3, parser.Res576p: 2, parser.Res480p: 1,
}

// qualityScore ranks by resolution only. Source is a filter (min/max) and a
// tiebreaker — NOT a score factor — so a high-bitrate WEBRip can beat a heavily
// compressed BluRay of the same resolution, which is what "highest bitrate in
// range" means.
func qualityScore(r parser.Release) int {
	return resBase[r.Resolution]
}

// lowQualityGroups are release groups widely regarded as over-compressed /
// low-quality despite carrying legit resolution/source tags. Kept conservative
// so decent groups (NAHOM, GalaxyRG, FraMeSToR…) are never penalized.
var lowQualityGroups = []string{"yify", "yts", "megusta"}

// lowQualityGroup reports whether a release name is from a known low-quality group.
func lowQualityGroup(lcName string) bool {
	for _, g := range lowQualityGroups {
		if strings.Contains(lcName, g) {
			return true
		}
	}
	return false
}

// --- Custom formats -------------------------------------------------------

// ConditionType is what a custom-format condition matches on.
type ConditionType string

const (
	CondDynamicRange ConditionType = "dynamic_range" // Value ∈ HDR tags (DV, HDR10, HDR10+)
	CondAudio        ConditionType = "audio"         // Value ∈ audio tags
	CondCodec        ConditionType = "codec"
	CondSource       ConditionType = "source"
	CondResolution   ConditionType = "resolution"
	CondEdition      ConditionType = "edition"
	CondReleaseGroup ConditionType = "release_group"
)

// Condition is one predicate over a parsed release.
type Condition struct {
	Type   ConditionType `json:"type"`
	Value  string        `json:"value"`
	Negate bool          `json:"negate,omitempty"`
}

func (c Condition) matches(r parser.Release) bool {
	var hit bool
	switch c.Type {
	case CondDynamicRange:
		hit = containsStr(r.HDR, c.Value)
	case CondAudio:
		hit = containsStr(r.Audio, c.Value)
	case CondCodec:
		hit = string(r.Codec) == c.Value
	case CondSource:
		hit = string(r.Source) == c.Value
	case CondResolution:
		hit = string(r.Resolution) == c.Value
	case CondEdition:
		hit = strings.EqualFold(r.Edition, c.Value)
	case CondReleaseGroup:
		hit = strings.EqualFold(r.Group, c.Value)
	}
	if c.Negate {
		return !hit
	}
	return hit
}

// CustomFormat is a named set of conditions (all must match — AND).
type CustomFormat struct {
	Name       string      `json:"name"`
	Conditions []Condition `json:"conditions"`
}

// Matches reports whether every condition holds for the release.
func (f CustomFormat) Matches(r parser.Release) bool {
	for _, c := range f.Conditions {
		if !c.matches(r) {
			return false
		}
	}
	return len(f.Conditions) > 0
}

// --- Profile & candidates -------------------------------------------------

// Profile expresses "what a good release looks like".
type Profile struct {
	Name               string                 `json:"name"`
	AllowedResolutions []parser.Resolution    `json:"allowed_resolutions"` // empty = any
	MinSource          parser.Source          `json:"min_source,omitempty"`
	MaxSource          parser.Source          `json:"max_source,omitempty"` // empty = no upper bound
	BitrateCapMbps     float64                `json:"bitrate_cap_mbps,omitempty"` // 0 = no cap; rejects releases whose bitrate exceeds it (length-independent)
	SmallBias          float64                `json:"small_bias,omitempty"`  // score penalty per GB
	FormatScores       map[string]int         `json:"format_scores,omitempty"`
	MinFormatScore     int                    `json:"min_format_score,omitempty"`
	Keywords           []Keyword              `json:"keywords,omitempty"`
	Rejected           []string               `json:"rejected,omitempty"`
	MinSeeders         int                    `json:"min_seeders,omitempty"`
}

// Candidate is a release under consideration (release + indexer metadata).
type Candidate struct {
	Name       string         `json:"name"`
	Release    parser.Release `json:"release"`
	SizeGB     float64        `json:"size_gb"`
	Seeders    int            `json:"seeders"`
	RuntimeMin int            `json:"runtime_min,omitempty"` // content length this release covers; 0 = unknown (bitrate cap can't apply)
}

// NewCandidate parses a release name into a Candidate.
func NewCandidate(name string, sizeGB float64, seeders int) Candidate {
	return Candidate{Name: name, Release: parser.Parse(name), SizeGB: sizeGB, Seeders: seeders}
}

// WithRuntime attaches the content runtime (minutes) so the bitrate ceiling can apply. For a
// movie that's the film's runtime; for a single episode the episode runtime; for a season pack
// the sum across its episodes. 0 leaves the bitrate cap inert for this candidate.
func (c Candidate) WithRuntime(min int) Candidate {
	c.RuntimeMin = min
	return c
}

// gibToMegabit converts one GiB to megabits (2^30 bytes × 8 bits ÷ 1e6), so bitrateMbps yields
// Mbps (decimal megabits per second) from a GiB size and a minutes runtime.
const gibToMegabit = 1024.0 * 1024.0 * 1024.0 * 8.0 / 1e6 // ≈ 8589.93

// bitrateMbps is the release's average bitrate in Mbps, or 0 when the runtime is unknown.
func (c Candidate) bitrateMbps() float64 { return BitrateMbps(c.SizeGB, c.RuntimeMin) }

// BitrateMbps is the average bitrate in Mbps for a GiB size over a minutes
// runtime, or 0 when either is unknown/zero.
func BitrateMbps(sizeGB float64, runtimeMin int) float64 {
	if runtimeMin <= 0 || sizeGB <= 0 {
		return 0
	}
	return sizeGB * gibToMegabit / float64(runtimeMin*60)
}

// Evaluation is the scored result for a single candidate.
type Evaluation struct {
	Candidate    Candidate `json:"candidate"`
	Eligible     bool      `json:"eligible"`
	RejectReason string    `json:"reject_reason,omitempty"`
	QualityScore int       `json:"quality_score"`
	FormatScore  int       `json:"format_score"`
	SizeScore    int       `json:"size_score"`
	Total        int       `json:"total"`
	Matched      []string  `json:"matched,omitempty"` // preferred formats that matched
}

// Decision is the ranked outcome over a set of candidates.
type Decision struct {
	Winner     *Evaluation  `json:"winner"`
	Why        []string     `json:"why,omitempty"`
	ChosenOver string       `json:"chosen_over,omitempty"`
	Eligible   []Evaluation `json:"eligible"`
	Rejected   []Evaluation `json:"rejected"`
}

// Engine scores releases using a catalog of custom formats.
type Engine struct {
	formats map[string]CustomFormat
}

// NewEngine builds an engine over the given custom-format catalog.
func NewEngine(formats []CustomFormat) *Engine {
	m := make(map[string]CustomFormat, len(formats))
	for _, f := range formats {
		m[f.Name] = f
	}
	return &Engine{formats: m}
}

// NewDefaultEngine uses the built-in custom formats.
func NewDefaultEngine() *Engine { return NewEngine(DefaultFormats()) }

// Evaluate scores one candidate against a profile.
func (e *Engine) Evaluate(p Profile, c Candidate) Evaluation {
	r := c.Release
	ev := Evaluation{Candidate: c}

	if len(p.AllowedResolutions) > 0 && !containsRes(p.AllowedResolutions, r.Resolution) {
		ev.RejectReason = fmt.Sprintf("Not in profile — %s", resLabel(r.Resolution))
		return ev
	}
	if p.MinSource != "" && sourceRank[r.Source] < sourceRank[p.MinSource] {
		ev.RejectReason = fmt.Sprintf("Not %s — this is %s", p.MinSource, sourceLabel(r.Source))
		return ev
	}
	if p.MaxSource != "" && sourceRank[r.Source] > sourceRank[p.MaxSource] {
		ev.RejectReason = fmt.Sprintf("Above your %s ceiling — this is %s", sourceLabel(p.MaxSource), sourceLabel(r.Source))
		return ev
	}
	// Bitrate ceiling (length-independent). Only applies when we know the runtime; without it
	// we can't turn a file size into a bitrate, so the cap is skipped rather than guessed.
	if p.BitrateCapMbps > 0 {
		if br := c.bitrateMbps(); br > p.BitrateCapMbps {
			ev.RejectReason = fmt.Sprintf("Over your %.0f Mbps ceiling (%.1f Mbps)", p.BitrateCapMbps, br)
			return ev
		}
	}
	if p.MinSeeders > 0 && c.Seeders < p.MinSeeders {
		ev.RejectReason = fmt.Sprintf("Only %d seeders (needs %d)", c.Seeders, p.MinSeeders)
		return ev
	}
	lcName := strings.ToLower(c.Name)
	for _, term := range p.Rejected {
		if containsTerm(lcName, strings.ToLower(strings.TrimSpace(term))) {
			ev.RejectReason = "Contains rejected term: " + term
			return ev
		}
	}

	ev.QualityScore = qualityScore(r)
	// Baseline quality signal: notorious low-quality groups (over-compressed
	// rips) rank below proper encodes of the same resolution/source. They stay
	// eligible — just not the default pick.
	if lowQualityGroup(lcName) {
		ev.QualityScore -= 180
	}
	for name, score := range p.FormatScores {
		f, ok := e.formats[name]
		if !ok || !f.Matches(r) {
			continue
		}
		ev.FormatScore += score
		if score > 0 {
			ev.Matched = append(ev.Matched, name)
		}
	}
	for _, k := range p.Keywords {
		if k.Term == "" || !strings.Contains(lcName, strings.ToLower(k.Term)) {
			continue
		}
		ev.FormatScore += k.Score
		if k.Score > 0 {
			ev.Matched = append(ev.Matched, k.Term)
		}
	}
	sort.Strings(ev.Matched) // stable output

	if ev.FormatScore < p.MinFormatScore {
		ev.RejectReason = "Below the profile's minimum format score"
		return ev
	}

	// Size doesn't enter the score — it only breaks ties (as bitrate) in Decide.
	// A strong small-size profile ("smallest") still nudges the score smaller.
	if p.SmallBias >= 4 {
		ev.SizeScore = -int(c.SizeGB * p.SmallBias)
	}
	ev.Total = ev.QualityScore + ev.FormatScore + ev.SizeScore
	ev.Eligible = true
	return ev
}

// containsTerm reports whether term appears in name as a whole token — bounded by non-alphanumeric
// characters (the "." / " " / "-" / "_" that separate release-name parts) or the string ends. This
// keeps a reject term like "com" (the .com executable extension) from matching inside "Complete".
func containsTerm(name, term string) bool {
	if term == "" {
		return false
	}
	for start := 0; ; {
		i := strings.Index(name[start:], term)
		if i < 0 {
			return false
		}
		i += start
		leftOK := i == 0 || !isAlnum(name[i-1])
		end := i + len(term)
		rightOK := end >= len(name) || !isAlnum(name[end])
		if leftOK && rightOK {
			return true
		}
		start = i + 1
	}
}

func isAlnum(b byte) bool { return (b >= 'a' && b <= 'z') || (b >= '0' && b <= '9') }

// Decide ranks candidates and explains the winner.
func (e *Engine) Decide(p Profile, cands []Candidate) Decision {
	var d Decision
	for _, c := range cands {
		ev := e.Evaluate(p, c)
		if ev.Eligible {
			d.Eligible = append(d.Eligible, ev)
		} else {
			d.Rejected = append(d.Rejected, ev)
		}
	}
	// Rank by score, then by bitrate. For one movie every candidate is the same
	// runtime, so a larger file = higher bitrate = better — unless the profile
	// explicitly optimizes for small size, in which case smaller wins the tie.
	preferSmaller := p.SmallBias >= 4
	sort.SliceStable(d.Eligible, func(i, j int) bool {
		a, b := d.Eligible[i], d.Eligible[j]
		if a.Total != b.Total {
			return a.Total > b.Total
		}
		if a.Candidate.SizeGB != b.Candidate.SizeGB {
			if preferSmaller {
				return a.Candidate.SizeGB < b.Candidate.SizeGB
			}
			return a.Candidate.SizeGB > b.Candidate.SizeGB // higher bitrate
		}
		// Same bitrate → prefer the better source, then more seeders.
		if sourceRank[a.Candidate.Release.Source] != sourceRank[b.Candidate.Release.Source] {
			return sourceRank[a.Candidate.Release.Source] > sourceRank[b.Candidate.Release.Source]
		}
		return a.Candidate.Seeders > b.Candidate.Seeders
	})
	if len(d.Eligible) > 0 {
		d.Winner = &d.Eligible[0]
		d.Why = whyReasons(p, *d.Winner)
		if len(d.Eligible) > 1 {
			ru := d.Eligible[1].Candidate.Release
			d.ChosenOver = fmt.Sprintf("Chosen over the %s %s — %s",
				resLabel(ru.Resolution), sourceLabel(ru.Source),
				loseReason(d.Winner.Candidate.Release, ru))
		}
	}
	return d
}

func whyReasons(p Profile, e Evaluation) []string {
	r := e.Candidate.Release
	var out []string

	top := bestAllowedRes(p)
	if (top == parser.ResUnknown || r.Resolution == top) && sourceRank[r.Source] >= sourceRank[parser.SourceWebDL] {
		out = append(out, "Highest quality that matches your goal")
	} else {
		out = append(out, "Best available that fits your profile")
	}
	for _, m := range e.Matched {
		out = append(out, m+" — matched")
	}
	if p.SmallBias >= 4 {
		out = append(out, "Smallest watchable size")
	} else if p.BitrateCapMbps > 0 {
		out = append(out, fmt.Sprintf("Highest bitrate under your %.0f Mbps ceiling", p.BitrateCapMbps))
	} else {
		out = append(out, "Highest bitrate available")
	}
	return out
}

func loseReason(win, other parser.Release) string {
	switch {
	case resRank[other.Resolution] < resRank[win.Resolution]:
		return "lower resolution"
	case sourceRank[other.Source] < sourceRank[win.Source]:
		return fmt.Sprintf("%s, not %s", sourceLabel(other.Source), sourceLabel(win.Source))
	default:
		return "fewer preferred extras"
	}
}

// --- helpers --------------------------------------------------------------

func containsStr(xs []string, v string) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}

func containsRes(xs []parser.Resolution, v parser.Resolution) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}

func bestAllowedRes(p Profile) parser.Resolution {
	best := parser.ResUnknown
	for _, r := range p.AllowedResolutions {
		if resRank[r] > resRank[best] {
			best = r
		}
	}
	return best
}

func resLabel(r parser.Resolution) string {
	if r == parser.ResUnknown {
		return "unknown"
	}
	return string(r)
}

func sourceLabel(s parser.Source) string {
	if s == parser.SourceUnknown {
		return "unknown"
	}
	return string(s)
}
