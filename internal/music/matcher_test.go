package music

import (
	"fmt"
	"math"
	"path/filepath"
	"strings"
	"testing"
)

// Test fixtures for the matcher: editions and files built in memory, shaped like real
// releases. Nothing here touches a disk or a database.

func cand(albumID int64, album, credit string, year int, e Edition, target bool) MatchCandidate {
	e.AlbumID = albumID
	e.Title = album
	return MatchCandidate{AlbumID: albumID, AlbumTitle: album, AlbumYear: year, Edition: e, Credit: credit, Target: target}
}

// okc is a 12-track album, with recording MBIDs.
func okcTracks() []tr {
	titles := []string{"Airbag", "Paranoid Android", "Subterranean Homesick Alien", "Exit Music (For a Film)",
		"Let Down", "Karma Police", "Fitter Happier", "Electioneering", "Climbing Up the Walls", "No Surprises",
		"Lucky", "The Tourist"}
	out := make([]tr, len(titles))
	for i, t := range titles {
		out[i] = tr{title: t, sec: 200 + i*13, rec: fmt.Sprintf("rec-okc-%02d", i+1)}
	}
	return out
}

// filesFor makes one file per track, named "NN - Title.flac" in dir, with the given tags.
func filesFor(dir string, tracks []tr, tag func(i int, t tr) FileTags) []MatchFile {
	out := make([]MatchFile, 0, len(tracks))
	for i, t := range tracks {
		f := MatchFile{Path: filepath.Join(dir, fmt.Sprintf("%02d - %s.flac", i+1, t.title)), DurationMs: t.sec * 1000}
		if tag != nil {
			f.Tags = tag(i, t)
		}
		out = append(out, f)
	}
	return out
}

func tagged(album, artist, release string) func(int, tr) FileTags {
	return func(i int, t tr) FileTags {
		return FileTags{Title: t.title, Artist: artist, AlbumArtist: artist, Album: album, Track: i + 1,
			RecordingMBID: t.rec, ReleaseMBID: release}
	}
}

// withDemos adds n demo recordings no edition lists, like a deluxe rip's bonus disc.
func withDemos(files []MatchFile, n int) []MatchFile {
	for i := 0; i < n; i++ {
		files = append(files, MatchFile{Path: fmt.Sprintf("/dl/okc/%02d - Demo %d (4-track demo).flac", 13+i, i+1), DurationMs: (90 + i*17) * 1000,
			Tags: FileTags{Title: fmt.Sprintf("Demo %d (4-track demo)", i+1), Album: "OK Computer", Track: 13 + i}})
	}
	return files
}

func plainTags(album, artist string) func(int, tr) FileTags {
	return func(i int, t tr) FileTags {
		return FileTags{Title: t.title, Artist: artist, AlbumArtist: artist, Album: album, Track: i + 1}
	}
}

func TestMatchWeights(t *testing.T) {
	want := map[string]float64{
		"album_id": 5, "artist": 3, "album": 3, "tracks": 2, "missing_tracks": 0.9, "unmatched_tracks": 0.6,
		"data_source": 2, "media_count": 1, "media_format": 1, "year": 1, "country": 0.5, "label": 0.5,
		"catalog_number": 0.5, "album_disambiguation": 0.5, "barcode": 1,
		"track_title": 3, "track_artist": 2, "track_index": 1, "track_length": 2, "track_id": 5, "isrc": 1,
	}
	if len(want) != len(componentWeights) {
		t.Errorf("components: %d, want %d", len(componentWeights), len(want))
	}
	for name, w := range want {
		if componentWeights[name] != w {
			t.Errorf("%s weight = %v, want %v (beets' value; missing 0.9 / unmatched 0.6, recording id 5, not Lidarr's)", name, componentWeights[name], w)
		}
	}
}

func TestStringDist(t *testing.T) {
	cases := []struct {
		a, b string
		want float64
	}{
		{"Airbag", "Airbag", 0},
		{"Airbag", "airbag", 0},
		{"Björk", "Bjork", 0},
		{"Airbag", "Airbag (2017 Remaster)", 0.1},
		{"Airbag [Bonus Track]", "Airbag", 0.1},
		{"Get Lucky feat. Pharrell Williams", "Get Lucky", 0.1},
		{"Pablo Honey", "PabloHoney", 0},
		{"Karma Police", "Let Down", -1}, // far apart: checked below
		{"夜に駆ける", "夜に駆ける", 0},
		{"", "x", 1},
	}
	for _, c := range cases {
		got := stringDist(c.a, c.b)
		if c.want < 0 {
			if got < 0.6 {
				t.Errorf("stringDist(%q, %q) = %.2f, want far apart", c.a, c.b, got)
			}
			continue
		}
		if math.Abs(got-c.want) > 1e-9 {
			t.Errorf("stringDist(%q, %q) = %.3f, want %.3f", c.a, c.b, got, c.want)
		}
	}
}

