package series

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/tristenlammi/arrmada/internal/store"
)

// An episode's pre-conversion baseline is recorded on every episode the file serves,
// survives Convert's repoint and a numbering rebuild, keeps the first original on a
// re-conversion, and is cleared when a new file is imported. Temp-dir files only.
func TestEpisodeConvertedFromBaseline(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	db := st.DB()
	ctx := context.Background()
	for _, q := range []string{
		`INSERT INTO series (id,tmdb_id,title,monitored) VALUES (1,7,'Show',1)`,
		`INSERT INTO seasons (series_id,season_number) VALUES (1,1)`,
		`INSERT INTO episodes (series_id,season_number,episode_number,absolute_number,monitored) VALUES (1,1,1,1,1)`,
		`INSERT INTO episodes (series_id,season_number,episode_number,absolute_number,monitored) VALUES (1,1,2,2,1)`,
	} {
		if _, err := db.ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	dir := t.TempDir()
	src := filepath.Join(dir, "Show - S01E01-E02.mkv") // one file, two episodes
	if err := os.WriteFile(src, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	svc := &Service{repo: NewRepo(db), log: slog.Default()}
	rel := "Show.S01E01E02.1080p.BluRay.x264-GRP"
	for _, ep := range []int{1, 2} {
		if err := svc.SupersedeEpisodeFile(ctx, 1, 1, ep, src, 3<<30, rel); err != nil {
			t.Fatal(err)
		}
	}
	read := func(ep int) EpisodeFile { return svc.CurrentEpisodeFile(ctx, 1, 1, ep) }

	if err := svc.SetConvertedFrom(ctx, 1, src, 3<<30); err != nil {
		t.Fatal(err)
	}
	conv := filepath.Join(dir, "Show - S01E01-E02 av1.mkv")
	if n, err := svc.RepointEpisodeFile(ctx, 1, src, conv, 1<<30); err != nil || n != 2 {
		t.Fatalf("repoint: n=%d err=%v", n, err)
	}
	_ = svc.SetEpisodeSourceRelease(ctx, 1, 1, 1, "Show.S01E01E02.1080p.BluRay.AV1-GRP")
	for _, ep := range []int{1, 2} {
		if f := read(ep); f.ConvertedFromRelease != rel || f.ConvertedFromSize != 3<<30 || f.Path != conv {
			t.Errorf("E%02d after convert: %+v", ep, f)
		}
	}
	// A re-conversion keeps the first original.
	if err := svc.SetConvertedFrom(ctx, 1, conv, 1<<30); err != nil {
		t.Fatal(err)
	}
	if f := read(1); f.ConvertedFromSize != 3<<30 {
		t.Errorf("a re-conversion replaced the first original: %+v", f)
	}

	// A numbering rebuild carries it with the file.
	seasons := []Season{{SeasonNumber: 1, Episodes: []Episode{
		{SeasonNumber: 1, EpisodeNumber: 1, AbsoluteNumber: 1}, {SeasonNumber: 1, EpisodeNumber: 2, AbsoluteNumber: 2},
	}}}
	if _, err := svc.repo.RebuildEpisodes(ctx, 1, seasons); err != nil {
		t.Fatal(err)
	}
	if f := read(2); f.ConvertedFromRelease != rel || f.ConvertedFromSize != 3<<30 {
		t.Errorf("a rebuild dropped the baseline: %+v", f)
	}

	// A new file for E01 clears E01 only.
	up := filepath.Join(dir, "Show - S01E01 2160p.mkv")
	if err := os.WriteFile(up, []byte("y"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := svc.SupersedeEpisodeFile(ctx, 1, 1, 1, up, 5<<30, "Show.S01E01.2160p.WEB-DL.x265-GRP"); err != nil {
		t.Fatal(err)
	}
	if f := read(1); f.ConvertedFromRelease != "" || f.ConvertedFromSize != 0 {
		t.Errorf("an import must clear the baseline: %+v", f)
	}
	if f := read(2); f.ConvertedFromRelease != rel {
		t.Errorf("E02 still holds the converted file: %+v", f)
	}
}
