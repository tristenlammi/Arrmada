package automation

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/tristenlammi/arrmada/internal/library"
	"github.com/tristenlammi/arrmada/internal/metadata"
	"github.com/tristenlammi/arrmada/internal/series"
)

// mapHarness is a lifecycle harness whose show has four episodes in season 1, with a
// held numbering review for a pack whose files carry no usable numbers. Temp dirs only.
func mapHarness(t *testing.T) (*lifecycleHarness, int64, string, string) {
	t.Helper()
	h := newLifecycleHarness(t)
	meta := &importMeta{d: metadata.SeriesDetails{
		SeriesResult:    metadata.SeriesResult{TMDBID: 4, Title: "Mapped Show"},
		NumberingSource: "tmdb",
		Seasons: []metadata.SeasonDetails{{SeasonNumber: 1, Episodes: []metadata.EpisodeDetails{
			{EpisodeNumber: 1}, {EpisodeNumber: 2}, {EpisodeNumber: 3}, {EpisodeNumber: 4},
		}}},
	}}
	root := t.TempDir()
	h.c.series = series.NewService(h.c.db, meta, root, h.c.log)
	h.c.imp = library.NewImporter(root, h.c.log)
	s, err := h.c.series.Add(h.ctx, 4, "", true)
	if err != nil {
		t.Fatal(err)
	}
	h.seriesID = s.ID
	name := "Mapped.Show.Season.One.1080p.WEB-DL-GRP"
	dir := h.download(t, name, "Pilot.mkv", "Second.mkv", "Two-Parter.mkv")
	hash := hashFor(name)
	h.seriesGrab(t, name, hash)
	h.c.addReview(h.ctx, Review{
		Hash: hash, Name: name, ContentPath: dir, MediaType: "series", ReasonCode: ReasonNumbering,
		ExpectedID: s.ID, ExpectedTitle: s.Title, Reason: "numbering",
	})
	rid := reviewFor(t, h.c, hash).ID
	return h, rid, dir, root
}

// Mapping files by hand places each at its episode's library path and marks the episodes
// present — both halves of a double episode — then closes the review and its grab.
func TestImportReviewMapped(t *testing.T) {
	h, rid, _, _ := mapHarness(t)
	n, err := h.c.ImportReviewMapped(h.ctx, rid, 0, []FileMapping{
		{RelPath: "Pilot.mkv", Season: 1, Episodes: []int{1}},
		{RelPath: "Second.mkv", Season: 1, Episodes: []int{2}},
		{RelPath: "Two-Parter.mkv", Season: 1, Episodes: []int{4, 3}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if n != 4 {
		t.Errorf("placed %d episodes, want 4", n)
	}
	s, _ := h.c.series.Get(h.ctx, h.seriesID)
	folder := h.c.series.ExistingFolderName(h.ctx, s.ID)
	for ep, src := range map[int]string{1: "Pilot.mkv", 2: "Second.mkv"} {
		cur := h.c.series.CurrentEpisodeFile(h.ctx, s.ID, 1, ep)
		want := h.c.imp.EpisodeTargetIn(folder, s.Title, s.Year, 1, ep, src, ".mkv")
		if cur.Path == "" {
			t.Errorf("E%02d has no file (from %s)", ep, src)
			continue
		}
		if filepath.Clean(cur.Path) != filepath.Clean(want) {
			t.Errorf("E%02d at %s, want %s", ep, cur.Path, want)
		}
		if _, err := os.Stat(cur.Path); err != nil {
			t.Errorf("E%02d file missing on disk: %v", ep, err)
		}
	}
	e3, e4 := h.c.series.CurrentEpisodeFile(h.ctx, s.ID, 1, 3), h.c.series.CurrentEpisodeFile(h.ctx, s.ID, 1, 4)
	if e3.Path == "" || e3.Path != e4.Path {
		t.Errorf("double episode: E03=%q E04=%q, want one file for both", e3.Path, e4.Path)
	}
	if r, _ := h.c.GetReview(h.ctx, rid); r.status != "resolved" {
		t.Errorf("review still %s", r.status)
	}
	var resolution, grab string
	_ = h.c.db.QueryRow(`SELECT resolution FROM import_reviews WHERE id = ?`, rid).Scan(&resolution)
	_ = h.c.db.QueryRow(`SELECT status FROM grabs WHERE media_type = 'series' ORDER BY id DESC LIMIT 1`).Scan(&grab)
	if resolution != ResolutionMapped || grab != grabStatusImported {
		t.Errorf("resolution %q, grab %q; want mapped and imported", resolution, grab)
	}
	if !hasEvent(h.seriesEvents(t), "imported", "mapped by hand") {
		t.Error("no 'imported … mapped by hand' history line")
	}
}

// A path outside the review's download is refused, and nothing at all is imported — not
// even the good mappings sent with it.
func TestImportReviewMappedRefusesEscapes(t *testing.T) {
	h, rid, dir, root := mapHarness(t)
	outside := filepath.Join(filepath.Dir(dir), "escape.mkv")
	if err := os.WriteFile(outside, make([]byte, 1024), 0o644); err != nil {
		t.Fatal(err)
	}
	cases := [][]FileMapping{
		{{RelPath: "Pilot.mkv", Season: 1, Episodes: []int{1}}, {RelPath: "../escape.mkv", Season: 1, Episodes: []int{2}}},
		{{RelPath: outside, Season: 1, Episodes: []int{2}}},
		{{RelPath: "Pilot.mkv", Season: 1, Episodes: []int{9}}},                                                         // no such episode
		{{RelPath: "Pilot.mkv", Season: 1, Episodes: []int{1}}, {RelPath: "Second.mkv", Season: 1, Episodes: []int{1}}}, // claimed twice
	}
	if os.Symlink(outside, filepath.Join(dir, "linked.mkv")) == nil {
		cases = append(cases, []FileMapping{{RelPath: "linked.mkv", Season: 1, Episodes: []int{2}}})
	}
	for i, m := range cases {
		if _, err := h.c.ImportReviewMapped(h.ctx, rid, 0, m); !errors.Is(err, ErrBadMapping) {
			t.Errorf("case %d: err = %v, want ErrBadMapping", i, err)
		}
	}
	for ep := 1; ep <= 4; ep++ {
		if cur := h.c.series.CurrentEpisodeFile(h.ctx, h.seriesID, 1, ep); cur.Path != "" {
			t.Errorf("E%02d got a file from a refused mapping: %s", ep, cur.Path)
		}
	}
	entries, _ := os.ReadDir(root)
	for _, e := range entries {
		if e.IsDir() {
			// The series folder may exist from Add; it must hold no episode files.
			_ = filepath.WalkDir(filepath.Join(root, e.Name()), func(p string, d os.DirEntry, err error) error {
				if err == nil && !d.IsDir() && isVideoName(p) {
					t.Errorf("a file was placed: %s", p)
				}
				return nil
			})
		}
	}
	if r, _ := h.c.GetReview(h.ctx, rid); r.status != "pending" {
		t.Error("a refused mapping settled the review")
	}
}