// The beets arithmetic, by hand: two components, one penalty each.
func TestDistanceArithmetic(t *testing.T) {
	d := newDistance()
	d.add(CompArtist, 0.5)      // 0.5 × 3
	d.add(CompTracks, 0)        // 0 × 2
	d.add(CompTracks, 1)        // 1 × 2
	d.add(CompMissingTracks, 1) // 1 × 0.9
	raw := 0.5*3 + 0 + 2 + 0.9
	max := 3.0 + 2 + 2 + 0.9
	if got := d.value(); math.Abs(got-raw/max) > 1e-9 {
		t.Errorf("distance = %v, want %v", got, raw/max)
	}
	if got, want := d.value(CompMissingTracks), (0.5*3+2)/7.0; math.Abs(got-want) > 1e-9 {
		t.Errorf("fit = %v, want %v", got, want)
	}
}

func TestTrackDistanceComponents(t *testing.T) {
	tk := candTrack{ID: 1, Disc: 2, Position: 3, Index: 15, Title: "Lucky", LengthMs: 260000, RecordingMBID: "rec-1", Credit: "Radiohead"}
	cases := []struct {
		name string
		f    MatchFile
		want float64 // -1: just above zero is fine
		note string
	}{
		{"perfect", MatchFile{Tags: FileTags{Title: "Lucky", Track: 3, Disc: 2, RecordingMBID: "rec-1"}, DurationMs: 260000}, 0, ""},
		{"running number", MatchFile{Tags: FileTags{Title: "Lucky", Track: 15}}, 0, ""},
		{"position without disc", MatchFile{Tags: FileTags{Title: "Lucky", Track: 3}}, 0, ""},
		{"within 10 s", MatchFile{Tags: FileTags{Title: "Lucky"}, DurationMs: 269000}, 0, ""},
		{"45 s off", MatchFile{Tags: FileTags{Title: "Lucky"}, DurationMs: 305000}, 2.0 / 5, "length off by 45 s"},
		{"wrong number", MatchFile{Tags: FileTags{Title: "Lucky", Track: 7}}, 1.0 / 4, "numbered 7"},
		{"other recording", MatchFile{Tags: FileTags{Title: "Lucky", RecordingMBID: "rec-2"}}, 5.0 / 8, "different recording"},
		{"nothing to compare", MatchFile{}, 1, "nothing"},
	}
	for _, c := range cases {
		f := c.f
		got, _, notes := trackDistance(&f, &tk, false)
		if math.Abs(got-c.want) > 1e-6 {
			t.Errorf("%s: distance %.4f, want %.4f (%v)", c.name, got, c.want, notes)
		}
		if c.note != "" && !strings.Contains(strings.Join(notes, "; "), c.note) {
			t.Errorf("%s: notes %v, want one saying %q", c.name, notes, c.note)
		}
	}
}

func TestAssignMin(t *testing.T) {
	cases := []struct {
		name string
		cost [][]float64
		want []int
	}{
		{"square", [][]float64{{4, 1, 3}, {2, 0, 5}, {3, 2, 2}}, []int{1, 0, 2}},
		{"more files than tracks", [][]float64{{0.1, 0.9}, {0.9, 0.1}, {0.5, 0.5}}, []int{0, 1, -1}},
		{"more tracks than files", [][]float64{{0.9, 0.1, 0.5}}, []int{1}},
		{"empty", nil, []int{}},
		{"no columns", [][]float64{{}, {}}, []int{-1, -1}},
		{"the costliest file is the one left over", [][]float64{{0.0, 5}, {5, 5}, {5, 0.5}}, []int{0, -1, 1}},
	}
	for _, c := range cases {
		got := assignMin(c.cost, 1.0)
		if fmt.Sprint(got) != fmt.Sprint(c.want) {
			t.Errorf("%s: assignment %v, want %v", c.name, got, c.want)
		}
	}
}

