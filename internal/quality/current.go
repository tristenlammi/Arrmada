package quality

import (
	"context"
	"slices"
	"strings"

	"github.com/tristenlammi/arrmada/internal/parser"
)

// One set of facts for the file on disk.
//
// The upgrader used to judge a file by its release name and the Library fit bars by what
// Convert's analysis found in the file itself, so the same file could "fit" on one page
// and be searched for an upgrade every six hours by the other — a tagless release name
// never meets an HDR or Atmos target, however HDR and Atmos the file really is. Every
// decision about a file already in the library now goes through CurrentFile: when the
// caller has probed facts for it they win, and without them the release name is read
// exactly as before.

// CurrentFile is the file a title already holds, as the upgrade, ceiling and downgrade
// decisions see it.
type CurrentFile struct {
	// Release is the release the file was imported from (or the synthetic baseline built
	// for a library-scanned file). Its source, group and edition always come from here: a
	// probe can't tell a BluRay from a WEB-DL.
	Release    string
	SizeGB     float64
	RuntimeMin int // content length, to turn the size into a bitrate; 0 = unknown
	// Facts are what Convert's analysis read from the file itself, nil when no current
	// analysis exists. When set they override the name's resolution, codec, HDR and audio.
	Facts *FileFacts
	// OrigRelease and OrigSizeGB are what a converted file was before Convert first shrank
	// it ("" / 0 when never converted, or unknown). Whether the file is done (TargetMet) is
	// judged on the file as it is, but an upgrade must also beat the original — its score,
	// its resolution and its bitrate — and is never the original release itself. Without
	// that, a remux the file was converted from (or another group's) looked like a large
	// bitrate upgrade over the smaller converted file, and got grabbed and converted again.
	OrigRelease string
	OrigSizeGB  float64
	// OrigLanguage is the title's original language (TMDB), for the profile's Audio
	// language rules ("Original language", and what an untagged track or name is read as).
	// "" leaves rules about the original language unapplied.
	OrigLanguage string
}

// upgradeBar is what a candidate has to beat to be an upgrade of the file.
type upgradeBar struct {
	total    int    // the higher of the file's score and its original's
	avoided  bool   // the file as it is carries a format the profile avoids
	resRank  int    // the higher of the file's resolution and its original's
	encode   Encode // the bitrate side: the original's size and codec when known
	current  string // the file's release, with curCodec its (probed) codec
	curCodec parser.Codec
	orig     string // the release it was converted from, "" when none
	// ev is the file as it is, scored: what the profile's upgrade trigger names a gain
	// against (rank.go). Under ranking v2 a file the profile no longer accepts is scored
	// with the profile's gates lifted (describe), so it still has rank keys to compare.
	ev Evaluation
	// mode is the ranking in effect (RankingV1 or RankingV2): which rule decides an upgrade.
	mode string
	// fits is whether the profile would still grab the file as it is (ev's eligibility
	// before describe).
	fits bool
	// origEv is the converted file's original, scored like ev; nil when never converted.
	// Under v2 an upgrade must rank above it as well as above the file.
	origEv *Evaluation
	// langAvoided says every language the file carries is one the profile avoids, as the
	// language rules read the file (its probed tracks over its name). Read with the
	// profile's gates lifted when the profile no longer accepts the file, so both rankings
	// read it alike.
	langAvoided bool
	// nameAvoided says the release the file is recorded under — and origLangAvoided the
	// release it was converted from, when orig is set — says it carries only languages the
	// profile avoids (nameLangAvoided). With langAvoided, the only files that may give up
	// resolution for a language (langEscape): a probe alone never makes a file eligible.
	nameAvoided, origLangAvoided bool
	// langRules are the profile's Audio language rules for this title (nil for a profile
	// without them), and titleLang the title's original language, for reading names.
	langRules map[string]string
	titleLang string
}

// nameLangAvoided reports a release name that says it carries only languages the rules
// avoid, read as a release on offer is read (releaseLangs): the languages it names, and an
// untagged name as the title's original language. A MULTi or Dual naming none (it may carry
// others), a name stating a language the rules don't avoid, and an untagged name of a title
// whose original language isn't known never are.
func nameLangAvoided(rules map[string]string, name, titleLang string) bool {
	if len(rules) == 0 || strings.TrimSpace(name) == "" {
		return false
	}
	return judgeLangs(rules, releaseLangs(parser.Parse(name), titleLang), mustReading{}).avoided
}

