package parser

import (
	"reflect"
	"testing"
)

// Fix 1: year-titled shows. A name that OPENS with its only year token ("1923",
// "1984") cut the title at position 0 and yielded Title="". The token is the
// title; the cut falls back to the season/episode/quality marker.
func TestYearTitledShows(t *testing.T) {
	cases := []struct {
		name  string
		title string
		year  int
	}{
		{"1923.S01E01.1080p.WEB-DL.DDP5.1.H.264-NTb", "1923", 0},
		{"1984.S01E01.720p.HDTV.x264-GRP", "1984", 0},
		// A second distinct year: the first token is the title, the last the year.
		{"2012.2009.1080p.BluRay.x264-GRP", "2012", 2009},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := Parse(c.name)
			if r.Title != c.title || r.Year != c.year {
				t.Errorf("Title=%q Year=%d, want Title=%q Year=%d", r.Title, r.Year, c.title, c.year)
			}
		})
	}
}

// Fix 2: "Sxx COMPLETE" is a complete SEASON, not a complete show. It must stay
// a season pack covering only that season, or every season of the show would be
// considered covered by one S02 pack.
func TestSxxCompleteIsSeasonPack(t *testing.T) {
	cases := []struct {
		name     string
		kind     Kind
		complete bool
	}{
		{"The.Wire.S02.COMPLETE.1080p.BluRay.x264-GRP", KindSeasonPack, false},
		// A range or an explicit "Complete Series" keeps the current behavior.
		{"Elementary S01-07 Complete 1080p WEB x264-Mixed-TL", KindCompleteShow, true},
		{"Severance Complete Series 1080p BluRay", KindCompleteShow, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := Parse(c.name)
			if r.Kind() != c.kind || r.Complete != c.complete {
				t.Errorf("Kind=%d Complete=%v, want Kind=%d Complete=%v (parsed %+v)",
					r.Kind(), r.Complete, c.kind, c.complete, r)
			}
		})
	}
	r := Parse("The.Wire.S02.COMPLETE.1080p.BluRay.x264-GRP")
	if !r.CoversSeason(2) || r.CoversSeason(1) || r.CoversSeason(3) {
		t.Errorf("S02 COMPLETE must cover season 2 only, got %+v", r)
	}
}

// Fix 3: an explicit resolution token beats uhd/4k inference — "Hybrid.1080p.UHD"
// is a 1080p encode of a UHD source.
func TestExplicitResolutionTokenWins(t *testing.T) {
	cases := []struct {
		name string
		res  Resolution
	}{
		{"Dune.Part.Two.2024.Hybrid.1080p.UHD.BluRay.DV.HDR10-TAoE", Res1080p},
		{"Movie.2024.UHD.BluRay", Res2160p}, // inference still works alone
		{"Movie.2024.4K.WEB-DL", Res2160p},
		{"Movie.2024.2160p.UHD.BluRay", Res2160p},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Parse(c.name).Resolution; got != c.res {
				t.Errorf("Resolution = %q, want %q", got, c.res)
			}
		})
	}
}

// Fix 4: contains() must bound the RIGHT side of the token too — "Property"
// contains "propert", not the tag "proper". A trailing digit still matches so
// scene tags like REPACK2/PROPER2 keep working.
func TestProperRepackWordBoundary(t *testing.T) {
	cases := []struct {
		name   string
		proper bool
		repack bool
	}{
		{"Property.Brothers.S01E01.720p.HDTV.x264-GRP", false, false},
		{"Show.S01E01.REPACK2.720p.HDTV.x264-GRP", false, true},
		{"The.Matrix.1999.PROPER.1080p.BluRay.x264-GROUP", true, false},
		{"Show.S01E01.PROPER.REPACK.720p.HDTV-GRP", true, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := Parse(c.name)
			if r.Proper != c.proper || r.Repack != c.repack {
				t.Errorf("Proper=%v Repack=%v, want Proper=%v Repack=%v", r.Proper, r.Repack, c.proper, c.repack)
			}
		})
	}
}

