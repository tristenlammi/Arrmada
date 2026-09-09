package subtitles

import (
	"testing"
	"time"
)

// A chunk that drifted to lowercase reads as sentences again: "i" forms are fixed,
// sentence starts capitalised, mid-sentence cue starts left alone.
func TestFixCasingTidiesDriftedText(t *testing.T) {
	sec := func(s float64) time.Duration { return time.Duration(s * float64(time.Second)) }
	cues := []cue{
		{start: sec(0), end: sec(2), text: "get some help. get some serious help"},
		{start: sec(2.2), end: sec(4), text: "i mean i don't know, i'm not sure"}, // continues the sentence
		{start: sec(7), end: sec(9), text: "talk to someone? learn how to"},       // 3 s pause: a new sentence
		{start: sec(9.1), end: sec(10), text: "\"okay,\" she said... and left"},
		{start: sec(10.1), end: sec(11), text: "This Is Already Cased."},
		{start: sec(11.1), end: sec(12), text: "— fine"},
	}
	got := fixCasing(cues, true)
	want := []string{
		"Get some help. Get some serious help",
		"I mean I don't know, I'm not sure",
		"Talk to someone? Learn how to",
		"\"okay,\" she said... and left",
		"This Is Already Cased.",
		"— Fine",
	}
	for i, w := range want {
		if got[i].text != w {
			t.Errorf("cue %d = %q, want %q", i, got[i].text, w)
		}
	}
	// Not English: the "i" rule is off, sentence starts still handled.
	fr := fixCasing([]cue{{start: 0, end: sec(1), text: "il a dit. je ne sais pas"}}, false)
	if fr[0].text != "Il a dit. Je ne sais pas" {
		t.Errorf("french = %q", fr[0].text)
	}
	if got := fixCasing([]cue{{text: "..."}}, true); got[0].text != "..." {
		t.Errorf("punctuation-only cue changed: %q", got[0].text)
	}
}