// namesAvoided is nameLangAvoided under this file's profile and title.
func (b upgradeBar) namesAvoided(name string) bool {
	return nameLangAvoided(b.langRules, name, b.titleLang)
}

// recordedAvoided reports a file whose recorded release — and, for a converted file, the
// release it was converted from — says it carries only avoided languages.
func (b upgradeBar) recordedAvoided() bool {
	return b.nameAvoided && (b.orig == "" || b.origLangAvoided)
}

// gain is THE verdict on whether cand's quality is worth replacing the file for, and on
// which key — shared by the upgrade sweeps (UpgradePick, for movies, single episodes and
// season packs alike) and the import gate (IsQualityUpgrade), so a release grabbed as an
// upgrade is never refused when it arrives. The ranking in effect decides how: the summed
// score under classic ranking (qualityGain, gainKey), the ordered keys under v2 (v2Gain).
func (b upgradeBar) gain(trigger string, cand Evaluation) (RankKey, bool) {
	cand = b.creditable(cand)
	if b.mode == RankingV2 {
		return b.v2Gain(trigger, cand)
	}
	if b.qualityGain(trigger, cand) {
		return b.gainKey(cand), true
	}
	return "", false
}

// creditable takes back the language credit cand earns for a language the file may already
// carry (b.ev.langUnknown: nothing to read on it, or an untagged track that might be it).
// An upgrade is only ever for a language the file is known to lack, so adding a language
// rule never re-downloads files whose audio nobody has read.
func (b upgradeBar) creditable(cand Evaluation) Evaluation {
	n := 0
	for _, code := range cand.langPrefCodes {
		if containsStr(b.ev.langUnknown, code) {
			n++
		}
	}
	if n == 0 {
		return cand
	}
	s := n * preferScore
	cand.langPrefs -= n
	cand.PrefCount -= n
	cand.TargetScore -= s
	cand.FormatScore -= s
	cand.Total -= s
	return cand
}

// intoAvoid reports a candidate the profile avoids where the file isn't avoided: never an
// upgrade, whatever else it has. Keeping the current file is always the preferable
// alternative. Under v2 only the avoids that are a tier count (TargetAvoided) — a negative
// custom format is a score there, as it is when ranking grabs.
//
// One more, for a file the language rules read as all-avoided (#137) whose recorded
// release doesn't say so (a MULTi, or a name stating a wanted language, that probed as
// English only): it is never replaced by a release whose name says it carries only avoided
// languages. Such a replacement would be eligible for the language escape (langEscape)
// where the file isn't, and the escape's lower-resolution pick could then be upgraded back
// to a release like the file, round and round. Every other file is judged as before.
func (b upgradeBar) intoAvoid(cand Evaluation) bool {
	if b.langAvoided && !b.recordedAvoided() && b.namesAvoided(cand.Candidate.Name) {
		return true
	}
	if b.mode == RankingV2 {
		return cand.TargetAvoided && !b.ev.TargetAvoided
	}
	return cand.Avoided && !b.avoided
}

// langEscape reports a candidate that may replace the file even though it has a lower
// resolution (#137): the file is in the avoided tier for its language — every language
// the file carries is avoided, AND the release it is recorded under says so too (untagged,
// read as an original language the profile avoids, or naming only avoided languages; for
// a converted file, the release it came from as well) — and cand is a release the profile
// would grab, alive, out of the avoided tier altogether, whose name states a language the
// profile wants or requires that the file lacks (statesWanted). The gain is named like
// escapeKey names a language escape, preferences, so a "resolution only" trigger never
// takes it.
//
// Why both readings of the file: a release the escape places names a wanted language, so
// its recorded name never reads as avoided again, whatever its probe finds — the escape
// can't fire twice in a row. A MULTi that probes as English only isn't eligible either, so
// a MULTi 2160p taken over the escape's 1080p stays (and intoAvoid keeps such a file from
// being replaced by one whose name is avoided, which would be eligible). So no chain of
// replacements leads from what the escape placed back to a file it may leave by language.
//
// Only the upgrade sweep's and import gates' lower-resolution paths ask this; at the
// same or a higher resolution the ordinary verdict (gain) decides, unchanged.
func (b upgradeBar) langEscape(trigger string, cand Evaluation) bool {
	if !b.langAvoided || !b.recordedAvoided() {
		return false
	}
	if !cand.Eligible || !cand.Candidate.alive() || cand.Avoided || cand.TargetAvoided || cand.langAvoided {
		return false
	}
	if !b.statesWanted(cand) {
		return false
	}
	if b.mode == RankingV2 {
		return v2TriggerAllows(trigger, KeyPreferences)
	}
	return triggerAllows(trigger, KeyPreferences)
}

