package automation

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/tristenlammi/arrmada/internal/library"
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

// placeEpisode stands in for an import having placed a file on S04E06 outside the gate:
// the episode is recorded under source, and the file's analysis reads res with one audio
// track lang.
func placeEpisode(t *testing.T, u upgradeShow, path, source, res, lang string) {
	t.Helper()
	u.exec(t, `UPDATE episodes SET source_release = ? WHERE series_id = ? AND season_number = 4 AND episode_number = 6`, source, u.id)
	setEpisodeFacts(u, path, res, lang)
}

func setEpisodeFacts(u upgradeShow, path, res, lang string) {
	u.h.c.SetFileFacts(fakeFacts{path: {size: langEscapeEpisodeBytes, facts: quality.FileFacts{Resolution: res, Codec: "h264", HDR: "SDR", AudioLangs: []string{lang}}}})
}

// importThroughGate is the series import for one file on S04E06: the gate (episodeGate),
// then — when it wants the file — the statement that records it, with the escape the gate
// reported (SupersedeEpisodeFileEscaping), as importSeriesFiles places it. The file keeps
// its path (the library name), and its analysis then reads res with one track lang.
func importThroughGate(t *testing.T, u upgradeShow, path, file string, bytes int64, res, lang string) (placed bool, err error) {
	t.Helper()
	s, err := u.h.c.series.Get(u.h.ctx, u.id)
	if err != nil {
		t.Fatal(err)
	}
	want, escape := u.h.c.episodeGate(u.h.ctx, s, 4, 6, parser.Parse(file), file, bytes, 1)
	if !want {
		return false, nil
	}
	if err := u.h.c.series.SupersedeEpisodeFileEscaping(u.h.ctx, u.id, 4, 6, path, langEscapeEpisodeBytes, file, escape); err != nil {
		return false, err
	}
	setEpisodeFacts(u, path, res, lang)
	return true, nil
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

// mustImport is importThroughGate that must place the file.
func mustImport(t *testing.T, u upgradeShow, path, file string, bytes int64, res, lang string) {
	t.Helper()
	placed, err := importThroughGate(t, u, path, file, bytes, res, lang)
	if err != nil || !placed {
		t.Fatalf("%s wasn't placed (%v)", file, err)
	}
}

// An episode takes the language escape once: the placement records it, and once it has,
// neither the sweep nor the gate takes another — even when the episode's file reads all
// English again, by name and by probe.
func TestSeriesLanguageEscapeIsOneShot(t *testing.T) {
	u, path := langEscapeShowWith(t, quality.StoredProfile{AllowedResolutions: []string{"1080p", "2160p"},
		Ideal: &quality.IdealFile{Language: italianOverEnglish}}, "Show.S04E06.2160p.WEB-DL.x265-OLD.mkv")
	if episodeEscaped(t, u) {
		t.Fatal("a new episode reads as escaped")
	}
	mustImport(t, u, path, "Show.S04E06.iTALiAN.1080p.WEB-DL.x264-SPQR.mkv", langEscapeEpisodeBytes, "1080p", "ita")
	if !episodeEscaped(t, u) {
		t.Fatal("the escape the gate took wasn't recorded with the file")
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

// The escape is recorded by the statement that places the file, never before: a
// placement that fails leaves the episode on its old file with the escape untaken, and a
// retry of the same release passes the gate again and records it then.
func TestSeriesLanguageEscapeRetriedAfterAFailedPlacement(t *testing.T) {
	const old = "Show.S04E06.2160p.WEB-DL.x265-OLD.mkv"
	u, path := langEscapeShowWith(t, quality.StoredProfile{AllowedResolutions: []string{"1080p", "2160p"},
		Ideal: &quality.IdealFile{Language: italianOverEnglish}}, old)
	const ita = "Show.S04E06.iTALiAN.1080p.WEB-DL.x264-SPQR.mkv"
	u.exec(t, `CREATE TRIGGER fail_escape BEFORE UPDATE OF lang_escaped ON episodes WHEN NEW.lang_escaped = 1
		BEGIN SELECT RAISE(ABORT, 'injected failure'); END`)
	if placed, err := importThroughGate(t, u, path, ita, langEscapeEpisodeBytes, "1080p", "ita"); placed || err == nil {
		t.Fatalf("placement = %v, %v; want the injected failure", placed, err)
	}
	var source string
	if err := u.h.c.db.QueryRow(`SELECT source_release FROM episodes WHERE series_id = ? AND season_number = 4 AND episode_number = 6`, u.id).Scan(&source); err != nil {
		t.Fatal(err)
	}
	if source != old || episodeEscaped(t, u) {
		t.Fatalf("after the failed placement the episode holds %q, escaped=%v; want the old file, not escaped", source, episodeEscaped(t, u))
	}
	u.exec(t, `DROP TRIGGER fail_escape`)
	mustImport(t, u, path, ita, langEscapeEpisodeBytes, "1080p", "ita")
	if !episodeEscaped(t, u) {
		t.Error("the retried escape wasn't recorded")
	}
	if importGateTakes(t, u, "Show.S04E06.iTALiAN.720p.WEB-DL.x264-SPQR.mkv") {
		t.Error("a second escape after the retry")
	}
}

// Equal by the gate's reading, lower by the quality rules': a converted episode whose
// original was 2160p, now probed 1080p English-only. An Italian 1080p is the language
// escape all the same (IsQualityUpgrade accepts it, as the sweep grabbed it), and its
// placement spends the episode's escape.
func TestSeriesConvertedEqualResolutionEscapeIsRecorded(t *testing.T) {
	u, path := langEscapeShowWith(t, quality.StoredProfile{AllowedResolutions: []string{"1080p", "2160p"},
		Ideal: &quality.IdealFile{Language: italianOverEnglish}}, "Show.S04E06.1080p.WEB-DL.x265-OLD.mkv")
	u.exec(t, `UPDATE episodes SET converted_from_release = 'Show.S04E06.2160p.WEB-DL.x264-ORIG.mkv', converted_from_size = ?
		WHERE series_id = ? AND season_number = 4 AND episode_number = 6`, int64(12<<30), u.id)
	setEpisodeFacts(u, path, "1080p", "eng")
	mustImport(t, u, path, "Show.S04E06.iTALiAN.1080p.WEB-DL.x264-SPQR.mkv", langEscapeEpisodeBytes, "1080p", "ita")
	if !episodeEscaped(t, u) {
		t.Fatal("the equal-resolution escape wasn't recorded")
	}
	placeEpisode(t, u, path, "Show.S04E06.2160p.WEB-DL.x265-NEW.mkv", "2160p", "eng")
	if importGateTakes(t, u, "Show.S04E06.iTALiAN.1080p.WEB-DL.x264-ROMA.mkv") {
		t.Error("a second escape after the converted one")
	}
}

// The review's chain A: the escape places an Italian 1080p; a MULTi 2160p is taken over it
// on resolution, recorded under the file inside the torrent — which dropped MULTi — and
// probes English only. By name and tracks it reads all-avoided again; the one-shot record
// keeps the sweep and the gate from escaping a second time.
func TestSeriesLanguageEscapeChainThroughAMULTi(t *testing.T) {
	u, path := langEscapeShowWith(t, quality.StoredProfile{AllowedResolutions: []string{"1080p", "2160p"},
		Ideal: &quality.IdealFile{Language: italianOverEnglish}}, "Show.S04E06.2160p.WEB-DL.x265-OLD.mkv")
	mustImport(t, u, path, "Show.S04E06.iTALiAN.1080p.WEB-DL.x264-SPQR.mkv", langEscapeEpisodeBytes, "1080p", "ita")
	// The MULTi torrent's file, recorded without MULTi.
	mustImport(t, u, path, "Show.S04E06.2160p.WEB-DL-MIX.mkv", langEscapeEpisodeBytes, "2160p", "eng")
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
	mustImport(t, u, path, ita, langEscapeEpisodeBytes, "1080p", "ita")
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
// the release source at a file named file, its profile's Audio language row row, any
// resolution from 720p to 2160p.
func langEscapeMovie(t *testing.T, row map[string]string, source, file string) (*Coordinator, string) {
	t.Helper()
	return langEscapeMovieWith(t, quality.StoredProfile{AllowedResolutions: []string{"720p", "1080p", "2160p"},
		Ideal: &quality.IdealFile{Language: row}}, source, file)
}

// langEscapeMovieWith is langEscapeMovie with the profile sp, returning the file's path.
func langEscapeMovieWith(t *testing.T, sp quality.StoredProfile, source, file string) (*Coordinator, string) {
	t.Helper()
	ctx := context.Background()
	c, _, _, st := attachTestCoord(t)
	c.quality = quality.NewService(st.DB())
	sp.MediaType, sp.Name, sp.UpgradesEnabled = quality.MediaMovie, "Italian", true
	sp, err := c.quality.Create(ctx, sp)
	if err != nil {
		t.Fatal(err)
	}
	existing := filepath.Join(t.TempDir(), "Dune (2021)", file)
	writeVideo(t, existing)
	mustExec(t, c, `INSERT INTO movies (id, tmdb_id, title, year, monitored, has_file, movie_file_path, source_release, quality_profile, extra_json, media_json)
		VALUES (7, 438631, 'Dune', 2021, 1, 1, ?, ?, ?, '{"original_language":"en"}', '{"size_bytes":1000}')`, existing, source, fmt.Sprintf("custom:%d", sp.ID))
	return c, existing
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

// movieEscaped reports movie 7's default track's one-shot language escape record.
func movieEscaped(t *testing.T, c *Coordinator) bool {
	t.Helper()
	var n int
	if err := c.db.QueryRow(`SELECT lang_escaped FROM movies WHERE id = 7`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n != 0
}

// dunePutBack stands in for the track holding an English-only 2160p again, however it got
// there.
func dunePutBack(t *testing.T, c *Coordinator) {
	t.Helper()
	again := filepath.Join(t.TempDir(), "Dune.2021.2160p.BluRay.mkv")
	writeVideo(t, again)
	mustExec(t, c, `UPDATE movies SET movie_file_path = ?, source_release = 'Dune.2021.ENG.2160p.BluRay.x265-NEW' WHERE id = 7`, again)
}

// A movie track takes the language escape once.
func TestMovieLanguageEscapeIsOneShot(t *testing.T) {
	c, _ := langEscapeMovie(t, italianOverEnglish, "Dune.2021.ENG.2160p.WEB-DL.x265-OLD", "Dune.2021.2160p.WEB-DL.mkv")
	if out, err := attachDune(t, c, "one", "Dune.2021.iTALiAN.1080p.WEB-DL.x264-SPQR", "Dune.2021.1080p.WEB-DL.mkv"); out != library.Attached || err != nil {
		t.Fatalf("first escape: %v, %v; want Attached", out, err)
	}
	if !movieEscaped(t, c) {
		t.Fatal("the escape wasn't recorded")
	}
	dunePutBack(t, c)
	if out, err := attachDune(t, c, "two", "Dune.2021.iTALiAN.1080p.BluRay.x264-ROMA", "Dune.2021.1080p.BluRay.mkv"); out != library.Refused || err == nil {
		t.Fatalf("second escape: %v, %v; want Refused", out, err)
	}
}

// The review's probe: a release with no resolution in its name, under a profile that
// allows any resolution. The sweep takes it as the escape (an unknown resolution is below
// the file's), and the movie gate — which can't compare an unknown resolution and takes
// it — records it all the same; so a second escape is refused.
func TestMovieUnknownResolutionEscapeIsRecorded(t *testing.T) {
	ctx := context.Background()
	c, _ := langEscapeMovieWith(t, quality.StoredProfile{Ideal: &quality.IdealFile{Language: italianOverEnglish}},
		"Dune.2021.ENG.2160p.WEB-DL.x265-OLD", "Dune.2021.2160p.WEB-DL.mkv")
	m, err := c.movies.Get(ctx, 7)
	if err != nil {
		t.Fatal(err)
	}
	ref := m.QualityProfile
	cur := c.CurrentMovieFile(ctx, m)
	const rel = "Dune.2021.iTALiAN.WEB-DL.x264-SPQR"
	if pick, key, ok := c.quality.UpgradePick(ctx, ref, cur, []quality.Candidate{quality.NewCandidate(rel, 3, 50)}); !ok || pick.Name != rel || key != quality.KeyPreferences {
		t.Fatalf("setup: the sweep's verdict on %s = %v %s %s, want the escape", rel, ok, pick.Name, key)
	}
	if out, err := attachDune(t, c, "unk", rel, "Dune.2021.WEB-DL.mkv"); out != library.Attached || err != nil {
		t.Fatalf("attach = %v, %v; want Attached", out, err)
	}
	if !movieEscaped(t, c) {
		t.Fatal("the escape the sweep grabbed was placed without being recorded")
	}
	dunePutBack(t, c)
	if out, err := attachDune(t, c, "two", "Dune.2021.iTALiAN.1080p.BluRay.x264-ROMA", "Dune.2021.1080p.BluRay.mkv"); out == library.Attached {
		t.Fatalf("second escape: %v, %v; want it refused", out, err)
	}
}

// Equal by the movie gate's reading, lower by the quality rules': a converted track whose
// original was 2160p, now a 1080p probed English-only. The Italian 1080p is the escape all
// the same, and attaching it records it.
func TestMovieConvertedEqualResolutionEscapeIsRecorded(t *testing.T) {
	c, existing := langEscapeMovie(t, italianOverEnglish, "Dune.2021.1080p.WEB-DL.x265-OLD", "Dune.2021.1080p.WEB-DL.mkv")
	mustExec(t, c, `UPDATE movies SET converted_from_release = 'Dune.2021.2160p.BluRay.REMUX.AVC-ORIG', converted_from_size = ? WHERE id = 7`, int64(50<<30))
	c.SetFileFacts(stubFacts{existing: {Resolution: "1080p", Codec: "hevc", HDR: "SDR", AudioLangs: []string{"eng"}}})
	if out, err := attachDune(t, c, "conv", "Dune.2021.iTALiAN.1080p.WEB-DL.x264-SPQR", "Dune.2021.1080p.iTALiAN.mkv"); out != library.Attached || err != nil {
		t.Fatalf("attach = %v, %v; want Attached", out, err)
	}
	if !movieEscaped(t, c) {
		t.Fatal("the equal-resolution escape wasn't recorded")
	}
}

// The escape is recorded in the transaction that records the new file: when that
// transaction fails, the track still holds its old file (on disk, not recycled), the
// escape is untaken, and the same release attached again goes through and records it.
func TestMovieLanguageEscapeRetriedAfterAFailedTransaction(t *testing.T) {
	c, existing := langEscapeMovie(t, italianOverEnglish, "Dune.2021.ENG.2160p.WEB-DL.x265-OLD", "Dune.2021.2160p.WEB-DL.mkv")
	mustExec(t, c, `CREATE TRIGGER fail_escape BEFORE UPDATE OF lang_escaped ON movies WHEN NEW.lang_escaped = 1
		BEGIN SELECT RAISE(ABORT, 'injected failure'); END`)
	const ita = "Dune.2021.iTALiAN.1080p.WEB-DL.x264-SPQR"
	incoming := filepath.Join(t.TempDir(), "Dune.2021.1080p.WEB-DL.mkv")
	writeVideo(t, incoming)
	rec := library.ImportRecord{Hash: "ita", TargetPath: incoming, Title: "Dune", Year: 2021, ReleaseName: ita}
	if out, err := c.AttachMovieImport(context.Background(), rec); out != library.AttachRetry || err == nil {
		t.Fatalf("attach with the record failing = %v, %v; want AttachRetry", out, err)
	}
	m, err := c.movies.Get(context.Background(), 7)
	if err != nil {
		t.Fatal(err)
	}
	if m.MovieFilePath != existing || m.SourceRelease != "Dune.2021.ENG.2160p.WEB-DL.x265-OLD" {
		t.Fatalf("after the failed transaction the movie holds %q (%s), want its old file", m.SourceRelease, m.MovieFilePath)
	}
	if _, err := os.Stat(existing); err != nil {
		t.Fatalf("the old file is gone after the failed transaction: %v", err)
	}
	if movieEscaped(t, c) {
		t.Fatal("the escape reads as taken though nothing was placed")
	}
	mustExec(t, c, `DROP TRIGGER fail_escape`)
	if out, err := c.AttachMovieImport(context.Background(), rec); out != library.Attached || err != nil {
		t.Fatalf("retry = %v, %v; want Attached", out, err)
	}
	if !movieEscaped(t, c) {
		t.Error("the retried escape wasn't recorded")
	}
}

// The movie gate takes an equal-resolution release whatever its language, as it always
// has (an interactive pick reaches it too) — and the one-shot record still holds after an
// English 1080p is attached over the escape's Italian 1080p: no second escape to 720p.
func TestMovieEqualResolutionTakenAndOneShotHolds(t *testing.T) {
	t.Run("English avoided", func(t *testing.T) {
		c, _ := langEscapeMovie(t, italianOverEnglish, "Dune.2021.ENG.2160p.WEB-DL.x265-OLD", "Dune.2021.2160p.WEB-DL.mkv")
		if out, err := attachDune(t, c, "ita", "Dune.2021.iTALiAN.1080p.WEB-DL.x264-SPQR", "Dune.2021.1080p.WEB-DL.mkv"); out != library.Attached || err != nil {
			t.Fatalf("escape: %v, %v; want Attached", out, err)
		}
		if out, err := attachDune(t, c, "eng", "Dune.2021.ENG.1080p.BluRay.x264-BRIT", "Dune.2021.1080p.BluRay.mkv"); out != library.Attached || err != nil {
			t.Fatalf("equal-resolution English: %v, %v; want Attached", out, err)
		}
		if out, err := attachDune(t, c, "720", "Dune.2021.iTALiAN.720p.WEB-DL.x264-SPQR", "Dune.2021.720p.WEB-DL.mkv"); out != library.Refused || err == nil {
			t.Fatalf("second escape, to 720p: %v, %v; want Refused", out, err)
		}
	})
	t.Run("no Avoid", func(t *testing.T) {
		c, _ := langEscapeMovie(t, italianOnly, "Dune.2021.iTALiAN.1080p.WEB-DL.x264-SPQR", "Dune.2021.1080p.WEB-DL.mkv")
		if out, err := attachDune(t, c, "eng", "Dune.2021.ENG.1080p.BluRay.x264-BRIT", "Dune.2021.1080p.BluRay.mkv"); out != library.Attached || err != nil {
			t.Fatalf("equal-resolution English: %v, %v; want Attached", out, err)
		}
		if movieEscaped(t, c) {
			t.Error("an escape was recorded under a profile that avoids nothing")
		}
	})
}