func TestGroupFiles(t *testing.T) {
	mk := func(path, album, artist string) MatchFile {
		return MatchFile{Path: path, Tags: FileTags{Album: album, AlbumArtist: artist}}
	}
	t.Run("disc folders merge", func(t *testing.T) {
		g := GroupFiles([]MatchFile{
			mk("/dl/Album/CD1/01 - A.flac", "", ""), mk("/dl/Album/CD1/02 - B.flac", "", ""),
			mk("/dl/Album/CD 2 - Live/01 - C.flac", "", ""),
		})
		if len(g) != 1 || len(g[0].Files) != 3 {
			t.Fatalf("groups %+v, want one of three", g)
		}
		discs := map[string]int{}
		for _, f := range g[0].Files {
			discs[filepath.Base(f.Path)+filepath.Base(filepath.Dir(f.Path))] = f.FolderDisc
		}
		if discs["01 - A.flacCD1"] != 1 || discs["01 - C.flacCD 2 - Live"] != 2 {
			t.Errorf("folder discs: %v", discs)
		}
	})
	t.Run("a bonus folder stays with its album", func(t *testing.T) {
		g := GroupFiles([]MatchFile{
			mk("/dl/Album/01 - A.flac", "Album", "X"), mk("/dl/Album/02 - B.flac", "Album", "X"),
			mk("/dl/Album/Bonus Tracks/13 - C.flac", "", ""),
		})
		if len(g) != 1 || len(g[0].Files) != 3 || g[0].Dir != filepath.FromSlash("/dl/Album") {
			t.Fatalf("groups %+v, want one of three in /dl/Album", g)
		}
	})
	t.Run("two albums in one folder split", func(t *testing.T) {
		var files []MatchFile
		for i := 0; i < 4; i++ {
			files = append(files, mk(fmt.Sprintf("/dl/x/%02d a.flac", i), "Kid A", "Radiohead"))
			files = append(files, mk(fmt.Sprintf("/dl/x/%02d b.flac", i+10), "Amnesiac", "Radiohead"))
		}
		if g := GroupFiles(files); len(g) != 2 {
			t.Fatalf("groups = %d, want 2", len(g))
		}
	})
	t.Run("a VA folder stays one", func(t *testing.T) {
		var files []MatchFile
		for i := 0; i < 10; i++ {
			f := mk(fmt.Sprintf("/dl/va/%02d.flac", i), "Now 47", "Various Artists")
			f.Tags.Artist = fmt.Sprintf("Artist %d", i) // a different artist every track
			files = append(files, f)
		}
		files = append(files, mk("/dl/va/stray.flac", "Now 46", "Various Artists"))
		if g := GroupFiles(files); len(g) != 1 {
			t.Fatalf("groups = %d, want 1 (one stray under 25%% doesn't split it)", len(g))
		}
	})
}

func TestMatchSettingsValidate(t *testing.T) {
	if err := DefaultMatchSettings().Validate(); err != nil {
		t.Fatalf("defaults: %v", err)
	}
	bad := []MatchSettings{
		{Strong: 0.2, Expected: 0.15, Review: 0.4, TrackMax: 0.3, Gap: 0.03, PartialMin: 0.5},
		{Strong: 0.04, Expected: 0.5, Review: 0.4, TrackMax: 0.3, Gap: 0.03, PartialMin: 0.5},
		{Strong: 0.04, Expected: 0.15, Review: 0.4, TrackMax: 0, Gap: 0.03, PartialMin: 0.5},
		{Strong: 0.04, Expected: 0.15, Review: 1.4, TrackMax: 0.3, Gap: 0.03, PartialMin: 0.5},
		{Strong: -0.1, Expected: 0.15, Review: 0.4, TrackMax: 0.3, Gap: 0.03, PartialMin: 0.5},
		{Strong: 0.04, Expected: 0.15, Review: 0.4, TrackMax: 0.3, Gap: 0.6, PartialMin: 0.5},
	}
	for i, s := range bad {
		if s.Validate() == nil {
			t.Errorf("case %d (%+v) passed", i, s)
		}
	}
}

// matchCase is one row of the decision table.
type matchCase struct {
	name     string
	in       MatchInput
	decision string
	reason   string
	album    int64 // 0: don't check
	edition  int64 // 0: don't check
	switched bool
	placed   *int // nil: don't check
	left     *int // nil: don't check
	summary  string
}

