package series

import (
	"testing"

	"github.com/tristenlammi/arrmada/internal/parser"
)

// A release carrying a romaji alternate title in parentheses must resolve to the show —
// otherwise its download won't show progress on the series page and, worse, the finished
// pack won't route to the series to import. Mirrors the real call: match on the parsed
// TITLE, normalized.
func TestAltTitleMatchesSeries(t *testing.T) {
	match := (&Service{}).TitleMatcher([]Series{{ID: 1, Title: "My Hero Academia"}})

	release := "My Hero Academia (Boku no Hero Academia) S04 2019 1080p WEB-DL"
	title := parser.Parse(release).Title // "My Hero Academia (Boku no Hero Academia)"
	if _, ok := match(NormTitle(title)); !ok {
		t.Errorf("parsed title %q with a parenthesised alt-title should match the library show", title)
	}
	if _, ok := match(NormTitle(parser.Parse("Some Other Anime S01").Title)); ok {
		t.Error("an unrelated title must not match")
	}
}
