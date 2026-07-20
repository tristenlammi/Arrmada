package subtitles

import (
	"strings"
	"testing"
)

func TestAIPlan(t *testing.T) {
	cases := []struct {
		audio  []string
		wanted string
		want   string
	}{
		{[]string{"eng"}, "en", "transcribe"},        // audio already English
		{[]string{"kor"}, "en", "translate"},         // foreign audio → English translate
		{[]string{"kor"}, "es", ""},                  // can't translate into non-English
		{[]string{"spa"}, "es", "transcribe"},        // Spanish audio → Spanish transcribe
		{nil, "en", "translate"},                     // unknown audio, English target → translate
		{nil, "fr", ""},                              // unknown audio, non-English target → impossible
		{[]string{"eng", "spa"}, "es", "transcribe"}, // matches a secondary audio track
	}
	for _, c := range cases {
		if got := aiPlan(c.audio, c.wanted); got != c.want {
			t.Errorf("aiPlan(%v,%q) = %q, want %q", c.audio, c.wanted, got, c.want)
		}
	}
}

func TestFilterStockPhrases(t *testing.T) {
	in := "1\n00:00:01,000 --> 00:00:03,000\nWe need to move now.\n\n" +
		"2\n00:00:20,000 --> 00:00:22,000\nThank you.\n\n" +
		"3\n00:00:40,000 --> 00:00:44,000\nThe bridge is out ahead.\n\n" +
		"4\n00:01:59,000 --> 00:02:01,000\nThanks for watching!\n\n"
	out := filterStockPhrases(in)
	if strings.Contains(strings.ToLower(out), "thank you") || strings.Contains(strings.ToLower(out), "thanks for watching") {
		t.Fatalf("stock phrases not removed:\n%s", out)
	}
	if !strings.Contains(out, "We need to move now.") || !strings.Contains(out, "The bridge is out ahead.") {
		t.Fatalf("real dialogue was dropped:\n%s", out)
	}
	// Two real cues survive and are renumbered 1, 2.
	if !strings.HasPrefix(out, "1\n") || !strings.Contains(out, "\n2\n") {
		t.Errorf("expected renumbered 1,2 cues:\n%s", out)
	}
	if strings.Contains(out, "\n3\n") {
		t.Errorf("expected only 2 cues after filtering:\n%s", out)
	}
}

func TestModelPath(t *testing.T) {
	// No models dir → nothing available, no crashes.
	w := &whisperGen{bin: "whisper-cli", modelsDir: ""}
	if w.available() {
		t.Error("expected unavailable with no models dir")
	}
	if w.modelPath(true) != "" || w.modelPath(false) != "" {
		t.Error("expected empty model paths with no models")
	}
}