func runMatchCases(t *testing.T, cases []matchCase) {
	t.Helper()
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res := Match(c.in)
			best := ""
			if res.Best != nil {
				best = fmt.Sprintf("best %s %.3f fit %.3f", EditionLabel(res.Best.Cand.Edition), res.Best.Distance, res.Best.Fit)
			}
			if res.Decision != c.decision || res.Reason != c.reason {
				t.Fatalf("decision %s/%s, want %s/%s: %s (%s)", res.Decision, res.Reason, c.decision, c.reason, res.Summary, best)
			}
			if c.album != 0 && res.AlbumID != c.album && (res.Best == nil || res.Best.Cand.AlbumID != c.album) {
				t.Errorf("album %d, want %d", res.AlbumID, c.album)
			}
			if c.edition != 0 && res.EditionID != c.edition {
				t.Errorf("edition %d, want %d (%s)", res.EditionID, c.edition, best)
			}
			if res.Switch != c.switched {
				t.Errorf("switch = %v, want %v", res.Switch, c.switched)
			}
			if c.placed != nil && len(res.Assignments) != *c.placed {
				t.Errorf("placed %d, want %d (%s)", len(res.Assignments), *c.placed, best)
			}
			if c.left != nil && len(res.Unplaced) != *c.left {
				t.Errorf("left %d, want %d: %+v", len(res.Unplaced), *c.left, res.Unplaced)
			}
			if c.summary != "" && !strings.Contains(res.Summary, c.summary) {
				t.Errorf("summary %q, want it to say %q", res.Summary, c.summary)
			}
			// Every held match carries an explanation Reviews can show.
			if res.Decision == MatchReview && (res.Report.Summary == "" || len(res.Report.Candidates) == 0 && len(c.in.Candidates) > 0) {
				t.Errorf("held without an explanation: %+v", res.Report)
			}
		})
	}
}

