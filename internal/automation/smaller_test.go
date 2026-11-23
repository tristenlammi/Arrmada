package automation

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tristenlammi/arrmada/internal/mediainfo"
	"github.com/tristenlammi/arrmada/internal/quality"
)

// --- the candidate filter, rule by rule ---------------------------------------------------

// heatFile is a 20 GiB 1080p H.264 film of 170 minutes: 16.8 Mb/s.
func heatFile() replaceCurrent {
	return replaceCurrent{Path: "/m/Heat (1995)/Heat.mkv", SizeBytes: 20 << 30, Release: "Heat.1995.1080p.BluRay.x264-GRP",
		OrigSize: 20 << 30, Resolution: "1080p", Codec: "x264", HDR: "SDR", DurationSec: 170 * 60}
}

func heatRules() replaceRules { return replaceRules{floor: 3, runtimeMin: 170} }

func row(title string, gb float64) RankedRelease {
	return RankedRelease{Title: title, SizeGB: gb, Eligible: true}
}

func TestJudgeReplacementQualifies(t *testing.T) {
	fit, why := judgeReplacement(heatFile(), row("Heat.1995.1080p.BluRay.x265-GOOD", 6), heatRules())
	if why != "" || fit == nil {
		t.Fatalf("a 6 GiB HEVC 1080p was turned down: %q", why)
	}
	if fit.SavesPct != 70 || fit.SavesGB < 13.9 || fit.SavesGB > 14.1 {
		t.Errorf("saving = %.1f GB / %d%%, want 14 GB / 70%%", fit.SavesGB, fit.SavesPct)
	}
	joined := strings.Join(fit.Why, " | ")
	for _, want := range []string{"Saves 14.0 GB (70%)", "HEVC at 5.1 Mb/s"} {
		if !strings.Contains(joined, want) {
			t.Errorf("why = %q, missing %q", joined, want)
		}
	}
	if len(fit.Loses) == 0 || !strings.Contains(fit.Loses[0], "Lower bitrate") {
		t.Errorf("loses = %q, want the lower bitrate said", fit.Loses)
	}
	// AV1 counts as well.
	if _, why := judgeReplacement(heatFile(), row("Heat.1995.1080p.WEB-DL.AV1-GRP", 5), heatRules()); why != "" {
		t.Errorf("AV1 turned down: %q", why)
	}
}

