package series

import (
	"strings"
	"testing"

	"github.com/tristenlammi/arrmada/internal/parser"
)

func origin(cc ...string) *SeriesExtra { return &SeriesExtra{OriginCountry: cc} }

var (
	who1963   = Series{ID: 1, Title: "Doctor Who", Year: 1963, Extra: origin("GB")}
	who2005   = Series{ID: 2, Title: "Doctor Who", Year: 2005, Extra: origin("GB")}
	officeUS  = Series{ID: 3, Title: "The Office", Year: 2005, Extra: origin("US")}
	officeUK  = Series{ID: 4, Title: "The Office", Year: 2001, Extra: origin("GB")}
	bleach    = Series{ID: 5, Title: "Bleach", Year: 2004, Aliases: []Alias{{ID: 1, Title: "BLEACH Thousand-Year Blood War", TMDBSeason: 17}}}
	thisIsUs  = Series{ID: 6, Title: "This Is Us", Year: 2016, Extra: origin("US")}
	yearTitle = Series{ID: 7, Title: "1923", Year: 2022, Extra: origin("US")}
)

func TestFitReleaseYear(t *testing.T) {
	cases := []struct {
		name string
		s    Series
		ok   bool
	}{
		{"Doctor.Who.2005.S01E01.1080p-GRP", who2005, true},
		{"Doctor.Who.2005.S01E01.1080p-GRP", who1963, false},
		{"Doctor.Who.S01E01.1080p-GRP", who1963, true},                          // no year: the title is all there is
		{"Doctor.Who.2006.S02E01.1080p-GRP", who2005, true},                     // within a year
		{"Show.S01E01.2019.1080p-GRP", Series{Title: "Show", Year: 2015}, true}, // air year after the marker
		{"Show.2019.S01E01.1080p-GRP", Series{Title: "Show"}, true},             // show year unknown
		{"1923.2022.S01E01.1080p-GRP", yearTitle, true},
		{"1923.S01E01.1080p-GRP", yearTitle, true},
	}
	for _, c := range cases {
		if f := FitRelease(parser.Parse(c.name), c.s); f.OK != c.ok {
			t.Errorf("%q vs %s (%d): OK=%v want %v (%s)", c.name, c.s.Title, c.s.Year, f.OK, c.ok, f.Why)
		}
	}
	if f := FitRelease(parser.Parse("Doctor.Who.2005.S01E01"), who1963); f.Why == "" || !f.Title {
		t.Errorf("a year mismatch should say why: %+v", f)
	}
}

func TestFitReleaseCountry(t *testing.T) {
	cases := []struct {
		name string
		s    Series
		ok   bool
	}{
		{"The.Office.US.S02E01.720p-GRP", officeUS, true},
		{"The.Office.US.S02E01.720p-GRP", officeUK, false},
		{"The.Office.(US).S02E01.720p-GRP", officeUK, false},
		{"The.Office.UK.S01E01.720p-GRP", officeUK, true},
		{"The.Office.S01E01.720p-GRP", officeUK, true},
		{"The.Office.S01E01.720p-GRP", officeUS, true},
		// The tag is part of the show's own name.
		{"This.Is.Us.S01E01.1080p-GRP", thisIsUs, true},
		{"This.Is.Us.S01E01.1080p-GRP", Series{Title: "This Is Us"}, true},
		// Origin not stored yet: a title that matches as written keeps matching; one that
		// only matches with the tag taken off doesn't (as before).
		{"The.Office.(US).S02E01.720p-GRP", Series{Title: "The Office"}, true},
		{"The.Office.US.S02E01.720p-GRP", Series{Title: "The Office"}, false},
	}
	for _, c := range cases {
		if f := FitRelease(parser.Parse(c.name), c.s); f.OK != c.ok {
			t.Errorf("%q vs %s %v: OK=%v want %v (%s)", c.name, c.s.Title, c.s.Extra, f.OK, c.ok, f.Why)
		}
	}
}

func TestFitReleaseAlias(t *testing.T) {
	f := FitRelease(parser.Parse("BLEACH.Thousand-Year.Blood.War.2022.S02E02.1080p-GRP"), bleach)
	if !f.OK || !f.Alias {
		t.Errorf("an alias match skips the year check: %+v", f)
	}
	if f := FitRelease(parser.Parse("Bleachers.S01E01.1080p-GRP"), bleach); f.Title {
		t.Errorf("Bleachers is not Bleach: %+v", f)
	}
}

func TestMatchReleasePrefersYear(t *testing.T) {
	match := (&Service{}).ReleaseMatcher([]Series{who2005, who1963})
	for name, want := range map[string]int64{
		"Doctor.Who.2005.S01E01.1080p-GRP":    2,
		"Doctor.Who.1963.S01E01.576p.DVD-GRP": 1,
	} {
		got, ok, _ := match(parser.Parse(name))
		if !ok || got.ID != want {
			t.Errorf("%q → %d/%v, want %d", name, got.ID, ok, want)
		}
	}
	// One show in the library: a wrong year doesn't import into it.
	only := (&Service{}).ReleaseMatcher([]Series{who1963})
	if _, ok, cands := only(parser.Parse("Doctor.Who.2005.S01E01.1080p-GRP")); ok || len(cands) != 0 {
		t.Errorf("the 2005 release must not import into the 1963 show (ok=%v, cands=%d)", ok, len(cands))
	}
	// Country picks between the Offices.
	offices := (&Service{}).ReleaseMatcher([]Series{officeUK, officeUS})
	if got, ok, _ := offices(parser.Parse("The.Office.US.S02E01.720p-GRP")); !ok || got.ID != officeUS.ID {
		t.Errorf("The.Office.US → %d/%v", got.ID, ok)
	}
}

func TestMatchReleaseAmbiguousNoYear(t *testing.T) {
	match := (&Service{}).ReleaseMatcher([]Series{who2005, who1963})
	got, ok, cands := match(parser.Parse("Doctor.Who.S01E01.1080p-GRP"))
	if ok {
		t.Fatalf("a yearless release with two same-titled shows must not pick one (got %d)", got.ID)
	}
	if len(cands) != 2 {
		t.Fatalf("candidates = %d, want both shows", len(cands))
	}
	if d := DescribeCandidates(cands); !strings.Contains(d, "Doctor Who (1963)") || !strings.Contains(d, "Doctor Who (2005)") {
		t.Errorf("DescribeCandidates = %q", d)
	}
}

func TestMatchReleaseAliasExact(t *testing.T) {
	match := (&Service{}).ReleaseMatcher([]Series{{ID: 9, Title: "Bleachers"}, bleach})
	if got, ok, _ := match(parser.Parse("BLEACH Thousand-Year Blood War S02E02 1080p WEB-DL")); !ok || got.ID != bleach.ID {
		t.Errorf("an alias-named release → %d/%v, want Bleach", got.ID, ok)
	}
	// A show's own title beats another show's alias.
	own := Series{ID: 10, Title: "Thousand-Year Blood War"}
	match = (&Service{}).ReleaseMatcher([]Series{bleach, own, {ID: 11, Title: "Other", Aliases: []Alias{{Title: "Thousand-Year Blood War"}}}})
	if got, ok, _ := match(parser.Parse("Thousand-Year.Blood.War.S01E01.1080p-GRP")); !ok || got.ID != own.ID {
		t.Errorf("own title should win over an alias, got %d/%v", got.ID, ok)
	}
}
