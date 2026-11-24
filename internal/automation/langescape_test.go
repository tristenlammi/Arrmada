package automation

import (
	"context"
	"fmt"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/tristenlammi/arrmada/internal/library"
	"github.com/tristenlammi/arrmada/internal/quality"
)

// Issue #137 end to end: a file whose every audio language the profile avoids may be
// replaced by a lower-resolution release in a language it wants. The sweep grabs it and the
// import gates place it — the same verdict on both sides, so the grab is never a wasted
// download — and nothing else lower gets through either gate.

var (
	italianOverEnglish = map[string]string{"it": quality.PrefWant, "en": quality.PrefAvoid}
	italianOnly        = map[string]string{"it": quality.PrefWant}
)

// langEscapeShow is an upgrade show of an English-original series whose profile has the
// Audio language row row, with S04E06 on disk: a 2160p file recorded under an untagged
// release (read as English), whose one probed audio track is English.
func langEscapeShow(t *testing.T, row map[string]string) upgradeShow {
	t.Helper()
	return langEscapeShowFrom(t, row, "Show.S04E06.2160p.WEB-DL.x265-OLD.mkv")
}

// langEscapeShowFrom is langEscapeShow with the file recorded under source.
func langEscapeShowFrom(t *testing.T, row map[string]string, source string) upgradeShow {
	t.Helper()
	u := newUpgradeShow(t, false)
	sp, err := u.h.c.quality.Create(u.h.ctx, quality.StoredProfile{MediaType: quality.MediaSeries, Name: "Italian TV",
		UpgradesEnabled: true, AllowedResolutions: []string{"1080p", "2160p"}, Ideal: &quality.IdealFile{Language: row}})
	if err != nil {
		t.Fatal(err)
	}
	u.exec(t, `UPDATE series SET quality_profile = ?, extra_json = '{"original_language":"en"}' WHERE id = ?`, fmt.Sprintf("custom:%d", sp.ID), u.id)
	path := filepath.Join(t.TempDir(), "Show", "Season 04", "Show - S04E06.mkv")
	const size = int64(4 << 30)
	sparseFile(t, path, size)
	u.addEpisode(t, 4, 6, 36, path, source, size, false)
	u.h.c.SetFileFacts(fakeFacts{path: {size: size, facts: quality.FileFacts{Resolution: "2160p", Codec: "hevc", HDR: "SDR", AudioLangs: []string{"eng"}}}})
	return u
}

func TestSeriesLanguageEscapeIsGrabbedAndImported(t *testing.T) {
	u := langEscapeShow(t, italianOverEnglish)
	const ita = "Show S04E06 iTALiAN 1080p WEB-DL x264-SPQR"
	u.h.ix.offer(ita, "Show S04E06 MULTi 1080p WEB-DL x264-MIX")
	if err := u.h.c.upgradeSeries(u.h.ctx, u.id, nil); err != nil {
		t.Fatal(err)
	}
	if got := u.grabbedTitles(t); !reflect.DeepEqual(got, []string{ita}) {
		t.Fatalf("grabbed %v, want %s", got, ita)
	}
	if !importGateTakes(t, u, "Show.S04E06.iTALiAN.1080p.WEB-DL.x264-SPQR.mkv") {
		t.Error("the import gate refused the Italian release the sweep grabbed")
	}
	// A release that doesn't say it has Italian is still a downgrade.
	if importGateTakes(t, u, "Show.S04E06.MULTi.1080p.WEB-DL.x264-MIX.mkv") {
		t.Error("the import gate took a MULTi 1080p over the 2160p file")
	}
}

// English not avoided: the file isn't in the avoided tier, and resolution still governs on
// both sides.
func TestSeriesLanguageEscapeNeedsTheFileAvoided(t *testing.T) {
	u := langEscapeShow(t, italianOnly)
	u.h.ix.offer("Show S04E06 iTALiAN 1080p WEB-DL x264-SPQR")
	if err := u.h.c.upgradeSeries(u.h.ctx, u.id, nil); err != nil {
		t.Fatal(err)
	}
	if got := u.grabbedTitles(t); len(got) != 0 {
		t.Fatalf("grabbed %v over a 2160p file English doesn't avoid", got)
	}
	if importGateTakes(t, u, "Show.S04E06.iTALiAN.1080p.WEB-DL.x264-SPQR.mkv") {
		t.Error("the import gate took a lower resolution over a file that isn't avoided")
	}
}

