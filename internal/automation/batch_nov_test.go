package automation

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tristenlammi/arrmada/internal/movies"
	"github.com/tristenlammi/arrmada/internal/series"
)

// A release numbered as a saga is the film it was grabbed for (#74), but only that film, and
// only when it was grabbed by its hash: nothing else may be picked on the number's strength.
func TestHoldMovieImportReadsASagaNumber(t *testing.T) {
	const numbered = "Star.Wars.The.Force.Awakens.Episode.VII.2015.1080p.BluRay.x264-GRP"
	setup := func(t *testing.T) (*stallHarness, movies.Movie) {
		t.Helper()
		h := newStallHarness(t)
		m, err := movies.NewRepo(h.c.db).Create(h.ctx, movies.Movie{TMDBID: 140607, Title: "Star Wars: The Force Awakens", Year: 2015, Monitored: true,
			Extra: &movies.MovieExtra{TitlesRead: true}})
		if err != nil {
			t.Fatal(err)
		}
		return h, m
	}

	t.Run("its own name with a number imports", func(t *testing.T) {
		h, m := setup(t)
		addGrab(t, h.c, "movie", m.ID, numbered, hashFor(numbered))
		if reason, hold := h.c.HoldMovieImport(h.ctx, hashFor(numbered), numbered, t.TempDir()); hold {
			t.Fatalf("held the film's own release: %s", reason)
		}
	})

	t.Run("a different film's number is still held", func(t *testing.T) {
		h, m := setup(t)
		const other = "Star.Wars.The.Last.Jedi.Episode.VIII.2015.1080p.BluRay.x264-GRP"
		addGrab(t, h.c, "movie", m.ID, other, hashFor(other))
		if _, hold := h.c.HoldMovieImport(h.ctx, hashFor(other), other, t.TempDir()); !hold {
			t.Fatal("another film's release was let through")
		}
	})

	t.Run("a name another library film has too is still held", func(t *testing.T) {
		h, m := setup(t)
		if _, err := movies.NewRepo(h.c.db).Create(h.ctx, movies.Movie{TMDBID: 99, Title: "Star Wars The Force Awakens", Year: 2015, Monitored: true,
			Extra: &movies.MovieExtra{TitlesRead: true}}); err != nil {
			t.Fatal(err)
		}
		addGrab(t, h.c, "movie", m.ID, numbered, hashFor(numbered))
		if _, hold := h.c.HoldMovieImport(h.ctx, hashFor(numbered), numbered, t.TempDir()); !hold {
			t.Fatal("a name two films share was let through")
		}
	})

	t.Run("the wrong year is still held", func(t *testing.T) {
		h, m := setup(t)
		const other = "Star.Wars.The.Force.Awakens.Episode.VII.2017.1080p.BluRay.x264-GRP"
		addGrab(t, h.c, "movie", m.ID, other, hashFor(other))
		if _, hold := h.c.HoldMovieImport(h.ctx, hashFor(other), other, t.TempDir()); !hold {
			t.Fatal("a release from another year was let through")
		}
	})
}

