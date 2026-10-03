package quality

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/tristenlammi/arrmada/internal/parser"
)

// The example the feature was asked for: HEVC or AV1, HDR10+, Atmos, 20–30 Mb/s at 4K.
func exampleTarget() IdealFile {
	return IdealFile{
		Codec:   map[string]string{"hevc": PrefWant, "av1": PrefWant},
		HDR:     map[string]string{"HDR10+": PrefWant, "HDR10": PrefOK, "DV": PrefAvoid},
		Audio:   map[string]string{"atmos": PrefMust},
		Bitrate: map[string]BitrateWindow{"2160p": {Min: 20, Max: 30}},
	}
}

func TestCheckFit(t *testing.T) {
	ideal := exampleTarget()
	perfect := FileFacts{Resolution: "2160p", Codec: "hevc", HDR: "HDR10+", Atmos: true, BitrateMbps: 24}
	cases := []struct {
		name   string
		mod    func(*FileFacts)
		status string
		kinds  []string
	}{
		{"fits", func(*FileFacts) {}, FitOK, nil},
		{"plain HDR10 is ok", func(f *FileFacts) { f.HDR = "HDR10" }, FitOK, nil},
		{"remux over the ceiling", func(f *FileFacts) { f.BitrateMbps = 71 }, FitOver, []string{"bitrate"}},
		{"starved", func(f *FileFacts) { f.BitrateMbps = 9 }, FitUnder, []string{"bitrate"}},
		{"H.264 without Atmos", func(f *FileFacts) { f.Codec = "h264"; f.Atmos = false }, FitMismatch, []string{"codec", "atmos"}},
		{"SDR isn't in the row", func(f *FileFacts) { f.HDR = "SDR" }, FitMismatch, []string{"hdr"}},
		{"Dolby Vision is avoided", func(f *FileFacts) { f.DolbyVision = true }, FitMismatch, []string{"hdr"}},
		{"over AND wrong codec: over wins, both listed", func(f *FileFacts) { f.BitrateMbps = 50; f.Codec = "h264" }, FitOver, []string{"codec", "bitrate"}},
		{"unknown bitrate isn't judged", func(f *FileFacts) { f.BitrateMbps = 0 }, FitOK, nil},
	}
	for _, c := range cases {
		f := perfect
		c.mod(&f)
		got := CheckFit(ideal, nil, f)
		var kinds []string
		for _, is := range got.Issues {
			kinds = append(kinds, is.Kind)
		}
		if got.Status != c.status || strings.Join(kinds, ",") != strings.Join(c.kinds, ",") {
			t.Errorf("%s: got %s %v, want %s %v (%+v)", c.name, got.Status, kinds, c.status, c.kinds, got.Issues)
		}
	}
	if msg := CheckFit(ideal, nil, FileFacts{Resolution: "2160p", Codec: "h264", HDR: "HDR10", Atmos: true, BitrateMbps: 25}).Issues[0].Msg; msg != "H.264, not AV1 or HEVC" {
		t.Errorf("codec message = %q", msg)
	}
}

// A row's musts narrow it: with "HEVC must", an AV1 file isn't the target even though AV1
// is wanted. An HDR10+ picture counts as HDR10 (its base), and a Dolby Vision file as the
// format under it unless Dolby Vision has a state.
func TestCheckFitRows(t *testing.T) {
	mustHEVC := IdealFile{Codec: map[string]string{"hevc": PrefMust, "av1": PrefWant}}
	if CheckFit(mustHEVC, nil, FileFacts{Codec: "av1"}).Status != FitMismatch {
		t.Error("an AV1 file can't fit a profile that must have HEVC")
	}
	hdr10 := IdealFile{HDR: map[string]string{"HDR10": PrefWant}}
	if got := CheckFit(hdr10, nil, FileFacts{HDR: "HDR10+"}); got.Status != FitOK {
		t.Errorf("HDR10+ should satisfy HDR10: %+v", got)
	}
	if got := CheckFit(hdr10, nil, FileFacts{HDR: "HDR10", DolbyVision: true}); got.Status != FitOK {
		t.Errorf("Dolby Vision over HDR10 should be judged by its base: %+v", got)
	}
	// A 1080p file under a 4K-only profile; SD covers 576p/480p.
	if got := CheckFit(IdealFile{}, []string{"2160p"}, FileFacts{Resolution: "1080p"}); got.Status != FitMismatch {
		t.Errorf("1080p file in a 4K profile: %+v", got)
	}
	if got := CheckFit(IdealFile{}, []string{"480p"}, FileFacts{Resolution: "SD"}); got.Status != FitOK {
		t.Errorf("SD file in a 480p profile: %+v", got)
	}
}

