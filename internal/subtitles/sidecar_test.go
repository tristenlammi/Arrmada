package subtitles

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
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
		// Plex pairs by base name only: a differently-named subtitle is an orphan, not
		// coverage (TestOrphanSidecars covers how it is reported).
		{"movie: differently-named .srt is not coverage (renamed video)", true,
			[]string{"Anchorman (2004) Bluray-2160p.mkv", "Anchorman The Legend of Ron Burgundy.srt"}, want, nil},
		{"movie: differently-named .eng.srt is not coverage", true,
			[]string{"Anchorman (2004) Bluray-2160p.mkv", "Anchorman.eng.srt"}, want, nil},
		{"both en + es sidecars", true,
			[]string{"Movie.mkv", "Movie.en.srt", "Movie.es.srt"}, want, []string{"en", "es"}},
		// A forced sidecar is the foreign-dialogue lines only — Plex would show a handful
		// of cues — so it is not the language's subtitle.
		{".en.forced.srt alone is not coverage", true,
			[]string{"Movie.mkv", "Movie.en.forced.srt"}, want, nil},
		{".en.forced.srt + .en.srt → en", true,
			[]string{"Movie.mkv", "Movie.en.forced.srt", "Movie.en.srt"}, want, []string{"en"}},
		{"untagged .forced.srt is not coverage", true,
			[]string{"Movie.mkv", "Movie.forced.srt"}, want, nil},
		{".en.sdh.srt → en", true,
			[]string{"Movie.mkv", "Movie.en.sdh.srt"}, want, []string{"en"}},
		// Bazarr writes hearing-impaired English as ".en.hi.srt"; that is English, not Hindi.
		{".en.hi.srt → en, not Hindi", true,
			[]string{"Movie.mkv", "Movie.en.hi.srt"}, []string{"en", "hi"}, []string{"en"}},
		{"bare .hi.srt → Hindi", true,
			[]string{"Movie.mkv", "Movie.hi.srt"}, []string{"en", "hi"}, []string{"hi"}},
		{"episode: .en.forced.srt is not coverage", false,
			[]string{"Show S01E01.mkv", "Show S01E01.en.forced.srt"}, want, nil},
		{"no sidecar", true, []string{"Movie.mkv"}, want, nil},
		// TV: a season folder — a differently-named .srt must NOT cross-count.
		{"episode: matching base counts", false,
			[]string{"Show S01E01.mkv", "Show S01E01.en.srt"}, want, []string{"en"}},
		{"episode: other episode's srt does NOT count", false,
			[]string{"Show S01E01.mkv", "Show S01E02.en.srt"}, want, nil},
	}
	for _, c := range cases {
		_, video := mkFiles(t, c.files...)
		kind := "episode"
		if c.single {
			kind = "movie"
		}
		got := scanSidecars(video, c.wanted, kind).Present
		if !reflect.DeepEqual(nonNil(got), nonNil(c.present)) {
			t.Errorf("%s: present = %v, want %v", c.name, got, c.present)
		}
	}
}

func TestParseSidecarTag(t *testing.T) {
	cases := []struct {
		segs          string
		lang, variant string
	}{
		{"en", "en", VariantFull},
		{"eng", "eng", VariantFull},
		{"english", "en", VariantFull},
		{"en.forced", "en", VariantForced},
		{"en.sdh", "en", VariantSDH},
		{"en.cc", "en", VariantSDH},
		{"en.hi", "en", VariantSDH}, // Bazarr's hearing-impaired
		{"hi", "hi", VariantFull},   // on its own, Hindi
		{"hindi.hi", "hi", VariantSDH},
		{"en.default", "en", VariantFull},
		{"en.default.forced", "en", VariantForced},
		{"en.sdh.forced", "en", VariantForced}, // forced wins: never the full track
		{"en.forced.sdh", "en", VariantForced},
		{"forced", "", VariantForced},
		{"anchorman the legend of ron burgundy", "", VariantFull},
		{"", "", VariantFull},
	}
	for _, c := range cases {
		lang, variant := parseSidecarTag(strings.Split(c.segs, "."), isKnownLang)
		if lang != c.lang || variant != c.variant {
			t.Errorf("%q: got (%q, %q), want (%q, %q)", c.segs, lang, variant, c.lang, c.variant)
		}
	}
}

func TestPresentVariants(t *testing.T) {
	_, video := mkFiles(t, "Movie.mkv", "Movie.en.srt", "Movie.en.forced.srt", "Movie.es.sdh.srt", "Other.fr.srt")
	got := map[LangVariant]bool{}
	for _, lv := range presentVariants(video) {
		got[lv] = true
	}
	for _, want := range []LangVariant{{"en", ""}, {"en", "forced"}, {"es", "sdh"}} {
		if !got[want] {
			t.Errorf("missing %+v in %v", want, got)
		}
	}
	if len(got) != 3 {
		t.Errorf("variants = %v, want exactly 3 (the other file's sidecar isn't this one's)", got)
	}
}

func TestSidecarPathV(t *testing.T) {
	v := filepath.Join("lib", "Movie (2020)", "Movie (2020).mkv")
	for variant, want := range map[string]string{
		VariantFull: "Movie (2020).en.srt", VariantForced: "Movie (2020).en.forced.srt", VariantSDH: "Movie (2020).en.sdh.srt",
	} {
		if got := filepath.Base(sidecarPathV(v, "en", variant)); got != want {
			t.Errorf("variant %q: %s, want %s", variant, got, want)
		}
	}
}
