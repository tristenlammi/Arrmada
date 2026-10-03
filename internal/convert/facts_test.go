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