// The first, report-only form of the ideal file is still read: its lists meant "fits"
// (ok) and its flags "must have" (want).
func TestIdealReadsFirstForm(t *testing.T) {
	var f IdealFile
	if err := json.Unmarshal([]byte(`{"codecs":["hevc","av1"],"hdr":["HDR10+"],"atmos":true,"bitrate":{"2160p":{"min":20,"max":30}}}`), &f); err != nil {
		t.Fatal(err)
	}
	if f.Codec["hevc"] != PrefOK || f.Codec["av1"] != PrefOK || f.HDR["HDR10+"] != PrefOK || f.Audio["atmos"] != PrefWant || f.Bitrate["2160p"].Max != 30 {
		t.Errorf("first form read as %+v", f)
	}
	var g IdealFile
	if err := json.Unmarshal([]byte(`{"hdr":{"HDR10+":"want"}}`), &g); err != nil || g.HDR["HDR10+"] != PrefWant {
		t.Errorf("current form: %+v (%v)", g, err)
	}
}

func TestIdealEmpty(t *testing.T) {
	if !(IdealFile{Bitrate: map[string]BitrateWindow{"2160p": {}}, Codec: map[string]string{"hevc": PrefNone}}).Empty() {
		t.Error("an all-zero window and no states is nothing set up")
	}
	if (IdealFile{Bitrate: map[string]BitrateWindow{"2160p": {Max: 30}}}).Empty() {
		t.Error("a ceiling is something set up")
	}
}

// A profile from before targets opens as one, showing what it already did — and saving it
// changes nothing about what it grabs.
func TestMigrateAndCompileRoundTrip(t *testing.T) {
	old := StoredProfile{
		MediaType: MediaMovie, AllowedResolutions: []string{"2160p", "1080p"}, BitrateCapMbps: 40,
		FormatScores:    map[string]int{"HDR10": 50, "Dolby Vision": -50, "Atmos": 80, "TrueHD": 30},
		RequiredFormats: []string{"HEVC"},
	}
	before := old.ToProfile()
	sp := old
	sp.FormatScores = map[string]int{}
	for k, v := range old.FormatScores {
		sp.FormatScores[k] = v
	}
	sp.Migrate()
	id := sp.Ideal
	if id == nil || id.HDR["HDR10"] != PrefWant || id.HDR["DV"] != PrefAvoid || id.Audio["atmos"] != PrefWant || id.Codec["hevc"] != PrefMust {
		t.Fatalf("migrated target: %+v", id)
	}
	if id.Bitrate["2160p"].Max != 40 || id.Bitrate["1080p"].Max != 40 {
		t.Errorf("the single ceiling should become each resolution's: %+v", id.Bitrate)
	}
	sp.Compile()
	after := sp.ToProfile()
	// Same scores (the tuned 80 survives), same requirement, ceilings now per resolution.
	for _, name := range []string{"HDR10", "Dolby Vision", "Atmos", "TrueHD"} {
		if after.FormatScores[name] != before.FormatScores[name] {
			t.Errorf("%s score %d → %d", name, before.FormatScores[name], after.FormatScores[name])
		}
	}
	if strings.Join(after.Required, ",") != "HEVC" || after.BitrateCapMbps != 0 || after.capFor(parser.Res2160p) != 40 {
		t.Errorf("after save: required %v, cap %.0f, 4K cap %.0f", after.Required, after.BitrateCapMbps, after.capFor(parser.Res2160p))
	}
}

