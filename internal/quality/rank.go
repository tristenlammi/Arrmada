package quality

import (
	"strings"

	"github.com/tristenlammi/arrmada/internal/parser"
)

// RankKey names one component of how two releases compare. Upgrades use it to say which
// kind of gain a candidate has over the file on disk, so a profile can choose which kinds
// are worth a re-download (its upgrade trigger).
type RankKey string

const (
	KeyAvoid       RankKey = "avoid"       // a format the profile avoids, or a bitrate under the floor
	KeyResolution  RankKey = "resolution"  // a higher resolution
	KeyPreferences RankKey = "preferences" // more of the target's preferred formats (codec, HDR, audio)
	KeyCustom      RankKey = "custom"      // custom formats, keywords and the other built-in formats
	KeySource      RankKey = "source"      // a better source (WEB → BluRay → Remux)
	KeyProper      RankKey = "proper"      // a PROPER or REPACK
	KeyGroup       RankKey = "group"       // the release group
	KeyBitrate     RankKey = "bitrate"     // a heavier encode
	KeyHealth      RankKey = "health"      // seeders
	KeySize        RankKey = "size"        // file size
)

// Upgrade triggers: which gains a profile replaces a file for (StoredProfile.UpgradeTrigger).
const (
	TriggerAny        = "any"        // every gain — what profiles did before triggers existed
	TriggerSource     = "source"     // resolution, preferences, custom formats or a better source
	TriggerFormat     = "format"     // resolution, preferences or custom formats
	TriggerResolution = "resolution" // resolution only
)

// ValidTrigger reports whether t is a trigger a profile may be saved with ("" is TriggerAny).
func ValidTrigger(t string) bool {
	switch t {
	case "", TriggerAny, TriggerSource, TriggerFormat, TriggerResolution:
		return true
	}
	return false
}

// NormalizeTrigger reads a stored trigger: "" and anything unrecognised are TriggerAny, so
// a profile never upgrades less than it did before triggers existed by accident.
func NormalizeTrigger(t string) string {
	if t == "" || !ValidTrigger(t) {
		return TriggerAny
	}
	return t
}

// improvementKey is the most significant way cand beats cur, checked in the order
// resolution, preferences, custom, source, proper. "" when none of those improved — the
// score gain came from somewhere no trigger names (a low-quality group penalty, a size
// lean), which only TriggerAny accepts.
//
// Only gains count, not the balance: a release that trades a preferred format for a better
// source improved on source, and whether that's worth a re-download is the trigger's call.
func improvementKey(cand, cur Evaluation) RankKey {
	cr, kr := cand.Candidate.Release, cur.Candidate.Release
	switch {
	case resRank[cr.Resolution] > resRank[kr.Resolution]:
		return KeyResolution
	case cand.TargetScore > cur.TargetScore:
		return KeyPreferences
	case cand.CustomScore > cur.CustomScore:
		return KeyCustom
	case sourceBonus[cr.Source] > sourceBonus[kr.Source]:
		return KeySource
	case isProper(cr) && !isProper(kr):
		return KeyProper
	}
	return ""
}

// triggerAllows reports whether a gain of kind k is worth replacing a file for under a
// profile's trigger. TriggerAny takes every gain, keyed or not, exactly as upgrades
// behaved before triggers existed.
func triggerAllows(trigger string, k RankKey) bool {
	switch NormalizeTrigger(trigger) {
	case TriggerSource:
		return k == KeyResolution || k == KeyPreferences || k == KeyCustom || k == KeySource
	case TriggerFormat:
		return k == KeyResolution || k == KeyPreferences || k == KeyCustom
	case TriggerResolution:
		return k == KeyResolution
	}
	return true
}

// sameGroupProper reports whether cand is a PROPER or REPACK of the file on disk: the same
// group, resolution and source, re-released to fix it. That is a fix, not an upgrade, and
// is taken whatever the trigger says.
func sameGroupProper(cand, cur Evaluation) bool {
	cr, kr := cand.Candidate.Release, cur.Candidate.Release
	return isProper(cr) && !isProper(kr) &&
		cr.Group != "" && strings.EqualFold(cr.Group, kr.Group) &&
		cr.Resolution == kr.Resolution && cr.Source == kr.Source
}

func isProper(r parser.Release) bool { return r.Proper || r.Repack }

// isTargetFormat reports whether a format is one the target file sets (codec, HDR, audio).
func isTargetFormat(name string) bool {
	for _, tf := range targetFormats {
		if tf.format == name {
			return true
		}
	}
	return false
}

// qualityGain is the "it scores higher" half of an upgrade decision, shared by the upgrade
// sweep (UpgradeCandidate) and the import gate (IsQualityUpgrade) so they can't disagree:
// cand must outscore cur, and the gain must be one the profile's trigger counts — or be a
// same-group PROPER of the file, which is always taken.
func qualityGain(trigger string, cand, cur Evaluation) bool {
	if cand.Total <= cur.Total {
		return false
	}
	return triggerAllows(trigger, improvementKey(cand, cur)) || sameGroupProper(cand, cur)
}
