package quality

import (
	"context"
	"database/sql"
	"strconv"
	"strings"

	"github.com/tristenlammi/arrmada/internal/parser"
)

// Service is the quality subsystem's application logic: it resolves a profile
// reference (a preset key like "4k-hdr" or a custom ref like "custom:12") into a
// runnable engine+profile, and manages user-defined profiles for the builder.
type Service struct {
	repo *Repo
}

// NewService wires the quality service over the database.
func NewService(db *sql.DB) *Service { return &Service{repo: NewRepo(db)} }

// ProfileInfo is a lightweight listing entry (presets + custom profiles).
type ProfileInfo struct {
	Key       string `json:"key"` // "4k-hdr" or "custom:12"
	Name      string `json:"name"`
	MediaType string `json:"media_type"`
	BuiltIn   bool   `json:"built_in"`
	IsDefault bool   `json:"is_default"`
	Summary   string `json:"summary"`
}

// DefaultProfile returns the profile reference used when adding media of this
// type. It honors the saved default when it still exists, otherwise falls back
// to the first available profile (empty string if the user has deleted them all).
// It is always a real profile: "n/a" is a marker on scanned titles, not a default, and
// one saved before SetDefaultProfile refused it is passed over.
func (s *Service) DefaultProfile(ctx context.Context, mediaType string) string {
	v, err := s.repo.getSetting(ctx, "default_profile:"+mediaType)
	if err == nil && v != "" && v != "n/a" && s.Known(ctx, v) {
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
	return s.repo.setSetting(ctx, "default_profile:"+mediaType, ref)
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
// so in practice the fallback only runs when no profile of the media type exists.
func (s *Service) Resolve(ctx context.Context, ref string) (Profile, *Engine) {
	if id, ok := customID(ref); ok {
		if sp, err := s.repo.Get(ctx, id); err == nil {
			return sp.ToProfile(), sp.Engine()
		}
	}
	return fallbackProfile(), NewDefaultEngine()
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

// Decide resolves the profile reference and ranks the candidates.
func (s *Service) Decide(ctx context.Context, ref string, cands []Candidate) Decision {
	p, e := s.Resolve(ctx, ref)
	return e.Decide(p, cands)
}

// UpgradeCandidate returns the best release that would upgrade the current file
// under the profile, or (zero, false) if none qualifies. currentRelease is the
// release the on-disk file was imported from (scored to represent the file);
// currentSizeGB is its size. Rules:
//   - the profile must have upgrades enabled;
//   - we must know what the current file is (empty currentRelease → skip, so we
//     never churn on a guess);
//   - a candidate never drops resolution (that's a downgrade, handled elsewhere);
//   - it wins if it scores strictly higher (better resolution/formats), OR — when
//     upgrade_min_percent > 0 — it's at least that much better on bitrate and no worse
//     on quality.
//
// runtimeMin is the content length (movie/episode minutes), needed to turn sizes
// into bitrates; 0 disables the bitrate-based upgrade (quality-only still applies).
func (s *Service) UpgradeCandidate(ctx context.Context, ref, currentRelease string, currentSizeGB float64, runtimeMin int, cands []Candidate) (Candidate, bool) {
	sp, err := s.GetStored(ctx, ref)
	if err != nil || !sp.UpgradesEnabled || strings.TrimSpace(currentRelease) == "" {
		return Candidate{}, false
	}
	// The file already is the profile's target — "upgrade until it fits" stops here, even
	// if some release would technically score higher.
	if sp.TargetMet(ReleaseFacts(parser.Parse(currentRelease), BitrateMbps(currentSizeGB, runtimeMin))) {
		return Candidate{}, false
	}
	p, e := s.Resolve(ctx, ref)
	curCand := NewCandidate(currentRelease, currentSizeGB, 1_000_000)
	cur := e.Evaluate(p, curCand)
	curResRank := resRank[curCand.Release.Resolution]
	curKey := strings.ToLower(strings.TrimSpace(currentRelease))

	d := e.Decide(p, cands)
	for _, ev := range d.Eligible { // sorted best-first
		// Never auto-upgrade INTO a format you avoid. Keeping the current file is always the
		// preferable alternative, so "Avoid Dolby Vision" also means the upgrader won't swap
		// a clean file for a DV one — matching how the picker now treats avoided releases.
		if ev.Avoided && !cur.Avoided {
			continue
		}
		if resRank[ev.Candidate.Release.Resolution] < curResRank {
			continue // never drop resolution — that's a downgrade, not an upgrade
		}
		if strings.ToLower(strings.TrimSpace(ev.Candidate.Name)) == curKey {
			continue // the release we already have
		}
		if convertedFrom(ev.Candidate.Name, currentRelease) {
			// The release this file was converted from: the same name but for the codec
			// Convert stamped in. Grabbing it would undo the conversion and loop forever.
			continue
		}
		qualityBetter := ev.Total > cur.Total
		// Same helper the import gate uses, so the two can't drift apart again — the
		// searcher deciding a release is worth grabbing and the importer then refusing to
		// place it is exactly the bug this shares its logic to prevent. It also brings the
		// margin floors and codec normalization to the grab side, which compared raw
		// bitrates and so over-valued a bloated older-codec encode.
		bitrateBetter := ev.Total >= cur.Total && s.IsBitrateUpgrade(ctx, ref,
			Encode{SizeGB: ev.Candidate.SizeGB, Codec: ev.Candidate.Release.Codec},
			Encode{SizeGB: currentSizeGB, Codec: curCand.Release.Codec}, runtimeMin)
		if qualityBetter || bitrateBetter {
			return ev.Candidate, true
		}
	}
	return Candidate{}, false
}

// IsQualityUpgrade reports whether a candidate scores strictly higher than the current
// file under this profile — the OTHER half of what UpgradeCandidate accepts, exposed so
// the import gate can honour the same verdict.
//
// The two used to disagree on exactly this. The searcher took a release on a quality gain
// that wasn't a resolution gain (a WEB-DL with Atmos over a plain WEB rip, say), the
// importer looked only at resolution and bitrate, and the finished download was refused on
// arrival — a wasted grab that then sat in the client seeding, blocking the show's sweeps
// for as long as it stayed there.
//
// Avoided formats are refused the same way UpgradeCandidate refuses them: keeping the
// current file is always preferable to swapping into something the profile avoids.
func (s *Service) IsQualityUpgrade(ctx context.Context, ref, candRelease string, candSizeGB float64, currentRelease string, currentSizeGB float64) bool {
	if strings.TrimSpace(candRelease) == "" || strings.TrimSpace(currentRelease) == "" {
		return false // no baseline to beat — the caller's other gates decide
	}
	// Same gate as IsBitrateUpgrade and UpgradeCandidate. With upgrades off the searcher
	// would never have chosen this release, so the importer replacing a file on its own
	// initiative would break the one promise that setting makes: the library stops churning.
	if sp, err := s.GetStored(ctx, ref); err != nil || !sp.UpgradesEnabled {
		return false
	}
	// The release a converted file came from is never an upgrade of it, whatever the
	// codec stamp makes the two score.
	if convertedFrom(candRelease, currentRelease) {
		return false
	}
	p, e := s.Resolve(ctx, ref)
	// Seeders are irrelevant here and unknown for a file on disk, so both sides get the
	// same large value rather than letting a seeder term skew the comparison.
	cand := e.Evaluate(p, NewCandidate(candRelease, candSizeGB, 1_000_000))
	cur := e.Evaluate(p, NewCandidate(currentRelease, currentSizeGB, 1_000_000))
	if cand.Avoided && !cur.Avoided {
		return false
	}
	return cand.Total > cur.Total
}

// convertedFrom reports whether cand looks like the release the current file was
// converted from: the current file reads as a codec Convert writes (AV1 or x265), and
// cand is the same release name with a different codec. It is deliberately no wider than
// that, so an x264 file can still be upgraded to the same group's x265 release.
func convertedFrom(cand, current string) bool {
	cur := parser.Parse(current).Codec
	if cur != parser.CodecAV1 && cur != parser.CodecX265 {
		return false
	}
	return parser.Parse(cand).Codec != cur && parser.WithoutCodec(cand) == parser.WithoutCodec(current)
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
// no longer fits). currentRelease is the file's source release name. runtimeMin
// is the content length in minutes, needed to turn the size into a bitrate so
// the profile's bitrate ceiling can apply; 0 skips the ceiling check.
func (s *Service) WouldReject(ctx context.Context, ref, currentRelease string, sizeGB float64, runtimeMin int) bool {
	if strings.TrimSpace(currentRelease) == "" {
		return false
	}
	p, e := s.Resolve(ctx, ref)
	return !e.Evaluate(p, NewCandidate(currentRelease, sizeGB, 1_000_000).WithRuntime(runtimeMin)).Eligible
}

// List returns the user's quality profiles for a media type. Every profile is a
// custom, editable row — the app just ships with a couple pre-loaded.
func (s *Service) List(ctx context.Context, mediaType string) ([]ProfileInfo, error) {
	def := s.DefaultProfile(ctx, mediaType)
	custom, err := s.repo.List(ctx, mediaType)
	if err != nil {
		return nil, err
	}
	var out []ProfileInfo
	for _, sp := range custom {
		key := "custom:" + strconv.FormatInt(sp.ID, 10)
		out = append(out, ProfileInfo{Key: key, Name: sp.Name, MediaType: sp.MediaType, BuiltIn: false, IsDefault: key == def, Summary: sp.Summary()})
	}
	return out, nil
}

// ListStored returns all custom profiles for a media type (with their format scores).
func (s *Service) ListStored(ctx context.Context, mediaType string) ([]StoredProfile, error) {
	return s.repo.List(ctx, mediaType)
}

// GetStored returns an editable profile for a custom reference.
func (s *Service) GetStored(ctx context.Context, ref string) (StoredProfile, error) {
	if id, ok := customID(ref); ok {
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
func (s *Service) AtCeiling(ctx context.Context, ref, currentRelease string, sizeGB float64, runtimeMin int) bool {
	if strings.TrimSpace(currentRelease) == "" {
		return false // nothing recorded to judge — let the normal path decide
	}
	sp, err := s.GetStored(ctx, ref)
	if err != nil {
		return false
	}
	cur := parser.Parse(currentRelease)
	// The file already is the profile's target: the search is over, whatever else might
	// technically still score higher. This is "upgrade until it fits".
	if sp.TargetMet(ReleaseFacts(cur, BitrateMbps(sizeGB, runtimeMin))) {
		return true
	}
	p, _ := s.Resolve(ctx, ref)
	limit := p.capFor(cur.Resolution)
	// No cap is no ceiling, and with no percentage step there's no bitrate upgrade path to
	// exhaust in the first place — in both cases only a quality gain can win, and that
	// can't be ruled out here.
	if limit <= 0 || sp.UpgradeMinPercent <= 0 {
		return false
	}
	if runtimeMin <= 0 || sizeGB <= 0 {
		return false // can't express the file as a bitrate — don't guess
	}
	// A resolution the profile allows and we don't have is still an upgrade, whatever the
	// bitrate says.
	best := 0
	for _, res := range p.AllowedResolutions {
		if resRank[res] > best {
			best = resRank[res]
		}
	}
	if resRank[cur.Resolution] < best {
		return false
	}

	// Raw, like the ceiling itself (Evaluate): the question is whether a permitted
	// release can be the required step above this one.
	curBr := BitrateMbps(sizeGB, runtimeMin)
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

// Create, Update, Delete manage user profiles.
func (s *Service) Create(ctx context.Context, sp StoredProfile) (StoredProfile, error) {
	normalize(&sp)
	return s.repo.Create(ctx, sp)
}

func (s *Service) Update(ctx context.Context, id int64, sp StoredProfile) error {
	normalize(&sp)
	return s.repo.Update(ctx, id, sp)
}

// DecideSpec runs the engine for a profile that may not be saved yet — the builder's "test
// on a real title" uses it with your unsaved edits.
func (s *Service) DecideSpec(sp StoredProfile, cands []Candidate) Decision {
	normalize(&sp)
	return sp.Engine().Decide(sp.ToProfile(), cands)
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

// Preview scores a (possibly unsaved) profile over the built-in sample set — the
// live feedback behind the builder.
func (s *Service) Preview(sp StoredProfile) Decision {
	normalize(&sp) // an edited target takes effect in the preview before it's saved
	return sp.Engine().Decide(sp.ToProfile(), SampleCandidates())
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