// statesWanted reports a candidate whose name itself states a language the profile wants
// or requires (ITA, iTALiAN…), one the file isn't merely unread for (creditable). For the
// language escape only: a language the name leaves to be assumed — an untagged release
// read as the title's original, the original a MULTi or Dual always carries — earns the
// escape nothing, though ordinary ranking still credits it.
func (b upgradeBar) statesWanted(cand Evaluation) bool {
	for _, code := range cand.langPrefCodes {
		if containsStr(b.ev.langUnknown, code) {
			continue
		}
		for _, l := range cand.Candidate.Release.Languages {
			if baseLang(l) == code {
				return true
			}
		}
	}
	return false
}

// noWorse is the bitrate upgrade's other half: a heavier encode only replaces the file
// when it gives nothing up in quality — no lower score than the file and its original
// under classic ranking; under v2, ranked no lower than either on the avoid and quality
// keys, and a torrent someone is seeding.
func (b upgradeBar) noWorse(cand Evaluation) bool {
	cand = b.creditable(cand)
	if b.mode != RankingV2 {
		return cand.Total >= b.total
	}
	if !cand.Eligible || !cand.Candidate.alive() {
		return false
	}
	if c, _ := upgradeOrder(&cand, &b.ev); c > 0 {
		return false
	}
	if b.origEv != nil {
		if c, _ := upgradeOrder(&cand, b.origEv); c > 0 {
			return false
		}
	}
	return true
}

// qualityGain is the "it scores higher" half of an upgrade under classic ranking: cand
// must beat the bar (the file and, for a converted file, its original), and its gain over
// the file must be one the profile's "Replace for" trigger counts — or a same-group PROPER
// of the file.
func (b upgradeBar) qualityGain(trigger string, cand Evaluation) bool {
	// Leaving a low-quality group's encode for a clean release that scores no worse is a
	// gain of its own: such files carried a hidden −180 that any clean release beat, and
	// every trigger counts it (triggerAllows). The Unwanted items that are new (an
	// upscale, Dolby Vision without a fallback) don't reach back into the library like this:
	// they rank what is grabbed from now on, and never start a wave of re-downloads.
	if b.ev.lowGroup && !cand.Avoided && cand.Total >= b.total && triggerAllows(trigger, KeyAvoid) {
		return true
	}
	return cand.Total > b.total && qualityGain(trigger, cand, b.ev)
}

// gainKey names the gain qualityGain accepted cand for: the most significant improvement
// (improvementKey), else a same-group PROPER, leaving a low-quality group's file
// (KeyAvoid), or — for a gain only the size lean scores — KeySize.
func (b upgradeBar) gainKey(cand Evaluation) RankKey {
	if k := improvementKey(cand, b.ev); k != "" {
		return k
	}
	switch {
	case sameGroupProper(cand, b.ev):
		return KeyProper
	case b.ev.lowGroup && !cand.Avoided:
		return KeyAvoid
	}
	return KeySize
}

