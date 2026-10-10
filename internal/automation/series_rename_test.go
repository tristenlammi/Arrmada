package automation

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tristenlammi/arrmada/internal/eventbus"
	"github.com/tristenlammi/arrmada/internal/library"
	"github.com/tristenlammi/arrmada/internal/series"
	"github.com/tristenlammi/arrmada/internal/store"
)

// nothingOnDisk is an occupied() for pure planner tests: no file exists anywhere except
// the ones the plan itself is moving.
func nothingOnDisk(string, string) bool { return false }

func stepsByFrom(steps []renameStep) map[string]renameStep {
	m := make(map[string]renameStep, len(steps))
	for _, st := range steps {
		m[st.From] = st
	}
	return m
}

func TestPlanSeriesRenameChainAndSwap(t *testing.T) {
	// Chain: file B takes the free name A, C takes B's name, D takes C's.
	chain := []renameRow{
		{Season: 3, Episode: 1, Path: "/tv/B", Target: "/tv/A"},
		{Season: 3, Episode: 2, Path: "/tv/C", Target: "/tv/B"},
		{Season: 3, Episode: 3, Path: "/tv/D", Target: "/tv/C"},
	}
	// Nothing at /tv/A yet, so the head of the chain moves directly.
	steps, conflicts := planSeriesRename(chain, nothingOnDisk)
	if len(conflicts) != 0 || len(steps) != 3 {
		t.Fatalf("chain: steps=%v conflicts=%v", steps, conflicts)
	}
	by := stepsByFrom(steps)
	if !by["/tv/B"].viaTemp || !by["/tv/C"].viaTemp {
		t.Error("B and C are taking other files' names, so they must step aside first")
	}
	if by["/tv/D"].viaTemp {
		t.Error("no file wants D's name, so D needn't go through a temporary one")
	}

	// Swap: each wants the other's name.
	swap := []renameRow{
		{Season: 1, Episode: 1, Path: "/tv/X", Target: "/tv/Y"},
		{Season: 1, Episode: 2, Path: "/tv/Y", Target: "/tv/X"},
	}
	// The disk holds both files, but they're both moving, so neither target is "taken".
	both := func(_, to string) bool { return to == "/tv/X" || to == "/tv/Y" }
	steps, conflicts = planSeriesRename(swap, both)
	if len(conflicts) != 0 || len(steps) != 2 {
		t.Fatalf("swap: steps=%v conflicts=%v", steps, conflicts)
	}
	for _, st := range steps {
		if !st.viaTemp {
			t.Errorf("swap step %s → %s must go through a temporary name", st.From, st.To)
		}
	}

	// Files already at their name aren't steps at all.
	steps, _ = planSeriesRename([]renameRow{{Season: 1, Episode: 1, Path: "/tv/ok", Target: "/tv/ok"}, {Season: 1, Episode: 2, Path: "/tv/z", Target: ""}}, nothingOnDisk)
	if len(steps) != 0 {
		t.Errorf("no-op rows produced steps: %v", steps)
	}
}

func TestPlanSeriesRenameForeignTargetConflict(t *testing.T) {
	rows := []renameRow{
		{Season: 2, Episode: 1, Path: "/tv/old1", Target: "/tv/new1"},     // an untracked file sits at new1
		{Season: 2, Episode: 2, Path: "/tv/old2", Target: "/tv/old1"},     // counts on old1 coming free
		{Season: 2, Episode: 3, Path: "/tv/old3", Target: "/tv/new3"},     // independent, free
		{Season: 2, Episode: 4, Path: "/tv/stays", Target: "/tv/stays"},   // already named right
		{Season: 2, Episode: 5, Path: "/tv/old5", Target: "/tv/stays"},    // aims at a file that stays
		{Season: 2, Episode: 6, Path: "/tv/old6", Target: "/tv/new3"},     // same target as E03
		{Season: 2, Episode: 7, Path: "/tv/old7", Target: "/tv/hardlink"}, // the target is the same inode
	}
	occupied := func(from, to string) bool {
		switch to {
		case "/tv/new1", "/tv/stays":
			return true
		case "/tv/hardlink":
			return false // same file under another name — Move handles it, nothing is lost
		}
		return false
	}
	steps, conflicts := planSeriesRename(rows, occupied)
	got := map[string]string{}
	for _, c := range conflicts {
		got[c.From] = c.Reason
	}
	want := map[string]string{
		"/tv/old1": reasonTargetExists,
		"/tv/old2": reasonTargetExists, // old1 isn't moving after all, so old2 is blocked too
		"/tv/old5": reasonTargetExists,
		"/tv/old6": reasonSameTarget,
	}
	for from, reason := range want {
		if got[from] != reason {
			t.Errorf("conflict for %s = %q, want %q", from, got[from], reason)
		}
	}
	if len(conflicts) != len(want) {
		t.Errorf("conflicts = %v, want exactly %v", conflicts, want)
	}
	by := stepsByFrom(steps)
	if len(steps) != 2 || by["/tv/old3"].To != "/tv/new3" || by["/tv/old7"].To != "/tv/hardlink" {
		t.Errorf("steps = %v, want only old3 and old7", steps)
	}
	if c := conflicts[0]; c.Season != 2 || c.Episode != 1 || c.To != "/tv/new1" {
		t.Errorf("a conflict should name its episode and target: %+v", c)
	}
}

