package parser

import (
	"reflect"
	"testing"
)

func TestParseMovies(t *testing.T) {
	cases := []struct {
		name string
		want Release
	}{
		{
			"Dune.Part.Two.2024.2160p.UHD.BluRay.REMUX.DV.HDR.TrueHD.Atmos.7.1-FraMeSToR",
			Release{
				Title: "Dune Part Two", Year: 2024, Resolution: Res2160p, Source: SourceRemux,
				HDR: []string{"DV", "HDR10"}, Audio: []string{"Atmos", "TrueHD"}, Group: "FraMeSToR",
			},
		},
		{
			"Dune.Part.Two.2024.2160p.WEB-DL.DDP5.1.Atmos.DV.HDR.H.265-FLUX",
			Release{
				Title: "Dune Part Two", Year: 2024, Resolution: Res2160p, Source: SourceWebDL,
				Codec: CodecX265, HDR: []string{"DV", "HDR10"}, Audio: []string{"Atmos", "DDP"}, Group: "FLUX",
			},
		},
		{
			"Dune.Part.Two.2024.1080p.BluRay.x264.DTS-HD.MA.5.1-SPARKS",
			Release{
				Title: "Dune Part Two", Year: 2024, Resolution: Res1080p, Source: SourceBluray,
				Codec: CodecX264, Audio: []string{"DTS-HD"}, Group: "SPARKS",
			},
		},
		{
			"Dune.Part.Two.2024.1080p.WEBRip.x265.AAC5.1-YTS",
			Release{
				Title: "Dune Part Two", Year: 2024, Resolution: Res1080p, Source: SourceWebRip,
				Codec: CodecX265, Audio: []string{"AAC"}, Group: "YTS",
			},
		},
		{
			"Blade.Runner.2049.2016.IMAX.2160p.WEB-DL.DDP5.1.HDR.H.265-RUMOUR",
			Release{
				Title: "Blade Runner 2049", Year: 2016, Resolution: Res2160p, Source: SourceWebDL,
				Codec: CodecX265, HDR: []string{"HDR10"}, Audio: []string{"DDP"}, Edition: "IMAX", Group: "RUMOUR",
			},
		},
		{
			"The.Matrix.1999.PROPER.1080p.BluRay.x264-GROUP",
			Release{
				Title: "The Matrix", Year: 1999, Resolution: Res1080p, Source: SourceBluray,
				Codec: CodecX264, Proper: true, Group: "GROUP",
			},
		},
		{
			// Parenthesized year (1337x-style names) — must not leave a stray "(".
			"The Matrix (1999) 720p BrRip x264-YIFY",
			Release{
				Title: "The Matrix", Year: 1999, Resolution: Res720p, Source: SourceBluray,
				Codec: CodecX264, Group: "YIFY",
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Parse(tc.name)
			assertRelease(t, got, tc.want)
		})
	}
}

func TestParseTV(t *testing.T) {
	cases := []struct {
		name string
		want Release
	}{
		{
			"Andor.S02E01.1080p.WEB-DL.DDP5.1.H.264-NTb",
			Release{
				Title: "Andor", Resolution: Res1080p, Source: SourceWebDL, Codec: CodecX264,
				Audio: []string{"DDP"}, Group: "NTb", Season: 2, Episodes: []int{1},
			},
		},
		{
			"Shogun.S01.1080p.WEB-DL.DDP5.1-NTb",
			Release{
				Title: "Shogun", Resolution: Res1080p, Source: SourceWebDL,
				Audio: []string{"DDP"}, Group: "NTb", Season: 1,
			},
		},
		{
			// Double-episode file with no second "E" — both episodes must parse.
			"Gotham - S03E21-22 - Heroes Rise Destiny Calling",
			Release{Title: "Gotham", Season: 3, Episodes: []int{21, 22}},
		},
		{
			"Show.S03E21-E22.1080p.WEB-DL-GRP",
			Release{Title: "Show", Resolution: Res1080p, Source: SourceWebDL, Group: "GRP", Season: 3, Episodes: []int{21, 22}},
		},
		{
			"Show.S01E01E02.720p.HDTV",
			Release{Title: "Show", Resolution: Res720p, Source: SourceHDTV, Season: 1, Episodes: []int{1, 2}},
		},
		{
			// A bare "-1080p" after the episode must NOT read as episode 1080/108.
			"Show.S01E01-1080p.WEB-DL-GRP",
			Release{Title: "Show", Resolution: Res1080p, Source: SourceWebDL, Group: "GRP", Season: 1, Episodes: []int{1}},
		},
		{
			// Anime: leading [Group] + absolute episode number.
			"[SubsPlease] Hunter x Hunter - 137 (1080p) [ABCD1234]",
			Release{Title: "Hunter x Hunter", Resolution: Res1080p, Group: "SubsPlease", AbsoluteEpisodes: []int{137}},
		},
		{
			"[Erai-raws] Some Show - 12v2 [1080p]",
			Release{Title: "Some Show", Resolution: Res1080p, Group: "Erai-raws", AbsoluteEpisodes: []int{12}},
		},
		{
			// Anime batch → absolute range.
			"[Judas] Some Show - 01-03 [1080p]",
			Release{Title: "Some Show", Resolution: Res1080p, Group: "Judas", AbsoluteEpisodes: []int{1, 2, 3}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertRelease(t, Parse(tc.name), tc.want)
		})
	}
}

func TestHelpers(t *testing.T) {
	r := Parse("Foo.2020.2160p.WEB-DL.DV.HDR.Atmos-X")
	if !r.HasHDR() {
		t.Error("expected HasHDR true")
	}
	if !r.HasAudio("Atmos") {
		t.Error("expected HasAudio(Atmos) true")
	}
	if r.IsTV() {
		t.Error("movie should not be TV")
	}
	if Parse("Show.S03E04.720p.HDTV.x264-Y").IsTV() != true {
		t.Error("expected IsTV true")
	}
}

func assertRelease(t *testing.T, got, want Release) {
	t.Helper()
	if got.Title != want.Title {
		t.Errorf("Title = %q, want %q", got.Title, want.Title)
	}
	if got.Year != want.Year {
		t.Errorf("Year = %d, want %d", got.Year, want.Year)
	}
	if got.Resolution != want.Resolution {
		t.Errorf("Resolution = %q, want %q", got.Resolution, want.Resolution)
	}
	if got.Source != want.Source {
		t.Errorf("Source = %q, want %q", got.Source, want.Source)
	}
	if got.Codec != want.Codec {
		t.Errorf("Codec = %q, want %q", got.Codec, want.Codec)
	}
	if want.HDR != nil && !reflect.DeepEqual(got.HDR, want.HDR) {
		t.Errorf("HDR = %v, want %v", got.HDR, want.HDR)
	}
	if want.Audio != nil && !reflect.DeepEqual(got.Audio, want.Audio) {
		t.Errorf("Audio = %v, want %v", got.Audio, want.Audio)
	}
	if got.Edition != want.Edition {
		t.Errorf("Edition = %q, want %q", got.Edition, want.Edition)
	}
	if got.Group != want.Group {
		t.Errorf("Group = %q, want %q", got.Group, want.Group)
	}
	if got.Proper != want.Proper {
		t.Errorf("Proper = %v, want %v", got.Proper, want.Proper)
	}
	if got.Season != want.Season {
		t.Errorf("Season = %d, want %d", got.Season, want.Season)
	}
	if want.Episodes != nil && !reflect.DeepEqual(got.Episodes, want.Episodes) {
		t.Errorf("Episodes = %v, want %v", got.Episodes, want.Episodes)
	}
}
