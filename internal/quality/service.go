package quality

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	"github.com/tristenlammi/arrmada/internal/parser"
	"github.com/tristenlammi/arrmada/internal/settings"
	"github.com/tristenlammi/arrmada/internal/store"
)

// Service is the quality subsystem's application logic: it resolves a profile
// reference (a preset key like "4k-hdr" or a custom ref like "custom:12") into a
// runnable engine+profile, and manages user-defined profiles for the builder.
type Service struct {
	repo     *Repo
	settings *settings.Service // holds each media type's default profile
	// specs, when set, answer GetStored and Resolve for their ids instead of the database:
	// how Impact runs the upgrade decisions against a profile as edited, before it's saved
	// (see withSpecs). nil on the app's service.
	specs map[int64]StoredProfile
	// ownerLangs reads the audio languages the household keeps (Convert's), for the
	// Unwanted pack's foreign-only item; nil reads as English.
	ownerLangs func(ctx context.Context) []string
	// log reports ranking v2's disagreements in shadow mode (shadow.go); nil is silent.
	log *slog.Logger
	// shadow buffers shadow mode's day counts. A pointer, so withSpecs' copies share it.
	shadow *shadowStats
}

// SetOwnerLanguages tells the service where the household's audio languages come from
// (Convert's "keep audio" languages). A release in one of them is never foreign-only.
func (s *Service) SetOwnerLanguages(fn func(ctx context.Context) []string) { s.ownerLangs = fn }

// ownerLanguages is the household's audio languages, English when none are set.
func (s *Service) ownerLanguages(ctx context.Context) []string {
	if s.ownerLangs != nil {
		if l := s.ownerLangs(ctx); len(l) > 0 {
			return l
		}
	}
	return []string{"en"}
}

// profileOf is sp's runtime profile with the household's languages attached.
func (s *Service) profileOf(ctx context.Context, sp StoredProfile) Profile {
	p := sp.ToProfile()
	p.OwnerLanguages = s.ownerLanguages(ctx)
	return p
}

// NewService wires the quality service over the database with a settings service of its
// own. The app passes its shared one through NewServiceWith, so a default saved here is
// what every other reader sees; this form is for tests and tools that only need quality.
func NewService(db store.Pool) *Service { return NewServiceWith(db, settings.NewService(db)) }

// NewServiceWith wires the quality service over the database and the app's settings.
func NewServiceWith(db store.Pool, set *settings.Service) *Service {
	return &Service{repo: NewRepo(db), settings: set, shadow: &shadowStats{}}
}

// defaultKey is the settings key holding a media type's default profile.
func defaultKey(mediaType string) string { return "default_profile:" + mediaType }

// ProfileInfo is a lightweight listing entry (presets + custom profiles).
type ProfileInfo struct {
	Key       string `json:"key"` // "4k-hdr" or "custom:12"
	Name      string `json:"name"`
	MediaType string `json:"media_type"`
	BuiltIn   bool   `json:"built_in"`
	IsDefault bool   `json:"is_default"`
	Summary   string `json:"summary"`
	// Kept is how many files on the profile are held out of upgrades ("keep existing
	// files"), so a forgotten hold stays visible on the profile's card. Video only.
	Kept int `json:"kept,omitempty"`
}

// DefaultProfile returns the profile reference used when adding media of this
// type. It honors the saved default when it still exists, otherwise falls back
// to the first available profile (empty string if the user has deleted them all).
// It is always a real profile: "n/a" is a marker on scanned titles, not a default, and
// one saved before SetDefaultProfile refused it is passed over.
func (s *Service) DefaultProfile(ctx context.Context, mediaType string) string {
	if v := s.settings.Get(ctx, defaultKey(mediaType), ""); v != "" && v != "n/a" && s.Known(ctx, v) {
		return v
	}
	if custom, err := s.repo.List(ctx, mediaType); err == nil && len(custom) > 0 {
		return "custom:" + strconv.FormatInt(custom[0].ID, 10)
	}
	return ""
}

// SetDefaultProfile records the default profile for a media type.
func (s *Service) SetDefaultProfile(ctx context.Context, mediaType, ref string) error {
	if ref == "n/a" || !s.Known(ctx, ref) {
		return errNotKnown
	}
	return s.settings.Set(ctx, defaultKey(mediaType), ref)
}