func TestPlanSeriesRenameMultiEpisodeFileOnce(t *testing.T) {
	rows := []renameRow{
		{Season: 1, Episode: 1, Path: "/tv/double.mkv", Target: "/tv/S01E01-E02.mkv"},
		{Season: 1, Episode: 2, Path: "/tv/double.mkv", Target: "/tv/S01E01-E02 - Second Title.mkv"},
		{Season: 1, Episode: 3, Path: "/tv/three.mkv", Target: "/tv/S01E03.mkv"},
	}
	steps, conflicts := planSeriesRename(rows, nothingOnDisk)
	if len(conflicts) != 0 || len(steps) != 2 {
		t.Fatalf("steps=%v conflicts=%v", steps, conflicts)
	}
	double := stepsByFrom(steps)["/tv/double.mkv"]
	if double.To != "/tv/S01E01-E02.mkv" {
		t.Errorf("a multi-episode file takes its first episode's name, got %q", double.To)
	}
	if len(double.Rows) != 2 || double.Rows[0].Episode != 1 || double.Rows[1].Episode != 2 {
		t.Errorf("both episode rows must ride on the one move: %+v", double.Rows)
	}
}

// renameFixture is a temp-dir library with one show whose episode rows point at files.
type renameFixture struct {
	c      *Coordinator
	ctx    context.Context
	id     int64
	folder string
	root   string
}

func newRenameFixture(t *testing.T, episodes ...[2]int) *renameFixture {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	root := t.TempDir()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx := context.Background()
	repo := series.NewRepo(st.DB())
	sr, err := repo.Create(ctx, series.Series{TMDBID: 77, Title: "Show", Year: 2020, Monitored: true})
	if err != nil {
		t.Fatal(err)
	}
	var seasons []series.Season
	bySeason := map[int][]series.Episode{}
	for _, key := range episodes {
		bySeason[key[0]] = append(bySeason[key[0]], series.Episode{SeasonNumber: key[0], EpisodeNumber: key[1], Title: "", AirDate: "2021-01-01"})
	}
	for sn, eps := range bySeason {
		seasons = append(seasons, series.Season{SeasonNumber: sn, Monitored: true, Episodes: eps})
	}
	if err := repo.InsertSeasons(ctx, sr.ID, seasons); err != nil {
		t.Fatal(err)
	}
	c := &Coordinator{
		db: st.DB(), log: log, bus: eventbus.New(log),
		series: series.NewService(st.DB(), nil, root, log),
		imp:    library.NewImporter(root, log),
	}
	return &renameFixture{c: c, ctx: ctx, id: sr.ID, folder: "Show (2020)", root: root}
}

// canonical is where the naming scheme puts an episode's file.
func (f *renameFixture) canonical(season, episode int) string {
	return f.c.imp.EpisodeTargetIn(f.folder, "Show", 2020, season, episode, "Show.S01E01.1080p.WEB-DL.mkv", ".mkv")
}

// place writes a file with the given content and points an episode row at it.
func (f *renameFixture) place(t *testing.T, season, episode int, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := f.c.series.MarkEpisodeImported(f.ctx, f.id, season, episode, path, int64(len(content))); err != nil {
		t.Fatal(err)
	}
}

