package convert

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func langs(tracks []AudioStream) string {
	var out []string
	for _, a := range tracks {
		l := a.Lang
		if a.Commentary {
			l += "(c)"
		}
		out = append(out, l)
	}
	return strings.Join(out, ",")
}

func TestKeptAudio(t *testing.T) {
	comm := aud("ac3", "eng", 2)
	comm.Commentary = true
	mi := film("h264", 1920, 1080, 12000, aud("truehd", "eng", 8), aud("aac", "jpn", 2), aud("aac", "und", 2), comm)

	cases := []struct {
		name string
		plan AudioPlan
		want string
	}{
		{"no filter keeps everything", AudioPlan{}, "eng,jpn,und,eng(c)"},
		{"English only; untagged stays", AudioPlan{KeepLangs: []string{"en"}}, "eng,und,eng(c)"},
		{"commentary removed", AudioPlan{KeepLangs: []string{"en"}, DropCommentary: true}, "eng,und"},
		{"original language kept too", AudioPlan{KeepLangs: []string{"en"}, OriginalLang: "ja"}, "eng,jpn,und,eng(c)"},
		{"commentary removed without a language filter", AudioPlan{DropCommentary: true}, "eng,jpn,und"},
	}
	for _, c := range cases {
		if got := langs(keptAudio(mi, Plan{Audio: c.plan})); got != c.want {
			t.Errorf("%s: kept %s, want %s", c.name, got, c.want)
		}
	}

	// Never a silent file: a filter that matches nothing keeps every track, and commentary
	// is only dropped when something else remains.
	two := film("h264", 1920, 1080, 12000, aud("ac3", "eng", 6), aud("ac3", "jpn", 6))
	if got := langs(keptAudio(two, Plan{Audio: AudioPlan{KeepLangs: []string{"fr"}}})); got != "eng,jpn" {
		t.Errorf("filter matching nothing kept %s, want every track", got)
	}
	onlyComm := film("h264", 1920, 1080, 12000, comm)
	if got := keptAudio(onlyComm, Plan{Audio: AudioPlan{DropCommentary: true}}); len(got) != 1 {
		t.Error("dropping commentary must never leave a file with no audio")
	}
}

func TestDefaultAudioPicksYourFirstLanguage(t *testing.T) {
	comm := aud("ac3", "eng", 2)
	comm.Commentary = true
	kept := []AudioStream{aud("aac", "jpn", 2), comm, aud("truehd", "eng", 8)}
	if got := defaultAudio(kept, Plan{Audio: AudioPlan{KeepLangs: []string{"en", "ja"}}}); got != 2 {
		t.Errorf("default = %d, want 2 (the English main track, not the commentary)", got)
	}
	if got := defaultAudio(kept, Plan{}); got != -1 {
		t.Errorf("no language list must leave the default flags alone, got %d", got)
	}
}