var errNotKnown = errorString("unknown quality profile")

type errorString string

func (e errorString) Error() string { return string(e) }

// Effective is the profile a title actually runs under. Its own profile when that still
// exists; otherwise — "n/a" on a library-scanned title, nothing at all, or a profile
// that was deleted — the default profile of its media type. Every acquisition path goes
// through this so one title can't be judged three different ways depending on which
// code path looks at it. Only with no profile of that media at all does the ref come
// back unchanged, and Resolve then uses the fallback.
func (s *Service) Effective(ctx context.Context, ref, media string) string {
	// "n/a" first: Known accepts it as a valid marker, but it names no real profile.
	if ref != "n/a" && s.Known(ctx, ref) {
		return ref
	}
	if def := s.DefaultProfile(ctx, media); def != "" {
		return def
	}
	return ref
}

// Resolve turns a profile reference into a runnable (Profile, Engine). An unknown
// reference gets the hidden fallback profile; callers resolve through Effective first,
// so in practice the fallback only runs when no profile of the media type exists. The
// engine ranks in the mode "quality_ranking" says.
func (s *Service) Resolve(ctx context.Context, ref string) (Profile, *Engine) {
	p, e := s.resolve(ctx, ref)
	e.Ranking = s.RankingMode(ctx)
	return s.withNetwork(ctx, p), e
}

func (s *Service) resolve(ctx context.Context, ref string) (Profile, *Engine) {
	if id, ok := customID(ref); ok {
		if sp, ok := s.specs[id]; ok {
			return s.profileOf(ctx, sp), sp.Engine()
		}
		if sp, err := s.repo.Get(ctx, id); err == nil {
			return s.profileOf(ctx, sp), sp.Engine()
		}
	}
	p := fallbackProfile()
	p.OwnerLanguages = s.ownerLanguages(ctx)
	return p, NewDefaultEngine()
}

// AllowedResolutions returns the resolution strings a profile allows (empty =
// any). Used to route multi-version imports to the right track.
func (s *Service) AllowedResolutions(ctx context.Context, ref string) []string {
	p, _ := s.Resolve(ctx, ref)
	out := make([]string, 0, len(p.AllowedResolutions))
	for _, r := range p.AllowedResolutions {
		out = append(out, string(r))
	}
	return out
}

// Stall-timeout values a profile may hold. 0 used to mean "off", which left every default
// install with dead torrents sitting at 0% forever; it now means "use the global default"
// (Settings → Downloads), and turning fail-over off for a profile is an explicit -1.
const (
	StallOff        = -1
	StallMaxMinutes = 7 * 24 * 60 // a week; anything longer is indistinguishable from off
)

// ValidStallMinutes reports whether n is an acceptable profile stall timeout.
func ValidStallMinutes(n int) bool { return n >= StallOff && n <= StallMaxMinutes }

// StallMinutes returns a profile's stall timeout in minutes: >0 a custom window, StallOff
// for off, 0 for "use the global default". An unknown profile also reads 0, so it gets
// the default rather than silently never failing over.
func (s *Service) StallMinutes(ctx context.Context, ref string) int {
	sp, err := s.GetStored(ctx, ref)
	if err != nil {
		return 0
	}
	return sp.StallMinutes
}

// Known reports whether a profile reference resolves to something real. "n/a"
// is accepted as a valid "no profile" marker (used by library-scanned files).
func (s *Service) Known(ctx context.Context, ref string) bool {
	if ref == "n/a" {
		return true
	}
	if id, ok := customID(ref); ok {
		_, err := s.repo.Get(ctx, id)
		return err == nil
	}
	return false
}

// Decide resolves the profile reference and ranks the candidates in the ranking mode in
// effect, and runs the other mode in the shadow: the pick is the mode in effect's, and the
// other's choice rides along in Decision.Shadow and, where the two differ, in the shadow
// record.
func (s *Service) Decide(ctx context.Context, ref string, cands []Candidate) Decision {
	p, e := s.Resolve(ctx, ref)
	d := e.Decide(p, cands)
	d.Shadow = shadowOf(p, d)
	s.recordShadow(ctx, ref, d)
	return d
}

