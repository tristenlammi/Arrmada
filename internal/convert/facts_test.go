package convert

import "testing"

// Atmos and lossless are read from ffprobe's audio profile, the way it reports them.
func TestAudioTraits(t *testing.T) {
	cases := []struct {
		codec, profile, title string
		atmos, lossless       bool
	}{
		{"truehd", "Dolby TrueHD + Dolby Atmos", "", true, true},
		{"eac3", "Dolby Digital Plus + Dolby Atmos", "", true, false},
		{"dts", "DTS-HD MA + DTS:X", "", false, true},
		{"dts", "DTS-HD MA", "", false, true},
		{"dts", "DTS-HD HRA", "", false, false},
		{"dts", "DTS", "", false, false},
		{"ac3", "", "English Atmos", true, false},
		{"flac", "", "", false, true},
		{"pcm_s24le", "", "", false, true},
		{"aac", "LC", "", false, false},
	}
	for _, c := range cases {
		if a, l := audioTraits(c.codec, c.profile, c.title); a != c.atmos || l != c.lossless {
			t.Errorf("%s %q %q: atmos=%v lossless=%v, want %v %v", c.codec, c.profile, c.title, a, l, c.atmos, c.lossless)
		}
	}
}

// A Dolby Vision file is judged by the format under its Dolby Vision layer.
func TestFactsDolbyVisionBase(t *testing.T) {
	mi := &MediaInfo{VideoCodec: "hevc", Resolution: "2160p", HDR: "Dolby Vision", DVBase: "HDR10", BitrateKbps: 71000,
		Audio: []AudioStream{{Codec: "truehd", Atmos: true, Lossless: true}}}
	f := Facts(mi)
	if f.HDR != "HDR10" || !f.DolbyVision || f.Codec != "hevc" || !f.Atmos || !f.Lossless || f.BitrateMbps != 71 {
		t.Errorf("facts = %+v", f)
	}
}

// FactsForPath trusts an analysis only while it still describes the file on disk.
func TestFactsForPath(t *testing.T) {
	s := newTestService(t)
	ctx := t.Context()
	mi := &MediaInfo{VideoCodec: "av1", Resolution: "1080p", HDR: "SDR", BitrateKbps: 8000, SizeBytes: 4 << 30,
		Audio: []AudioStream{{Codec: "truehd", Atmos: true, Lossless: true}}}
	if err := s.index.upsert(ctx, indexRow{Path: "/movies/a.mkv", MediaType: "movie", MovieID: 1, SizeBytes: 4 << 30, Codec: "av1", Info: mi}); err != nil {
		t.Fatal(err)
	}

	f, ok := s.FactsForPath(ctx, "/movies/a.mkv", 4<<30)
	if !ok || f.Codec != "av1" || !f.Atmos || !f.Lossless || f.LosslessCodec != "TrueHD" || f.BitrateMbps != 8 {
		t.Fatalf("matching entry: ok=%v facts=%+v", ok, f)
	}
	if _, ok := s.FactsForPath(ctx, "/movies/a.mkv", 5<<30); ok {
		t.Error("a different size means a different file: the entry must be ignored")
	}
	if _, ok := s.FactsForPath(ctx, "/movies/a.mkv", 0); ok {
		t.Error("an unknown size can't be checked: the entry must be ignored")
	}
	if _, ok := s.FactsForPath(ctx, "/movies/missing.mkv", 4<<30); ok {
		t.Error("no entry, no facts")
	}
	ix, err := s.FactsByPath(ctx, "movie")
	if err != nil {
		t.Fatal(err)
	}
	if f, ok := ix.Lookup("/movies/a.mkv", 4<<30); !ok || f.Codec != "av1" {
		t.Errorf("batch lookup: ok=%v facts=%+v", ok, f)
	}
	if _, ok := ix.Lookup("/movies/a.mkv", 1); ok {
		t.Error("the batch lookup applies the size gate too")
	}

	// An entry written by an older analysis may lack facts the decisions read.
	if _, err := s.index.db.ExecContext(ctx, `UPDATE convert_library SET info_ver = ? WHERE path = ?`, probeSchemaVersion-1, "/movies/a.mkv"); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.FactsForPath(ctx, "/movies/a.mkv", 4<<30); ok {
		t.Error("an old info_ver must fall back to the release name")
	}
	ix, _ = s.FactsByPath(ctx, "movie")
	if _, ok := ix.Lookup("/movies/a.mkv", 4<<30); ok {
		t.Error("the batch lookup skips old analyses too")
	}
}

// A profile-5 Dolby Vision picture has no HDR base to fall back on.
func TestFactsDolbyVisionNoFallback(t *testing.T) {
	f := Facts(&MediaInfo{VideoCodec: "hevc", Resolution: "2160p", HDR: "Dolby Vision", DVProfile: 5, DVBase: "SDR"})
	if !f.DolbyVision || !f.DVNoFallback {
		t.Errorf("profile 5: %+v", f)
	}
	f = Facts(&MediaInfo{VideoCodec: "hevc", Resolution: "2160p", HDR: "Dolby Vision", DVProfile: 8, DVBase: "HDR10"})
	if f.DVNoFallback || f.HDR != "HDR10" {
		t.Errorf("profile 8.1 falls back to HDR10: %+v", f)
	}
}
