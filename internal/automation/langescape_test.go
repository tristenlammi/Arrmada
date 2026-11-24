package automation

import (
	"context"
	"fmt"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/tristenlammi/arrmada/internal/library"
	"github.com/tristenlammi/arrmada/internal/movies"
	"github.com/tristenlammi/arrmada/internal/parser"
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
	u, _ := langEscapeShowWith(t, quality.StoredProfile{AllowedResolutions: []string{"1080p", "2160p"}, Ideal: &quality.IdealFile{Language: row}}, source)
	return u
}

// langEscapeEpisodeBytes is the size of langEscapeShowWith's S04E06 file.
const langEscapeEpisodeBytes = int64(4 << 30)

// langEscapeShowWith is langEscapeShowFrom with the profile sp (made an upgrading series
// profile), returning the episode file's path too.
func langEscapeShowWith(t *testing.T, sp quality.StoredProfile, source string) (upgradeShow, string) {
	t.Helper()
	u := newUpgradeShow(t, false)
	sp.MediaType, sp.Name, sp.UpgradesEnabled = quality.MediaSeries, "Italian TV", true
	sp, err := u.h.c.quality.Create(u.h.ctx, sp)
	if err != nil {
		t.Fatal(err)
	}
	u.exec(t, `UPDATE series SET quality_profile = ?, extra_json = '{"original_language":"en"}' WHERE id = ?`, fmt.Sprintf("custom:%d", sp.ID), u.id)
	path := filepath.Join(t.TempDir(), "Show", "Season 04", "Show - S04E06.mkv")
	sparseFile(t, path, langEscapeEpisodeBytes)
	u.addEpisode(t, 4, 6, 36, path, source, langEscapeEpisodeBytes, false)
	u.h.c.SetFileFacts(fakeFacts{path: {size: langEscapeEpisodeBytes, facts: quality.FileFacts{Resolution: "2160p", Codec: "hevc", HDR: "SDR", AudioLangs: []string{"eng"}}}})
	return u, path
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

// placeEpisode stands in for an import having placed a file on S04E06: the episode is
// recorded under source, and the file's analysis reads res with one audio track lang.
func placeEpisode(t *testing.T, u upgradeShow, path, source, res, lang string) {
	t.Helper()
	u.exec(t, `UPDATE episodes SET source_release = ? WHERE series_id = ? AND season_number = 4 AND episode_number = 6`, source, u.id)
	u.h.c.SetFileFacts(fakeFacts{path: {size: langEscapeEpisodeBytes, facts: quality.FileFacts{Resolution: res, Codec: "h264", HDR: "SDR", AudioLangs: []string{lang}}}})
}

// episodeEscaped reports S04E06's one-shot language escape record.
func episodeEscaped(t *testing.T, u upgradeShow) bool {
	t.Helper()
	var n int
	if err := u.h.c.db.QueryRow(`SELECT lang_escaped FROM episodes WHERE series_id = ? AND season_number = 4 AND episode_number = 6`, u.id).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n != 0
}

// gateTakesSized is importGateTakes for a file of the given size.
func gateTakesSized(t *testing.T, u upgradeShow, file string, bytes int64) bool {
	t.Helper()
	s, err := u.h.c.series.Get(u.h.ctx, u.id)
	if err != nil {
		t.Fatal(err)
	}
	return u.h.c.wantsEpisodeFile(u.h.ctx, s, 4, 6, parser.Parse(file), file, bytes, 1)
}

// An episode takes the language escape once, ever: the gate records it, and once it has,
// neither the sweep nor the gate takes another — even when the episode's file reads all
// English again, by name and by probe.
func TestSeriesLanguageEscapeIsOneShot(t *testing.T) {
	u, path := langEscapeShowWith(t, quality.StoredProfile{AllowedResolutions: []string{"1080p", "2160p"},
		Ideal: &quality.IdealFile{Language: italianOverEnglish}}, "Show.S04E06.2160p.WEB-DL.x265-OLD.mkv")
	if episodeEscaped(t, u) {
		t.Fatal("a new episode reads as escaped")
	}
	if !importGateTakes(t, u, "Show.S04E06.iTALiAN.1080p.WEB-DL.x264-SPQR.mkv") {
		t.Fatal("the first escape was refused")
	}
	if !episodeEscaped(t, u) {
		t.Fatal("the escape the gate took wasn't recorded")
	}
	// The episode's file is English-only 2160p again, however it got there.
	placeEpisode(t, u, path, "Show.S04E06.2160p.WEB-DL.x265-NEW.mkv", "2160p", "eng")
	u.h.ix.offer("Show S04E06 iTALiAN 1080p WEB-DL x264-ROMA")
	if err := u.h.c.upgradeSeries(u.h.ctx, u.id, nil); err != nil {
		t.Fatal(err)
	}
	if got := u.grabbedTitles(t); len(got) != 0 {
		t.Errorf("the sweep grabbed %v for a second escape", got)
	}
	if importGateTakes(t, u, "Show.S04E06.iTALiAN.1080p.WEB-DL.x264-ROMA.mkv") {
		t.Error("the import gate took a second escape")
	}
	// An ordinary upgrade still goes through.
	if !importGateTakes(t, u, "Show.S04E06.iTALiAN.2160p.WEB-DL.x265-SPQR.mkv") {
		t.Error("an Italian 2160p was refused after the escape")
	}
}

// The review's chain A: the escape places an Italian 1080p; a MULTi 2160p is taken over it
// on resolution, recorded under the file inside the torrent — which dropped MULTi — and
// probes English only. By name and tracks it reads all-avoided again; the one-shot record
// keeps the sweep and the gate from escaping a second time.
func TestSeriesLanguageEscapeChainThroughAMULTi(t *testing.T) {
	u, path := langEscapeShowWith(t, quality.StoredProfile{AllowedResolutions: []string{"1080p", "2160p"},
		Ideal: &quality.IdealFile{Language: italianOverEnglish}}, "Show.S04E06.2160p.WEB-DL.x265-OLD.mkv")
	const ita = "Show.S04E06.iTALiAN.1080p.WEB-DL.x264-SPQR.mkv"
	if !importGateTakes(t, u, ita) {
		t.Fatal("the escape was refused")
	}
	placeEpisode(t, u, path, ita, "1080p", "ita")
	const inner = "Show.S04E06.2160p.WEB-DL-MIX.mkv" // the MULTi torrent's file
	if !importGateTakes(t, u, inner) {
		t.Fatal("the MULTi 2160p's file was refused over the Italian 1080p")
	}
	placeEpisode(t, u, path, inner, "2160p", "eng")
	u.h.ix.offer("Show S04E06 iTALiAN 1080p WEB-DL x264-ROMA")
	if err := u.h.c.upgradeSeries(u.h.ctx, u.id, nil); err != nil {
		t.Fatal(err)
	}
	if got := u.grabbedTitles(t); len(got) != 0 {
		t.Errorf("the sweep grabbed %v for a second escape", got)
	}
	if importGateTakes(t, u, "Show.S04E06.iTALiAN.1080p.WEB-DL.x264-ROMA.mkv") {
		t.Error("the import gate took a second escape")
	}
}

// The review's chain B: with a bitrate margin set, an English 1080p BluRay over the Italian
// 1080p the escape placed met the margin at the gate, which reads no language, and the
// episode could then escape again to a 720p. Now the bitrate accept refuses a release
// whose name says only avoided languages over a file that isn't all-avoided — and had one
// been placed anyway, the 720p is refused as a second escape.
func TestSeriesLanguageEscapeBitrateChain(t *testing.T) {
	profile := func(row map[string]string) quality.StoredProfile {
		return quality.StoredProfile{AllowedResolutions: []string{"720p", "1080p", "2160p"}, UpgradeMinPercent: 20,
			Ideal: &quality.IdealFile{Language: row}}
	}
	const (
		ita    = "Show.S04E06.iTALiAN.1080p.WEB-DL.x264-SPQR.mkv"
		eng    = "Show.S04E06.1080p.BluRay.x264-BRIT.mkv"
		ita720 = "Show.S04E06.iTALiAN.720p.WEB-DL.x264-SPQR.mkv"
	)
	u, path := langEscapeShowWith(t, profile(italianOverEnglish), "Show.S04E06.2160p.WEB-DL.x265-OLD.mkv")
	if !importGateTakes(t, u, ita) {
		t.Fatal("the escape was refused")
	}
	placeEpisode(t, u, path, ita, "1080p", "ita")
	if gateTakesSized(t, u, eng, 2*langEscapeEpisodeBytes) {
		t.Error("an English 1080p replaced the Italian 1080p on the bitrate margin")
	}
	// Placed anyway: no second escape.
	placeEpisode(t, u, path, eng, "1080p", "eng")
	if gateTakesSized(t, u, ita720, langEscapeEpisodeBytes/2) {
		t.Error("the episode escaped a second time, to 720p")
	}

	// A profile that avoids no language: the bitrate margin decides, exactly as before.
	plain, plainPath := langEscapeShowWith(t, profile(italianOnly), ita)
	placeEpisode(t, plain, plainPath, ita, "1080p", "ita")
	if !gateTakesSized(t, plain, eng, 2*langEscapeEpisodeBytes) {
		t.Error("without an Avoid, the bitrate margin no longer replaces the file")
	}
	if episodeEscaped(t, plain) {
		t.Error("without an Avoid, an escape was recorded")
	}
}

// langEscapeMovie is attachTestCoord with movie 7 (Dune, English original) on disk under
// the release source at a file named file, its profile's Audio language row row.
func langEscapeMovie(t *testing.T, row map[string]string, source, file string) (*Coordinator, *movies.Service) {
	t.Helper()
	ctx := context.Background()
	c, mv, _, st := attachTestCoord(t)
	c.quality = quality.NewService(st.DB())
	sp, err := c.quality.Create(ctx, quality.StoredProfile{MediaType: quality.MediaMovie, Name: "Italian",
		UpgradesEnabled: true, AllowedResolutions: []string{"720p", "1080p", "2160p"}, Ideal: &quality.IdealFile{Language: row}})
	if err != nil {
		t.Fatal(err)
	}
	existing := filepath.Join(t.TempDir(), "Dune (2021)", file)
	writeVideo(t, existing)
	mustExec(t, c, `INSERT INTO movies (id, tmdb_id, title, year, monitored, has_file, movie_file_path, source_release, quality_profile, extra_json)
		VALUES (7, 438631, 'Dune', 2021, 1, 1, ?, ?, ?, '{"original_language":"en"}')`, existing, source, fmt.Sprintf("custom:%d", sp.ID))
	return c, mv
}

// attachDune attaches an import of release (a file named file) to movie 7.
func attachDune(t *testing.T, c *Coordinator, hash, release, file string) (library.AttachOutcome, error) {
	t.Helper()
	incoming := filepath.Join(t.TempDir(), file)
	writeVideo(t, incoming)
	return c.AttachMovieImport(context.Background(), library.ImportRecord{
		Hash: hash, TargetPath: incoming, Title: "Dune", Year: 2021, ReleaseName: release,
	})
}

// A movie track takes the language escape once, ever.
func TestMovieLanguageEscapeIsOneShot(t *testing.T) {
	c, _ := langEscapeMovie(t, italianOverEnglish, "Dune.2021.ENG.2160p.WEB-DL.x265-OLD", "Dune.2021.2160p.WEB-DL.mkv")
	if out, err := attachDune(t, c, "one", "Dune.2021.iTALiAN.1080p.WEB-DL.x264-SPQR", "Dune.2021.1080p.WEB-DL.mkv"); out != library.Attached || err != nil {
		t.Fatalf("first escape: %v, %v; want Attached", out, err)
	}
	var n int
	if err := c.db.QueryRow(`SELECT lang_escaped FROM movies WHERE id = 7`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("the escape wasn't recorded (%d, %v)", n, err)
	}
	// The track holds an English-only 2160p again, however it got there.
	again := filepath.Join(t.TempDir(), "Dune.2021.2160p.BluRay.mkv")
	writeVideo(t, again)
	mustExec(t, c, `UPDATE movies SET movie_file_path = ?, source_release = 'Dune.2021.ENG.2160p.BluRay.x265-NEW' WHERE id = 7`, again)
	if out, err := attachDune(t, c, "two", "Dune.2021.iTALiAN.1080p.BluRay.x264-ROMA", "Dune.2021.1080p.BluRay.mkv"); out != library.Refused || err == nil {
		t.Fatalf("second escape: %v, %v; want Refused", out, err)
	}
}

// The movie gate's equal-resolution accept refuses a release that says it carries only
// avoided languages over a file that isn't all-avoided — under a profile that avoids a
// language only.
func TestMovieImportGateRefusesAvoidedAtEqualResolution(t *testing.T) {
	for _, tc := range []struct {
		name string
		row  map[string]string
		want library.AttachOutcome
	}{
		{"English avoided", italianOverEnglish, library.Refused},
		{"no Avoid", italianOnly, library.Attached},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := langEscapeMovie(t, tc.row, "Dune.2021.iTALiAN.1080p.WEB-DL.x264-SPQR", "Dune.2021.1080p.WEB-DL.mkv")
			out, err := attachDune(t, c, "eng", "Dune.2021.ENG.1080p.BluRay.x264-BRIT", "Dune.2021.1080p.BluRay.mkv")
			if out != tc.want || (tc.want == library.Attached) != (err == nil) {
				t.Fatalf("attach = %v, %v; want %v", out, err, tc.want)
			}
		})
	}
}