func (c CurrentFile) upgradeBar(p Profile, e *Engine) upgradeBar {
	// No runtime on the file's own candidate: scored as it always was, so a file over a
	// lowered ceiling isn't treated as worthless and replaced by anything at all.
	curCand := c.candidate()
	p.onDisk = true // the file and its original are judged as files, not releases on offer
	cur := e.Evaluate(p, curCand)
	b := upgradeBar{
		total: cur.Total, avoided: cur.Avoided, resRank: resRank[curCand.Release.Resolution],
		encode:  Encode{SizeGB: c.SizeGB, Codec: curCand.Release.Codec},
		current: c.Release, curCodec: curCand.Release.Codec, ev: cur,
		mode: NormalizeRanking(e.Ranking), fits: cur.Eligible,
	}
	b.titleLang = strings.TrimSpace(c.OrigLanguage)
	if !p.NoUnwanted { // book and music profiles have no language rules (Evaluate)
		b.langRules = langRules(p.Languages, b.titleLang)
	}
	b.nameAvoided = b.namesAvoided(c.Release)
	b.langAvoided = cur.langAvoided
	if !cur.Eligible {
		// Evaluate stops at the first gate a file fails (a missing Must language, a
		// resolution no longer allowed), before the language avoid is read.
		d := e.describe(p, curCand)
		b.langAvoided = d.langAvoided
		if b.mode == RankingV2 {
			b.ev = d
		}
	}
	if orig := strings.TrimSpace(c.OrigRelease); orig != "" && !strings.EqualFold(orig, strings.TrimSpace(c.Release)) {
		size := c.OrigSizeGB
		if size <= 0 {
			size = c.SizeGB
		}
		oc := NewCandidate(orig, size, 1_000_000)
		// Read by its name, with the title's language, like any release on offer.
		oc.ruleLang = strings.TrimSpace(c.OrigLanguage)
		o := e.Evaluate(p, oc)
		if o.Total > b.total {
			b.total = o.Total
		}
		// Read by its name, like the file's own recorded release (nameAvoided).
		b.origLangAvoided = b.namesAvoided(orig)
		if b.mode == RankingV2 {
			if !o.Eligible {
				o = e.describe(p, oc)
			}
			b.origEv = &o
		}
		if r := resRank[oc.Release.Resolution]; r > b.resRank {
			b.resRank = r
		}
		b.orig = orig
	}
	if enc, ok := c.OriginalEncode(); ok {
		b.encode = enc
	}
	return b
}

// OriginalEncode is the bitrate side of a converted file's original — its size and the
// codec its release names — for an import gate's IsBitrateUpgrade; false when the file
// was never converted or its original size is unknown.
func (c CurrentFile) OriginalEncode() (Encode, bool) {
	if c.OrigSizeGB <= 0 {
		return Encode{}, false
	}
	codec := parser.CodecUnknown
	if strings.TrimSpace(c.OrigRelease) != "" {
		codec = parser.Parse(c.OrigRelease).Codec
	}
	return Encode{SizeGB: c.OrigSizeGB, Codec: codec}, true
}

// excludes reports a candidate that is the file itself or what it was converted from:
// the same release name, or the same name with only the codec changed.
func (b upgradeBar) excludes(name string) bool {
	key := strings.ToLower(strings.TrimSpace(name))
	if key == strings.ToLower(strings.TrimSpace(b.current)) || sameRelease(name, b.current, true) {
		return true // the release we already have
	}
	if convertedFrom(name, b.current, b.curCodec) {
		return true // the release the converted file came from, by its stamped name
	}
	if b.orig != "" {
		if key == strings.ToLower(b.orig) || parser.WithoutCodec(name) == parser.WithoutCodec(b.orig) || sameRelease(name, b.orig, false) {
			return true // the release the file was converted from, as recorded
		}
	}
	return false
}

