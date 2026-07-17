package subtitles

import (
	"regexp"
	"strconv"
	"strings"
)

// stockHallucinations are the phrases Whisper most often invents over non-speech audio (music,
// silence) — artefacts of training on YouTube captions. On their own, as a whole cue, they're
// almost always hallucinations, so we drop such cues from generated SRTs (belt-and-suspenders on
// top of VAD). Matched case-insensitively, ignoring surrounding punctuation.
var stockHallucinations = map[string]bool{
	"thank you":                    true,
	"thanks for watching":          true,
	"thank you for watching":       true,
	"thank you very much":          true,
	"please subscribe":             true,
	"like and subscribe":           true,
	"subtitles by the amara.org community": true,
	"you":                          true,
	"bye":                          true,
	"bye bye":                      true,
	"okay":                         true,
	"the end":                      true,
}

var srtIndexLine = regexp.MustCompile(`^\d+$`)
var srtTimeLine = regexp.MustCompile(`-->`)

// filterStockPhrases removes SRT cues whose entire text is a known stock hallucination. It rebuilds
// the block indices so the output stays a valid SRT. Non-matching cues pass through untouched.
func filterStockPhrases(srt string) string {
	// Normalise line endings for parsing; emit \n.
	blocks := strings.Split(strings.ReplaceAll(srt, "\r\n", "\n"), "\n\n")
	kept := make([]string, 0, len(blocks))
	for _, b := range blocks {
		if strings.TrimSpace(b) == "" {
			continue
		}
		if isStockCue(b) {
			continue
		}
		kept = append(kept, strings.TrimRight(b, "\n"))
	}
	// Renumber.
	var out strings.Builder
	n := 0
	for _, b := range kept {
		lines := strings.Split(b, "\n")
		// Drop a leading numeric index line if present; we re-add our own.
		if len(lines) > 0 && srtIndexLine.MatchString(strings.TrimSpace(lines[0])) {
			lines = lines[1:]
		}
		if len(lines) == 0 {
			continue
		}
		n++
		out.WriteString(strconv.Itoa(n))
		out.WriteString("\n")
		out.WriteString(strings.Join(lines, "\n"))
		out.WriteString("\n\n")
	}
	return out.String()
}

// isStockCue reports whether an SRT block's text lines are (only) a stock hallucination phrase.
func isStockCue(block string) bool {
	var text []string
	for _, ln := range strings.Split(block, "\n") {
		t := strings.TrimSpace(ln)
		if t == "" || srtIndexLine.MatchString(t) || srtTimeLine.MatchString(t) {
			continue
		}
		text = append(text, t)
	}
	if len(text) == 0 {
		return false
	}
	joined := strings.ToLower(strings.Join(text, " "))
	joined = strings.TrimSpace(strings.Trim(joined, ".!?,-–— \t\"'"))
	return stockHallucinations[joined]
}
