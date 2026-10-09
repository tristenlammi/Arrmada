package convert

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
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
	for _, n := range []string{"Film (2020).mkv", "Film (2020).en.srt", "Film (2020).fr.forced.srt", "Other.de.srt",
		"Film (2020).es.sdh.srt", "Film (2020).eng.srt", "Film (2020).it.hi.srt", "Film (2020).hi.srt"} {
		if err := os.WriteFile(filepath.Join(dir, n), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	full, forced := sidecarLangs(filepath.Join(dir, "Film (2020).mkv"), nil)
	sort.Strings(full)
	if strings.Join(full, ",") != "en,eng,es,hi,it" {
		t.Errorf("full = %v, want en, eng, es (SDH), hi (bare = Hindi), it (.it.hi = Italian SDH)", full)
	}
	if strings.Join(forced, ",") != "fr" {
		t.Errorf("forced = %v, want fr", forced)
	}
}

// A file with only '<base>.en.forced.srt' beside it keeps its full English PGS track.
func TestForcedOnlySidecarKeepsFullPGS(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{"Film.mkv", "Film.en.forced.srt"} {
		if err := os.WriteFile(filepath.Join(dir, n), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mi := withSubs(film("h264", 1920, 1080, 12000), pgs("eng"))
	plan := withSidecars(Plan{Subs: SubPlan{ImageSubs: ImageSubsWhenText}}, filepath.Join(dir, "Film.mkv"), nil)
	if got := keptSubs(mi, plan); len(got) != 1 {
		t.Errorf("kept %d tracks; a forced-only sidecar must not cost the full English PGS", len(got))
	}
}

func forcedPGS(lang string) SubStream {
	s := pgs(lang)
	s.Forced = true
	return s
}

func TestForcedImageSubsNeedForcedText(t *testing.T) {
	desc := func(subs []SubStream) string {
		var out []string
		for _, s := range subs {
			k := "img"
			if s.Text {
				k = "txt"
			}
			if s.Forced {
				k += "(f)"
			}
			out = append(out, s.Lang+":"+k)
		}
		return strings.Join(out, ",")
	}
	whenText := Plan{Subs: SubPlan{ImageSubs: ImageSubsWhenText}}

	// A full en.srt (the Subtitles module's) + forced PGS + full PGS: the full PGS goes,
	// the forced PGS stays — nothing else carries the foreign-dialogue lines.
	sidecar := whenText
	sidecar.Subs.TextSidecarLangs = []string{"en"}
	mi := withSubs(film("h264", 1920, 1080, 12000), forcedPGS("eng"), pgs("eng"))
	if got := desc(keptSubs(mi, sidecar)); got != "eng:img(f)" {
		t.Errorf("full sidecar: kept %s, want the forced PGS only", got)
	}
	// The same with an embedded full text track.
	mi = withSubs(film("h264", 1920, 1080, 12000), forcedPGS("eng"), pgs("eng"), srt("eng"))
	if got := desc(keptSubs(mi, whenText)); got != "eng:img(f),eng:txt" {
		t.Errorf("full text: kept %s", got)
	}
	// An embedded forced text track covers the forced PGS.
	forcedSRT := srt("eng")
	forcedSRT.Forced = true
	mi = withSubs(film("h264", 1920, 1080, 12000), forcedPGS("eng"), forcedSRT)
	if got := desc(keptSubs(mi, whenText)); got != "eng:txt(f)" {
		t.Errorf("forced text: kept %s, want the forced PGS dropped", got)
	}
	// ...as does a .forced.srt sidecar.
	fs := whenText
	fs.Subs.TextSidecarForcedLangs = []string{"en"}
	mi = withSubs(film("h264", 1920, 1080, 12000), forcedPGS("eng"))
	if got := keptSubs(mi, fs); len(got) != 0 {
		t.Errorf("forced sidecar: kept %s, want the forced PGS dropped", desc(got))
	}
	// An untagged forced image track is never dropped, even with text everywhere.
	mi = withSubs(film("h264", 1920, 1080, 12000), forcedPGS(""), srt("eng"))
	if got := desc(keptSubs(mi, whenText)); got != ":img(f),eng:txt" {
		t.Errorf("untagged forced: kept %s", got)
	}
}

// Removing a forced track under "remove" is never silent.
func TestPlanWarnsWhenForcedRemoved(t *testing.T) {
	mi := withSubs(film("h264", 1920, 1080, 12000), forcedPGS("eng"), srt("eng"))
	got := strings.Join(planWarnings(mi, Plan{Subs: SubPlan{ImageSubs: ImageSubsRemove}}), " | ")
	if !strings.Contains(got, "forced subtitles (foreign dialogue) removed") {
		t.Errorf("warnings = %q, want the forced-removal warning", got)
	}
	// Not when a forced text subtitle still carries those lines.
	plan := Plan{Subs: SubPlan{ImageSubs: ImageSubsRemove, TextSidecarForcedLangs: []string{"en"}}}
	if w := planWarnings(mi, plan); len(w) != 0 {
		t.Errorf("warned %v although a forced sidecar remains", w)
	}
	if w := planWarnings(mi, Plan{Subs: SubPlan{ImageSubs: ImageSubsWhenText}}); len(w) != 0 {
		t.Errorf("when_text kept the forced track yet warned %v", w)
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