func TestMatchDecisions(t *testing.T) {
	okc := okcTracks()
	std := edition("rel-std", "GB", "1997-05-21", "CD", okc)
	stdC := cand(1, "OK Computer", "Radiohead", 1997, std, true)

	bonus := append(append([]tr(nil), okc...), tr{"Polyethylene (Parts 1 & 2)", 263, "rec-poly"},
		tr{"Pearly*", 213, "rec-pearly"}, tr{"Meeting in the Aisle", 188, "rec-meeting"})
	deluxe := edition("rel-dlx", "XE", "2009-03-24", "CD", bonus)
	deluxeC := cand(1, "OK Computer", "Radiohead", 1997, deluxe, false)

	// The deluxe as the target, the standard another edition (for the switch back).
	stdNotTarget := cand(1, "OK Computer", "Radiohead", 1997, std, false)
	deluxeTarget := cand(1, "OK Computer", "Radiohead", 1997, deluxe, true)

	// A US pressing with exactly the standard's tracks: equivalent.
	us := edition("rel-us", "US", "1997-07-01", "CD", okc)
	usC := cand(1, "OK Computer", "Radiohead", 1997, us, false)

	// Two pressings that differ only in one track's length by 5 s: not equivalent, and the
	// files fit both equally.
	jpTracks := append([]tr(nil), okc...)
	jpTracks[11].sec += 5
	jp := edition("rel-jp", "JP", "1997-05-21", "CD", jpTracks)
	ca := edition("rel-ca", "CA", "1997-05-21", "CD", okc)
	jpC, caC := cand(1, "OK Computer", "Radiohead", 1997, jp, false), cand(1, "OK Computer", "Radiohead", 1997, ca, false)
	weirdTarget := cand(1, "OK Computer", "Radiohead", 1997, edition("rel-box", "XE", "2017-06-23", "CD", okc[:3]), true)

	kidA := edition("rel-kida", "GB", "2000-10-02", "CD", []tr{{"Everything in Its Right Place", 251, "rec-k1"},
		{"Kid A", 284, "rec-k2"}, {"The National Anthem", 351, "rec-k3"}, {"How to Disappear Completely", 356, "rec-k4"}})
	kidAC := cand(2, "Kid A", "Radiohead", 2000, kidA, true)

	twoDisc := edition("rel-2cd", "GB", "2007-12-03", "CD",
		[]tr{{"15 Step", 237, "r-a1"}, {"Bodysnatchers", 242, "r-a2"}, {"Nude", 255, "r-a3"}},
		[]tr{{"MK 1", 64, "r-b1"}, {"Down Is the New Up", 299, "r-b2"}, {"Go Slowly", 228, "r-b3"}})
	twoDiscC := cand(3, "In Rainbows", "Radiohead", 2007, twoDisc, true)

	vaTracks := []tr{{"Song A", 200, "va-1"}, {"Song B", 210, "va-2"}, {"Song C", 220, "va-3"}, {"Song D", 230, "va-4"}}
	va := edition("rel-va", "GB", "2000-01-01", "CD", vaTracks)
	vaC := cand(4, "Now That's What I Call Music! 45", "Various Artists", 2000, va, true)
	vaC.VariousArtists = true
	vaC.TrackCredits = map[int64]string{}
	for i, rt := range va.Media[0].Tracks {
		vaC.TrackCredits[rt.ID] = fmt.Sprintf("Artist %c", 'A'+i)
	}

	ram := edition("rel-ram", "XW", "2013-05-17", "Digital Media", []tr{{"Give Life Back to Music", 274, "ram-1"}, {"Get Lucky", 369, "ram-8"}})
	ramC := cand(5, "Random Access Memories", "Daft Punk", 2013, ram, true)
	ramC.TrackCredits = map[int64]string{ram.Media[0].Tracks[1].ID: "Daft Punk feat. Pharrell Williams & Nile Rodgers"}

	yoasobi := edition("rel-jp-ya", "JP", "2021-01-06", "Digital Media", []tr{{"夜に駆ける", 261, "ya-1"}, {"ハルジオン", 219, "ya-2"}, {"たぶん", 251, "ya-3"}})
	yoasobiC := cand(6, "THE BOOK", "YOASOBI", 2021, yoasobi, true)

	untagged := func(dir string, tracks []tr) []MatchFile { return filesFor(dir, tracks, nil) }

	cases := []matchCase{
		{
			name: "files with MusicBrainz ids match strongly, no grab needed",
			in: MatchInput{Files: filesFor("/dl/okc", okc, tagged("OK Computer", "Radiohead", "rel-std")),
				Candidates: []MatchCandidate{stdC, deluxeC}},
			decision: MatchImport, album: 1, edition: std.ID, placed: p(12), left: p(0),
		},
		{
			name: "stripped tags, good names, grabbed for it: imports",
			in: MatchInput{Files: untagged("/dl/Radiohead - OK Computer (1997) [FLAC]", okc), FolderName: "Radiohead - OK Computer (1997) [FLAC]",
				Candidates: []MatchCandidate{stdC}, GrabbedAlbumID: 1},
			decision: MatchImport, placed: p(12),
		},
		{
			name: "stripped tags, good names, nobody grabbed it: Reviews",
			in: MatchInput{Files: untagged("/dl/Radiohead - OK Computer (1997) [FLAC]", okc), FolderName: "Radiohead - OK Computer (1997) [FLAC]",
				Candidates: []MatchCandidate{stdC}},
			decision: MatchReview, reason: MatchReasonLowConfidence, placed: p(0), summary: "Only the file names",
		},
		{
			name: "a deluxe download completes the album by switching to the deluxe",
			in: MatchInput{Files: filesFor("/dl/okc-dlx", bonus, plainTags("OK Computer", "Radiohead")),
				Candidates: []MatchCandidate{stdC, deluxeC}, GrabbedAlbumID: 1},
			decision: MatchImport, edition: deluxe.ID, switched: true, placed: p(15), left: p(0),
		},
		{
			name: "a standard download against a deluxe target switches to the standard",
			in: MatchInput{Files: filesFor("/dl/okc", okc, plainTags("OK Computer", "Radiohead")),
				Candidates: []MatchCandidate{deluxeTarget, stdNotTarget}, GrabbedAlbumID: 1},
			decision: MatchImport, edition: std.ID, switched: true, placed: p(12),
		},
		{
			name: "pinned to the standard: a deluxe download places only the standard's tracks",
			in: MatchInput{Files: filesFor("/dl/okc-dlx", bonus, plainTags("OK Computer", "Radiohead")),
				Candidates: []MatchCandidate{pinned(stdC), pinned(deluxeC)}, GrabbedAlbumID: 1},
			decision: MatchImport, edition: std.ID, switched: false, placed: p(12), left: p(3),
		},
		{
			name: "a remaster's bracketed titles cost little",
			in: MatchInput{Files: filesFor("/dl/okc-rm", okc, func(i int, t tr) FileTags {
				return FileTags{Title: t.title + " (Remastered)", Artist: "Radiohead", AlbumArtist: "Radiohead", Album: "OK Computer (Remastered)", Track: i + 1}
			}), Candidates: []MatchCandidate{stdC}, GrabbedAlbumID: 1},
			decision: MatchImport, placed: p(12),
		},
		{
			name: "two equivalent pressings import silently onto the target",
			in: MatchInput{Files: filesFor("/dl/okc", okc, plainTags("OK Computer", "Radiohead")),
				Candidates: []MatchCandidate{usC, stdC}, GrabbedAlbumID: 1},
			decision: MatchImport, edition: std.ID, switched: false, placed: p(12),
		},
		{
			name: "a close runner-up: two pressings the files fit equally go to Reviews",
			in: MatchInput{Files: filesFor("/dl/okc", okc, plainTags("OK Computer", "Radiohead")),
				Candidates: []MatchCandidate{weirdTarget, jpC, caC}, GrabbedAlbumID: 1},
			decision: MatchReview, reason: MatchReasonCloseEditions, summary: "Two editions fit almost equally",
		},
		{
			name: "a close runner-up loses to the album's target",
			in: MatchInput{Files: filesFor("/dl/okc", okc, plainTags("OK Computer", "Radiohead")),
				Candidates: []MatchCandidate{jpC, target(caC)}, GrabbedAlbumID: 1},
			decision: MatchImport, edition: ca.ID, switched: false,
		},
		{
			name:     "multi-disc in CD1/CD2 folders",
			in:       MatchInput{Files: multiDiscFiles(twoDisc), Candidates: []MatchCandidate{twoDiscC}, GrabbedAlbumID: 3},
			decision: MatchImport, placed: p(6), left: p(0),
		},
		{
			name: "a bonus track in the download stays in it",
			in: MatchInput{Files: append(filesFor("/dl/okc", okc, plainTags("OK Computer", "Radiohead")),
				MatchFile{Path: "/dl/okc/13 - Lull.flac", DurationMs: 147000, Tags: FileTags{Title: "Lull", Album: "OK Computer", Track: 13}}),
				Candidates: []MatchCandidate{stdC}, GrabbedAlbumID: 1},
			decision: MatchImport, placed: p(12), left: p(1),
		},
		{
			// #115: a download grabbed for the album with the whole album and twelve demo
			// tracks. The demos stay in the download; they don't hold the album back.
			name:     "the whole album and many extras, grabbed for it, imports",
			in:       MatchInput{Files: withDemos(filesFor("/dl/okc", okc, plainTags("OK Computer", "Radiohead")), 12), Candidates: []MatchCandidate{stdC}, GrabbedAlbumID: 1},
			decision: MatchImport, placed: p(12), left: p(12),
		},
		{
			name:     "the same files nobody grabbed are still held",
			in:       MatchInput{Files: withDemos(filesFor("/dl/okc", okc, plainTags("OK Computer", "Radiohead")), 12), Candidates: []MatchCandidate{stdC}},
			decision: MatchReview, reason: MatchReasonLowConfidence,
		},
		{
			name: "a hidden track (a ten-minute track 13) stays in the download",
			in: MatchInput{Files: append(filesFor("/dl/okc", okc, plainTags("OK Computer", "Radiohead")),
				MatchFile{Path: "/dl/okc/13 - (hidden).flac", DurationMs: 600000}),
				Candidates: []MatchCandidate{stdC}, GrabbedAlbumID: 1},
			decision: MatchImport, placed: p(12), left: p(1),
		},
		{
			name: "one track missing: a partial import",
			in: MatchInput{Files: filesFor("/dl/okc", okc[:11], plainTags("OK Computer", "Radiohead")),
				Candidates: []MatchCandidate{stdC}, GrabbedAlbumID: 1},
			decision: MatchPartial, placed: p(11),
		},
		{
			name: "a partial above the minimum imports",
			in: MatchInput{Files: filesFor("/dl/okc", okc[:7], plainTags("OK Computer", "Radiohead")),
				Candidates: []MatchCandidate{stdC}, GrabbedAlbumID: 1},
			decision: MatchPartial, placed: p(7),
		},
		{
			name: "a partial below the minimum goes to Reviews",
			in: MatchInput{Files: filesFor("/dl/okc", okc[:3], plainTags("OK Computer", "Radiohead")),
				Candidates: []MatchCandidate{stdC}, GrabbedAlbumID: 1},
			decision: MatchReview, reason: MatchReasonPartial, summary: "Only 3 of the 12 tracks",
		},
		{
			name: "a partial of exactly the tracks still wanted imports",
			in: MatchInput{Files: filesForAt("/dl/okc", okc, 9, 12, plainTags("OK Computer", "Radiohead")),
				Candidates: []MatchCandidate{stdC}, GrabbedAlbumID: 1, Wanted: wantedOf(1, std, 9, 12)},
			decision: MatchPartial, placed: p(3),
		},
		{
			name: "one file far off its track is left; the rest import",
			in: MatchInput{Files: offTrack(filesFor("/dl/okc", okc, plainTags("OK Computer", "Radiohead")), 6),
				Candidates: []MatchCandidate{stdC}, GrabbedAlbumID: 1},
			decision: MatchImport, placed: p(11), left: p(1), summary: "1 left in the download",
		},
		{
			name: "Various Artists: the album and track artists aren't compared",
			in: MatchInput{Files: filesFor("/dl/now45", vaTracks, func(i int, t tr) FileTags {
				return FileTags{Title: t.title, Artist: fmt.Sprintf("Artist %c", 'A'+i), AlbumArtist: "Various Artists",
					Album: "Now That's What I Call Music! 45", Track: i + 1, Compilation: true}
			}), Candidates: []MatchCandidate{vaC}, GrabbedAlbumID: 4},
			decision: MatchImport, placed: p(4),
		},
		{
			name: "featured artists in the tags",
			in: MatchInput{Files: []MatchFile{
				{Path: "/dl/ram/01.flac", DurationMs: 274000, Tags: FileTags{Title: "Give Life Back to Music", Artist: "Daft Punk", Album: "Random Access Memories", Track: 1}},
				{Path: "/dl/ram/08.flac", DurationMs: 369000, Tags: FileTags{Title: "Get Lucky (feat. Pharrell Williams)", Artist: "Daft Punk feat. Pharrell Williams", Album: "Random Access Memories", Track: 2}},
			}, Candidates: []MatchCandidate{ramC}, GrabbedAlbumID: 5},
			decision: MatchImport, placed: p(2),
		},
		{
			name: "non-Latin titles",
			in: MatchInput{Files: filesFor("/dl/thebook", []tr{{"夜に駆ける", 261, ""}, {"ハルジオン", 219, ""}, {"たぶん", 251, ""}},
				plainTags("THE BOOK", "YOASOBI")), Candidates: []MatchCandidate{yoasobiC}},
			decision: MatchImport, placed: p(3),
		},
		{
			name: "grabbed for one album, the files are another: mismatch",
			in: MatchInput{Files: filesFor("/dl/kida", kidA.Media[0].trs(), plainTags("Kid A", "Radiohead")),
				Candidates: []MatchCandidate{stdC, kidAC}, GrabbedAlbumID: 1},
			decision: MatchReview, reason: MatchReasonMismatch,
		},
		{
			name: "tags naming another album's recordings: tag conflict",
			in: MatchInput{Files: conflicting(filesFor("/dl/okc", okc, tagged("OK Computer", "Radiohead", "")), 4),
				Candidates: []MatchCandidate{stdC}, GrabbedAlbumID: 1},
			decision: MatchReview, reason: MatchReasonTagConflict,
		},
		{
			name: "AcoustID disagreeing on two tracks: fingerprint conflict",
			in: MatchInput{Files: fingerprinted(filesFor("/dl/okc", okc, plainTags("OK Computer", "Radiohead")), 2),
				Candidates: []MatchCandidate{stdC}, GrabbedAlbumID: 1},
			decision: MatchReview, reason: MatchReasonFingerprintConflict,
		},
		{
			name: "nothing like it: couldn't identify",
			in: MatchInput{Files: filesFor("/dl/x", []tr{{"Foo", 100, ""}, {"Bar", 500, ""}}, plainTags("Something Else", "Nobody")),
				Candidates: []MatchCandidate{stdC}},
			decision: MatchReview, reason: MatchReasonUnmatched, summary: "Couldn't identify",
		},
		{
			name:     "no candidates at all",
			in:       MatchInput{Files: filesFor("/dl/x", okc[:2], nil)},
			decision: MatchReview, reason: MatchReasonUnmatched, placed: p(0),
		},
		{
			name: "an album with no target edition yet: the one that fits becomes it",
			in: MatchInput{Files: filesFor("/dl/okc", okc, plainTags("OK Computer", "Radiohead")),
				Candidates: []MatchCandidate{stdNotTarget}, GrabbedAlbumID: 1},
			decision: MatchImport, edition: std.ID, switched: true, placed: p(12),
		},
		{
			name:     "forced: a person chose the album, the fitting files go in",
			in:       MatchInput{Files: filesFor("/dl/okc", okc[:3], nil), Candidates: []MatchCandidate{stdC}, Forced: true},
			decision: MatchImport, placed: p(3),
		},
		{
			name: "forced onto an edition a person chose, over the pin",
			in: MatchInput{Files: filesFor("/dl/okc-dlx", bonus, plainTags("OK Computer", "Radiohead")),
				Candidates: []MatchCandidate{pinned(stdC), pinned(deluxeC)}, Forced: true, ForceEditionID: deluxe.ID},
			decision: MatchImport, edition: deluxe.ID, switched: true, placed: p(15),
		},
	}
	runMatchCases(t, cases)
}