// The escape's chain ends where it ended before #137: the Italian 1080p the escape placed
// is upgraded to a MULTi 2160p on resolution, and once that MULTi probes as English only,
// neither the sweep nor the import gate gives it up for another Italian 1080p — its
// recorded name doesn't say it carries only avoided languages.
func TestSeriesLanguageEscapeEndsAtAMULTi(t *testing.T) {
	u := langEscapeShowFrom(t, italianOverEnglish, "Show.S04E06.MULTi.2160p.WEB-DL.x265-MIX.mkv")
	u.h.ix.offer("Show S04E06 iTALiAN 1080p WEB-DL x264-SPQR", "Show S04E06 iTALiAN 1080p BluRay x264-ROMA")
	if err := u.h.c.upgradeSeries(u.h.ctx, u.id, nil); err != nil {
		t.Fatal(err)
	}
	if got := u.grabbedTitles(t); len(got) != 0 {
		t.Fatalf("grabbed %v over a MULTi 2160p file", got)
	}
	for _, file := range []string{"Show.S04E06.iTALiAN.1080p.WEB-DL.x264-SPQR.mkv", "Show.S04E06.iTALiAN.1080p.BluRay.x264-ROMA.mkv"} {
		if importGateTakes(t, u, file) {
			t.Errorf("the import gate took %s over the MULTi 2160p file", file)
		}
	}
}

// The movie import gate (MarkImportedTo's worse-quality check) takes the escape and
// nothing else lower.
func TestMovieImportGateTakesTheLanguageEscape(t *testing.T) {
	const engSource = "Dune.2021.ENG.2160p.WEB-DL.x265-OLD"
	cases := []struct {
		name, incoming string
		row            map[string]string
		want           bool
		// source is the release the file is recorded under (engSource when ""), and
		// engProbe gives the file an analysis that found one English track.
		source   string
		engProbe bool
	}{
		{"Italian 1080p over an English-only 2160p", "Dune.2021.iTALiAN.1080p.WEB-DL.x264-SPQR", italianOverEnglish, true, "", false},
		{"Italian 1080p over an untagged 2160p probed English", "Dune.2021.iTALiAN.1080p.WEB-DL.x264-SPQR", italianOverEnglish, true, "Dune.2021.2160p.WEB-DL.x265-OLD", true},
		{"MULTi 1080p", "Dune.2021.MULTi.1080p.WEB-DL.x264-MIX", italianOverEnglish, false, "", false},
		{"English 1080p", "Dune.2021.ENG.1080p.WEB-DL.x264-BRIT", italianOverEnglish, false, "", false},
		{"English not avoided", "Dune.2021.iTALiAN.1080p.WEB-DL.x264-SPQR", italianOnly, false, "", false},
		// The MULTi 2160p that replaced the escape's Italian 1080p, probed English only.
		{"Italian 1080p over a MULTi 2160p probed English", "Dune.2021.iTALiAN.1080p.BluRay.x264-ROMA", italianOverEnglish, false, "Dune.2021.MULTi.2160p.WEB-DL.x265-MIX", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			c, mv, _, st := attachTestCoord(t)
			c.quality = quality.NewService(st.DB())
			sp, err := c.quality.Create(ctx, quality.StoredProfile{MediaType: quality.MediaMovie, Name: "Italian",
				UpgradesEnabled: true, AllowedResolutions: []string{"1080p", "2160p"}, Ideal: &quality.IdealFile{Language: tc.row}})
			if err != nil {
				t.Fatal(err)
			}
			existing := filepath.Join(t.TempDir(), "Dune (2021)", "Dune.2021.2160p.WEB-DL.mkv")
			writeVideo(t, existing)
			source := tc.source
			if source == "" {
				source = engSource
			}
			mustExec(t, c, `INSERT INTO movies (id, tmdb_id, title, year, monitored, has_file, movie_file_path, source_release, quality_profile, extra_json, media_json)
				VALUES (7, 438631, 'Dune', 2021, 1, 1, ?, ?, ?, '{"original_language":"en"}', '{"size_bytes":1000}')`, existing, source, fmt.Sprintf("custom:%d", sp.ID))
			if tc.engProbe {
				c.SetFileFacts(stubFacts{existing: {Resolution: "2160p", Codec: "hevc", HDR: "SDR", AudioLangs: []string{"eng"}}})
			}
			incoming := filepath.Join(t.TempDir(), "Dune.2021.1080p.WEB-DL.mkv")
			writeVideo(t, incoming)
			out, err := c.AttachMovieImport(ctx, library.ImportRecord{
				Hash: "lang", TargetPath: incoming, Title: "Dune", Year: 2021, ReleaseName: tc.incoming,
			})
			if !tc.want {
				if out != library.Refused || err == nil {
					t.Fatalf("attach = %v, %v; want Refused", out, err)
				}
				return
			}
			if out != library.Attached || err != nil {
				t.Fatalf("attach = %v, %v; want Attached", out, err)
			}
			m, err := mv.Get(ctx, 7)
			if err != nil {
				t.Fatal(err)
			}
			if m.SourceRelease != tc.incoming || m.MovieFilePath != incoming {
				t.Errorf("movie holds %q (%s), want %q", m.SourceRelease, m.MovieFilePath, tc.incoming)
			}
		})
	}
}
