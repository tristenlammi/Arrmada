package quality

import (
	"context"
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
func (c CurrentFile) candidate() Candidate {
	return Candidate{Name: c.Release, Release: c.release(), SizeGB: c.SizeGB, Seeders: 1_000_000}
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
	v.Current = e.Evaluate(p, cur.candidate().WithRuntime(cur.RuntimeMin))
	v.Eligible = v.Current.Eligible
	if sp != nil {
		v.TargetMet = sp.TargetMet(v.Facts)
		v.AtCeiling = v.TargetMet || atCeiling(*sp, p, cur)
	}
	return v
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