// sameRelease reports whether two names are one release however each is written. The
// file's recorded name is the downloaded file's ("Show.S04E06.1080p.AMZN.WEB-DL.DDP5.1.
// Atmos.H.264-NTb.mkv"), while the upgrade sweep sees the indexer's title for the same
// torrent, which some indexers rewrite ("Show S04E06 1080p AMZN WEB-DL DD+ 5 1 Atmos H
// 264-NTb"). Compared as strings the two never matched, so the sweep didn't know the
// release on offer was the file it already had: whenever the file's probed facts scored
// lower than its name (a track the probe didn't report), the same release was grabbed as
// an "upgrade" every sweep, and the import gate — which compares the file's own name —
// refused it every time.
//
// Both must name the same group, and every tag the parser reads must agree; an episode
// title one name carries and the other doesn't is not a tag. withCodec false leaves the
// codec out, for a converted file whose name was restamped with its new codec.
func sameRelease(a, b string, withCodec bool) bool {
	if strings.TrimSpace(a) == "" || strings.TrimSpace(b) == "" {
		return false
	}
	pa, pb := parser.Parse(a), parser.Parse(b)
	if pa.Group == "" || !strings.EqualFold(pa.Group, pb.Group) {
		return false // without a group, two names with the same tags can be two encodes
	}
	if parser.TitleKey(pa.Title) != parser.TitleKey(pb.Title) {
		return false
	}
	// A year only one name states (an indexer adding the show's year) says nothing.
	if pa.Year != 0 && pb.Year != 0 && pa.Year != pb.Year {
		return false
	}
	if withCodec && pa.Codec != pb.Codec {
		return false
	}
	return pa.Season == pb.Season && slices.Equal(pa.Episodes, pb.Episodes) &&
		slices.Equal(pa.Seasons, pb.Seasons) && pa.Complete == pb.Complete &&
		slices.Equal(pa.AbsoluteEpisodes, pb.AbsoluteEpisodes) &&
		pa.Resolution == pb.Resolution && pa.Source == pb.Source &&
		strings.EqualFold(pa.Streaming, pb.Streaming) && strings.EqualFold(pa.Edition, pb.Edition) &&
		pa.Proper == pb.Proper && pa.Repack == pb.Repack &&
		sameSet(pa.HDR, pb.HDR) && sameSet(pa.Audio, pb.Audio) && pa.AudioLossless == pb.AudioLossless &&
		pa.FullDisc == pb.FullDisc && pa.ThreeD == pb.ThreeD && pa.Upscaled == pb.Upscaled &&
		pa.Extras == pb.Extras && pa.Multi == pb.Multi && sameSet(pa.Languages, pb.Languages)
}

// sameSet reports whether two tag lists hold the same tags, in any order.
func sameSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	x, y := slices.Clone(a), slices.Clone(b)
	slices.Sort(x)
	slices.Sort(y)
	return slices.Equal(x, y)
}

// release is the file read as a release: the parsed name, with probed facts laid over it.
func (c CurrentFile) release() parser.Release {
	r := parser.Parse(c.Release)
	if c.Facts != nil {
		r = ReleaseWithFacts(r, *c.Facts)
	}
	return r
}

// facts are what the target is judged on: the probe's when there are any, else the
// release name's with a bitrate from the size and runtime.
func (c CurrentFile) facts() FileFacts {
	if c.Facts != nil {
		f := *c.Facts
		if f.BitrateMbps <= 0 {
			f.BitrateMbps = BitrateMbps(c.SizeGB, c.RuntimeMin)
		}
		return f
	}
	return ReleaseFacts(parser.Parse(c.Release), BitrateMbps(c.SizeGB, c.RuntimeMin))
}

// bitrateMbps is the file's raw average bitrate: the probe's when known, else from size.
func (c CurrentFile) bitrateMbps() float64 {
	if c.Facts != nil && c.Facts.BitrateMbps > 0 {
		return c.Facts.BitrateMbps
	}
	return BitrateMbps(c.SizeGB, c.RuntimeMin)
}

// candidate is the file as an engine candidate. Seeders are irrelevant for a file on disk
// and unknown, so it gets a large value rather than letting a seeder rule reject it.
//
// It carries the title's language for the language rules only (ruleLang, not OrigLanguage:
// the Unwanted pack's foreign-only check has never judged files on disk), and the probed
// facts, whose audio tracks the language rules read over the name.
func (c CurrentFile) candidate() Candidate {
	return Candidate{Name: c.Release, Release: c.release(), SizeGB: c.SizeGB, Seeders: 1_000_000,
		ruleLang: strings.TrimSpace(c.OrigLanguage), onDisk: true, heard: c.Facts}
}

// known reports whether there is anything to judge the file by.
func (c CurrentFile) known() bool {
	return strings.TrimSpace(c.Release) != "" || c.Facts != nil
}

// losslessLabels are the release-name audio tags that mean a lossless track.
var losslessLabels = []string{"TrueHD", "DTS-HD", "FLAC", "LPCM"}

