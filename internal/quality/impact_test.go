package quality

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// impactProfile saves a 1080p movie profile with upgrades on and the given target.
func impactProfile(t *testing.T, s *Service, ctx context.Context, ideal *IdealFile) StoredProfile {
	t.Helper()
	sp, err := s.Create(ctx, StoredProfile{
		MediaType: MediaMovie, Name: "impact", UpgradesEnabled: true,
		AllowedResolutions: []string{"1080p"}, Ideal: ideal,
	})
	if err != nil {
		t.Fatal(err)
	}
	return sp
}

// bytesAt is a file's size for a bitrate over a two-hour film.
func bytesAt(mbps float64) int64 { return int64(sizeForBitrate(mbps, 120) * (1 << 30)) }

func TestImpactMustHEVCMovesH264ToReplace(t *testing.T) {
	s, ctx := testService(t)
	old := impactProfile(t, s, ctx, nil)
	files := []ImpactFile{
		{Title: "A (2020)", Release: "A.2020.1080p.BluRay.x264-GRP", Bytes: bytesAt(10), RuntimeMin: 120},
		{Title: "B (2021)", Release: "B.2021.1080p.WEB-DL.H.264-GRP", Bytes: bytesAt(6), RuntimeMin: 120},
		{Title: "C (2022)", Release: "C.2022.1080p.WEB-DL.x265-GRP", Bytes: bytesAt(5), RuntimeMin: 120},
	}
	edited := old
	edited.Ideal = &IdealFile{Codec: map[string]string{"hevc": PrefMust}}
	got := s.Impact(ctx, old, edited, files)
	if got.Replace.Files != 2 || got.Replace.Bytes != files[0].Bytes+files[1].Bytes {
		t.Errorf("replace = %+v, want the two H.264 files", got.Replace)
	}
	if len(got.Replace.Examples) != 2 || got.Replace.Examples[0] != "A (2020)" {
		t.Errorf("examples = %v", got.Replace.Examples)
	}
	if got.Search.Files != 0 {
		t.Errorf("search = %+v, want none (no target floor: they were already searched for)", got.Search)
	}
	if got.Files != 3 {
		t.Errorf("judged %d files, want 3", got.Files)
	}
}

func TestImpactPreferAtmosMovesFilesToSearch(t *testing.T) {
	s, ctx := testService(t)
	// A target with a floor, so a file inside it is finished and the sweeps leave it be.
	target := func() *IdealFile {
		return &IdealFile{Codec: map[string]string{"hevc": PrefWant}, Bitrate: map[string]BitrateWindow{"1080p": {Min: 4, Max: 20}}}
	}
	old := impactProfile(t, s, ctx, target())
	files := []ImpactFile{
		{Title: "Has Atmos", Release: "A.2020.1080p.WEB-DL.DDP5.1.Atmos.x265-GRP", Bytes: bytesAt(8), RuntimeMin: 120},
		{Title: "No Atmos", Release: "B.2020.1080p.WEB-DL.DDP5.1.x265-GRP", Bytes: bytesAt(8), RuntimeMin: 120},
	}
	edited := old
	edited.Ideal = target()
	edited.Ideal.Audio = map[string]string{"atmos": PrefWant}
	got := s.Impact(ctx, old, edited, files)
	if got.Search.Files != 1 || got.Search.Examples[0] != "No Atmos" {
		t.Errorf("search = %+v, want only the file without Atmos", got.Search)
	}
	if got.Replace.Files != 0 {
		t.Errorf("replace = %+v, want none: Prefer never makes a file ineligible", got.Replace)
	}
}

func TestImpactLowerCeilingMovesHeavyFilesToReplace(t *testing.T) {
	s, ctx := testService(t)
	old := impactProfile(t, s, ctx, &IdealFile{Bitrate: map[string]BitrateWindow{"1080p": {Max: 20}}})
	files := []ImpactFile{
		{Title: "Heavy", Release: "A.2020.1080p.BluRay.x264-GRP", Bytes: bytesAt(15), RuntimeMin: 120},
		{Title: "Light", Release: "B.2020.1080p.BluRay.x264-GRP", Bytes: bytesAt(8), RuntimeMin: 120},
		// No runtime: the bitrate can't be known, so the ceiling can't judge it.
		{Title: "Unknown", Release: "C.2020.1080p.BluRay.x264-GRP", Bytes: bytesAt(15)},
	}
	edited := old
	edited.Ideal = &IdealFile{Bitrate: map[string]BitrateWindow{"1080p": {Max: 10}}}
	got := s.Impact(ctx, old, edited, files)
	if got.Replace.Files != 1 || got.Replace.Examples[0] != "Heavy" {
		t.Errorf("replace = %+v, want only the file over the new ceiling", got.Replace)
	}
}