// decideUnsaved ranks for a profile that may not be saved yet (the builder): the ranking
// mode in effect, and the other mode's shadow pick shown alongside — but never recorded,
// since these are samples and edits, not the library's real choices.
func (s *Service) decideUnsaved(sp StoredProfile, cands []Candidate) Decision {
	ctx := context.Background()
	normalize(&sp)
	p, e := s.withNetwork(ctx, s.profileOf(ctx, sp)), sp.Engine()
	e.Ranking = s.RankingMode(ctx)
	d := e.Decide(p, cands)
	d.Shadow = shadowOf(p, d)
	return d
}

// UpgradeCandidate returns the best release that would upgrade the current file
// under the profile, or (zero, false) if none qualifies. current.Release is the
// release the on-disk file was imported from (scored to represent the file), laid
// over with its probed facts when current.Facts is set. Rules:
//   - the profile must have upgrades enabled;
//   - we must know what the current file is (empty Release → skip, so we never churn
//     on a guess);
//   - a file that already meets the profile's target is done (JudgeFile);
//   - a candidate never drops resolution (that's a downgrade, handled elsewhere);
//   - it wins if it is better (resolution, formats, source…) in a way the profile's
//     upgrade trigger counts (a same-group PROPER always counts) — by the summed score
//     under classic ranking (rank.go), by the ordered keys under v2 (upgradev2.go) — OR,
//     when upgrade_min_percent > 0, it's at least that much better on bitrate and no
//     worse on quality.
//
// current.RuntimeMin is the content length (movie/episode minutes), needed to turn
// sizes into bitrates; 0 disables the bitrate-based upgrade (quality-only still applies).
func (s *Service) UpgradeCandidate(ctx context.Context, ref string, current CurrentFile, cands []Candidate) (Candidate, bool) {
	c, _, ok := s.UpgradePick(ctx, ref, current, cands)
	return c, ok
}

// UpgradePick is UpgradeCandidate with the key that decided it, so an upgrade grab records
// what kind of gain it was: the improvement the profile's trigger accepted (resolution,
// preferences, custom, source, proper), KeyAvoid for leaving a low-quality group's file
// (under v2 also a file the profile no longer accepts), KeySize for a gain only the size
// lean scores (classic ranking only), or KeyBitrate for the bitrate step.
func (s *Service) UpgradePick(ctx context.Context, ref string, current CurrentFile, cands []Candidate) (Candidate, RankKey, bool) {
	currentRelease, runtimeMin := current.Release, current.RuntimeMin
	sp, err := s.GetStored(ctx, ref)
	if err != nil || !sp.UpgradesEnabled || strings.TrimSpace(currentRelease) == "" {
		return Candidate{}, "", false
	}
	// The file already is the profile's target — "upgrade until it fits" stops here, even
	// if some release would technically score higher.
	if sp.JudgeFile(current).TargetMet {
		return Candidate{}, "", false
	}
	p, e := s.Resolve(ctx, ref)
	// What a candidate has to beat: the file as it is, and for a converted file also what
	// it was before Convert shrank it (current.OrigRelease / OrigSizeGB).
	bar := current.upgradeBar(p, e)

	d := e.Decide(p, cands)
	for _, ev := range d.Eligible { // sorted best-first, in the ranking mode in effect
		// Never auto-upgrade INTO a format you avoid. Keeping the current file is always the
		// preferable alternative, so "Avoid Dolby Vision" also means the upgrader won't swap
		// a clean file for a DV one — matching how the picker now treats avoided releases.
		if bar.intoAvoid(ev) {
			continue
		}
		if resRank[ev.Candidate.Release.Resolution] < bar.resRank {
			continue // never drop resolution — that's a downgrade, not an upgrade
		}
		if bar.excludes(ev.Candidate.Name) {
			// The release we already have, or the one this file was converted from.
			// Grabbing that would undo the conversion and loop forever.
			continue
		}
		// Better than the file (and its original) in a way the profile's "Replace for"
		// counts — the one verdict the import gate also applies (upgradeBar.gain).
		if key, ok := bar.gain(sp.UpgradeTrigger, ev); ok {
			return ev.Candidate, key, true
		}
		// Same helper the import gate uses, so the two can't drift apart again — the
		// searcher deciding a release is worth grabbing and the importer then refusing to
		// place it is exactly the bug this shares its logic to prevent. It also brings the
		// margin floors and codec normalization to the grab side, which compared raw
		// bitrates and so over-valued a bloated older-codec encode.
		if bar.noWorse(ev) && s.IsBitrateUpgrade(ctx, ref,
			Encode{SizeGB: sizeOver(ev.Candidate, runtimeMin), Codec: ev.Candidate.Release.Codec},
			bar.encode, runtimeMin) {
			return ev.Candidate, KeyBitrate, true
		}
	}
	return Candidate{}, "", false
}

