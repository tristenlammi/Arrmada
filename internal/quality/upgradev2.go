package quality

import (
	"math"
	"sort"
)

// Upgrades under ranking v2. The grab side ranks releases key by key (compareV2); the
// upgrade side has to read the same keys, or the two disagree — a release the sweep grabs
// as an upgrade gets refused by the import gate when it arrives, or a file is replaced by
// something v2 would never have picked over it. So under v2 a candidate replaces the file
// when the comparator ranks it above the file on one of the keys that describe the release
// itself, and the profile's "Replace for" trigger counts that key:
//
//	any:        resolution, preferences, custom, source, proper (and leaving a low-quality
//	            group's file, or one the profile no longer accepts — see escapeKey)
//	source:     resolution, preferences, custom, source
//	format:     resolution, preferences, custom
//	resolution: resolution
//
// The group, network, bitrate, health and size keys never decide an upgrade: a release
// that is only a tier-1 group's, or on the preferred network, or heavier, or better seeded
// is a tie-break between equals, not a reason to re-download a file. A heavier encode has
// its own rule (IsBitrateUpgrade), which still applies when the candidate gives nothing up
// on the keys above (noWorse). A same-group PROPER of the file is always taken, as it is
// under classic ranking.
//
// Upgrades can't loop between keys: the comparator is one total order, so a release taken
// for a source gain can't be undone by the old file winning on bitrate — bitrate never
// decides an upgrade, and the old file is the release the sweep already tried once.

// v2TriggerAllows reports whether an upgrade decided by key k is worth a re-download under
// a profile's trigger. Unlike classic ranking's triggerAllows, "any" is a list too: the
// keys that never upgrade stay out of it. Leaving the avoided tier (KeyAvoid) counts under
// every trigger: the profile already said it doesn't want that file. TriggerLanguage takes
// only a language gain — the target's language, or a preferred/required one the file
// lacks — under both rankings.
func v2TriggerAllows(trigger string, k RankKey) bool {
	if k == KeyAvoid {
		return true
	}
	if NormalizeTrigger(trigger) == TriggerLanguage {
		return k == KeyLanguage
	}
	switch NormalizeTrigger(trigger) {
	case TriggerSource:
		return k == KeyResolution || k == KeyPreferences || k == KeyCustom || k == KeySource
	case TriggerFormat:
		return k == KeyResolution || k == KeyPreferences || k == KeyCustom
	case TriggerResolution:
		return k == KeyResolution
	}
	switch k {
	case KeyResolution, KeyPreferences, KeyCustom, KeySource, KeyProper, KeyAvoid, KeyLanguage:
		return true
	}
	return false
}

// upgradeOrder compares a and b on the avoid key and the quality keys only (compareV2
// without health, group, network, bitrate and size): negative when a ranks above b.
func upgradeOrder(a, b *Evaluation) (int, RankKey) {
	if a.TargetAvoided != b.TargetAvoided {
		return order(!a.TargetAvoided), KeyAvoid
	}
	return compareQualityKeys(a, b)
}

// v2Gain is the upgrade verdict under ranking v2 (see the top of this file): the key cand
// is accepted on, and whether it is accepted. b.ev is the file; the caller has already
// refused a candidate the profile avoids where the file isn't avoided (intoAvoid), one
// that drops resolution, and the file's own release.
func (b upgradeBar) v2Gain(trigger string, cand Evaluation) (RankKey, bool) {
	cur := b.ev
	// A release the profile wouldn't grab is never an upgrade, and a dead torrent never
	// downloads: the comparator ranks it below the file before any quality key.
	if !cand.Eligible || !cand.Candidate.alive() || (cand.TargetAvoided && !cur.TargetAvoided) {
		return "", false
	}
	// A converted file's upgrade must also rank above what it was before Convert shrank it,
	// or the original (or another group's copy of it) comes back on every sweep.
	if b.origEv != nil {
		if c, _ := upgradeOrder(&cand, b.origEv); c >= 0 {
			return "", false
		}
	}
	c, k := compareQualityKeys(&cand, &cur)
	if c > 0 {
		// The file ranks higher on what the release is. Leaving an avoided file for one that
		// gives up a resolution, a preference or the source is no upgrade either.
		return "", false
	}
	if c < 0 {
		if v2TriggerAllows(trigger, k) {
			return k, true
		}
		if sameGroupProper(cand, cur) {
			return KeyProper, true // a fix of the release we have, whatever the trigger
		}
	}
	// No worse on any quality key, and out of the avoided tier: that can be a gain of its own.
	if !cand.TargetAvoided && (cur.TargetAvoided || !b.fits) {
		if ek := escapeKey(cur, b.fits); ek != "" && v2TriggerAllows(trigger, ek) {
			return ek, true
		}
	}
	return "", false
}

// escapeKey is the gain in leaving the avoided tier for a candidate that carries none of
// the file's avoids, named the way classic ranking's score named it so a trigger reads the
// same in both modes: a target format the profile avoids (Dolby Vision under "Avoid") is a
// preferences gain, an avoided "Other formats" built-in (AMZN) a custom one. Leaving a
// low-quality group's encode, or a file the profile would no longer grab at all, is KeyAvoid,
// which every trigger takes — as classic ranking's low-quality-group escape. "" when the avoid
// was an Unwanted item that never reaches back into the library (an upscale, Dolby Vision
// without a fallback): those rank what is grabbed from now on and don't start a wave of
// re-downloads on their own.
func escapeKey(cur Evaluation, fits bool) RankKey {
	// The target's Audio language row is a target row like codec and HDR.
	if cur.langAvoided {
		return KeyPreferences
	}
	for _, f := range cur.AvoidedFormats {
		if isTargetFormat(f) {
			return KeyPreferences
		}
	}
	for _, f := range cur.AvoidedFormats {
		if isBuiltinFormat(f) {
			return KeyCustom
		}
	}
	if cur.lowGroup || !fits {
		return KeyAvoid
	}
	return ""
}

// describe scores a file the profile would no longer grab — a resolution it no longer
// allows, a source outside its range, a missing Must, a rejected term — as though the
// profile had none of those gates, so ranking v2 still has its resolution, preferences,
// custom score and source to compare an upgrade against. Classic ranking compares such a
// file's zero score instead. The evaluation it returns is only ever a comparison side:
// Eligible on it says nothing about the profile.
func (e *Engine) describe(p Profile, c Candidate) Evaluation {
	q := p
	q.AllowedResolutions, q.MinSource, q.MaxSource, q.RejectPreRelease = nil, "", "", false
	q.BitrateCapMbps, q.Windows = 0, nil
	q.MinSeeders, q.Rejected, q.Required, q.MinFormatScore = 0, nil, nil, math.MinInt
	q.noLangGate = true // a missing Must language too; the ones it has still count
	ev := e.Evaluate(q, c)
	// Evaluate lists a required format the release has only while the profile requires
	// it; put those back, so the preferences key counts the Musts the file does carry.
	for _, name := range p.Required {
		// ev.Candidate.Release: as Evaluate read it (a file's, ForFile).
		if f, ok := e.formats[name]; ok && e.match(f, c.Name, ev.Candidate.Release) && !containsStr(ev.Matched, name) {
			ev.Matched = append(ev.Matched, name)
		}
	}
	sort.Strings(ev.Matched)
	ev.fillRankKeys(p)
	return ev
}
