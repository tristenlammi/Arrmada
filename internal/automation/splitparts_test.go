package automation

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/tristenlammi/arrmada/internal/library"
	"github.com/tristenlammi/arrmada/internal/series"
)

func TestSplitPartTitle(t *testing.T) {
	for _, c := range []struct {
		in, base string
		part     int
	}{
		{"The Simpsons Guy Part 1", "The Simpsons Guy", 1},
		{"The Simpsons Guy Part 2", "The Simpsons Guy", 2},
		{"And Then There Were Fewer Pt 2", "And Then There Were Fewer", 2},
		{"And Then There Were Fewer Pt. 1", "And Then There Were Fewer", 1},
		{"Blue Harvest (Part 2)", "Blue Harvest", 2},
		{"Blue Harvest, Part II", "Blue Harvest", 2},
		{"The Return - Part Three", "The Return", 3},
		{"Road to the Multiverse", "Road to the Multiverse", 0},
		{"Part 2", "Part 2", 0},                         // nothing left of the title
		{"Part of Your World", "Part of Your World", 0}, // not a marker
		{"Apartment 2", "Apartment 2", 0},
		{"The Story Part 7", "The Story Part 7", 0}, // more parts than are ever joined
		{"Counterpart 1", "Counterpart 1", 0},
	} {
		if base, part := splitPartTitle(c.in); base != c.base || part != c.part {
			t.Errorf("%q: %q part %d, want %q part %d", c.in, base, part, c.base, c.part)
		}
	}
}

func splitFile(name string, season, num int, placed ...int) resolvedVideo {
	f := resolvedVideo{v: library.FoundVideo{Path: "/dl/" + name, Size: 100},
		numbered: []series.EpisodeRef{{Season: season, Episode: num}}}
	ep := num
	if len(placed) > 0 {
		ep = placed[0]
	}
	f.refs = []series.EpisodeRef{{Season: season, Episode: ep}}
	f.byTitle = ep != num
	return f
}