func p(n int) *int { return &n }

func pinned(c MatchCandidate) MatchCandidate { c.Pinned = true; return c }
func target(c MatchCandidate) MatchCandidate { c.Target = true; return c }

// trs reads a fixture medium's tracks back as tr rows.
func (m Medium) trs() []tr {
	out := make([]tr, 0, len(m.Tracks))
	for _, t := range m.Tracks {
		out = append(out, tr{title: t.Title, sec: t.LengthMs / 1000, rec: t.RecordingMBID})
	}
	return out
}

// multiDiscFiles names each disc's files from 01 in CD1/CD2 folders, untagged but for
// the album, as rips often are.
func multiDiscFiles(e Edition) []MatchFile {
	var files []MatchFile
	for _, m := range e.Media {
		for i, t := range m.Tracks {
			files = append(files, MatchFile{
				Path:       fmt.Sprintf("/dl/In Rainbows/CD%d/%02d - %s.flac", m.Position, i+1, t.Title),
				DurationMs: t.LengthMs, Tags: FileTags{Album: "In Rainbows", Title: t.Title, AlbumArtist: "Radiohead"},
			})
		}
	}
	groups := GroupFiles(files)
	if len(groups) != 1 {
		panic("disc folders didn't merge")
	}
	return groups[0].Files
}

