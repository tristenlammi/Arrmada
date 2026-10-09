package quality

import (
	"context"
	"strconv"
	"strings"
)

// The dry run behind the builder's Save: before an edit to a profile is saved, how many
// of its files would the upgrade sweeps start replacing? Saving can otherwise quietly
// queue terabytes of re-downloads — a new Must makes every file without it ineligible,
// a new Prefer sends the sweep looking again — with no number shown first.
//
// Each file is judged twice, under the profile as saved and as edited, by the same
// exported decisions the sweeps and the profile-change prompt run (AllowsUpgrades,
// WouldReject, AtCeiling), so the estimate follows them wherever they change. Only the
// files whose state got worse are counted.

// ImpactFile is one file on the profile, as the upgrade sweeps see it.
type ImpactFile struct {
	Title string // what the examples show: "Film (2021)", or a show's title
	// Release is the baseline the sweeps score the file by: its source release, or for a
	// scanned movie the synthetic one built from its probed quality. "" is never upgraded.
	Release    string
	Bytes      int64
	RuntimeMin int // the movie's or episode's length, so the bitrate window applies
	// Held files are kept out of upgrades ("keep existing files"), so no edit moves them.
	Held bool
}

// Bucket counts the files that moved into one state.
type Bucket struct {
	Files    int      `json:"files"`
	Bytes    int64    `json:"bytes"`
	Examples []string `json:"examples"` // up to impactExamples distinct titles
}

// Impact is what saving an edit would change: files that would newly no longer meet the
// profile (Replace — any release it accepts beats them), and files that met it before
// and would now be searched again for something better (Search).
type Impact struct {
	Replace Bucket `json:"replace"`
	Search  Bucket `json:"search"`
	Files   int    `json:"files"` // every file judged
}

const impactExamples = 5

type upgradeState int

const (
	stateSettled upgradeState = iota // the sweeps leave it alone
	stateSearch                      // the sweeps keep looking for something better
	stateReplace                     // it doesn't meet the profile, so anything it accepts wins
)

// Impact judges every file under old (the profile as saved) and edited (as it would be
// saved) and counts the ones whose state got worse. Both must be the same saved profile:
// edited.ID is old.ID.
func (s *Service) Impact(ctx context.Context, old, edited StoredProfile, files []ImpactFile) Impact {
	out := Impact{Replace: Bucket{Examples: []string{}}, Search: Bucket{Examples: []string{}}, Files: len(files)}
	for i, now := range s.worsened(ctx, old, edited, files) {
		switch now {
		case stateReplace:
			out.Replace.add(files[i])
		case stateSearch:
			out.Search.add(files[i])
		}
	}
	return out
}

// Worsened reports, file by file, whether saving edited over old makes its state worse —
// exactly the files Impact counts, for acting on them ("keep existing files" holds them).
func (s *Service) Worsened(ctx context.Context, old, edited StoredProfile, files []ImpactFile) []bool {
	out := make([]bool, len(files))
	for i, now := range s.worsened(ctx, old, edited, files) {
		out[i] = now != stateSettled
	}
	return out
}

// worsened is each file's state under edited when that's worse than under old, else
// stateSettled.
func (s *Service) worsened(ctx context.Context, old, edited StoredProfile, files []ImpactFile) []upgradeState {
	// Both sides as Update would store them. Saving normalises the profile whether or not
	// anything changed, so a difference that normalising alone makes isn't the edit's.
	edited.ID, edited.MediaType = old.ID, old.MediaType
	normalize(&old)
	normalize(&edited)
	ref := "custom:" + strconv.FormatInt(old.ID, 10)
	before, after := s.withSpecs(old), s.withSpecs(edited)
	out := make([]upgradeState, len(files))
	for i, f := range files {
		if now := after.upgradeState(ctx, ref, f); now > before.upgradeState(ctx, ref, f) {
			out[i] = now
		}
	}
	return out
}

// upgradeState is what the upgrade sweeps would do with a file under ref. Held files and
// files with no baseline are never upgraded; with upgrades off nothing is.
func (s *Service) upgradeState(ctx context.Context, ref string, f ImpactFile) upgradeState {
	if f.Held || strings.TrimSpace(f.Release) == "" || !s.AllowsUpgrades(ctx, ref) {
		return stateSettled
	}
	gb := float64(f.Bytes) / (1 << 30)
	if s.WouldReject(ctx, ref, f.Release, gb, f.RuntimeMin) {
		return stateReplace
	}
	if !s.AtCeiling(ctx, ref, f.Release, gb, f.RuntimeMin) {
		return stateSearch
	}
	return stateSettled
}

// withSpecs is a view of the service that reads the given profiles from memory instead of
// the database — the same decisions, run against a profile that isn't saved (yet).
func (s *Service) withSpecs(sps ...StoredProfile) *Service {
	v := *s
	v.specs = make(map[int64]StoredProfile, len(sps))
	for _, sp := range sps {
		v.specs[sp.ID] = sp
	}
	return &v
}

func (b *Bucket) add(f ImpactFile) {
	b.Files++
	b.Bytes += f.Bytes
	if len(b.Examples) < impactExamples && f.Title != "" && !containsStr(b.Examples, f.Title) {
		b.Examples = append(b.Examples, f.Title)
	}
}