// addPausedShow is a show with searching off and a season it still wants: only the show's own
// switch says not to search.
func addPausedShow(t *testing.T, h *stallHarness) int64 {
	t.Helper()
	repo := series.NewRepo(h.c.db)
	sr, err := repo.Create(h.ctx, series.Series{TMDBID: 9, Title: "Show", Year: 2020, Monitored: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.InsertSeasons(h.ctx, sr.ID, []series.Season{{SeasonNumber: 3, Monitored: true, Episodes: []series.Episode{
		{SeasonNumber: 3, EpisodeNumber: 1, AirDate: "2021-01-01", Monitored: true},
	}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.c.db.Exec(`UPDATE series SET monitored = 0 WHERE id = ?`, sr.ID); err != nil {
		t.Fatal(err)
	}
	return sr.ID
}

func showEventHas(t *testing.T, h *stallHarness, id int64, want string) bool {
	t.Helper()
	evs, err := h.c.series.Events(h.ctx, id, 20)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range evs {
		if strings.Contains(e.Detail, want) {
			return true
		}
	}
	return false
}

// A show with searching off keeps its stalled download rather than grabbing another (#95), and
// its history says why instead of "no other release found".
func TestStallFailoverLeavesAPausedShowAlone(t *testing.T) {
	h := newStallHarness(t)
	id := addPausedShow(t, h)
	const stalled = "Show.S03.1080p.WEB-DL.x264-OLD"
	const alt = "Show.S03.1080p.WEB-DL.x264-NEW"
	gid := h.addStalledGrab(t, "series", id, stalled, seriesCategory)
	h.ix.offer(stalled, alt)

	h.observe()
	h.tick()

	if calls := h.qbit.callLog(); len(calls) != 0 {
		t.Fatalf("client calls = %q, want none", calls)
	}
	if s := h.status(t, gid); s == "failed" {
		t.Errorf("grab status = %q: a kept download's grab stays open", s)
	}
	if !showEventHas(t, h, id, "searching is off for this show") {
		t.Error("the history should say searching is off")
	}
	if showEventHas(t, h, id, "no other release found") {
		t.Error("the history shouldn't read as a search that came up empty")
	}
}

// Someone choosing "Try another release" in Needs you asked for it, paused show or not.
func TestFailOverNowStillReplacesForAPausedShow(t *testing.T) {
	h := newStallHarness(t)
	id := addPausedShow(t, h)
	const stalled = "Show.S03.1080p.WEB-DL.x264-OLD"
	const alt = "Show.S03.1080p.WEB-DL.x264-NEW"
	h.addStalledGrab(t, "series", id, stalled, seriesCategory)
	h.ix.offer(stalled, alt)

	out, err := h.c.FailOverNow(h.ctx, hashFor(stalled))
	if err != nil {
		t.Fatalf("FailOverNow: %v", err)
	}
	if out.Replacement != alt {
		t.Errorf("replacement = %q, want %q", out.Replacement, alt)
	}
}

// A show split across TV folders: the one with the gone files being down is what counts, not
// the one holding most of the show. And a file in no TV folder can't be told from a dead disk.
func TestForgetRefusalJudgesEachFilesOwnFolder(t *testing.T) {
	up, down := t.TempDir(), filepath.Join(t.TempDir(), "gone")
	if err := os.WriteFile(filepath.Join(up, "other.mkv"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	rootOf := func(p string) (string, bool) {
		switch {
		case strings.HasPrefix(p, up):
			return up, true
		case strings.HasPrefix(p, down):
			return down, true
		}
		return "", false
	}
	gone := func(paths ...string) []series.Episode {
		var out []series.Episode
		for _, p := range paths {
			out = append(out, series.Episode{FilePath: p})
		}
		return out
	}
	inUp, inDown := filepath.Join(up, "Show", "a.mkv"), filepath.Join(down, "Show", "b.mkv")
	if why := forgetRefusal(rootOf, gone(inUp)); why != "" {
		t.Errorf("a file in a mounted TV folder was refused: %s", why)
	}
	if why := forgetRefusal(rootOf, gone(inUp, inDown)); !strings.Contains(why, "isn't reachable") {
		t.Errorf("a file on the folder that's down: %q", why)
	}
	if why := forgetRefusal(rootOf, gone(filepath.Join(t.TempDir(), "elsewhere.mkv"))); !strings.Contains(why, "isn't in any of your TV folders") {
		t.Errorf("a file in no TV folder: %q", why)
	}
}

// Someone who deleted a show's files on purpose can say so (#97): what a rescan refuses as a
// possible dead disk clears, and nothing on disk is touched.
func TestForgetMissingEpisodes(t *testing.T) {
	eps := [][2]int{{1, 1}, {1, 2}}

	t.Run("a missing show folder clears", func(t *testing.T) {
		f := newRenameFixture(t, eps...)
		f.placeAll(t, eps...)
		other := filepath.Join(f.root, "Another Show (2019)", "keep.mkv")
		if err := os.MkdirAll(filepath.Dir(other), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(other, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.RemoveAll(filepath.Join(f.root, f.folder)); err != nil {
			t.Fatal(err)
		}
		wantRefused(t, f.c.RescanSeries(f.ctx, f.id), "Forget missing files")

		n, err := f.c.ForgetMissingEpisodes(f.ctx, f.id)
		if err != nil || n != 2 {
			t.Fatalf("ForgetMissingEpisodes = %d, %v; want 2 cleared", n, err)
		}
		for _, ep := range eps {
			if f.hasFile(t, ep[0], ep[1]) {
				t.Errorf("S%02dE%02d still reads as having its file", ep[0], ep[1])
			}
		}
		if _, err := os.Stat(other); err != nil {
			t.Errorf("another show's file: %v", err)
		}
	})

	t.Run("every file gone from a folder still there clears", func(t *testing.T) {
		f := newRenameFixture(t, eps...)
		f.placeAll(t, eps...)
		for _, ep := range eps {
			if err := os.Remove(f.canonical(ep[0], ep[1])); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.WriteFile(filepath.Join(f.root, f.folder, "poster.jpg"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		if n, err := f.c.ForgetMissingEpisodes(f.ctx, f.id); err != nil || n != 2 {
			t.Fatalf("ForgetMissingEpisodes = %d, %v; want 2 cleared", n, err)
		}
	})

	t.Run("files still there stay, and only the gone clear", func(t *testing.T) {
		f := newRenameFixture(t, eps...)
		f.placeAll(t, eps...)
		if err := os.Remove(f.canonical(1, 2)); err != nil {
			t.Fatal(err)
		}
		if n, err := f.c.ForgetMissingEpisodes(f.ctx, f.id); err != nil || n != 1 {
			t.Fatalf("ForgetMissingEpisodes = %d, %v; want 1 cleared", n, err)
		}
		if !f.hasFile(t, 1, 1) || f.hasFile(t, 1, 2) {
			t.Errorf("S01E01 has file = %v, S01E02 has file = %v", f.hasFile(t, 1, 1), f.hasFile(t, 1, 2))
		}
		if _, err := os.Stat(f.canonical(1, 1)); err != nil {
			t.Errorf("the file still there: %v", err)
		}
	})

	t.Run("an unmounted TV folder is never waved through", func(t *testing.T) {
		f := newRenameFixture(t, eps...)
		f.placeAll(t, eps...)
		if err := os.RemoveAll(f.root); err != nil {
			t.Fatal(err)
		}
		_, err := f.c.ForgetMissingEpisodes(f.ctx, f.id)
		wantRefused(t, err, "The TV folder isn't reachable")
		for _, ep := range eps {
			if !f.hasFile(t, ep[0], ep[1]) {
				t.Errorf("S%02dE%02d was cleared while the TV folder was unmounted", ep[0], ep[1])
			}
		}
	})
}