// filesForAt makes files for tracks [from, to) only, keeping their numbers.
func filesForAt(dir string, tracks []tr, from, to int, tag func(int, tr) FileTags) []MatchFile {
	all := filesFor(dir, tracks, tag)
	return all[from:to]
}

func wantedOf(albumID int64, e Edition, from, to int) []WantedTrack {
	var out []WantedTrack
	for _, t := range e.Media[0].Tracks[from:to] {
		out = append(out, WantedTrack{AlbumID: albumID, TrackID: t.ID, RecordingID: t.RecordingID, Title: t.Title})
	}
	return out
}

// offTrack makes file i an unrelated recording: another title, 45 s longer, no number.
func offTrack(files []MatchFile, i int) []MatchFile {
	files[i].Tags.Title = "Something Completely Different"
	files[i].Tags.Track = 0
	files[i].Path = filepath.Join(filepath.Dir(files[i].Path), "zz - Something Completely Different.flac")
	files[i].DurationMs += 45000
	return files
}

// conflicting gives n files recording MBIDs of another album.
func conflicting(files []MatchFile, n int) []MatchFile {
	for i := 0; i < n; i++ {
		files[i].Tags.RecordingMBID = fmt.Sprintf("rec-other-album-%d", i)
	}
	return files
}

// fingerprinted has AcoustID name other recordings for the first n files.
func fingerprinted(files []MatchFile, n int) []MatchFile {
	for i := range files {
		files[i].Fingerprint = []string{fmt.Sprintf("rec-okc-%02d", i+1)}
		if i < n {
			files[i].Fingerprint = []string{fmt.Sprintf("rec-elsewhere-%d", i)}
		}
	}
	return files
}