func TestKeptSubs(t *testing.T) {
	subLangs := func(subs []SubStream) string {
		var out []string
		for _, s := range subs {
			k := "img"
			if s.Text {
				k = "txt"
			}
			out = append(out, s.Lang+":"+k)
		}
		return strings.Join(out, ",")
	}
	mi := withSubs(film("h264", 1920, 1080, 12000), pgs("eng"), srt("eng"), pgs("fre"), srt("ger"))

	cases := []struct {
		name string
		subs SubPlan
		want string
	}{
		{"keep everything", SubPlan{ImageSubs: ImageSubsKeep}, "eng:img,eng:txt,fre:img,ger:txt"},
		{"English only", SubPlan{KeepLangs: []string{"en"}, ImageSubs: ImageSubsKeep}, "eng:img,eng:txt"},
		{"image only when a text one exists", SubPlan{ImageSubs: ImageSubsWhenText}, "eng:txt,fre:img,ger:txt"},
		{"a sidecar counts as text", SubPlan{ImageSubs: ImageSubsWhenText, TextSidecarLangs: []string{"fr"}}, "eng:txt,ger:txt"},
		{"remove every image track", SubPlan{ImageSubs: ImageSubsRemove}, "eng:txt,ger:txt"},
		{"a filter matching nothing keeps everything", SubPlan{KeepLangs: []string{"jpn"}, ImageSubs: ImageSubsKeep}, "eng:img,eng:txt,fre:img,ger:txt"},
	}
	for _, c := range cases {
		if got := subLangs(keptSubs(mi, Plan{Subs: c.subs})); got != c.want {
			t.Errorf("%s: kept %s, want %s", c.name, got, c.want)
		}
	}

	// "Remove" means remove, even a language's only subtitle — that's what you asked for.
	onlyPGS := withSubs(film("h264", 1920, 1080, 12000), pgs("eng"))
	if got := keptSubs(onlyPGS, Plan{Subs: SubPlan{ImageSubs: ImageSubsRemove}}); len(got) != 0 {
		t.Errorf("remove mode kept %d image track(s)", len(got))
	}
	if got := keptSubs(onlyPGS, Plan{Subs: SubPlan{ImageSubs: ImageSubsWhenText}}); len(got) != 1 {
		t.Error("when-text mode must keep a language's only subtitle")
	}
	// An untagged image track goes only when SOME text subtitle exists.
	untagged := withSubs(film("h264", 1920, 1080, 12000), pgs(""))
	if got := keptSubs(untagged, Plan{Subs: SubPlan{ImageSubs: ImageSubsWhenText}}); len(got) != 1 {
		t.Error("an untagged image track with no text subtitle anywhere must stay")
	}
}

func TestTrackArgs(t *testing.T) {
	mi := withSubs(film("h264", 1920, 1080, 12000, aud("aac", "jpn", 2), aud("truehd", "eng", 8)),
		SubStream{Codec: "mov_text", Lang: "eng", Text: true}, srt("ger"))

	// Nothing to change: copy everything, MP4 text subs converted for Matroska.
	all := strings.Join(trackArgs(mi, Plan{}, 0), " ")
	for _, want := range []string{"-map 0:a? -c:a copy", "-map 0:s? -c:s copy", "-c:s:0 srt", "-map 0:t? -c:t copy"} {
		if !strings.Contains(all, want) {
			t.Errorf("untouched tracks: missing %q in %s", want, all)
		}
	}

	// Filtered from input 1 (the HDR10+ mux): per-stream maps, OUTPUT indexes for the
	// overrides, and the English track flagged default.
	plan := Plan{Audio: AudioPlan{KeepLangs: []string{"en", "ja"}}, Subs: SubPlan{KeepLangs: []string{"en"}}}
	got := trackArgs(mi, plan, 1)
	want := []string{
		"-map", "1:a:0", "-disposition:a:0", "-default",
		"-map", "1:a:1", "-disposition:a:1", "+default",
		"-c:a", "copy",
		"-map", "1:s:0", "-c:s:0", "srt",
		"-map", "1:t?", "-c:t", "copy",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("filtered tracks:\n got %v\nwant %v", got, want)
	}
}

func TestSidecarLangs(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{"Film (2020).mkv", "Film (2020).en.srt", "Film (2020).fr.forced.srt", "Other.de.srt"} {
		if err := os.WriteFile(filepath.Join(dir, n), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got := sidecarLangs(filepath.Join(dir, "Film (2020).mkv"), nil)
	if strings.Join(got, ",") != "en,fr" {
		t.Errorf("sidecars = %v, want en, fr", got)
	}
}

func TestLanguageCodeVariants(t *testing.T) {
	for _, c := range [][2]string{{"fre", "fr"}, {"fra", "french-ish fr"}, {"ger", "de"}, {"en", "eng"}} {
		if !langIn(c[0], []string{strings.Fields(c[1])[len(strings.Fields(c[1]))-1]}) {
			t.Errorf("%s should match %s", c[0], c[1])
		}
	}
	if langIn("jpn", []string{"en"}) {
		t.Error("jpn must not match en")
	}
}