// ReleaseWithFacts lays probed facts over a parsed release name. Resolution, codec, HDR
// and the Atmos/lossless audio come from the probe; source, group, edition and the other
// audio tags stay as the name has them.
func ReleaseWithFacts(r parser.Release, f FileFacts) parser.Release {
	// A resolution read from the file is measured, not inferred from an SD-era tag
	// (parser.ResolutionInferred), so nothing downstream may treat it as a guess.
	switch f.Resolution {
	case "2160p", "1080p", "720p", "SD":
		r.ResolutionInferred = false
	}
	switch f.Resolution {
	case "2160p":
		r.Resolution = parser.Res2160p
	case "1080p":
		r.Resolution = parser.Res1080p
	case "720p":
		r.Resolution = parser.Res720p
	case "SD":
		// The probe says SD without saying which; keep the name's when it agrees.
		if r.Resolution != parser.Res576p && r.Resolution != parser.Res480p {
			r.Resolution = parser.Res480p
		}
	}
	switch f.Codec {
	case "hevc":
		r.Codec = parser.CodecX265
	case "av1":
		r.Codec = parser.CodecAV1
	case "h264":
		r.Codec = parser.CodecX264
	}
	// A codec the engine has no name for (VP9, MPEG-2) keeps whatever the name said.

	var hdr []string
	if f.DolbyVision {
		hdr = append(hdr, "DV")
	}
	if !f.dvOnly() {
		switch f.HDR {
		case "HDR10+":
			hdr = append(hdr, "HDR10+", "HDR10") // tagged like the parser tags it
		case "HDR10":
			hdr = append(hdr, "HDR10")
		case "HLG":
			hdr = append(hdr, "HLG")
		}
	}
	r.HDR = hdr

	audio := make([]string, 0, len(r.Audio)+2)
	hasLossless := false
	for _, a := range r.Audio {
		switch {
		case a == "Atmos":
			continue // the probe decides
		case containsStr(losslessLabels, a):
			if !f.Lossless {
				continue // the name claims a lossless track the file doesn't have
			}
			hasLossless = true
		}
		audio = append(audio, a)
	}
	if f.Atmos {
		audio = append([]string{"Atmos"}, audio...)
	}
	if f.Lossless && !hasLossless && f.LosslessCodec != "" {
		audio = append(audio, f.LosslessCodec)
	}
	if len(audio) == 0 {
		audio = nil
	}
	r.Audio = audio
	// The probe decides lossless too: a name's "DTS-HD MA" on a file whose track is lossy
	// (or a tagless name on a TrueHD file) must not score as the name says.
	r.AudioLossless = f.Lossless
	return r
}

// FileVerdict is the one answer to "is this file done?" under a profile.
type FileVerdict struct {
	// Facts are what the file was judged on, and Probed says whether they came from
	// Convert's analysis (true) or the release name (false).
	Facts  FileFacts
	Probed bool
	// Current is the file scored as a release under the profile, runtime included so the
	// bitrate ceiling applies.
	Current Evaluation
	// Eligible is false when the profile would no longer grab this file — a resolution it
	// doesn't allow, a missing Must, over the ceiling. Switching a title to a profile that
	// rejects its file is a downgrade.
	Eligible bool
	// TargetMet: the file already is the profile's target, so upgrading stops.
	TargetMet bool
	// AtCeiling: no release the profile would accept can clear the upgrade step, so
	// searching for one is spent traffic. True whenever TargetMet is.
	AtCeiling bool
}

// JudgeFile is the single verdict on a file already on disk under a saved profile ref.
// The upgrade sweeps, the import gates, the profile-change prompt and the profile-edit
// impact count all read a file through here (or JudgeFile on a StoredProfile), so the
// same file can't be judged two ways depending on who asks.
//
// An unknown ref is judged under the fallback profile for eligibility only; it has no
// target and no upgrade settings, so TargetMet and AtCeiling are false.
func (s *Service) JudgeFile(ctx context.Context, ref string, cur CurrentFile) FileVerdict {
	sp, err := s.GetStored(ctx, ref)
	if err != nil {
		p, e := s.Resolve(ctx, ref)
		return judge(nil, p, e, cur)
	}
	return sp.JudgeFile(cur)
}

