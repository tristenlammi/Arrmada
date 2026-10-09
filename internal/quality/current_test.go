package quality

import (
	"strconv"
	"testing"

	"github.com/tristenlammi/arrmada/internal/parser"
)

// cf is a release-name-only CurrentFile, the shape every caller had before probed facts.
func cf(release string, sizeGB float64, runtimeMin int) CurrentFile {
	return CurrentFile{Release: release, SizeGB: sizeGB, RuntimeMin: runtimeMin}
}

// withFacts is cf with probed facts laid over the name.
func withFacts(release string, sizeGB float64, runtimeMin int, f FileFacts) CurrentFile {
	c := cf(release, sizeGB, runtimeMin)
	c.Facts = &f
	return c
}

// A release name that says nothing about HDR or Atmos, on a file the probe found to be
// HDR10 with Atmos, meets a target that prefers both — and the upgrade sweep stops.
func TestProbedFactsMeetTheTarget(t *testing.T) {
	s, ctx := testService(t)
	sp, err := s.Create(ctx, StoredProfile{
		MediaType: MediaMovie, Name: "4K HDR", AllowedResolutions: []string{"2160p"}, UpgradesEnabled: true,
		Ideal: &IdealFile{
			HDR:     map[string]string{"HDR10": PrefWant},
			Audio:   map[string]string{"atmos": PrefWant},
			Bitrate: map[string]BitrateWindow{"2160p": {Min: 15, Max: 80}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	ref := "custom:" + strconv.FormatInt(sp.ID, 10)
	const runtime = 120
	name := "Film.2021.2160p.WEB-DL.x265-GRP" // no HDR, no Atmos in the name
	size := sizeForBitrate(30, runtime)
	probed := FileFacts{Resolution: "2160p", Codec: "hevc", HDR: "HDR10", Atmos: true, BitrateMbps: 30}
	better := []Candidate{NewCandidate("Film.2021.2160p.BluRay.HDR10.TrueHD.Atmos.x265-OTHER", sizeForBitrate(40, runtime), 50).WithRuntime(runtime)}

	if v := s.JudgeFile(ctx, ref, cf(name, size, runtime)); v.TargetMet || v.Probed {
		t.Fatalf("by its name alone the file can't meet an HDR + Atmos target: %+v", v)
	}
	if _, ok := s.UpgradeCandidate(ctx, ref, cf(name, size, runtime), better); !ok {
		t.Fatal("by its name alone, the HDR Atmos release should be an upgrade")
	}

	cur := withFacts(name, size, runtime, probed)
	v := s.JudgeFile(ctx, ref, cur)
	if !v.TargetMet || !v.AtCeiling || !v.Probed || !v.Eligible {
		t.Fatalf("probed HDR10 + Atmos should meet the target: %+v", v)
	}
	if pick, ok := s.UpgradeCandidate(ctx, ref, cur, better); ok {
		t.Errorf("the file already is the target, yet %q was picked as an upgrade", pick.Name)
	}
	if !s.AtCeiling(ctx, ref, cur) {
		t.Error("a file that meets its target is at its ceiling: the sweep should stop searching")
	}
}

// An AV1 file is judged as AV1 even when its release name says x264.
func TestProbedCodecOverridesTheName(t *testing.T) {
	s, ctx := testService(t)
	sp, err := s.Create(ctx, StoredProfile{
		MediaType: MediaMovie, Name: "Prefer AV1", UpgradesEnabled: true,
		Ideal: &IdealFile{Codec: map[string]string{"av1": PrefWant}},
	})
	if err != nil {
		t.Fatal(err)
	}
	ref := "custom:" + strconv.FormatInt(sp.ID, 10)
	name := "Film.2021.1080p.BluRay.x264-GRP"
	cands := []Candidate{NewCandidate("Film.2021.1080p.BluRay.AV1-OTHER", 5, 100).WithRuntime(120)}

	if _, ok := s.UpgradeCandidate(ctx, ref, cf(name, 6, 120), cands); !ok {
		t.Fatal("read as x264, an AV1 release should be an upgrade under Prefer AV1")
	}
	probed := withFacts(name, 6, 120, FileFacts{Resolution: "1080p", Codec: "av1", HDR: "SDR"})
	if pick, ok := s.UpgradeCandidate(ctx, ref, probed, cands); ok {
		t.Errorf("the file is AV1 by probe; %q is no upgrade", pick.Name)
	}
	if s.IsQualityUpgrade(ctx, ref, cands[0].Name, 5, probed) {
		t.Error("the import gate must read the probed codec too")
	}
	// The name the file carried before conversion is never an upgrade of it.
	if s.IsQualityUpgrade(ctx, ref, name, 12, probed) {
		t.Error("the release the AV1 file came from was taken as an upgrade of it")
	}
}

// The ceiling is judged on the probed bitrate when there is one.
func TestAtCeilingUsesProbedBitrate(t *testing.T) {
	s, ctx := testService(t)
	sp, err := s.Create(ctx, StoredProfile{
		MediaType: MediaSeries, Name: "1080p ≤30", AllowedResolutions: []string{"1080p"},
		BitrateCapMbps: 30, UpgradeMinPercent: 25, UpgradesEnabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	ref := "custom:" + strconv.FormatInt(sp.ID, 10)
	const runtime = 45
	name := "Show.S01E01.1080p.WEB-DL.H.264-GRP"
	size := sizeForBitrate(10, runtime) // 10 Mb/s by size: plenty of headroom
	if s.AtCeiling(ctx, ref, cf(name, size, runtime)) {
		t.Fatal("at 10 Mb/s by size the file has headroom")
	}
	// The probe says 25 Mb/s: 25 × 1.25 is over the 30 ceiling.
	if !s.AtCeiling(ctx, ref, withFacts(name, size, runtime, FileFacts{Resolution: "1080p", Codec: "h264", HDR: "SDR", BitrateMbps: 25})) {
		t.Error("the probed 25 Mb/s leaves no room for a 25% step under 30")
	}
	// Without a runtime only the probe can say what the bitrate is.
	if !s.AtCeiling(ctx, ref, withFacts(name, size, 0, FileFacts{Resolution: "1080p", Codec: "h264", HDR: "SDR", BitrateMbps: 25})) {
		t.Error("the probed bitrate needs no runtime")
	}
}

// Under "Avoid SDR", a Dolby Vision file with no HDR base gets the same verdict from the
// Library fit and from the engine: it is Dolby Vision, not SDR.
func TestDVOnlyIsNotSDR(t *testing.T) {
	sp := StoredProfile{MediaType: MediaMovie, Name: "No SDR", Ideal: &IdealFile{HDR: map[string]string{"SDR": PrefAvoid}}}
	sp.Compile()
	ideal := *sp.Ideal

	byName := ReleaseFacts(parser.Parse("Film.2021.2160p.WEB-DL.DV.x265-GRP"), 0)
	if !byName.DolbyVision || !byName.DVNoFallback {
		t.Fatalf("a DV-only release name reads as DV with no fallback: %+v", byName)
	}
	probed := FileFacts{Resolution: "2160p", Codec: "hevc", HDR: "SDR", DolbyVision: true, DVNoFallback: true}
	for name, f := range map[string]FileFacts{"by name": byName, "by probe": probed} {
		if fit := CheckFit(ideal, nil, f); fit.Status != FitOK {
			t.Errorf("%s: Library fit flags a DV-only file under Avoid SDR: %+v", name, fit)
		}
	}
	e, p := sp.Engine(), sp.ToProfile()
	ev := e.Evaluate(p, NewCandidate("Film.2021.2160p.WEB-DL.DV.x265-GRP", 10, 10))
	if ev.Avoided {
		t.Errorf("the engine avoids a DV-only release as SDR: %+v", ev.AvoidedFormats)
	}
	cur := withFacts("Film.2021.2160p.WEB-DL.x265-GRP", 10, 0, probed)
	if v := sp.JudgeFile(cur); v.Current.Avoided {
		t.Errorf("a probed DV-only file is scored as SDR: %+v", v.Current.AvoidedFormats)
	}

	// A real SDR file still is.
	sdr := FileFacts{Resolution: "1080p", Codec: "h264", HDR: "SDR"}
	if fit := CheckFit(ideal, nil, sdr); fit.Status == FitOK {
		t.Error("an SDR file should not fit under Avoid SDR")
	}
	if v := sp.JudgeFile(withFacts("Film.2021.1080p.WEB-DL.HDR10.x264-GRP", 4, 0, sdr)); !v.Current.Avoided {
		t.Error("a file probed SDR is SDR whatever its name claims")
	}
}

// The probe decides Atmos and lossless: a name's claim the file can't back is dropped, and
// a track the name never mentioned counts.
func TestReleaseWithFactsAudio(t *testing.T) {
	r := ReleaseWithFacts(parser.Parse("Film.2021.1080p.BluRay.TrueHD.Atmos.7.1.x264-GRP"),
		FileFacts{Resolution: "1080p", Codec: "hevc", HDR: "SDR"})
	if containsStr(r.Audio, "Atmos") || containsStr(r.Audio, "TrueHD") {
		t.Errorf("a converted file without Atmos or TrueHD still claims them: %v", r.Audio)
	}
	if r.Codec != parser.CodecX265 || r.Source != parser.SourceBluray || r.Group != "GRP" {
		t.Errorf("codec from the probe, source and group from the name: %+v", r)
	}
	r = ReleaseWithFacts(parser.Parse("Film.2021.2160p.WEB-DL.DDP5.1.x265-GRP"),
		FileFacts{Resolution: "2160p", Codec: "hevc", HDR: "HDR10+", Atmos: true, Lossless: true, LosslessCodec: "TrueHD"})
	if !containsStr(r.Audio, "Atmos") || !containsStr(r.Audio, "TrueHD") || !containsStr(r.Audio, "DDP") {
		t.Errorf("probed Atmos and TrueHD should be added beside the name's tags: %v", r.Audio)
	}
	if !containsStr(r.HDR, "HDR10+") || !containsStr(r.HDR, "HDR10") {
		t.Errorf("HDR10+ is tagged like the parser tags it: %v", r.HDR)
	}
}

// A file with nothing recorded is not judged at all, and nothing rejects it.
func TestJudgeFileUnknown(t *testing.T) {
	sp := StoredProfile{MediaType: MediaMovie, Name: "x", AllowedResolutions: []string{"2160p"}}
	if v := sp.JudgeFile(CurrentFile{}); !v.Eligible || v.TargetMet || v.AtCeiling {
		t.Errorf("an unknown file got a verdict: %+v", v)
	}
	// Probed facts alone are enough to judge it.
	f := FileFacts{Resolution: "1080p", Codec: "h264", HDR: "SDR"}
	if v := sp.JudgeFile(CurrentFile{Facts: &f}); v.Eligible {
		t.Error("a probed 1080p file doesn't meet a 2160p-only profile")
	}
}
