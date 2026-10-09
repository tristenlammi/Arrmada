package series

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/tristenlammi/arrmada/internal/library"
	"github.com/tristenlammi/arrmada/internal/store"
)

func supersedeFixture(t *testing.T, episodes int) (*Service, string, string) {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()
	if _, err := st.DB().ExecContext(ctx, `INSERT INTO series (id,tmdb_id,title) VALUES (1,1,'S')`); err != nil {
		t.Fatal(err)
	}
	for e := 1; e <= episodes; e++ {
		if _, err := st.DB().ExecContext(ctx, `INSERT INTO episodes (series_id,season_number,episode_number) VALUES (1,1,?)`, e); err != nil {
			t.Fatal(err)
		}
	}
	dir := t.TempDir()
	bin := filepath.Join(t.TempDir(), "bin")
	svc := &Service{repo: NewRepo(st.DB()), bin: library.SingleBin(bin), log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	return svc, dir, bin
}

func touch(t *testing.T, p string) {
	t.Helper()
	if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// An upgrade recycles the old release's subtitles next to the old video, and leaves the
// new file's own subtitles and its neighbours alone.
func TestSupersedeEpisodeFileRecyclesSidecars(t *testing.T) {
	svc, dir, bin := supersedeFixture(t, 1)
	ctx := context.Background()
	oldF := filepath.Join(dir, "Show - S01E01 - WEBDL-1080p.mkv")
	newF := filepath.Join(dir, "Show - S01E01 - Bluray-2160p.mkv")
	for _, p := range []string{oldF, newF,
		filepath.Join(dir, "Show - S01E01 - WEBDL-1080p.en.srt"),
		filepath.Join(dir, "Show - S01E01 - WEBDL-1080p.en.forced.srt"),
		filepath.Join(dir, "Show - S01E01 - Bluray-2160p.es.srt"),
		filepath.Join(dir, "Show - S01E02.en.srt")} {
		touch(t, p)
	}
	if err := svc.MarkEpisodeImported(ctx, 1, 1, 1, oldF, 1); err != nil {
		t.Fatal(err)
	}
	if err := svc.SupersedeEpisodeFile(ctx, 1, 1, 1, newF, 1, ""); err != nil {
		t.Fatal(err)
	}
	for _, gone := range []string{"Show - S01E01 - WEBDL-1080p.mkv", "Show - S01E01 - WEBDL-1080p.en.srt", "Show - S01E01 - WEBDL-1080p.en.forced.srt"} {
		if exists(filepath.Join(dir, gone)) {
			t.Errorf("%s left behind", gone)
		}
		binned := filepath.Join(bin, filepath.Base(dir), gone)
		if !exists(binned) || !exists(binned+library.RecycleMetaExt) {
			t.Errorf("%s not in the bin with its .arrmeta", gone)
		}
	}
	for _, kept := range []string{"Show - S01E01 - Bluray-2160p.mkv", "Show - S01E01 - Bluray-2160p.es.srt", "Show - S01E02.en.srt"} {
		if !exists(filepath.Join(dir, kept)) {
			t.Errorf("%s was removed", kept)
		}
	}
}

// One half of a double-episode file that a sibling still uses: the file and its
// subtitles stay for the sibling.
func TestSupersedeKeepsSidecarsWhenShared(t *testing.T) {
	svc, dir, _ := supersedeFixture(t, 2)
	ctx := context.Background()
	shared := filepath.Join(dir, "Show - S01E01-E02.mkv")
	sub := filepath.Join(dir, "Show - S01E01-E02.en.srt")
	newF := filepath.Join(dir, "Show - S01E01 - Proper.mkv")
	touch(t, shared)
	touch(t, sub)
	touch(t, newF)
	for _, e := range []int{1, 2} {
		if err := svc.MarkEpisodeImported(ctx, 1, 1, e, shared, 1); err != nil {
			t.Fatal(err)
		}
	}
	if err := svc.SupersedeEpisodeFile(ctx, 1, 1, 1, newF, 1, ""); err != nil {
		t.Fatal(err)
	}
	if !exists(shared) || !exists(sub) {
		t.Error("the shared file or its subtitle was removed while a sibling still uses it")
	}
}

// A container swap (X.mp4 replaced by X.mkv) keeps X.en.srt: it pairs with the new file.
func TestSupersedeKeepsSidecarsOnSameBaseSwap(t *testing.T) {
	svc, dir, _ := supersedeFixture(t, 1)
	ctx := context.Background()
	oldF := filepath.Join(dir, "X.mp4")
	newF := filepath.Join(dir, "X.mkv")
	sub := filepath.Join(dir, "X.en.srt")
	touch(t, oldF)
	touch(t, newF)
	touch(t, sub)
	if err := svc.MarkEpisodeImported(ctx, 1, 1, 1, oldF, 1); err != nil {
		t.Fatal(err)
	}
	if err := svc.SupersedeEpisodeFile(ctx, 1, 1, 1, newF, 1, ""); err != nil {
		t.Fatal(err)
	}
	if exists(oldF) {
		t.Error("old container not removed")
	}
	if !exists(sub) {
		t.Error("X.en.srt removed on a same-name container swap")
	}
}
