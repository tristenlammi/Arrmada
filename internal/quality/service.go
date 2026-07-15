package quality

import (
	"context"
	"database/sql"
	"strconv"
	"strings"
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
func (s *Service) DefaultProfile(ctx context.Context, mediaType string) string {
	v, err := s.repo.getSetting(ctx, "default_profile:"+mediaType)
	if err == nil && v != "" && s.Known(ctx, v) {
		return v
	}
	if custom, err := s.repo.List(ctx, mediaType); err == nil && len(custom) > 0 {
		return "custom:" + strconv.FormatInt(custom[0].ID, 10)
	}
	return ""
}

// SetDefaultProfile records the default profile for a media type.
func (s *Service) SetDefaultProfile(ctx context.Context, mediaType, ref string) error {
	if !s.Known(ctx, ref) {
		return errNotKnown
	}
	return s.repo.setSetting(ctx, "default_profile:"+mediaType, ref)
}

var errNotKnown = errorString("unknown quality profile")

type errorString string

func (e errorString) Error() string { return string(e) }

// Resolve turns a profile reference into a runnable (Profile, Engine). Falls
// back to a permissive profile if the reference is unknown (e.g. it was
// deleted), so acquisition never stalls.
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

// StallMinutes returns the stall-timeout (minutes) for a profile reference; 0
// means fail-over detection is off.
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
//     upgrade_gb > 0 — it's at least that many GB bigger and no worse on quality.
func (s *Service) UpgradeCandidate(ctx context.Context, ref, currentRelease string, currentSizeGB float64, cands []Candidate) (Candidate, bool) {
	sp, err := s.GetStored(ctx, ref)
	if err != nil || !sp.UpgradesEnabled || strings.TrimSpace(currentRelease) == "" {
		return Candidate{}, false
	}
	p, e := s.Resolve(ctx, ref)
	curCand := NewCandidate(currentRelease, currentSizeGB, 1_000_000)
	cur := e.Evaluate(p, curCand)
	curResRank := resRank[curCand.Release.Resolution]
	curKey := strings.ToLower(strings.TrimSpace(currentRelease))

	d := e.Decide(p, cands)
	for _, ev := range d.Eligible { // sorted best-first
		if resRank[ev.Candidate.Release.Resolution] < curResRank {
			continue // never drop resolution — that's a downgrade, not an upgrade
		}
		if strings.ToLower(strings.TrimSpace(ev.Candidate.Name)) == curKey {
			continue // the release we already have
		}
		qualityBetter := ev.Total > cur.Total
		bitrateBetter := sp.UpgradeGB > 0 && ev.Total >= cur.Total &&
			ev.Candidate.SizeGB >= currentSizeGB+sp.UpgradeGB
		if qualityBetter || bitrateBetter {
			return ev.Candidate, true
		}
	}
	return Candidate{}, false
}

// WouldReject reports whether the profile would reject the given release — used
// to tell if switching a movie to this profile is a downgrade (its current file
// no longer fits). currentRelease is the file's source release name.
func (s *Service) WouldReject(ctx context.Context, ref, currentRelease string, sizeGB float64) bool {
	if strings.TrimSpace(currentRelease) == "" {
		return false
	}
	p, e := s.Resolve(ctx, ref)
	return !e.Evaluate(p, NewCandidate(currentRelease, sizeGB, 1_000_000)).Eligible
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

// Create, Update, Delete manage user profiles.
func (s *Service) Create(ctx context.Context, sp StoredProfile) (StoredProfile, error) {
	normalize(&sp)
	return s.repo.Create(ctx, sp)
}

func (s *Service) Update(ctx context.Context, id int64, sp StoredProfile) error {
	normalize(&sp)
	return s.repo.Update(ctx, id, sp)
}

func (s *Service) Delete(ctx context.Context, id int64) error { return s.repo.Delete(ctx, id) }

// Preview scores a (possibly unsaved) profile over the built-in sample set — the
// live feedback behind the builder.
func (s *Service) Preview(sp StoredProfile) Decision {
	return sp.Engine().Decide(sp.ToProfile(), SampleCandidates())
}

func normalize(sp *StoredProfile) {
	if sp.MediaType == "" {
		sp.MediaType = MediaMovie
	}
	if sp.FormatScores == nil {
		sp.FormatScores = map[string]int{}
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