func (f *renameFixture) pathOf(t *testing.T, season, episode int) string {
	t.Helper()
	p, err := f.c.series.EpisodeFilePath(f.ctx, f.id, season, episode)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func mustRead(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

// After a renumber moves every file one slot (S2E22's file now belongs to S3E01, S3E01's
// to S3E02, S3E02's to S3E03), each rename's target is the next file's current name. Run
// row by row as bare renames, that destroyed the season one file at a time.
func TestSeriesRenameSwapChain(t *testing.T) {
	f := newRenameFixture(t, [2]int{2, 22}, [2]int{3, 1}, [2]int{3, 2}, [2]int{3, 3})
	oldS2E22, oldS3E01, oldS3E02 := f.canonical(2, 22), f.canonical(3, 1), f.canonical(3, 2)
	f.place(t, 3, 1, oldS2E22, "was S02E22")
	f.place(t, 3, 2, oldS3E01, "was S03E01")
	f.place(t, 3, 3, oldS3E02, "was S03E02")

	items, err := f.c.SeriesRenamePreview(f.ctx, f.id)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 3 {
		t.Fatalf("preview = %+v, want three moves", items)
	}
	for _, it := range items {
		if it.Conflict != "" {
			t.Errorf("preview flagged %s as a conflict (%s) — the chain is safe to run", filepath.Base(it.From), it.Conflict)
		}
	}

	res, err := f.c.SeriesRename(f.ctx, f.id, items)
	if err != nil {
		t.Fatal(err)
	}
	if res.Moved != 3 || len(res.Skipped) != 0 {
		t.Fatalf("result = %+v, want 3 moved and none skipped", res)
	}
	for _, want := range []struct {
		season, episode int
		content         string
	}{{3, 1, "was S02E22"}, {3, 2, "was S03E01"}, {3, 3, "was S03E02"}} {
		p := f.pathOf(t, want.season, want.episode)
		if p != f.canonical(want.season, want.episode) {
			t.Errorf("S%02dE%02d row points at %s, want its canonical path", want.season, want.episode, p)
		}
		if got := mustRead(t, p); got != want.content {
			t.Errorf("S%02dE%02d holds %q, want %q — a file was overwritten", want.season, want.episode, got, want.content)
		}
	}
	// No temporary names are left behind.
	_ = filepath.WalkDir(f.root, func(p string, d os.DirEntry, err error) error {
		if err == nil && strings.Contains(d.Name(), "arrmada-rename") {
			t.Errorf("temporary file left behind: %s", p)
		}
		return nil
	})

	// A swap works the same way.
	g := newRenameFixture(t, [2]int{1, 1}, [2]int{1, 2})
	a, b := g.canonical(1, 1), g.canonical(1, 2)
	g.place(t, 1, 1, b, "belongs to E01")
	g.place(t, 1, 2, a, "belongs to E02")
	// Each video's subtitle has to follow it through the swap, not collide with the other's.
	sub := func(video string) string { return strings.TrimSuffix(video, ".mkv") + ".en.srt" }
	if err := os.WriteFile(sub(b), []byte("E01 subs"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sub(a), []byte("E02 subs"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err = g.c.SeriesRename(g.ctx, g.id, nil)
	if err != nil || res.Moved != 2 {
		t.Fatalf("swap: %+v, %v", res, err)
	}
	if mustRead(t, a) != "belongs to E01" || mustRead(t, b) != "belongs to E02" {
		t.Error("swap: files didn't end up under each other's names")
	}
	if mustRead(t, sub(a)) != "E01 subs" || mustRead(t, sub(b)) != "E02 subs" {
		t.Error("swap: subtitles didn't follow their videos")
	}
	if g.pathOf(t, 1, 1) != a || g.pathOf(t, 1, 2) != b {
		t.Error("swap: rows don't point at the new paths")
	}
}

// An untracked file at a target is never replaced; the move is skipped and reported, in
// the result and in History.
func TestSeriesRenameSkipsForeignTarget(t *testing.T) {
	f := newRenameFixture(t, [2]int{1, 1})
	target := f.canonical(1, 1)
	src := filepath.Join(f.root, f.folder, "Season 1", "Show.S01E01.1080p.WEB-DL.mkv")
	f.place(t, 1, 1, src, "tracked")
	if err := os.WriteFile(target, []byte("someone else's file"), 0o644); err != nil {
		t.Fatal(err)
	}
	items, _ := f.c.SeriesRenamePreview(f.ctx, f.id)
	if len(items) != 1 || items[0].Conflict != reasonTargetExists {
		t.Fatalf("preview = %+v, want one conflicting item", items)
	}
	res, err := f.c.SeriesRename(f.ctx, f.id, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Moved != 0 || len(res.Skipped) != 1 || res.Skipped[0].Reason != reasonTargetExists {
		t.Fatalf("result = %+v, want the move skipped", res)
	}
	if mustRead(t, src) != "tracked" || mustRead(t, target) != "someone else's file" {
		t.Error("both files must survive")
	}
	if f.pathOf(t, 1, 1) != src {
		t.Error("the row must keep pointing at the file that didn't move")
	}
	evs, _ := f.c.series.Events(f.ctx, f.id, 10)
	found := false
	for _, e := range evs {
		if strings.HasPrefix(e.Detail, "Rename skipped 1 file") {
			found = true
		}
	}
	if !found {
		t.Errorf("no 'Rename skipped' event in History: %+v", evs)
	}
}

// Confirm applies what was previewed, nothing else: a move whose target changed after the
// preview, or that wasn't in it, is skipped.
func TestSeriesRenameSkipsChangedSincePreview(t *testing.T) {
	f := newRenameFixture(t, [2]int{1, 1}, [2]int{1, 2}, [2]int{1, 3})
	dir := filepath.Join(f.root, f.folder, "Season 1")
	p1 := filepath.Join(dir, "Show.S01E01.1080p.WEB-DL.mkv")
	p2 := filepath.Join(dir, "Show.S01E02.1080p.WEB-DL.mkv")
	f.place(t, 1, 1, p1, "one")
	f.place(t, 1, 2, p2, "two")

	preview, err := f.c.SeriesRenamePreview(f.ctx, f.id)
	if err != nil || len(preview) != 2 {
		t.Fatalf("preview = %+v, %v", preview, err)
	}
	// After the preview: E02's previewed target is no longer what the plan says, and E03
	// gains a file that was never previewed.
	for i := range preview {
		if preview[i].Episode == 2 {
			preview[i].To = filepath.Join(dir, "something else.mkv")
		}
	}
	p3 := filepath.Join(dir, "Show.S01E03.1080p.WEB-DL.mkv")
	f.place(t, 1, 3, p3, "three")

	res, err := f.c.SeriesRename(f.ctx, f.id, preview)
	if err != nil {
		t.Fatal(err)
	}
	if res.Moved != 1 {
		t.Errorf("moved %d, want only the unchanged E01", res.Moved)
	}
	if f.pathOf(t, 1, 1) != f.canonical(1, 1) {
		t.Error("E01 was previewed unchanged and should have moved")
	}
	if f.pathOf(t, 1, 2) != p2 || mustRead(t, p2) != "two" {
		t.Error("E02 changed since the preview and must not move")
	}
	if f.pathOf(t, 1, 3) != p3 || mustRead(t, p3) != "three" {
		t.Error("E03 was never previewed and must not move")
	}
	if len(res.Skipped) != 1 || res.Skipped[0].Reason != reasonChangedPreview || res.Skipped[0].Episode != 2 {
		t.Errorf("skipped = %+v, want E02 reported as changed since preview", res.Skipped)
	}

	// An empty confirmation applies nothing.
	res, err = f.c.SeriesRename(f.ctx, f.id, []SeriesRenameItem{})
	if err != nil || res.Moved != 0 {
		t.Errorf("an empty confirmation moved %d (%v)", res.Moved, err)
	}
}

// A file a failed rename couldn't put back is recorded under its temporary name. The next
// rename has to give it a real video name back, not "Show - S01E01.arrmada-rename-0".
func TestSeriesRenameRecoversFileLeftUnderTempName(t *testing.T) {
	f := newRenameFixture(t, [2]int{1, 1})
	orig := filepath.Join(f.root, f.folder, "Season 1", "Show.S01E01.1080p.WEB-DL.mkv")
	tmp := renameTempPath(orig, 0)
	f.place(t, 1, 1, tmp, "the episode")
	// Its subtitle was moved aside with it, the way phase 1 names it.
	if err := os.WriteFile(filepath.Join(filepath.Dir(orig), ".Show.S01E01.1080p.WEB-DL.mkv.en.srt"), []byte("subs"), 0o644); err != nil {
		t.Fatal(err)
	}

	items, err := f.c.SeriesRenamePreview(f.ctx, f.id)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].To != f.canonical(1, 1) || items[0].Conflict != "" {
		t.Fatalf("preview = %+v, want the temp file renamed to %s", items, f.canonical(1, 1))
	}
	res, err := f.c.SeriesRename(f.ctx, f.id, items)
	if err != nil || res.Moved != 1 {
		t.Fatalf("result = %+v, %v", res, err)
	}
	if p := f.pathOf(t, 1, 1); p != f.canonical(1, 1) || mustRead(t, p) != "the episode" {
		t.Errorf("S01E01 row points at %s, want the canonical .mkv name", p)
	}
	if mustRead(t, strings.TrimSuffix(f.canonical(1, 1), ".mkv")+".en.srt") != "subs" {
		t.Error("the subtitle should follow the video out of its temporary name")
	}
}

// The process stops between moving a chained file aside and moving it on: the row still
// names the original path, which is now empty, and the file sits hidden under its
// temporary name. A rescan used to mark the episode missing (so it was downloaded again)
// and nothing ever brought the file back.
func TestInterruptedRenameIsPutBack(t *testing.T) {
	f := newRenameFixture(t, [2]int{1, 1})
	orig := f.canonical(1, 1)
	f.place(t, 1, 1, orig, "the episode")
	if err := os.Rename(orig, renameTempPath(orig, 0)); err != nil {
		t.Fatal(err)
	}

	f.c.RescanSeries(f.ctx, f.id)
	if mustRead(t, orig) != "the episode" {
		t.Fatal("the file should be back under its original name")
	}
	s, _ := f.c.series.Get(f.ctx, f.id)
	if !s.Seasons[0].Episodes[0].HasFile {
		t.Error("the episode was marked missing while its file was only set aside")
	}

	// The same through a rename, where the original name is free again.
	g := newRenameFixture(t, [2]int{1, 1})
	src := filepath.Join(g.root, g.folder, "Season 1", "Show.S01E01.1080p.WEB-DL.mkv")
	g.place(t, 1, 1, src, "the episode")
	if err := os.Rename(src, renameTempPath(src, 0)); err != nil {
		t.Fatal(err)
	}
	if res, err := g.c.SeriesRename(g.ctx, g.id, nil); err != nil || res.Moved != 1 {
		t.Fatalf("result = %+v, %v", res, err)
	}
	if mustRead(t, g.canonical(1, 1)) != "the episode" || g.pathOf(t, 1, 1) != g.canonical(1, 1) {
		t.Error("the set-aside file should be restored and then renamed")
	}
}

// A leftover temporary file whose original name is taken stays where it is, and doesn't
// stop the file now at that name being moved through a temporary name of its own.
func TestSeriesRenameSkipsPastLeftoverTemp(t *testing.T) {
	g := newRenameFixture(t, [2]int{1, 1}, [2]int{1, 2})
	a, b := g.canonical(1, 1), g.canonical(1, 2)
	g.place(t, 1, 1, b, "belongs to E01")
	g.place(t, 1, 2, a, "belongs to E02")
	leftover := renameTempPath(a, 0)
	if err := os.WriteFile(leftover, []byte("leftover"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := g.c.SeriesRename(g.ctx, g.id, nil)
	if err != nil || res.Moved != 2 || len(res.Skipped) != 0 {
		t.Fatalf("swap past a leftover: %+v, %v", res, err)
	}
	if mustRead(t, a) != "belongs to E01" || mustRead(t, b) != "belongs to E02" {
		t.Error("swap: files didn't end up under each other's names")
	}
	if mustRead(t, leftover) != "leftover" {
		t.Error("the leftover temporary file must be left alone")
	}
}

// A rename that moved files tells Plex's scanner about the show folder, once; a rename
// with nothing to move tells it nothing.
func TestSeriesRenameReportsShowFolder(t *testing.T) {
	f := newRenameFixture(t, [2]int{1, 1}, [2]int{1, 2})
	var got []string
	f.c.SetLibraryChanged(func(kind, dir string) { got = append(got, kind+"|"+dir) })
	dir := filepath.Dir(f.canonical(1, 1))
	f.place(t, 1, 1, filepath.Join(dir, "odd name one.mkv"), "one")
	f.place(t, 1, 2, filepath.Join(dir, "odd name two.mkv"), "two")

	res, err := f.c.SeriesRename(f.ctx, f.id, nil)
	if err != nil || res.Moved != 2 {
		t.Fatalf("rename: %+v, %v", res, err)
	}
	want := "show|" + series.ShowFolder(f.canonical(1, 1))
	if len(got) != 1 || got[0] != want {
		t.Fatalf("reported %v, want [%s]", got, want)
	}
	got = nil
	if _, err := f.c.SeriesRename(f.ctx, f.id, nil); err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("a rename with nothing to move reported %v", got)
	}
}