// IsQualityUpgrade reports whether a candidate is a quality upgrade of the current file
// under this profile — the OTHER half of what UpgradeCandidate accepts (upgradeBar.gain,
// in the ranking mode in effect), exposed so the import gate can honour the same verdict.
//
// The two used to disagree on exactly this. The searcher took a release on a quality gain
// that wasn't a resolution gain (a WEB-DL with Atmos over a plain WEB rip, say), the
// importer looked only at resolution and bitrate, and the finished download was refused on
// arrival — a wasted grab that then sat in the client seeding, blocking the show's sweeps
// for as long as it stayed there.
//
// Avoided formats are refused the same way UpgradeCandidate refuses them: keeping the
// current file is always preferable to swapping into something the profile avoids.
// The current side is read like UpgradeCandidate reads it: probed facts over the name.
func (s *Service) IsQualityUpgrade(ctx context.Context, ref, candRelease string, candSizeGB float64, current CurrentFile) bool {
	currentRelease := current.Release
	if strings.TrimSpace(candRelease) == "" || strings.TrimSpace(currentRelease) == "" {
		return false // no baseline to beat — the caller's other gates decide
	}
	// Same gate as IsBitrateUpgrade and UpgradeCandidate. With upgrades off the searcher
	// would never have chosen this release, so the importer replacing a file on its own
	// initiative would break the one promise that setting makes: the library stops churning.
	sp, err := s.GetStored(ctx, ref)
	if err != nil || !sp.UpgradesEnabled {
		return false
	}
	p, e := s.Resolve(ctx, ref)
	bar := current.upgradeBar(p, e)
	// The release a converted file came from is never an upgrade of it, whatever the
	// codec stamp makes the two score.
	if bar.excludes(candRelease) {
		return false
	}
	// Seeders are irrelevant here and unknown for a file on disk, so both sides get the
	// same large value rather than letting a seeder term skew the comparison.
	cand := NewCandidate(candRelease, candSizeGB, 1_000_000)
	// The title's language reaches the language rules only, as for the file it'd replace
	// (CurrentFile.candidate): the gate has never run the foreign-only check.
	cand.ruleLang = strings.TrimSpace(current.OrigLanguage)
	ev := e.Evaluate(p, cand)
	if bar.intoAvoid(ev) {
		return false
	}
	// The same verdict the searcher applied (upgradeBar.gain, in the ranking mode in
	// effect), so a release it grabbed as an upgrade is placed, and one it wouldn't have
	// grabbed isn't placed as one either.
	_, ok := bar.gain(sp.UpgradeTrigger, ev)
	return ok
}

// convertedFrom reports whether cand looks like the release the current file was
// converted from: the current file is a codec Convert writes (AV1 or x265 — curCodec,
// probed when known), and cand is the same release name with a different codec. It is
// deliberately no wider than that, so an x264 file can still be upgraded to the same
// group's x265 release.
func convertedFrom(cand, current string, curCodec parser.Codec) bool {
	if curCodec != parser.CodecAV1 && curCodec != parser.CodecX265 {
		return false
	}
	return parser.Parse(cand).Codec != curCodec &&
		(parser.WithoutCodec(cand) == parser.WithoutCodec(current) || sameRelease(cand, current, false))
}

// Encode is one side of a bitrate comparison: how big it is and what codec it used.
type Encode struct {
	SizeGB float64
	Codec  parser.Codec
}