func TestJudgeReplacementRules(t *testing.T) {
	hdr := heatFile()
	hdr.HDR = "HDR10"
	dv := heatFile()
	dv.HDR, dv.DolbyVision = "HDR10", true
	plus := heatFile()
	plus.HDR = "HDR10+"
	atmos := heatFile()
	atmos.Atmos = true
	lossless := heatFile()
	lossless.Lossless = true
	extended := heatFile()
	extended.Edition = "Extended"
	german := heatRules()
	german.needLangs = []string{"de"}
	windowed := heatRules()
	windowed.floor, windowed.ceiling = 6, 7.5

	blocked := row("Heat.1995.1080p.BluRay.x265-GOOD", 6)
	blocked.Blocklisted = true
	refused := row("Heat.1995.1080p.BluRay.x265-GOOD", 6)
	refused.Eligible, refused.RejectReason = false, "Avoided group"
	english := row("Heat.1995.1080p.BluRay.x265-GOOD", 6)
	english.AudioLangs = []string{"en"}

	cases := []struct {
		name  string
		cur   replaceCurrent
		rr    RankedRelease
		rules replaceRules
		want  string
	}{
		{"blocklisted", heatFile(), blocked, heatRules(), "Blocklisted"},
		{"profile refuses it", heatFile(), refused, heatRules(), "Your profile doesn't take it: Avoided group"},
		{"the file's own release", heatFile(), row("Heat.1995.1080p.BluRay.x264-GRP", 6), heatRules(), "the release your file came from"},
		{"lower resolution", heatFile(), row("Heat.1995.720p.BluRay.x265-GRP", 3), heatRules(), "Lower resolution (720p; your file is 1080p)"},
		{"higher resolution", heatFile(), row("Heat.1995.2160p.BluRay.x265-GRP", 12), heatRules(), "A different resolution"},
		{"no resolution named", heatFile(), row("Heat.1995.BluRay.x265-GRP", 6), heatRules(), "doesn't say its resolution"},
		{"H.264", heatFile(), row("Heat.1995.1080p.BluRay.x264-SMALL", 8), heatRules(), "Not HEVC or AV1"},
		{"barely smaller", heatFile(), row("Heat.1995.1080p.BluRay.x265-BIG", 18), heatRules(), "Only 10% smaller"},
		{"bigger", heatFile(), row("Heat.1995.1080p.BluRay.x265-HUGE", 25), heatRules(), "Not smaller"},
		{"starved", heatFile(), row("Heat.1995.1080p.BluRay.x265-TINY", 2), heatRules(), "under the 3.0 Mb/s floor"},
		{"profile floor", heatFile(), row("Heat.1995.1080p.BluRay.x265-GOOD", 6), windowed, "under the 6.0 Mb/s floor"},
		{"profile ceiling", heatFile(), row("Heat.1995.1080p.BluRay.x265-GOOD", 9.5), windowed, "over your profile's 7.5 Mb/s ceiling"},
		{"runtime unknown", heatFile(), row("Heat.1995.1080p.BluRay.x265-GOOD", 6), replaceRules{floor: 3}, "length isn't known"},
		{"Convert would re-encode it", heatFile(), row("Heat.1995.1080p.BluRay.x265-BLOAT", 11), heatRules(), "Convert would re-encode this one too"},
		{"another edition", heatFile(), row("Heat.1995.Extended.1080p.BluRay.x265-GRP", 6), heatRules(), "A different edition (Extended; your file is the standard cut)"},
		{"the standard cut for an Extended file", extended, row("Heat.1995.1080p.BluRay.x265-GRP", 6), heatRules(), "your file is Extended"},
		{"loses HDR", hdr, row("Heat.1995.1080p.BluRay.x265-GRP", 6), heatRules(), "Would lose HDR (your file is HDR10)"},
		{"loses Dolby Vision", dv, row("Heat.1995.1080p.BluRay.HDR10.x265-GRP", 6), heatRules(), "Would lose Dolby Vision"},
		{"loses HDR10+", plus, row("Heat.1995.1080p.BluRay.HDR10.x265-GRP", 6), heatRules(), "Would lose HDR10+"},
		{"loses Atmos", atmos, row("Heat.1995.1080p.BluRay.DDP5.1.x265-GRP", 6), heatRules(), "Would lose Atmos"},
		{"loses lossless audio", lossless, row("Heat.1995.1080p.BluRay.DDP5.1.x265-GRP", 6), heatRules(), "Would lose lossless audio"},
		{"loses a language", heatFile(), english, german, "Would lose German audio"},
	}
	for _, tc := range cases {
		fit, why := judgeReplacement(tc.cur, tc.rr, tc.rules)
		if fit != nil || !strings.Contains(why, tc.want) {
			t.Errorf("%s: why = %q (fit %v), want %q", tc.name, why, fit != nil, tc.want)
		}
	}

	// What it keeps, said when it does.
	keeps := []struct {
		name string
		cur  replaceCurrent
		rel  string
		want string
	}{
		{"Dolby Vision", dv, "Heat.1995.1080p.BluRay.DV.HDR10.x265-GRP", "Keeps Dolby Vision"},
		{"HDR10+", plus, "Heat.1995.1080p.BluRay.HDR10+.x265-GRP", "Keeps HDR10+"},
		{"Atmos", atmos, "Heat.1995.1080p.BluRay.TrueHD.Atmos.x265-GRP", "Keeps Atmos"},
		{"lossless", lossless, "Heat.1995.1080p.BluRay.TrueHD.7.1.x265-GRP", "Keeps lossless audio"},
		{"edition", extended, "Heat.1995.Extended.1080p.BluRay.x265-GRP", "Same edition (Extended)"},
	}
	for _, k := range keeps {
		fit, why := judgeReplacement(k.cur, row(k.rel, 6), heatRules())
		if fit == nil {
			t.Errorf("%s: turned down: %q", k.name, why)
			continue
		}
		if !strings.Contains(strings.Join(fit.Why, " | "), k.want) {
			t.Errorf("%s: why = %q, want %q", k.name, fit.Why, k.want)
		}
	}
	german2 := row("Heat.1995.German.DL.1080p.BluRay.x265-GRP", 6)
	german2.AudioLangs = []string{"de", "en"}
	if fit, why := judgeReplacement(heatFile(), german2, german); fit == nil || !strings.Contains(strings.Join(fit.Why, " "), "Keeps German audio") {
		t.Errorf("German dual-audio: fit %v, why %q", fit, why)
	}
	remux := heatFile()
	remux.Remux = true
	if fit, _ := judgeReplacement(remux, row("Heat.1995.1080p.BluRay.x265-GRP", 6), heatRules()); fit == nil || !strings.Contains(strings.Join(fit.Loses, " "), "disc's own picture") {
		t.Errorf("a remux's replacement should say it is an encode: %+v", fit)
	}
}

