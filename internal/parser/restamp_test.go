package parser

import "testing"

// The codec token is swapped in place, so a converted file's recorded release reads as
// the new codec and keeps its group.
func TestRestampCodec(t *testing.T) {
	cases := []struct {
		name, in string
		codec    Codec
		want     string
	}{
		{"H.264 to AV1", "Movie.2020.1080p.WEB.H.264-GRP", CodecAV1, "Movie.2020.1080p.WEB.AV1-GRP"},
		{"x265 to AV1", "Film.2021.2160p.BluRay.HDR.x265-GRP", CodecAV1, "Film.2021.2160p.BluRay.HDR.AV1-GRP"},
		{"H.264 to x265", "Film.2021.1080p.BluRay.x264-GRP", CodecX265, "Film.2021.1080p.BluRay.x265-GRP"},
		{"no codec token, inserted before the group", "Film.2021.1080p.BluRay-GRP", CodecAV1, "Film.2021.1080p.BluRay.AV1-GRP"},
		{"no group, appended", "Film.2021.1080p.BluRay", CodecX265, "Film.2021.1080p.BluRay.x265"},
		{"spaces, no group, appended", "Bambi 1942 1080p BluRay", CodecAV1, "Bambi 1942 1080p BluRay AV1"},
		{"already AV1", "Film.2021.1080p.BluRay.AV1-GRP", CodecAV1, "Film.2021.1080p.BluRay.AV1-GRP"},
		{"already HEVC reads as x265", "Film.2021.1080p.BluRay.HEVC-GRP", CodecX265, "Film.2021.1080p.BluRay.HEVC-GRP"},
		{"H.264 with spaces", "Movie 2020 1080p WEB H 264-GRP", CodecAV1, "Movie 2020 1080p WEB AV1-GRP"},
		{"two codec tokens, the second dropped", "Film.2021.1080p.BluRay.HEVC.x265-GRP", CodecAV1, "Film.2021.1080p.BluRay.AV1-GRP"},
		{"AVC inside a word untouched", "Film.2021.1080p.XAVC.BluRay.x264-GRP", CodecAV1, "Film.2021.1080p.XAVC.BluRay.AV1-GRP"},
		{"WEB-DL is not a group", "Film.2021.1080p.WEB-DL", CodecAV1, "Film.2021.1080p.WEB-DL.AV1"},
		{"container extension kept last", "Film.2021.1080p.BluRay.x264-GRP.mkv", CodecAV1, "Film.2021.1080p.BluRay.AV1-GRP.mkv"},
		{"unknown target leaves it alone", "Film.2021.1080p.BluRay.x264-GRP", CodecUnknown, "Film.2021.1080p.BluRay.x264-GRP"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := RestampCodec(c.in, c.codec)
			if got != c.want {
				t.Fatalf("RestampCodec(%q, %s) = %q, want %q", c.in, c.codec, got, c.want)
			}
			if c.codec == CodecUnknown {
				return
			}
			r := Parse(got)
			if r.Codec != c.codec {
				t.Errorf("%q parses as %q, want %q", got, r.Codec, c.codec)
			}
			// "DL" off "WEB-DL" was never a real group, so losing it is the point.
			if want := Parse(c.in).Group; r.Group != want && want != "DL" {
				t.Errorf("%q lost its group: %q, want %q", got, r.Group, want)
			}
		})
	}
}

// The acceptance pair: a legacy name restamped to AV1 parses as AV1 with its group.
func TestRestampCodecParsesBack(t *testing.T) {
	r := Parse(RestampCodec("Movie.2020.1080p.WEB.H.264-GRP", CodecAV1))
	if r.Codec != CodecAV1 || r.Group != "GRP" {
		t.Errorf("parsed = codec %q group %q, want AV1/GRP", r.Codec, r.Group)
	}
}

// WithoutCodec is the release's identity apart from its codec, so a converted file's
// release and the one it was converted from compare equal, and different releases don't.
func TestWithoutCodec(t *testing.T) {
	src := "Film.2021.1080p.BluRay.H.264-GRP"
	for _, other := range []string{
		RestampCodec(src, CodecAV1),
		RestampCodec(src, CodecX265),
		"Film.2021.1080p.BluRay.H.264-GRP AV1", // the legacy appended stamp
		"film 2021 1080p bluray x264 grp",
	} {
		if WithoutCodec(other) != WithoutCodec(src) {
			t.Errorf("WithoutCodec(%q) = %q, want it equal to %q", other, WithoutCodec(other), WithoutCodec(src))
		}
	}
	if got := WithoutCodec(src); got != "film 2021 1080p bluray grp" {
		t.Errorf("WithoutCodec(%q) = %q", src, got)
	}
	for _, other := range []string{"Film.2021.1080p.BluRay.x265-OTHER", "Film.2021.2160p.BluRay.x264-GRP", "Film.2021.1080p.BluRay.PROPER.x264-GRP"} {
		if WithoutCodec(other) == WithoutCodec(src) {
			t.Errorf("%q should not equal %q apart from the codec", other, src)
		}
	}
}