// Guard rails on the upgrade threshold, so a small or careless setting can't churn a
// library through an endless ladder of barely-better files.
const (
	// MinUpgradePercent is the smallest improvement that can count, whatever a profile
	// asks for. Below this, "better" is within the noise of how two groups encoded the
	// same source, and acting on it means re-downloading a library for nothing.
	MinUpgradePercent = 20.0
	// MinUpgradeMarginMbps also applies in absolute terms. A percentage alone is a tiny
	// number at low bitrates — 20% of a 0.6 Mbps file is 0.12 Mbps — so this stops churn
	// at the bottom of the range where the percentage can't.
	MinUpgradeMarginMbps = 0.5
)

// IsBitrateUpgrade reports whether a candidate beats the current file by enough to be
// worth replacing it, at equal resolution.
//
// This is the same test Upgrade() applies when deciding to GRAB a better release, exposed
// so the IMPORT gate can apply it too. They used to disagree: the searcher would find a
// same-resolution higher-bitrate release, grab it, download it — and the importer would
// then refuse to place it because resolution hadn't increased.
//
// Comparison is in H.264-equivalent terms. Raw bitrate is a poor measure across codecs —
// see codecEfficiency — and comparing it directly would rate a bloated x264 encode above a
// better x265 one, then swap a good file for a worse one.
//
// Returns false when the profile doesn't enable upgrades, sets no margin, or when the
// runtime/sizes needed to compute a bitrate are missing.
func (s *Service) IsBitrateUpgrade(ctx context.Context, ref string, cand, current Encode, runtimeMin int) bool {
	sp, err := s.GetStored(ctx, ref)
	if err != nil || !sp.UpgradesEnabled || sp.UpgradeMinPercent <= 0 {
		return false
	}
	if runtimeMin <= 0 || cand.SizeGB <= 0 || current.SizeGB <= 0 {
		return false // can't express either as a bitrate — don't guess
	}
	candBr := BitrateMbps(cand.SizeGB, runtimeMin) * codecEfficiency(cand.Codec)
	curBr := BitrateMbps(current.SizeGB, runtimeMin) * codecEfficiency(current.Codec)

	pct := sp.UpgradeMinPercent
	if pct < MinUpgradePercent {
		pct = MinUpgradePercent
	}
	return candBr >= curBr*(1+pct/100) && candBr >= curBr+MinUpgradeMarginMbps
}

// WouldReject reports whether the profile would reject the given release — used
// to tell if switching a movie to this profile is a downgrade (its current file
// no longer fits). current.Release is the file's source release name, with its
// probed facts laid over it when known. current.RuntimeMin is the content length in
// minutes, needed to turn the size into a bitrate so the profile's bitrate ceiling can
// apply; 0 skips the ceiling check.
func (s *Service) WouldReject(ctx context.Context, ref string, current CurrentFile) bool {
	_, rejected := s.WouldRejectReason(ctx, ref, current)
	return rejected
}

// Rejection is why a profile refuses the file on disk.
type Rejection struct {
	Reason string // the profile's reject reason ("Not in profile — 1080p")
	// Ceiling is the profile ceiling the file is above ("40 Mb/s", "BluRay"), "" when it
	// was refused for something else. Above a ceiling, a smaller release fixes it; for
	// anything else the file is simply not what the profile asks for.
	Ceiling string
}

// WouldRejectReason is WouldReject with the reason, so a profile change can say whether
// the file is too big for the new profile or just a different kind of file.
func (s *Service) WouldRejectReason(ctx context.Context, ref string, current CurrentFile) (Rejection, bool) {
	if strings.TrimSpace(current.Release) == "" {
		return Rejection{}, false
	}
	v := s.JudgeFile(ctx, ref, current)
	if v.Eligible {
		return Rejection{}, false
	}
	return Rejection{Reason: v.Current.RejectReason, Ceiling: v.Current.ceiling}, true
}

// List returns the user's quality profiles for a media type. Every profile is a
// custom, editable row — the app just ships with a couple pre-loaded.
func (s *Service) List(ctx context.Context, mediaType string) ([]ProfileInfo, error) {
	def := s.DefaultProfile(ctx, mediaType)
	custom, err := s.repo.List(ctx, mediaType)
	if err != nil {
		return nil, err
	}
	kept := s.keptByProfile(ctx, mediaType)
	var out []ProfileInfo
	for _, sp := range custom {
		key := "custom:" + strconv.FormatInt(sp.ID, 10)
		out = append(out, ProfileInfo{Key: key, Name: sp.Name, MediaType: sp.MediaType, BuiltIn: false, IsDefault: key == def, Summary: sp.Summary(), Kept: kept[key]})
	}
	return out, nil
}

