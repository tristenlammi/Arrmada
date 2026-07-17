package subtitles

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// mkFiles creates empty files in dir and returns the path to the first (the "video").
func mkFiles(t *testing.T, names ...string) (dir, video string) {
	t.Helper()
	dir = t.TempDir()
	for i, n := range names {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			video = filepath.Join(dir, n)
		}
	}
	return dir, video
}

func TestPresentLanguages(t *testing.T) {
	want := []string{"en", "es"}

	cases := []struct {
		name    string
		single  bool
		files   []string // first is the video
		wanted  []string
		present []string
	}{
		{"exact .en.srt", true,
			[]string{"Movie (2004) Bluray-2160p.mkv", "Movie (2004) Bluray-2160p.en.srt"}, want, []string{"en"}},
		{"3-letter .eng.srt matches en", true,
			[]string{"Movie (2004) Bluray-2160p.mkv", "Movie (2004) Bluray-2160p.eng.srt"}, want, []string{"en"}},
		{"full name .english.srt matches en", true,
			[]string{"Movie (2004) Bluray-2160p.mkv", "Movie (2004) Bluray-2160p.english.srt"}, want, []string{"en"}},
		{"bare .srt → first wanted (en)", true,
			[]string{"Movie (2004) Bluray-2160p.mkv", "Movie (2004) Bluray-2160p.srt"}, want, []string{"en"}},
		{"movie: differently-named .srt still counts (renamed video)", true,
			[]string{"Anchorman (2004) Bluray-2160p.mkv", "Anchorman The Legend of Ron Burgundy.srt"}, want, []string{"en"}},
		{"movie: differently-named .eng.srt → en", true,
			[]string{"Anchorman (2004) Bluray-2160p.mkv", "Anchorman.eng.srt"}, want, []string{"en"}},
		{"both en + es sidecars", true,
			[]string{"Movie.mkv", "Movie.en.srt", "Movie.es.srt"}, want, []string{"en", "es"}},
		{".en.forced.srt still counts", true,
			[]string{"Movie.mkv", "Movie.en.forced.srt"}, want, []string{"en"}},
		{"no sidecar", true, []string{"Movie.mkv"}, want, nil},
		// TV: a season folder — a differently-named .srt must NOT cross-count.
		{"episode: matching base counts", false,
			[]string{"Show S01E01.mkv", "Show S01E01.en.srt"}, want, []string{"en"}},
		{"episode: other episode's srt does NOT count", false,
			[]string{"Show S01E01.mkv", "Show S01E02.en.srt"}, want, nil},
	}
	for _, c := range cases {
		_, video := mkFiles(t, c.files...)
		got := presentLanguages(video, c.wanted, c.single)
		if !reflect.DeepEqual(nonNil(got), nonNil(c.present)) {
			t.Errorf("%s: present = %v, want %v", c.name, got, c.present)
		}
	}
}
