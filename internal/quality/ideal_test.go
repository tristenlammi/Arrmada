package quality

import (
	"strconv"
	"strings"
	"testing"
)

// The example the feature was asked for: HEVC or AV1, HDR10+, Atmos, 20–30 Mb/s at 4K.
func TestCheckFit(t *testing.T) {
	ideal := IdealFile{
		Codecs:  []string{"hevc", "av1"},
		HDR:     []string{"HDR10+"},
		Atmos:   true,
		Bitrate: map[string]BitrateWindow{"2160p": {Min: 20, Max: 30}},
	}
	perfect := FileFacts{Resolution: "2160p", Codec: "hevc", HDR: "HDR10+", Atmos: true, BitrateMbps: 24}
	cases := []struct {
		name   string
		mod    func(*FileFacts)
		status string
		kinds  []string
	}{
		{"fits", func(*FileFacts) {}, FitOK, nil},
		{"remux over the ceiling", func(f *FileFacts) { f.BitrateMbps = 71 }, FitOver, []string{"bitrate"}},
		{"starved", func(f *FileFacts) { f.BitrateMbps = 9 }, FitUnder, []string{"bitrate"}},
		{"H.264 without Atmos", func(f *FileFacts) { f.Codec = "h264"; f.Atmos = false }, FitMismatch, []string{"codec", "atmos"}},
		{"plain HDR10", func(f *FileFacts) { f.HDR = "HDR10" }, FitMismatch, []string{"hdr"}},
		{"Dolby Vision over HDR10+", func(f *FileFacts) { f.DolbyVision = true }, FitOK, nil},
		{"Dolby Vision over HDR10", func(f *FileFacts) { f.DolbyVision = true; f.HDR = "HDR10" }, FitMismatch, []string{"hdr"}},
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
	// A 1080p file under a 4K-only window has no bitrate judgement, but the profile's
	// resolutions still apply; SD covers 576p/480p.
	hd := perfect
	hd.Resolution, hd.BitrateMbps = "1080p", 99
	if got := CheckFit(ideal, []string{"2160p"}, hd); got.Status != FitMismatch || got.Issues[0].Kind != "resolution" {
		t.Errorf("1080p file in a 4K profile: %+v", got)
	}
	sd := perfect
	sd.Resolution = "SD"
	if got := CheckFit(IdealFile{}, []string{"480p"}, sd); got.Status != FitOK {
		t.Errorf("SD file in a 480p profile: %+v", got)
	}
	if msg := CheckFit(ideal, nil, FileFacts{Resolution: "2160p", Codec: "h264", HDR: "SDR", BitrateMbps: 25}).Issues[0].Msg; msg != "H.264, not HEVC or AV1" {
		t.Errorf("codec message = %q", msg)
	}
}

func TestIdealEmpty(t *testing.T) {
	if !(IdealFile{Bitrate: map[string]BitrateWindow{"2160p": {}}}).Empty() {
		t.Error("an all-zero window is nothing set up")
	}
	if (IdealFile{Bitrate: map[string]BitrateWindow{"2160p": {Max: 30}}}).Empty() {
		t.Error("a ceiling is something set up")
	}
}

// Both survive a save: the ideal file, and the required formats — which used to be
// dropped on every save because they had no column.
func TestProfileSavesIdealAndRequired(t *testing.T) {
	svc, ctx := testService(t)
	ideal := &IdealFile{Codecs: []string{"hevc"}, Atmos: true, Bitrate: map[string]BitrateWindow{"2160p": {Min: 20, Max: 30}}}
	sp, err := svc.Create(ctx, StoredProfile{MediaType: MediaMovie, Name: "4K", RequiredFormats: []string{"Atmos"}, Ideal: ideal})
	if err != nil {
		t.Fatal(err)
	}
	got, err := svc.GetStored(ctx, "custom:"+strconv.FormatInt(sp.ID, 10))
	if err != nil {
		t.Fatal(err)
	}
	if len(got.RequiredFormats) != 1 || got.RequiredFormats[0] != "Atmos" {
		t.Errorf("required formats after save: %v", got.RequiredFormats)
	}
	if got.Ideal == nil || !got.Ideal.Atmos || got.Ideal.Bitrate["2160p"].Max != 30 {
		t.Errorf("ideal file after save: %+v", got.Ideal)
	}
	got.Ideal = nil // cleared in the builder
	if err := svc.Update(ctx, sp.ID, got); err != nil {
		t.Fatal(err)
	}
	if again, _ := svc.GetStored(ctx, "custom:"+strconv.FormatInt(sp.ID, 10)); again.Ideal != nil {
		t.Errorf("a cleared ideal file should stay cleared, got %+v", again.Ideal)
	}
}