func TestNeededLangs(t *testing.T) {
	rules := map[string]string{"de": quality.PrefMust, quality.LangOriginal: quality.PrefWant, "fr": quality.PrefAvoid, "es": quality.PrefWant}
	got := neededLangs(rules, "en", []string{"en", "de"})
	if strings.Join(got, ",") != "de,en" {
		t.Errorf("needed = %v, want de,en (asked for and in the file; not Spanish, which the file lacks)", got)
	}
	// Languages unknown: only what the profile requires.
	if got := neededLangs(rules, "en", nil); strings.Join(got, ",") != "de" {
		t.Errorf("unknown languages: needed = %v, want de", got)
	}
	if got := neededLangs(nil, "en", []string{"en"}); len(got) != 0 {
		t.Errorf("no rules: needed = %v", got)
	}
}

// --- the check when the download lands -----------------------------------------------------

func TestReplacementProblems(t *testing.T) {
	exp := heatFile()
	exp.HDR, exp.DolbyVision = "HDR10", true
	good := mediainfo.Info{VideoCodec: "x265", Resolution: "1080p", HDR: []string{"HDR10", "DV"}, DurationSec: 170*60 + 40}
	if p := replacementProblems(exp, good); len(p) != 0 {
		t.Fatalf("a matching download has problems: %q", p)
	}
	cases := []struct {
		name string
		mi   mediainfo.Info
		want string
	}{
		{"no video", mediainfo.Info{}, "no video"},
		{"720p", mediainfo.Info{VideoCodec: "x265", Resolution: "720p", HDR: []string{"HDR10", "DV"}, DurationSec: 170 * 60}, "it's 720p, not 1080p"},
		{"no HDR", mediainfo.Info{VideoCodec: "x265", Resolution: "1080p", DurationSec: 170 * 60}, "no HDR (your file is HDR10)"},
		{"no Dolby Vision", mediainfo.Info{VideoCodec: "x265", Resolution: "1080p", HDR: []string{"HDR10"}, DurationSec: 170 * 60}, "no Dolby Vision"},
		{"shorter cut", mediainfo.Info{VideoCodec: "x265", Resolution: "1080p", HDR: []string{"HDR10", "DV"}, DurationSec: 160 * 60}, "it runs 2h 40m and your file 2h 50m"},
		{"length unreadable", mediainfo.Info{VideoCodec: "x265", Resolution: "1080p", HDR: []string{"HDR10", "DV"}}, "length couldn't be read"},
	}
	for _, tc := range cases {
		p := replacementProblems(exp, tc.mi)
		if !strings.Contains(strings.Join(p, "; "), tc.want) {
			t.Errorf("%s: problems = %q, want %q", tc.name, p, tc.want)
		}
	}
	// 1% of the length (here 102 s) is the same cut.
	sdr := heatFile()
	if p := replacementProblems(sdr, mediainfo.Info{VideoCodec: "x265", Resolution: "1080p", DurationSec: 170*60 - 100}); len(p) != 0 {
		t.Errorf("100 s off on 170 minutes: %q", p)
	}
}