// keptByProfile counts held files per profile each title actually runs under (Effective),
// so a title on "n/a" or a deleted profile counts toward the default. Best effort: a failed
// count shows no number rather than failing the list.
func (s *Service) keptByProfile(ctx context.Context, media string) map[string]int {
	out := map[string]int{}
	if media != MediaMovie && media != MediaSeries {
		return out
	}
	byRef, err := s.repo.heldByRef(ctx, media)
	if err != nil {
		return out
	}
	for ref, n := range byRef {
		out[s.Effective(ctx, ref, media)] += n
	}
	return out
}

// ListStored returns all custom profiles for a media type (with their format scores).
func (s *Service) ListStored(ctx context.Context, mediaType string) ([]StoredProfile, error) {
	return s.repo.List(ctx, mediaType)
}

// GetStored returns an editable profile for a custom reference.
func (s *Service) GetStored(ctx context.Context, ref string) (StoredProfile, error) {
	if id, ok := customID(ref); ok {
		if sp, ok := s.specs[id]; ok {
			return sp, nil
		}
		return s.repo.Get(ctx, id)
	}
	return StoredProfile{}, ErrNotFound
}

// AllowsUpgrades reports whether a profile has upgrades turned on — used to skip the upgrade
// indexer-search entirely for movies whose profile doesn't want them.
func (s *Service) AllowsUpgrades(ctx context.Context, ref string) bool {
	sp, err := s.GetStored(ctx, ref)
	return err == nil && sp.UpgradesEnabled
}

// AtCeiling reports whether a file has run out of room under its profile, so no release
// the profile would accept can clear the upgrade threshold and searching for one again is
// spent traffic.
//
// It falls straight out of the two numbers the profile already declares. Upgrades move in
// percentage steps (UpgradeMinPercent, floored by MinUpgradePercent), and BitrateCapMbps
// rejects anything above the ceiling — so an upgrade has to land in the band
//
//	current × (1 + pct/100)  …  cap
//
// and once the bottom of that band is above the cap, the band is empty. A 25 Mbps file
// under a 30 Mbps ceiling with a 25% step would need 31.25 Mbps, which the profile would
// reject: nothing can win, this sweep or any other.
//
// Both sides are H.264-equivalent, the same units the cap and IsBitrateUpgrade use, so an
// x265 file isn't judged against a raw number that means something different for it.
//
// Resolution is still checked, because UpgradeCandidate also upgrades on a pure quality
// gain: a 720p file can be maxing out the bitrate ceiling and still have a 1080p release
// waiting for it. Only a file that is BOTH at the best resolution the profile allows AND
// out of bitrate headroom is genuinely finished.
//
// Deliberately not part of the test: source and format scores. Those can technically still
// improve (a BluRay over a WEB-DL at the same resolution), and giving up that churn is the
// point of a ceiling — "it meets the profile" should mean the searching stops.
//
// The file is judged by JudgeFile: probed facts (current.Facts) when there are any, its
// release name otherwise.
func (s *Service) AtCeiling(ctx context.Context, ref string, current CurrentFile) bool {
	if strings.TrimSpace(current.Release) == "" {
		return false // nothing recorded to judge — let the normal path decide
	}
	sp, err := s.GetStored(ctx, ref)
	if err != nil {
		return false
	}
	return sp.JudgeFile(current).AtCeiling
}

// Create, Update, Delete manage user profiles. Create and Update refuse custom formats
// that don't hold together (see Validate) with an *InvalidProfileError.
func (s *Service) Create(ctx context.Context, sp StoredProfile) (StoredProfile, error) {
	if err := checkFormats(sp); err != nil {
		return StoredProfile{}, err
	}
	normalize(&sp)
	return s.repo.Create(ctx, sp)
}

func (s *Service) Update(ctx context.Context, id int64, sp StoredProfile) error {
	if err := checkFormats(sp); err != nil {
		return err
	}
	normalize(&sp)
	return s.repo.Update(ctx, id, sp)
}