// The report a held match stores names its candidates with their breakdowns in words.
func TestMatchReportBreakdown(t *testing.T) {
	okc := okcTracks()
	std := edition("rel-std2", "GB", "1997-05-21", "CD", okc)
	files := filesFor("/dl/okc", okc[:3], plainTags("OK Computer", "Radiohead"))
	files[1].DurationMs += 45000 // the right song, 45 s too long
	res := Match(MatchInput{Files: files, Candidates: []MatchCandidate{cand(1, "OK Computer", "Radiohead", 1997, std, true)}, GrabbedAlbumID: 1})
	if res.Reason != MatchReasonPartial {
		t.Fatalf("reason %q (%s)", res.Reason, res.Summary)
	}
	rep := res.Report
	if len(rep.Candidates) != 1 || rep.Candidates[0].Edition == "" || len(rep.Candidates[0].Components) == 0 {
		t.Fatalf("report %+v", rep)
	}
	c := rep.Candidates[0]
	if len(c.Missing) != 9 {
		t.Errorf("missing %v, want the 9 tracks with no file", c.Missing)
	}
	found := false
	for _, tk := range c.Tracks {
		if strings.Contains(strings.Join(tk.Notes, " "), "length off by 45 s") {
			found = true
		}
	}
	if !found {
		t.Errorf("tracks %+v, want one saying its length is off by 45 s", c.Tracks)
	}
	share := 0.0
	for _, comp := range c.Components {
		share += comp.Share
	}
	if math.Abs(share-c.Distance) > 0.01 {
		t.Errorf("component shares sum to %.4f, distance %.4f", share, c.Distance)
	}
}