// The evidence a group needs, one piece missing at a time.
func TestFindSplitPartsNeedsAllItsEvidence(t *testing.T) {
	list := map[int]string{1: "The Long Goodbye Begins", 2: "Second Story Here", 3: "Third Story Here"}
	titles := func(l map[int]string) func(int) map[int]string {
		return func(int) map[int]string { return l }
	}
	p := func(n, part string) string {
		return "Show.S01E0" + n + ".The.Long.Goodbye.Begins.Part." + part + ".1080p.WEB-DL.x264-GRP.mkv"
	}
	for _, c := range []struct {
		name  string
		files []resolvedVideo
		list  map[int]string
		want  int
	}{
		{"both parts, numbered in turn", []resolvedVideo{splitFile(p("1", "1"), 1, 1), splitFile(p("2", "2"), 1, 2, 1)}, list, 1},
		{"found whatever order the files come in", []resolvedVideo{splitFile(p("2", "2"), 1, 2, 1), splitFile(p("1", "1"), 1, 1)}, list, 1},
		{"both parts under one number", []resolvedVideo{splitFile(p("1", "1"), 1, 1), splitFile(p("1", "2"), 1, 1)}, list, 1},
		{"three parts", []resolvedVideo{splitFile(p("1", "1"), 1, 1), splitFile(p("2", "2"), 1, 2), splitFile(p("3", "3"), 1, 3)}, list, 1},
		{"only part 1", []resolvedVideo{splitFile(p("1", "1"), 1, 1)}, list, 0},
		{"parts 1 and 3", []resolvedVideo{splitFile(p("1", "1"), 1, 1), splitFile(p("2", "3"), 1, 2)}, list, 0},
		{"part 2 twice", []resolvedVideo{splitFile(p("1", "2"), 1, 1), splitFile(p("2", "2"), 1, 2)}, list, 0},
		{"a REPACK of part 1 beside both", []resolvedVideo{splitFile(p("1", "1"), 1, 1), splitFile(p("2", "2"), 1, 2), splitFile("Show.S01E01.The.Long.Goodbye.Begins.Part.1.REPACK.1080p.WEB-DL.x264-GRP.mkv", 1, 1)}, list, 0},
		{"numbered out of order", []resolvedVideo{splitFile(p("2", "1"), 1, 2), splitFile(p("1", "2"), 1, 1)}, list, 0},
		{"numbered apart", []resolvedVideo{splitFile(p("1", "1"), 1, 1), splitFile(p("3", "2"), 1, 3)}, list, 0},
		{"parts in different seasons", []resolvedVideo{splitFile(p("1", "1"), 1, 1), splitFile(p("2", "2"), 2, 2)}, list, 0},
		{"no episode called that", []resolvedVideo{splitFile(p("1", "1"), 1, 1), splitFile(p("2", "2"), 1, 2)},
			map[int]string{1: "Something Else Entirely", 2: "Second Story Here"}, 0},
		{"two episodes called that", []resolvedVideo{splitFile(p("1", "1"), 1, 1), splitFile(p("2", "2"), 1, 2)},
			map[int]string{1: "The Long Goodbye Begins", 2: "The Long Goodbye Begins"}, 0},
		{"the list counts the parts too", []resolvedVideo{splitFile(p("1", "1"), 1, 1), splitFile(p("2", "2"), 1, 2)},
			map[int]string{1: "The Long Goodbye Begins (Part 1)", 2: "The Long Goodbye Begins (Part 2)"}, 0},
		{"the list has the episode and a part of it", []resolvedVideo{splitFile(p("1", "1"), 1, 1), splitFile(p("2", "2"), 1, 2)},
			map[int]string{1: "The Long Goodbye Begins", 2: "The Long Goodbye Begins, Part 2"}, 0},
		{"a Part 4 beside parts 1 to 3", []resolvedVideo{splitFile(p("1", "1"), 1, 1), splitFile(p("2", "2"), 1, 2), splitFile(p("3", "3"), 1, 3), splitFile(p("4", "4"), 1, 4)}, list, 0},
		{"the list names the second half (2)", []resolvedVideo{splitFile(p("1", "1"), 1, 1), splitFile(p("2", "2"), 1, 2)},
			map[int]string{1: "The Long Goodbye Begins", 2: "The Long Goodbye Begins (2)"}, 0},
		{"the list names the second half II", []resolvedVideo{splitFile(p("1", "1"), 1, 1), splitFile(p("2", "2"), 1, 2)},
			map[int]string{1: "The Long Goodbye Begins", 2: "The Long Goodbye Begins II"}, 0},
		{"the list names the second half Part 2 of 2", []resolvedVideo{splitFile(p("1", "1"), 1, 1), splitFile(p("2", "2"), 1, 2)},
			map[int]string{1: "The Long Goodbye Begins", 2: "The Long Goodbye Begins (Part 2 of 2)"}, 0},
		{"a two-episode file isn't a part", []resolvedVideo{
			{v: library.FoundVideo{Path: "/dl/" + p("1", "1")}, numbered: []series.EpisodeRef{{Season: 1, Episode: 1}, {Season: 1, Episode: 2}}},
			splitFile(p("3", "2"), 1, 3)}, list, 0},
	} {
		got := findSplitParts(c.files, titles(c.list))
		if len(got) != c.want {
			t.Errorf("%s: %d groups, want %d", c.name, len(got), c.want)
			continue
		}
		if c.want == 1 && (got[0].ref != series.EpisodeRef{Season: 1, Episode: 1}) {
			t.Errorf("%s: the parts are %+v, want S01E01", c.name, got[0].ref)
		}
	}
}

