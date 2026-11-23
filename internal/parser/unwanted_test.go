package parser

import (
	"reflect"
	"testing"
)

func TestFullDisc(t *testing.T) {
	cases := []struct {
		name string
		want bool
	}{
		// Disc images.
		{"Movie.2020.1080p.Blu-ray.AVC.DTS-HD.MA.5.1-GRP", true},
		{"Movie.2020.2160p.UHD.BD66.HEVC.TrueHD.Atmos-GRP", true},
		{"Movie.2020.2160p.UHD.Blu-ray.HEVC.DTS-HD.MA-GRP", true}, // HEVC at 2160p, no encoder tag
		{"Movie.2020.BDMV.1080p-GRP", true},
		{"Movie 2020 COMPLETE UHD BLURAY-GRP", true},
		{"Movie.2020.COMPLETE.BLURAY-GRP", true},
		{"Movie.2020.1080p.BR-DISK-GRP", true},
		{"Movie.2020.3D.BD.1080p-GRP", true},
		{"Movie.2020.Full.Blu-ray.1080p-GRP", true},
		{"Movie.2020.1080p.BluRay.VC-1.DTS-HD.MA-GRP", true},
		{"Movie.2020.BD50.1080p.AVC-GRP", true},
		{"Movie.2020.1080p.BluRay.ISO-GRP", true},
		// Encodes and remuxes.
		{"Movie.2020.1080p.BluRay.REMUX.AVC.DTS-HD.MA", false},
		{"Movie.2020.1080p.BluRay.x264", false},
		{"Movie.2020.1080p.BluRay.HEVC.10bit-GRP", false}, // a 1080p HEVC release is an encode
		{"Movie.2020.2160p.UHD.BluRay.x265.HDR-GRP", false},
		{"Movie.2020.720p.BluRay.AVC-GRP", false},
		{"Movie.2020.1080p.BluRay.H.264.DTS-GRP", false},
		{"Movie.2020.1080p.BDRip.AVC-GRP", false},
		{"Movie.2020.1080p.WEB-DL.AVC.DDP5.1-GRP", false},
		{"Movie.2020.German.DL.1080p.BluRay.AVC-GRP", false},   // German remux convention
		{"[Group] Show (BD 1080p AVC FLAC) [ABCD1234]", false}, // fansub BD encode
		{"Show.S01.COMPLETE.1080p.BluRay.x264-GRP", false},
		// A disc word in the title is the title.
		{"Blu-ray.Dreams.2020.1080p.WEB-DL.AVC-GRP", false},
	}
	for _, tc := range cases {
		if got := Parse(tc.name).FullDisc; got != tc.want {
			t.Errorf("Parse(%q).FullDisc = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestThreeDUpscaledExtras(t *testing.T) {
	cases := []struct {
		name                     string
		threeD, upscaled, extras bool
	}{
		{"Movie.2010.3D.1080p.BluRay.Half-SBS.x264-GRP", true, false, false},
		{"Movie.2010.1080p.BluRay.HSBS.x264-GRP", true, false, false},
		{"Movie.2010.1080p.BluRay.H-SBS.x264-GRP", true, false, false},
		{"Movie.2010.1080p.BluRay.Half-OU.x264-GRP", true, false, false},
		{"Movie.2010.1080p.BluRay.HOU.x264-GRP", true, false, false},
		{"Movie.1990.2160p.UPSCALED.WEB-DL.x265-GRP", false, true, false},
		{"Movie.1990.2160p.AI.UPSCALE.x265-GRP", false, true, false},
		{"Movie.1990.2160p.Regrade.x265-GRP", false, false, false},
		{"Movie.2020.EXTRAS.1080p.BluRay.x264-GRP", false, false, true},
		{"Movie.2020.Featurettes.1080p.BluRay.x264-GRP", false, false, true},
		{"Movie.2020.Bonus.Disc.1080p.BluRay.x264-GRP", false, false, true},
		{"Movie.2020.Special.Features.720p.BluRay.x264-GRP", false, false, true},
		{"Movie.2020.Behind.The.Scenes.720p.WEB-DL-GRP", false, false, true},
		{"Show.S02.Extras.1080p.BluRay.x264-GRP", false, false, true},
		// Title words are titles.
		{"The.3D.Man.2020.1080p.WEB-DL.x264-GRP", false, false, false},
		{"Extras.2005.S01E01.720p.BluRay.x264-GRP", false, false, false},
		{"Bonus.Track.2020.1080p.WEB-DL-GRP", false, false, false},
		// An episode title isn't a tag either.
		{"Show.S01E05.Behind.the.Scenes.1080p.WEB-DL-GRP", false, false, false},
		{"Show.S01E05.The.Extras.720p.HDTV.x264-GRP", false, false, false},
		// A group named after a tag is a group.
		{"Movie.2020.1080p.BluRay.x264-3D", false, false, false},
	}
	for _, tc := range cases {
		r := Parse(tc.name)
		if r.ThreeD != tc.threeD || r.Upscaled != tc.upscaled || r.Extras != tc.extras {
			t.Errorf("Parse(%q): 3D=%v upscaled=%v extras=%v, want %v %v %v", tc.name,
				r.ThreeD, r.Upscaled, r.Extras, tc.threeD, tc.upscaled, tc.extras)
		}
	}
}

func TestLanguages(t *testing.T) {
	cases := []struct {
		name  string
		langs []string
		multi bool
	}{
		{"Film.2019.FRENCH.1080p.WEB.H264-GRP", []string{"fr"}, false},
		{"Film.2019.TRUEFRENCH.1080p.BluRay.x264-GRP", []string{"fr"}, false},
		{"Film.2019.VFF.1080p.BluRay.x264-GRP", []string{"fr"}, false},
		{"Film.2019.VFQ.1080p.BluRay.x264-GRP", []string{"fr"}, false},
		{"Movie.2020.MULTi.1080p.WEB.H264-GRP", nil, true},
		{"Movie.2020.MULTi.TRUEFRENCH.1080p.WEB.H264-GRP", []string{"fr"}, true},
		{"Movie.2020.German.DL.1080p.BluRay.x264-GRP", []string{"de"}, true},
		{"Movie.2020.GERMAN.ML.1080p.BluRay.x264-GRP", []string{"de"}, true},
		{"Movie.2020.GERMAN.1080p.WEB-DL.x264-GRP", []string{"de"}, false}, // WEB-DL's DL isn't dual language
		{"Movie.2020.iTALiAN.1080p.BluRay.x264-GRP", []string{"it"}, false},
		{"Movie.2020.SPANiSH.1080p.WEB-DL-GRP", []string{"es"}, false},
		{"Movie.2020.CASTELLANO.1080p.WEB-DL-GRP", []string{"es"}, false},
		{"Movie.2020.LATINO.1080p.WEB-DL-GRP", []string{"es"}, false},
		{"Movie.2020.RUSSIAN.1080p.WEB-DL-GRP", []string{"ru"}, false},
		{"Movie.2020.HINDI.1080p.WEB-DL-GRP", []string{"hi"}, false},
		{"Anime.2020.JAPANESE.1080p.BluRay.x264-GRP", []string{"ja"}, false},
		{"Movie.2020.KOREAN.1080p.WEB-DL-GRP", []string{"ko"}, false},
		{"Movie.2020.ITA.ENG.1080p.BluRay.x264-GRP", []string{"it", "en"}, false},
		{"[Group] Show - 05 (1080p) [Dual Audio] [ABCD1234]", nil, true},
		// What the audio-language rules (LANG) read as well.
		{"Movie.2020.Dual.Language.1080p.BluRay.x264-GRP", nil, true},
		{"Movie.2020.MULTi.GERMAN.DL.1080p.BluRay.x264-GRP", []string{"de"}, true},
		{"Show.S01E01.German.DL.1080p.WEB.h264-GRP", []string{"de"}, true},
		{"Movie.2020.PLDUB.1080p.WEB-DL.x264-GRP", []string{"pl"}, false},
		{"Movie.2020.DUBPL.1080p.WEB-DL.x264-GRP", []string{"pl"}, false},
		{"Movie.2020.POL.ENG.1080p.BluRay.x264-GRP", []string{"pl", "en"}, false},
		{"Movie.2020.MULTi.POR.1080p.BluRay.x264-GRP", []string{"pt"}, true},
		{"Movie.2020.DUT.1080p.WEB-DL.x264-GRP", []string{"nl"}, false},
		{"Movie.2020.HUN.ENG.1080p.WEB-DL.x264-GRP", []string{"hu", "en"}, false},
		{"Movie.2020.SPANiSH.ENG.1080p.WEB-DL.x264-GRP", []string{"es", "en"}, false},
		{"Movie.2020.ITA.1080p.WEB-DL.x264-GRP", []string{"it"}, false},
		// The spellings a Must audio language is read from (issue #58): a name must say it.
		{"Movie.2020.MULTi.ITA.1080p.BluRay.x264-GRP", []string{"it"}, true},
		{"Movie.2020.iTA.ENG.1080p.BluRay.x264-GRP", []string{"it", "en"}, false},
		{"Movie.2020.ITA-ENG.1080p.BluRay.x264-GRP", []string{"it", "en"}, false},
		{"Movie.2020.Italian.1080p.WEB-DL.x264-GRP", []string{"it"}, false},
		{"Movie.2020.VFB.1080p.WEB-DL.x264-GRP", []string{"fr"}, false},
		{"Movie.2020.MULTi.VF2.1080p.WEB-DL.x264-GRP", []string{"fr"}, true},
		{"Movie.2020.FRA.ENG.1080p.WEB-DL.x264-GRP", []string{"fr", "en"}, false},
		{"Movie.2020.DEU.1080p.WEB-DL.x264-GRP", []string{"de"}, false},
		{"Movie.2020.NLD.1080p.WEB-DL.x264-GRP", []string{"nl"}, false},
		{"Movie.2020.ESP.ENG.1080p.WEB-DL.x264-GRP", []string{"es", "en"}, false},
		{"Anime.2020.JAP.ENG.1080p.BluRay.x264-GRP", []string{"ja", "en"}, false},
		// Lower case, they stay words ("esp" and "fra" are in plenty of titles), and so does
		// mixed case: VfB Stuttgart is a football club, not Belgian French.
		{"Movie.2020.1080p.WEB-DL.esp-GRP", nil, false},
		{"VfB.Stuttgart.vs.Bayern.2024.1080p.WEB.h264-GRP", nil, false},
		{"Bundesliga.2024.VfB.Stuttgart.vs.Bayern.1080p.WEB.h264-GRP", nil, false},
		// Where an episode title sits, the newer codes stay words even in capitals.
		{"Show.S01E02.The.JAP.Problem.1080p.WEB.h264-GRP", nil, false},
		{"Show.S01E02.ESP.vs.FRA.1080p.WEB.h264-GRP", nil, false},
		// German TV dubs where an episode title would sit: alone before the technical tokens,
		// or with a dub tag after it.
		{"Show.S01E01.German.720p.WEB.h264-GRP", []string{"de"}, false},
		{"Show.S01E01.German.Dubbed.720p.WEB.h264-GRP", []string{"de"}, false},
		{"Show.S01E01.German.AC3.720p.WEB.h264-GRP", []string{"de"}, false},
		{"Show.S01.German.720p.WEB.h264-GRP", []string{"de"}, false},
		// …but a language word in a longer episode title is part of the title.
		{"Show.S01E01.Going.Dutch.720p.WEB.h264-GRP", nil, false},
		{"Show.S02E04.The.German.Spy.1080p.WEB.h264-GRP", nil, false},
		// Lower-case codes stay words, and so do the codes that are everyday words.
		{"Movie.2020.1080p.WEB-DL.pol-GRP", nil, false},
		{"Movie.2020.FIN.1080p.WEB-DL.x264-GRP", nil, false},
		{"Movie.2020.NOR.1080p.WEB-DL.x264-GRP", nil, false},
		{"Show.S01E01.FRENCH.1080p.WEB.H264-GRP", []string{"fr"}, false},
		// Subtitles aren't audio.
		{"Movie.2020.German.Subbed.1080p.WEB-DL-GRP", nil, false},
		{"Movie.2020.ENG.SUBS.1080p.WEB-DL-GRP", nil, false},
		{"Movie.2020.MULTi.SUBS.1080p.WEB-DL-GRP", nil, false},
		{"Movie.2020.VOSTFR.1080p.WEB-DL-GRP", nil, false},
		// Italian trackers write the subtitles after the word (issue #58): "ENG Sub ITA" is
		// English audio with Italian subtitles, never Italian audio.
		{"Show.S01E03.1080p.WEB.ENG.Sub.ITA.H264-GRP", []string{"en"}, false},
		{"Show.S01E03.1080p.WEB-DL.ENG.SUB-ITA.H264-GRP", []string{"en"}, false},
		{"Show.S01E03.1080p.WEB-DL.H264.Sub.ITA-GRP", nil, false},
		{"Show.S01E03.1080p.WEB-DL.ENG.Subs.ITA.ENG.H264-GRP", []string{"en"}, false},
		{"Show.S01E03.1080p.WEB-DL.ITA.ENG.Sub.ITA.ENG.H264-GRP", []string{"it", "en"}, false},
		{"Show.S01E03.1080p.WEB.MULTi.Sub.ITA.H264-GRP", nil, false},
		{"Movie.2020.1080p.WEB-DL.Subbed.Italian-GRP", nil, false},
		{"Movie.2020.SUBITA.1080p.WEB-DL-GRP", nil, false},
		// Titles and episode titles aren't tags.
		{"French.Kiss.1995.1080p.BluRay.x264-GRP", nil, false},
		{"The.Italian.Job.2003.1080p.BluRay.x264-GRP", nil, false},
		{"Show.S01E03.The.French.Connection.1080p.WEB-DL-GRP", nil, false},
		// Lower-case three-letter codes are words.
		{"Movie.2020.1080p.WEB-DL.ita-GRP", nil, false},
		// Ordinary English releases state no language.
		{"Movie.2020.1080p.BluRay.DTS-HD.MA.5.1.x264-GRP", nil, false},
		{"Movie.2020.2160p.UHD.BluRay.REMUX.HDR.HEVC.Atmos-GRP", nil, false},
	}
	for _, tc := range cases {
		r := Parse(tc.name)
		if !reflect.DeepEqual(r.Languages, tc.langs) || r.Multi != tc.multi {
			t.Errorf("Parse(%q): languages=%v multi=%v, want %v %v", tc.name, r.Languages, r.Multi, tc.langs, tc.multi)
		}
	}
}

// FileLanguages (ForFile) is the name read without the spellings added for issue #58, so a
// library file's verdict doesn't move; a name without them reads the same both ways.
func TestFileLanguagesLeaveOutNewSpellings(t *testing.T) {
	cases := []struct {
		name      string
		file      []string
		fileMulti bool
	}{
		{"Movie.2020.ESP.ENG.1080p.WEB-DL.x264-GRP", []string{"en"}, false},
		{"Movie.2020.FRA.1080p.WEB-DL.x264-GRP", nil, false},
		{"Movie.2020.DEU.DL.1080p.BluRay.x264-GRP", nil, false}, // DL needs a language beside it
		{"Movie.2020.MULTi.VFB.1080p.WEB-DL.x264-GRP", nil, true},
		{"Movie.2020.ITA.ENG.1080p.BluRay.x264-GRP", []string{"it", "en"}, false},
		{"Movie.2020.German.DL.1080p.BluRay.x264-GRP", []string{"de"}, true},
		// Subtitles named after the word are subtitles for a file too (issue #58): the old
		// reading took this for Italian audio.
		{"Show.S01E03.1080p.WEB.ENG.Sub.ITA.H264-GRP", []string{"en"}, false},
	}
	for _, tc := range cases {
		r := Parse(tc.name)
		f := r.ForFile()
		if !reflect.DeepEqual(f.Languages, tc.file) || f.Multi != tc.fileMulti {
			t.Errorf("%s: for a file %v multi %v, want %v %v (release reads %v %v)", tc.name, f.Languages, f.Multi, tc.file, tc.fileMulti, r.Languages, r.Multi)
		}
	}
	// A Release Parse didn't make keeps what it was given.
	if got := (Release{Languages: []string{"fr"}}).ForFile(); !reflect.DeepEqual(got.Languages, []string{"fr"}) {
		t.Errorf("hand-made release lost its languages: %v", got.Languages)
	}
}

// Common tag words must never read as a language, whatever the table knows.
// DUAL / Dual-Audio is told apart from MULTi: on a foreign film it's original + English.
func TestDualAudioTag(t *testing.T) {
	for name, dual := range map[string]bool{
		"[Group] Show - 05 (1080p) [Dual Audio] [ABCD1234]": true,
		"Show.S01E05.1080p.BluRay.x264.Dual-Audio-GRP":      true,
		"Show.S01E05.1080p.WEB.x264.MULTi-GRP":              false,
		"Movie.2020.German.DL.1080p.BluRay.x264-GRP":        false,
	} {
		if r := Parse(name); r.Dual != dual || !r.Multi {
			t.Errorf("Parse(%q): dual=%v multi=%v, want dual %v and multi", name, r.Dual, r.Multi, dual)
		}
	}
}

func TestTagWordsAreNotLanguages(t *testing.T) {
	words := []string{"Atmos", "TrueHD", "BluRay", "REMUX", "HDR10", "PROPER", "REPACK", "INTERNAL",
		"EXTENDED", "IMAX", "UNCUT", "COMPLETE", "Hybrid", "DUBBED", "LIMITED", "UNCENSORED", "CRITERION",
		"REMASTERED", "UNRATED", "THEATRICAL", "DIRECTORS", "Collection", "Edition", "DOCU", "FESTIVAL",
		"READNFO", "RERIP", "SAMPLE", "HEVC", "x265", "10bit", "DDP5", "AAC2", "FLAC", "OPUS", "DoVi",
		"HDTV", "AMZN", "WEBRip", "DSNP", "Rifftrax", "Colorized", "Open.Matte", "SUBBED", "Retail"}
	for _, w := range words {
		name := "Movie.2020." + w + ".1080p.WEB-DL.x264-GRP"
		if r := Parse(name); len(r.Languages) > 0 {
			t.Errorf("Parse(%q).Languages = %v, want none", name, r.Languages)
		}
	}
}

func TestStreaming(t *testing.T) {
	cases := []struct{ name, want string }{
		{"Show.S01E01.1080p.AMZN.WEB-DL.DDP5.1.H.264-GRP", "AMZN"},
		{"Show.S01E01.1080p.NF.WEB-DL.DDP5.1.x264-GRP", "NF"},
		{"Movie.2020.2160p.ATVP.WEB-DL.DDP5.1.Atmos.DV.H.265-GRP", "ATVP"},
		{"Movie.2020.1080p.DSNP.WEB-DL-GRP", "DSNP"},
		{"Movie.2020.1080p.HMAX.WEB-DL-GRP", "HMAX"},
		{"Movie.2020.1080p.MAX.WEB-DL-GRP", "MAX"},
		{"Show.S01E01.1080p.PCOK.WEB-DL-GRP", "PCOK"},
		{"Show.S01E01.1080p.PMTP.WEB-DL-GRP", "PMTP"},
		{"Show.S01E01.1080p.HULU.WEB-DL-GRP", "HULU"},
		{"Show.S01E01.1080p.CRAV.WEB-DL-GRP", "CRAV"},
		{"Show.S01E01.1080p.STAN.WEB-DL-GRP", "STAN"},
		{"Movie.2020.1080p.amzn.webrip-GRP", "AMZN"},
		{"Movie.2020.1080p.MA.WEB-DL.DDP5.1-GRP", "MA"},
		{"Movie.2020.1080p.WEB-DL.MA.DDP5.1-GRP", "MA"},
		{"Movie.2020.1080p.iT.WEB-DL.DDP5.1-GRP", "iT"},
		// MA beside an audio tag is DTS-HD Master Audio.
		{"Movie.2020.1080p.BluRay.DTS-HD.MA.5.1-GRP", ""},
		{"Movie.2020.1080p.WEB-DL.DTS-HD.MA.5.1-GRP", ""},
		// iT only in its exact case beside WEB.
		{"Movie.2020.1080p.IT.WEB-DL-GRP", ""},
		{"Movie.2020.1080p.iT.BluRay-GRP", ""},
		// Short services only in capitals.
		{"Movie.2020.1080p.Max.WEB-DL-GRP", ""},
		{"Show.S01E01.Stan.Lee.1080p.WEB-DL-GRP", ""},
		// Title words and groups aren't services.
		{"Mad.Max.1979.1080p.BluRay.x264-GRP", ""},
		{"Movie.2020.1080p.WEB-DL.x264-NF", ""},
	}
	for _, tc := range cases {
		if got := Parse(tc.name).Streaming; got != tc.want {
			t.Errorf("Parse(%q).Streaming = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// The traits don't disturb anything the parser already read.
func TestTraitsLeaveTheRestAlone(t *testing.T) {
	r := Parse("Film.2019.FRENCH.1080p.AMZN.WEB-DL.DDP5.1.H.264-GRP")
	if r.Title != "Film" || r.Year != 2019 || r.Resolution != Res1080p || r.Source != SourceWebDL || r.Group != "GRP" {
		t.Fatalf("parse = %+v", r)
	}
}