func TestImpactNothingWorse(t *testing.T) {
	s, ctx := testService(t)
	old := impactProfile(t, s, ctx, &IdealFile{Codec: map[string]string{"hevc": PrefWant}})
	files := []ImpactFile{
		{Title: "A", Release: "A.2020.1080p.BluRay.x264-GRP", Bytes: bytesAt(10), RuntimeMin: 120},
		{Title: "B", Release: "B.2020.720p.BluRay.x264-GRP", Bytes: bytesAt(4), RuntimeMin: 120},
	}
	// Unchanged, as the builder sends it back after reading it.
	stored, _ := s.GetStored(ctx, "custom:"+fmt.Sprint(old.ID))
	if got := s.Impact(ctx, stored, stored, files); got.Replace.Files+got.Search.Files != 0 {
		t.Errorf("unchanged profile: %+v", got)
	}
	// Upgrades off, even with a Must that would otherwise reject everything.
	off := stored
	off.UpgradesEnabled = false
	off.Ideal = &IdealFile{Codec: map[string]string{"av1": PrefMust}}
	if got := s.Impact(ctx, stored, off, files); got.Replace.Files+got.Search.Files != 0 {
		t.Errorf("upgrades off: %+v", got)
	}
	// Held files and files with no baseline are never upgraded, whatever the edit.
	held := []ImpactFile{
		{Title: "Held", Release: "A.2020.1080p.BluRay.x264-GRP", Bytes: bytesAt(10), RuntimeMin: 120, Held: true},
		{Title: "No baseline", Bytes: bytesAt(10), RuntimeMin: 120},
	}
	must := stored
	must.Ideal = &IdealFile{Codec: map[string]string{"av1": PrefMust}}
	if got := s.Impact(ctx, stored, must, held); got.Replace.Files+got.Search.Files != 0 {
		t.Errorf("held / no baseline: %+v", got)
	}
}

// For a Must or a resolution change, the replace count is the Library fit's "don't fit"
// count for the same edit (on the files whose release says what the probe would).
func TestImpactAgreesWithFitForMust(t *testing.T) {
	s, ctx := testService(t)
	old := impactProfile(t, s, ctx, nil)
	names := []string{"A.2020.1080p.BluRay.x264-GRP", "B.2020.1080p.BluRay.x265-GRP", "C.2020.1080p.WEB-DL.AV1-GRP"}
	edited := old
	edited.Ideal = &IdealFile{Codec: map[string]string{"hevc": PrefMust}}
	var files []ImpactFile
	misfits := 0
	for _, n := range names {
		files = append(files, ImpactFile{Title: n, Release: n, Bytes: bytesAt(8), RuntimeMin: 120})
		facts := ReleaseFacts(NewCandidate(n, 0, 0).Release, 8)
		if CheckFit(*edited.Ideal, edited.AllowedResolutions, facts).Status != FitOK {
			misfits++
		}
	}
	if got := s.Impact(ctx, old, edited, files); got.Replace.Files != misfits {
		t.Errorf("replace %d, fit says %d don't fit", got.Replace.Files, misfits)
	}
}

// A big TV library is judged in one pass with no database reads per file.
func TestImpactLargeLibrary(t *testing.T) {
	if testing.Short() {
		t.Skip("timing")
	}
	s, ctx := testService(t)
	old := impactProfile(t, s, ctx, &IdealFile{Bitrate: map[string]BitrateWindow{"1080p": {Min: 2, Max: 20}}})
	edited := old
	edited.Ideal = &IdealFile{Codec: map[string]string{"hevc": PrefMust}, Bitrate: map[string]BitrateWindow{"1080p": {Min: 2, Max: 20}}}
	files := make([]ImpactFile, 20000)
	for i := range files {
		files[i] = ImpactFile{Title: "Show", Release: fmt.Sprintf("Show.S%02dE%02d.1080p.WEB-DL.x264-GRP", i/100+1, i%100+1), Bytes: 1 << 30, RuntimeMin: 45}
	}
	start := time.Now()
	got := s.Impact(ctx, old, edited, files)
	t.Logf("20k episodes judged in %v", time.Since(start))
	if got.Replace.Files != len(files) {
		t.Errorf("replace %d, want all %d", got.Replace.Files, len(files))
	}
}