// Nothing is joined or moved unless a later file's title confirms the count; then a file
// is moved by the count alone only when it names nothing.
func TestApplySplitPartsNeedsTheSeasonToConfirmIt(t *testing.T) {
	list := map[int]string{1: "The Long Goodbye Begins", 2: "Second Story Here", 3: "Third Story Here", 4: "Fourth Story Here"}
	titles := func(int) map[int]string { return list }
	exists := func(r series.EpisodeRef) bool { return r.Season == 1 && r.Episode >= 1 && r.Episode <= 4 }
	parts := []resolvedVideo{
		splitFile("Show.S01E01.The.Long.Goodbye.Begins.Part.1.1080p.WEB-DL.x264-GRP.mkv", 1, 1),
		splitFile("Show.S01E02.The.Long.Goodbye.Begins.Part.2.1080p.WEB-DL.x264-GRP.mkv", 1, 2, 1),
	}
	run := func(rest ...resolvedVideo) (out []resolvedVideo, rescued int) {
		files := append(append([]resolvedVideo(nil), parts...), rest...)
		out, _, rescued = applySplitParts(files, findSplitParts(files, titles), titles, exists)
		return out, rescued
	}
	place := func(out []resolvedVideo) string {
		s := ""
		for _, f := range out {
			if len(f.parts) > 0 {
				s += "J"
			}
			s += string(rune('0'+f.refs[0].Episode)) + " "
		}
		return s
	}

	// A later title agrees with the count; a file that names nothing moves with it.
	out, _ := run(splitFile("Show.S01E03.Second.Story.Here.mkv", 1, 3, 2), splitFile("Show.S01E04.Shortened.mkv", 1, 4))
	if got := place(out); got != "J1 2 3 " {
		t.Errorf("a later title that agrees: %s", got)
	}
	if out[0].v.Size != 200 || len(out[0].parts) != 2 || out[0].byTitle {
		t.Errorf("the joined file: %+v", out[0])
	}
	// Numbers only: nothing confirms the count, so nothing is joined or moved.
	out, _ = run(splitFile("Show.S01E03.mkv", 1, 3), splitFile("Show.S01E04.mkv", 1, 4))
	if got := place(out); got != "1 1 3 4 " {
		t.Errorf("numbers only: %s", got)
	}
	// One title that disagrees with the count: as it came.
	out, _ = run(splitFile("Show.S01E03.Fourth.Story.Here.mkv", 1, 3, 4), splitFile("Show.S01E04.Shortened.mkv", 1, 4))
	if got := place(out); got != "1 1 4 4 " {
		t.Errorf("a title that disagrees: %s", got)
	}
	// A title confirming its own number disagrees too.
	out, _ = run(splitFile("Show.S01E03.Third.Story.Here.mkv", 1, 3), splitFile("Show.S01E04.mkv", 1, 4))
	if got := place(out); got != "1 1 3 4 " {
		t.Errorf("a title confirming the number: %s", got)
	}
	// A file with two numbers after the parts can't be counted: as it came.
	two := resolvedVideo{v: library.FoundVideo{Path: "/dl/Show.S01E04E05.mkv"},
		numbered: []series.EpisodeRef{{Season: 1, Episode: 4}, {Season: 1, Episode: 5}},
		refs:     []series.EpisodeRef{{Season: 1, Episode: 4}}}
	out, _ = run(splitFile("Show.S01E03.Second.Story.Here.mkv", 1, 3, 2), two)
	if got := place(out); got != "1 1 2 4 " {
		t.Errorf("a two-episode file after the parts: %s", got)
	}
	// Another season's files neither confirm nor move.
	out, _ = run(splitFile("Show.S02E03.Second.Story.Here.mkv", 2, 3))
	if got := place(out); got != "1 1 3 " {
		t.Errorf("another season: %s", got)
	}
	// The season's last file, numbered one past its end: placed, and counted as rescued.
	beyond := resolvedVideo{v: library.FoundVideo{Path: "/dl/Show.S01E05.mkv"}, numbered: []series.EpisodeRef{{Season: 1, Episode: 5}}, beyond: true}
	out, rescued := run(splitFile("Show.S01E03.Second.Story.Here.mkv", 1, 3, 2), splitFile("Show.S01E04.mkv", 1, 4), beyond)
	if got := place(out); got != "J1 2 3 4 " || rescued != 1 {
		t.Errorf("one past the season: %s, rescued %d", got, rescued)
	}
	// Unconfirmed, one past the season stays out, uncounted.
	out, rescued = run(beyond)
	if got := place(out); got != "1 1 " || rescued != 0 {
		t.Errorf("unconfirmed, past the season: %s, rescued %d", got, rescued)
	}
}

// A join's leftovers from a restart are cleared; a join running now is left alone.
func TestSweepJoinDir(t *testing.T) {
	base := t.TempDir()
	old, fresh, other := filepath.Join(base, "j-old"), filepath.Join(base, "j-new"), filepath.Join(base, "keep")
	for _, d := range []string{old, fresh, other} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now()
	past := now.Add(-joinLeftoverAge - time.Hour)
	_ = os.Chtimes(old, past, past)
	_ = os.Chtimes(other, past, past)
	sweepJoinDir(base, now)
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Error("an old join folder was left")
	}
	for _, d := range []string{fresh, other} {
		if _, err := os.Stat(d); err != nil {
			t.Errorf("%s was removed", filepath.Base(d))
		}
	}
}