// checkFormats is Validate as the error a save returns.
func checkFormats(sp StoredProfile) error {
	if problems := ValidateFormats(sp.CustomFormats); len(problems) > 0 {
		return &InvalidProfileError{Problems: problems}
	}
	return nil
}

// DecideSpec runs the engine for a profile that may not be saved yet — the builder's "test
// on a real title" uses it with your unsaved edits.
func (s *Service) DecideSpec(sp StoredProfile, cands []Candidate) Decision {
	return s.decideUnsaved(sp, cands)
}

// FormatCheck is one format tested against a release name: whether the name matches it, and
// how the profile treats it.
type FormatCheck struct {
	Name     string `json:"name"`
	Matched  bool   `json:"matched"`
	Score    int    `json:"score"`              // the profile's score for it (0 = no opinion)
	Required bool   `json:"required,omitempty"` // a Must: never grabbed without it
	Custom   bool   `json:"custom,omitempty"`   // one of the profile's own custom formats
}

// Explanation is one release name judged by a profile: what the name parses to, the
// verdict, and every format the profile knows with whether the name has it.
type Explanation struct {
	Release    parser.Release `json:"release"`
	Evaluation Evaluation     `json:"evaluation"`
	Formats    []FormatCheck  `json:"formats"`
}

// Explain judges one release name against a profile that may not be saved yet — the
// builder's "Test a release name". sizeGB and runtimeMin are optional (0 = unknown); with
// both, the bitrate window applies just as it does to a real search.
//
// A pasted name has no seeders, so it's judged as though it had the profile's minimum:
// the box answers "would this release fit?", not "is this torrent alive?".
func (s *Service) Explain(sp StoredProfile, name string, sizeGB float64, runtimeMin int) Explanation {
	normalize(&sp)
	p, e := s.profileOf(context.Background(), sp), sp.Engine()
	c := NewCandidate(name, max(sizeGB, 0), max(p.MinSeeders, 1)).WithRuntime(max(runtimeMin, 0))
	// No title, so no original language: an untagged name can't be judged on the language
	// rules (nothing refused), and a MULTi that doesn't name a Must says why it's refused.
	c.tested = true
	out := Explanation{Release: c.Release, Evaluation: e.Evaluate(p, c)}
	custom := map[string]bool{}
	for _, f := range sp.CustomFormats {
		custom[f.Name] = true
	}
	seen := map[string]bool{}
	for _, f := range append(DefaultFormats(), sp.CustomFormats...) {
		if seen[f.Name] {
			continue // a custom format named like a built-in replaces it in the engine
		}
		seen[f.Name] = true
		ef := e.formats[f.Name]
		out.Formats = append(out.Formats, FormatCheck{
			Name:     f.Name,
			Matched:  e.match(ef, c.Name, c.Release), // with the name, so release_title formats count
			Score:    p.FormatScores[f.Name],
			Required: containsStr(p.Required, f.Name),
			Custom:   custom[f.Name],
		})
	}
	return out
}

// Delete removes a profile and moves every title, pending request and in-flight grab
// on it to moveTo, returning the counts and the ref they moved to. An empty moveTo means
// the media type's default, or the first other profile when the default is the one
// going. The last profile of a media type can't be deleted: its titles would have
// nowhere real to go.
func (s *Service) Delete(ctx context.Context, id int64, moveTo string) (Reassigned, string, error) {
	sp, err := s.repo.Get(ctx, id)
	if err != nil {
		return Reassigned{}, "", err
	}
	others, err := s.repo.List(ctx, sp.MediaType)
	if err != nil {
		return Reassigned{}, "", err
	}
	first := ""
	for _, o := range others {
		if o.ID != id {
			first = "custom:" + strconv.FormatInt(o.ID, 10)
			break
		}
	}
	if first == "" {
		return Reassigned{}, "", ErrLastProfile
	}
	if moveTo == "" {
		moveTo = s.DefaultProfile(ctx, sp.MediaType)
		if moveTo == "" || moveTo == "custom:"+strconv.FormatInt(id, 10) {
			moveTo = first
		}
	}
	moved, err := s.repo.DeleteAndReassign(ctx, id, moveTo)
	if err != nil {
		return Reassigned{}, "", err
	}
	// Deleting the default hands the role to the target, so new titles land where the
	// old ones went rather than on whichever profile happens to sort first. If this save
	// fails the profile is gone all the same and DefaultProfile already falls back to a
	// real one; the error says the default didn't move.
	if s.settings.Get(ctx, defaultKey(sp.MediaType), "") == "custom:"+strconv.FormatInt(id, 10) {
		if err := s.settings.Set(ctx, defaultKey(sp.MediaType), moveTo); err != nil {
			return moved, moveTo, fmt.Errorf("the profile was deleted, but %s couldn't be made the default: %w", moveTo, err)
		}
	}
	return moved, moveTo, nil
}