// Fix 5: the bare " web " token is a real word in titles ("Charlottes Web",
// "Web of Lies"); explicit source tags must be checked before it.
func TestSourceOrderBareWebLast(t *testing.T) {
	cases := []struct {
		name string
		src  Source
	}{
		{"Charlottes.Web.2006.DVDRip.XviD-DoNE", SourceDVD},
		{"Web.of.Lies.S01E01.HDTV.x264-GRP", SourceHDTV},
		{"Show.2020.WEB.h264-GRP", SourceWebRip}, // bare web with nothing else: unchanged
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Parse(c.name).Source; got != c.src {
				t.Errorf("Source = %q, want %q", got, c.src)
			}
		})
	}
}

// Fix 6: trailing pack words are only stripped with pack evidence — a movie
// genuinely titled "The Box" or "The Pack" keeps its last word.
func TestPackWordsNeedPackEvidence(t *testing.T) {
	cases := []struct{ name, title string }{
		{"The.Box.2009.1080p.BluRay.x264-AMIABLE", "The Box"},
		{"The.Pack.2015.1080p.BluRay.x264-GRP", "The Pack"},
		// With pack evidence stripping still happens (see packword_test.go).
		{"The.Wire.collection.S01-S05.1080p", "The Wire"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Parse(c.name).Title; got != c.title {
				t.Errorf("Title = %q, want %q", got, c.title)
			}
		})
	}
}

// Fix 7: "S01.E05" (dot/space between season and episode) is an episode, not a
// full season pack.
func TestSpacedEpisodeForm(t *testing.T) {
	cases := []struct {
		name     string
		season   int
		episodes []int
		kind     Kind
	}{
		{"Show.S01.E05.720p.HDTV.x264", 1, []int{5}, KindEpisode},
		{"Show S01 E05 720p HDTV x264", 1, []int{5}, KindEpisode},
		{"Show.S01.E05.E06.720p.HDTV.x264", 1, []int{5, 6}, KindEpisode},
		// The plain forms are untouched.
		{"Show.S01E05.720p.HDTV.x264", 1, []int{5}, KindEpisode},
		{"Show.S03E21-E22.1080p.WEB-DL-GRP", 3, []int{21, 22}, KindEpisode},
		{"Shogun.S01.1080p.WEB-DL.DDP5.1-NTb", 1, nil, KindSeasonPack},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := Parse(c.name)
			if r.Season != c.season || !reflect.DeepEqual(r.Episodes, c.episodes) || r.Kind() != c.kind {
				t.Errorf("Season=%d Episodes=%v Kind=%d, want Season=%d Episodes=%v Kind=%d",
					r.Season, r.Episodes, r.Kind(), c.season, c.episodes, c.kind)
			}
		})
	}
}

// Fix 8a: "aac"/"flac" must be whole tokens — they hide inside real words.
func TestAudioNeedlesTokenBounded(t *testing.T) {
	cases := []struct {
		name  string
		audio []string
	}{
		{"Isaac.2024.1080p.WEB-DL.x264-GRP", nil},
		{"Flack.S01E01.720p.HDTV.x264-GRP", nil},
		{"Dune.Part.Two.2024.1080p.WEBRip.x265.AAC5.1-YTS", []string{"AAC"}},
		{"Album.Show.S01E01.1080p.FLAC.2.0-GRP", []string{"FLAC"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Parse(c.name).Audio; !reflect.DeepEqual(got, c.audio) {
				t.Errorf("Audio = %v, want %v", got, c.audio)
			}
		})
	}
}

// Fix 8b: a release group literally named "DV" is a tag, not Dolby Vision.
func TestGroupNamedDVIsNotHDR(t *testing.T) {
	cases := []struct {
		name string
		hdr  []string
	}{
		{"Movie.2024.2160p.WEB-DL.HDR10.x265-DV", []string{"HDR10"}},
		{"Movie.2024.2160p.DV.HDR10.x265-GRP", []string{"DV", "HDR10"}},
		{"Movie.2024.2160p.DV.x265-DV", []string{"DV"}}, // DV token elsewhere still counts
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := Parse(c.name)
			if !reflect.DeepEqual(r.HDR, c.hdr) {
				t.Errorf("HDR = %v, want %v (group %q)", r.HDR, c.hdr, r.Group)
			}
		})
	}
}
