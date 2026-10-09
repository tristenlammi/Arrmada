package series

import (
	"testing"

	"github.com/tristenlammi/arrmada/internal/parser"
)

// A release carrying a romaji alternate title in parentheses must resolve to the show —
// otherwise its download won't show progress on the series page and, worse, the finished
// pack won't route to the series to import. The air year after the season marker must not
// count against the show's own year.
func TestAltTitleMatchesSeries(t *testing.T) {
	match := (&Service{}).ReleaseMatcher([]Series{{ID: 1, Title: "My Hero Academia", Year: 2016}})

	if _, ok, _ := match(parser.Parse("My Hero Academia (Boku no Hero Academia) S04 2019 1080p WEB-DL")); !ok {
		t.Error("a parenthesised alt-title with an air year after the marker should match the library show")
	}
	if _, ok, _ := match(parser.Parse("Some Other Anime S01")); ok {
		t.Error("an unrelated title must not match")
	}
}