// RepairDanglingRefs points every title, request and grab whose profile no longer
// exists at its media type's default, returning the counts per table. Profiles deleted
// before deletes reassigned their titles left these behind; Effective already runs them
// on the default, and this makes the stored ref say so. Only refs naming a missing
// profile are touched — "n/a", "" and every real ref stay as they are — so it is cheap
// and safe to run on every boot.
func (s *Service) RepairDanglingRefs(ctx context.Context) (map[string]int, error) {
	out := map[string]int{}
	repair := func(key, table, media, where string, args ...any) error {
		def := s.DefaultProfile(ctx, media)
		if def == "" {
			return nil // no profile of this media at all: nothing better to point at
		}
		n, err := s.repo.repointDangling(ctx, table, where, def, args...)
		if err != nil {
			return err
		}
		if n > 0 {
			out[key] += n
		}
		return nil
	}
	fixed := []struct{ key, table, media string }{
		{"movies", "movies", MediaMovie},
		{"movie_versions", "movie_versions", MediaMovie},
		{"series", "series", MediaSeries},
		{"books", "books", MediaBook},
		{"artists", "artists", MediaMusic},
	}
	for _, f := range fixed {
		if err := repair(f.key, f.table, f.media, ""); err != nil {
			return out, err
		}
	}
	// Requests and grabs carry their own media type. Grabs written before series
	// existed have '' for a film.
	for _, media := range []string{MediaMovie, MediaSeries, MediaBook, MediaMusic} {
		if err := repair("requests", "requests", media, "media_type = ?", media); err != nil {
			return out, err
		}
		where := "media_type = ?"
		if media == MediaMovie {
			where = "media_type IN (?, '')"
		}
		if err := repair("grabs", "grabs", media, where, media); err != nil {
			return out, err
		}
	}
	return out, nil
}

// Preview scores a (possibly unsaved) profile over the built-in sample set for its media
// type (Samples) — the live feedback behind the builder. A series profile is judged on
// episode-length samples, so its bitrate window means what it will mean on a real grab.
func (s *Service) Preview(sp StoredProfile) Decision {
	media := sp.MediaType
	if media == "" {
		media = MediaMovie
	}
	cands, _ := Samples(media)
	// decideUnsaved normalizes: an edited target takes effect in the preview before it's saved.
	return s.decideUnsaved(sp, cands)
}

func normalize(sp *StoredProfile) {
	if sp.MediaType == "" {
		sp.MediaType = MediaMovie
	}
	if sp.FormatScores == nil {
		sp.FormatScores = map[string]int{}
	}
	// A video profile's target is the source of truth for the formats it covers and for
	// the bitrate ceiling: write it into what the engine reads. A profile sent with no
	// target at all (rather than an empty one, which is how the builder clears it) is
	// read as one first, so a caller that only knows scores keeps them.
	if sp.MediaType == MediaMovie || sp.MediaType == MediaSeries {
		if sp.Ideal == nil {
			sp.Migrate()
		}
		sp.Compile()
		sp.Unwanted = sp.Unwanted.normalized()
	} else {
		sp.Unwanted = Unwanted{} // the pack is video-only
		// Like the Audio language row it qualifies.
		sp.MultiCountsLanguage = false
	}
}

// customID parses "custom:<n>" into an id.
func customID(ref string) (int64, bool) {
	if !strings.HasPrefix(ref, "custom:") {
		return 0, false
	}
	id, err := strconv.ParseInt(strings.TrimPrefix(ref, "custom:"), 10, 64)
	if err != nil {
		return 0, false
	}
	return id, true
}
