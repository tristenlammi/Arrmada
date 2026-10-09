package automation

import (
	"strings"
	"testing"

	"github.com/tristenlammi/arrmada/internal/parser"
	"github.com/tristenlammi/arrmada/internal/series"
)

func withOrigin(s series.Series, cc ...string) series.Series {
	s.Extra = &series.SeriesExtra{OriginCountry: cc}
	return s
}

// A year before the season marker names the show; one after it is an air year.
func TestSeriesIdentityYear(t *testing.T) {
	who1963 := withOrigin(series.Series{Title: "Doctor Who", Year: 1963}, "GB")
	who2005 := withOrigin(series.Series{Title: "Doctor Who", Year: 2005}, "GB")
	cases := []struct {
		release string
		s       series.Series
		want    bool
	}{
		{"Doctor.Who.2005.S01E01.1080p.BluRay-GRP", who2005, true},
		{"Doctor.Who.2005.S01E01.1080p.BluRay-GRP", who1963, false},
		{"Doctor.Who.S01E01.1080p.BluRay-GRP", who1963, true},
		{"Doctor.Who.S01E01.1080p.BluRay-GRP", who2005, true},
		{"Battlestar.Galactica.2003.S01E01.1080p-GRP", series.Series{Title: "Battlestar Galactica", Year: 2004}, true},
		{"Battlestar.Galactica.2003.S01E01.1080p-GRP", series.Series{Title: "Battlestar Galactica", Year: 1978}, false},
		{"Show.S01E01.2019.1080p.WEB-DL-GRP", series.Series{Title: "Show", Year: 2014}, true},
		{"1923.S01E01.1080p.WEB-DL-GRP", series.Series{Title: "1923", Year: 2022}, true},
	}
	for _, c := range cases {
		if ok, why := seriesIdentity(parser.Parse(c.release), c.s); ok != c.want {
			t.Errorf("%q vs %s (%d) = %v (%s), want %v", c.release, c.s.Title, c.s.Year, ok, why, c.want)
		}
	}
}

// A country tag picks between same-named shows from different countries.
func TestSeriesIdentityCountry(t *testing.T) {
	officeUS := withOrigin(series.Series{Title: "The Office", Year: 2005}, "US")
	officeUK := withOrigin(series.Series{Title: "The Office", Year: 2001}, "GB")
	cases := []struct {
		release string
		s       series.Series
		want    bool
	}{
		{"The.Office.US.S02E01.720p.HDTV-GRP", officeUS, true},
		{"The.Office.US.S02E01.720p.HDTV-GRP", officeUK, false},
		{"The.Office.UK.S01E01.720p.HDTV-GRP", officeUK, true},
		{"The.Office.UK.S01E01.720p.HDTV-GRP", officeUS, false},
		{"The.Office.S01E01.720p.HDTV-GRP", officeUS, true},
		{"The.Office.S01E01.720p.HDTV-GRP", officeUK, true},
		{"This.Is.Us.S01E01.1080p-GRP", withOrigin(series.Series{Title: "This Is Us", Year: 2016}, "US"), true},
	}
	for _, c := range cases {
		if ok, why := seriesIdentity(parser.Parse(c.release), c.s); ok != c.want {
			t.Errorf("%q vs %s %v = %v (%s), want %v", c.release, c.s.Title, c.s.Extra.OriginCountry, ok, why, c.want)
		}
	}
	// The grab path and the in-flight check read the same rule.
	if seriesTitleMatches("The.Office.US.S02E01.720p.HDTV-GRP", officeUK) {
		t.Error("seriesTitleMatches must refuse the US release for the UK show")
	}
}

// A release grabbed for a show under one of its aliases imports into that show — it used
// to be held for review as "grabbed for X but the download looks like Y".
func TestImportRoutesAliasGrabToGrabbedSeries(t *testing.T) {
	h := newLifecycleHarness(t)
	if _, err := h.c.series.AddAlias(h.ctx, h.seriesID, "Show Other Name", 0); err != nil {
		t.Fatal(err)
	}
	name := "Show.Other.Name.S01E01.1080p.WEB-DL-GRP"
	hash := hashFor(name)
	h.seriesGrab(t, name, hash)
	dir := h.download(t, name, name+".mkv")
	h.completedTorrent(name, seriesCategory, dir)

	h.c.ImportSeriesDownloads(h.ctx)

	if n := pendingReviews(t, h); n != 0 {
		t.Fatalf("%d review(s) held for an alias grab, want it imported", n)
	}
	if !episodeHasFile(t, h, 1, 1) {
		t.Error("S01E01 has no file after importing the alias-named grab")
	}
}

// Same-titled shows: a release whose year names one imports there; a yearless one that
// nobody grabbed goes to review naming both; a yearless one grabbed for a show goes to it.
func TestImportSameTitledShows(t *testing.T) {
	h := newLifecycleHarness(t)
	mustExec(t, h.c, `UPDATE series SET year = 2005 WHERE id = ?`, h.seriesID)
	mustExec(t, h.c, `INSERT INTO series (tmdb_id, title, year, monitored) VALUES (99, 'Show', 1990, 1)`)

	dated := "Show.2005.S01E01.1080p.WEB-DL-GRP"
	h.completedTorrent(dated, seriesCategory, h.download(t, dated, dated+".mkv"))
	plain := "Show.S01E02.1080p.WEB-DL-GRP"
	plainHash := h.completedTorrent(plain, seriesCategory, h.download(t, plain, plain+".mkv"))

	h.c.ImportSeriesDownloads(h.ctx)

	if !episodeHasFile(t, h, 1, 1) {
		t.Error("the 2005 release didn't import into the 2005 show")
	}
	r := reviewFor(t, h.c, plainHash)
	if r.ReasonCode != ReasonUnmatched || !strings.Contains(r.Reason, "Show (1990)") || !strings.Contains(r.Reason, "Show (2005)") {
		t.Errorf("yearless release: review %q (%s), want both shows named", r.Reason, r.ReasonCode)
	}

	// The same yearless name, grabbed for the 2005 show, imports there.
	grabbed := "Show.S01E02.1080p.WEB-DL-OTHER"
	gHash := hashFor(grabbed)
	h.seriesGrab(t, grabbed, gHash)
	h.completedTorrent(grabbed, seriesCategory, h.download(t, grabbed, grabbed+".mkv"))
	h.c.ImportSeriesDownloads(h.ctx)
	if !episodeHasFile(t, h, 1, 2) {
		t.Error("a yearless release grabbed for the 2005 show didn't import into it")
	}
}

func pendingReviews(t *testing.T, h *lifecycleHarness) int {
	t.Helper()
	var n int
	if err := h.c.db.QueryRow(`SELECT COUNT(*) FROM import_reviews WHERE status = 'pending'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func episodeHasFile(t *testing.T, h *lifecycleHarness, season, episode int) bool {
	t.Helper()
	var has int
	if err := h.c.db.QueryRow(`SELECT has_file FROM episodes WHERE series_id = ? AND season_number = ? AND episode_number = ?`,
		h.seriesID, season, episode).Scan(&has); err != nil {
		t.Fatal(err)
	}
	return has == 1
}