// JudgeFile is the verdict on a file under this profile as it stands — saved or not, so a
// profile edit can be judged before it is saved. The profile should be compiled the way a
// save compiles it (Service.Create/Update, DecideSpec).
func (sp StoredProfile) JudgeFile(cur CurrentFile) FileVerdict {
	return judge(&sp, sp.ToProfile(), sp.Engine(), cur)
}

func judge(sp *StoredProfile, p Profile, e *Engine, cur CurrentFile) FileVerdict {
	v := FileVerdict{Facts: cur.facts(), Probed: cur.Facts != nil}
	if !cur.known() {
		// Nothing recorded to judge it by: no verdict either way, and nothing to reject.
		v.Eligible = true
		return v
	}
	p.onDisk = true
	v.Current = e.Evaluate(p, cur.candidate().WithRuntime(cur.RuntimeMin))
	v.Eligible = v.Current.Eligible
	if sp != nil {
		// A preferred or required audio language the file lacks (or only avoided ones)
		// means it isn't the target yet, whatever its picture and bitrate.
		v.TargetMet = sp.TargetMet(v.Facts) && !v.Current.langShort
		// The bitrate ceiling only ends the search for a file that already fits: no
		// missing Must, every Prefer met, nothing the profile avoids. Then a heavier
		// encode is the one gain left, and the ceiling can rule it out. A file short of a
		// Must or a Prefer (a heavy H.264 episode under "Must HEVC") can still be beaten
		// by a release that has it, however close to the ceiling its bitrate sits.
		v.AtCeiling = v.TargetMet || (!v.Current.langShort &&
			fitsBesidesBitrate(*sp, v.Facts) && atCeiling(*sp, p, cur))
	}
	return v
}

// fitsBesidesBitrate reports whether a file meets the profile's target in everything but
// its bitrate window: the right codec, HDR and audio with no Must missing and nothing
// avoided, and every Prefer met. A profile with no target asks nothing of the file's
// formats, so it fits.
func fitsBesidesBitrate(sp StoredProfile, f FileFacts) bool {
	if sp.Ideal == nil || sp.Ideal.Empty() {
		return true
	}
	for _, is := range CheckFit(*sp.Ideal, sp.AllowedResolutions, f).Issues {
		if is.Kind != "bitrate" {
			return false
		}
	}
	return prefersMet(*sp.Ideal, f)
}

// atCeiling is the bitrate half of AtCeiling (see there): the file is at the best
// resolution the profile allows and the next upgrade step lands above its ceiling.
func atCeiling(sp StoredProfile, p Profile, cur CurrentFile) bool {
	res := cur.release().Resolution
	limit := p.capFor(res)
	// No cap is no ceiling, and with no percentage step there's no bitrate upgrade path to
	// exhaust in the first place — in both cases only a quality gain can win, and that
	// can't be ruled out here.
	if limit <= 0 || sp.UpgradeMinPercent <= 0 {
		return false
	}
	// Raw, like the ceiling itself (Evaluate): the question is whether a permitted
	// release can be the required step above this one.
	curBr := cur.bitrateMbps()
	if curBr <= 0 {
		return false // can't express the file as a bitrate — don't guess
	}
	// A converted file's upgrade has to beat what it was before conversion (see
	// upgradeBar), so its headroom is measured from there.
	if origBr := BitrateMbps(cur.OrigSizeGB, cur.RuntimeMin); origBr > curBr {
		curBr = origBr
	}
	// A resolution the profile allows and we don't have is still an upgrade, whatever the
	// bitrate says.
	best := 0
	for _, r := range p.AllowedResolutions {
		if resRank[r] > best {
			best = resRank[r]
		}
	}
	if resRank[res] < best {
		return false
	}
	pct := sp.UpgradeMinPercent
	if pct < MinUpgradePercent {
		pct = MinUpgradePercent
	}
	// The smallest bitrate that would actually count as an upgrade — both gates from
	// IsBitrateUpgrade, so the ceiling can't disagree with the thing it's predicting.
	needed := curBr * (1 + pct/100)
	if floor := curBr + MinUpgradeMarginMbps; floor > needed {
		needed = floor
	}
	return needed > limit
}