// The target steers grabbing: a must is never grabbed without (one of a row's musts is
// enough), a resolution's ceiling rejects, and falling under its floor ranks a release
// below everything inside the window.
func TestTargetDrivesGrabbing(t *testing.T) {
	sp := StoredProfile{MediaType: MediaMovie, AllowedResolutions: []string{"2160p", "1080p"},
		Ideal: &IdealFile{
			Codec:   map[string]string{"hevc": PrefMust, "av1": PrefMust},
			HDR:     map[string]string{"HDR10+": PrefWant},
			Bitrate: map[string]BitrateWindow{"2160p": {Min: 20, Max: 30}, "1080p": {Max: 12}},
		}}
	normalize(&sp)
	p, e := sp.ToProfile(), sp.Engine()
	at := func(name string, gb float64) Evaluation {
		return e.Evaluate(p, NewCandidate(name, gb, 50).WithRuntime(120))
	}
	// 120 min: 1 GiB ≈ 1.19 Mb/s.
	if ev := at("Film.2024.2160p.WEB-DL.DDP5.1.H.264-GRP", 20); ev.Eligible {
		t.Error("an H.264 release must be refused when the codec row must be HEVC or AV1")
	}
	if ev := at("Film.2024.2160p.WEB-DL.DDP5.1.AV1-GRP", 20); !ev.Eligible {
		t.Errorf("AV1 is one of the musts: %s", ev.RejectReason)
	}
	if ev := at("Film.2024.2160p.BluRay.REMUX.HEVC-GRP", 60); ev.Eligible || !strings.Contains(ev.RejectReason, "30") {
		t.Errorf("a 71 Mb/s remux is over the 4K ceiling: %+v", ev.RejectReason)
	}
	if ev := at("Film.2024.1080p.BluRay.x265-GRP", 15); ev.Eligible {
		t.Error("an 18 Mb/s 1080p release is over the 1080p ceiling")
	}
	starved := at("Film.2024.2160p.WEB-DL.HDR10+.x265-GRP", 8)
	if !starved.Eligible || !starved.Avoided {
		t.Errorf("a 9.5 Mb/s 4K release is grabbable but avoided: %+v", starved)
	}
	d := e.Decide(p, []Candidate{
		NewCandidate("Film.2024.2160p.WEB-DL.HDR10+.x265-STARVED", 8, 50).WithRuntime(120),
		NewCandidate("Film.2024.2160p.WEB-DL.HDR10.x265-GOOD", 20, 50).WithRuntime(120),
	})
	if d.Winner == nil || !strings.Contains(d.Winner.Candidate.Name, "GOOD") {
		t.Errorf("the release inside the window should win over a starved one with a wanted format: %+v", d.Winner)
	}
}

// Upgrading stops once the file is the target: best allowed resolution, fitting.
func TestTargetMet(t *testing.T) {
	sp := StoredProfile{MediaType: MediaMovie, AllowedResolutions: []string{"2160p", "1080p"}, Ideal: func() *IdealFile { i := exampleTarget(); return &i }()}
	fits := ReleaseFacts(parser.Parse("Film.2024.2160p.WEB-DL.DDP5.1.Atmos.HDR10+.x265-GRP"), 24)
	if !sp.TargetMet(fits) {
		t.Errorf("a fitting 4K file is the target: %+v", fits)
	}
	if sp.TargetMet(ReleaseFacts(parser.Parse("Film.2024.1080p.WEB-DL.DDP5.1.Atmos.x265-GRP"), 8)) {
		t.Error("a 1080p file isn't the target of a 4K profile")
	}
	if (StoredProfile{AllowedResolutions: []string{"2160p"}}).TargetMet(fits) {
		t.Error("with no target set up, nothing is 'the target'")
	}
}

// Both survive a save: the target, and the required formats — which used to be dropped on
// every save because they had no column.
func TestProfileSavesIdealAndRequired(t *testing.T) {
	svc, ctx := testService(t)
	ideal := &IdealFile{Codec: map[string]string{"hevc": PrefWant}, Audio: map[string]string{"atmos": PrefMust},
		Bitrate: map[string]BitrateWindow{"2160p": {Min: 20, Max: 30}}}
	sp, err := svc.Create(ctx, StoredProfile{MediaType: MediaMovie, Name: "4K", Ideal: ideal})
	if err != nil {
		t.Fatal(err)
	}
	got, err := svc.GetStored(ctx, "custom:"+strconv.FormatInt(sp.ID, 10))
	if err != nil {
		t.Fatal(err)
	}
	if len(got.RequiredFormats) != 1 || got.RequiredFormats[0] != "Atmos" {
		t.Errorf("a must should be saved as a required format: %v", got.RequiredFormats)
	}
	if got.Ideal == nil || got.Ideal.Audio["atmos"] != PrefMust || got.Ideal.Bitrate["2160p"].Max != 30 || got.FormatScores["HEVC"] != preferScore {
		t.Errorf("target after save: %+v, scores %v", got.Ideal, got.FormatScores)
	}
	got.Ideal = &IdealFile{} // cleared in the builder (an empty target, not a missing one)
	if err := svc.Update(ctx, sp.ID, got); err != nil {
		t.Fatal(err)
	}
	again, _ := svc.GetStored(ctx, "custom:"+strconv.FormatInt(sp.ID, 10))
	if again.Ideal != nil || len(again.RequiredFormats) != 0 || len(again.FormatScores) != 0 {
		t.Errorf("a cleared target should clear what it compiled to: %+v %v %v", again.Ideal, again.RequiredFormats, again.FormatScores)
	}
}