// --- end to end through the coordinator -------------------------------------------------

// heatHarness is a harness with Heat in the library: a 20 GiB 1080p H.264 file, 170 minutes.
func heatHarness(t *testing.T) (*stallHarness, int64, string) {
	t.Helper()
	h := newStallHarness(t)
	id := h.addMovie(t, 949, "Heat", 1995)
	path := filepath.Join(t.TempDir(), "Heat (1995).mkv")
	if _, err := h.c.db.Exec(`UPDATE movies SET has_file = 1, movie_file_path = ?, source_release = ?, runtime = 170,
		media_json = ? WHERE id = ?`, path, "Heat.1995.1080p.BluRay.x264-GRP",
		`{"path":"`+filepath.ToSlash(path)+`","size_bytes":21474836480,"probed":true,"resolution":"1080p","duration_min":170,"v":2}`, id); err != nil {
		t.Fatal(err)
	}
	h.c.probeFileFn = func(p string) (mediainfo.Info, error) {
		return mediainfo.Info{VideoCodec: "x264", Resolution: "1080p", DurationSec: 170 * 60}, nil
	}
	return h, id, path
}

func TestSmallerReleasesFiltersTheSearch(t *testing.T) {
	h, id, _ := heatHarness(t)
	h.ix.offerSized(map[string]float64{
		"Heat.1995.1080p.BluRay.x265-GOOD":    6,
		"Heat.1995.1080p.BluRay.x265-TINY":    2,
		"Heat.1995.1080p.BluRay.x264-SMALL":   8,
		"Heat.1995.720p.BluRay.x265-LOW":      3,
		"Heat.1995.1080p.BluRay.x265-BIG":     18,
		"Heat.1995.1080p.BluRay.x265-BLOAT":   11,
		"Heat.1995.Extended.1080p.x265-OTHER": 6,
	})
	list, err := h.c.SmallerReleases(h.ctx, id, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Releases) != 1 || list.Releases[0].Title != "Heat.1995.1080p.BluRay.x265-GOOD" {
		t.Fatalf("qualifying = %+v, want only the 6 GiB HEVC", list.Releases)
	}
	if r := list.Releases[0]; r.Replace == nil || r.Replace.SavesPct != 70 || r.Recommended {
		t.Errorf("row = %+v, want a 70%% saving and no title-wide recommendation", r)
	}
	if len(list.NotSuitable) != 6 {
		t.Errorf("not suitable = %d rows, want 6: %+v", len(list.NotSuitable), list.NotSuitable)
	}
	for _, ns := range list.NotSuitable {
		if ns.Why == "" {
			t.Errorf("%s left out without a reason", ns.Title)
		}
	}
	c := list.Current
	if c.Resolution != "1080p" || c.FloorMbps != 3 || c.FloorFromProfile || c.RuntimeMin != 170 || c.SizeGB != 20 {
		t.Errorf("current = %+v", c)
	}
	if len(list.Notes) == 0 || !strings.Contains(list.Notes[0], "recycle bin") {
		t.Errorf("notes = %q, want where the old file goes", list.Notes)
	}

	// A track with no file has nothing to replace.
	if _, err := h.c.db.Exec(`UPDATE movies SET has_file = 0 WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := h.c.SmallerReleases(h.ctx, id, 0); err != ErrNoFileToReplace {
		t.Errorf("no file: err = %v", err)
	}
}

// sparseFilm makes a 60 MB sparse .mkv in its own folder — big enough to be the download's
// film rather than a sample — and returns the folder.
func sparseFilm(t *testing.T, name string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(filepath.Join(dir, name+".mkv"))
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(60 << 20); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	return dir
}

func TestReplaceGrabIsCheckedAndLeavesTheBaseline(t *testing.T) {
	h, id, path := heatHarness(t)
	good, short := "Heat.1995.1080p.BluRay.x265-GOOD", "Heat.1995.1080p.BluRay.x265-SHORT"
	plain := "Heat.1995.1080p.WEB-DL.x265-PLAIN"
	for _, rel := range []string{good, short, plain} {
		h.c.RecordManualGrab(h.ctx, id, 0, rel, "Fake", hashFor(rel))
	}
	for _, rel := range []string{good, short} {
		if err := h.c.MarkReplaceGrab(h.ctx, id, 0, rel); err != nil {
			t.Fatal(err)
		}
	}
	var raw string
	if err := h.c.db.QueryRow(`SELECT replace_expect FROM grabs WHERE title = ?`, good).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(raw, `"release":"Heat.1995.1080p.BluRay.x264-GRP"`) || !strings.Contains(raw, `"duration_sec":10200`) {
		t.Errorf("replace_expect = %s", raw)
	}
	if err := h.c.db.QueryRow(`SELECT replace_expect FROM grabs WHERE title = ?`, plain).Scan(&raw); err != nil || raw != "" {
		t.Errorf("an ordinary grab got replace_expect %q (%v)", raw, err)
	}

	goodDir, shortDir, plainDir := sparseFilm(t, good), sparseFilm(t, short), sparseFilm(t, plain)
	h.c.probeFileFn = func(p string) (mediainfo.Info, error) {
		switch {
		case strings.Contains(p, "SHORT"):
			return mediainfo.Info{VideoCodec: "x265", Resolution: "1080p", DurationSec: 150 * 60}, nil
		case strings.Contains(p, "x265"):
			return mediainfo.Info{VideoCodec: "x265", Resolution: "1080p", DurationSec: 170 * 60}, nil
		}
		return mediainfo.Info{VideoCodec: "x264", Resolution: "1080p", DurationSec: 170 * 60}, nil
	}

	if reason, hold := h.c.HoldMovieImport(h.ctx, hashFor(good), good, goodDir); hold {
		t.Errorf("a download that matches was held: %s", reason)
	}
	reason, hold := h.c.HoldMovieImport(h.ctx, hashFor(short), short, shortDir)
	if !hold || !strings.Contains(reason, "it runs 2h 30m and your file 2h 50m") {
		t.Errorf("a shorter cut: hold %v, %q", hold, reason)
	}
	var code string
	if err := h.c.db.QueryRow(`SELECT reason_code FROM import_reviews WHERE hash = ?`, hashFor(short)).Scan(&code); err != nil || code != ReasonReplaceCheck {
		t.Errorf("review reason = %q (%v), want %q", code, err, ReasonReplaceCheck)
	}
	if reason, hold := h.c.HoldMovieImport(h.ctx, hashFor(plain), plain, plainDir); hold {
		t.Errorf("an ordinary grab was held: %s", reason)
	}

	// The good one is imported: the old release becomes the new file's baseline.
	newPath := filepath.Join(filepath.Dir(path), "Heat (1995) x265.mkv")
	if _, err := h.c.db.Exec(`UPDATE movies SET movie_file_path = ?, source_release = ? WHERE id = ?`, newPath, good, id); err != nil {
		t.Fatal(err)
	}
	h.c.keepReplacedBaseline(h.ctx, id, hashFor(plain)) // an ordinary download: nothing
	m, err := h.c.movies.Get(h.ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if m.ConvertedFromRelease != "" {
		t.Fatalf("an ordinary import set a baseline: %q", m.ConvertedFromRelease)
	}
	h.c.keepReplacedBaseline(h.ctx, id, hashFor(good))
	if m, err = h.c.movies.Get(h.ctx, id); err != nil {
		t.Fatal(err)
	}
	if m.ConvertedFromRelease != "Heat.1995.1080p.BluRay.x264-GRP" || m.ConvertedFromSize != 20<<30 {
		t.Errorf("baseline = %q / %d, want the replaced release at 20 GiB", m.ConvertedFromRelease, m.ConvertedFromSize)
	}
	// The upgrader now measures against the replaced file, so it never fetches it back.
	cur := h.c.CurrentMovieFile(h.ctx, m)
	if cur.OrigRelease != "Heat.1995.1080p.BluRay.x264-GRP" {
		t.Errorf("upgrade baseline = %q", cur.OrigRelease)
	}
}
