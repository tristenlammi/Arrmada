package automation

import (
	"testing"

	"github.com/tristenlammi/arrmada/internal/series"
)

// The title gate is where every Thousand-Year Blood War release was being dropped —
// twenty of them in one real search. An alias has to get them through it without
// letting anything else through.
func TestSeriesTitleMatchesUsesAliases(t *testing.T) {
	bleach := series.Series{
		Title:      "Bleach",
		SeriesType: "anime",
		Aliases: []series.Alias{
			{Title: "BLEACH Thousand-Year Blood War", TMDBSeason: 17},
			{Title: "Bleach - Sennen Kessen Hen", TMDBSeason: 17},
		},
	}

	for _, rel := range []string{
		"BLEACH Thousand-Year Blood War S02E02 1080p WEB h264-QUiNTESSENCE",
		// Hyphenation and case vary by group; the shared title key absorbs both.
		"BLEACH Thousand Year Blood War S01E41 1080p WEBRip x265-Xiangliu",
		"[SubsPlease] Bleach - Sennen Kessen Hen - 45 (1080p) [70D690AF]",
		"Bleach S04E04 1080p BluRay x264-GRP", // the real title still matches
	} {
		if !seriesTitleMatches(rel, bleach) {
			t.Errorf("%q was rejected — it belongs to this series", rel)
		}
	}

	// An alias must not become a way for unrelated shows to slip through.
	for _, rel := range []string{
		"Bleach Brave Souls Gameplay 1080p",
		"Thousand-Year Door S01E01 1080p WEB h264-GRP",
		"Blue Exorcist S01E01 1080p WEB h264-GRP",
	} {
		if seriesTitleMatches(rel, bleach) {
			t.Errorf("%q matched, but it isn't this show", rel)
		}
	}
}

// The isolation guarantee: with no aliases configured, matching is exactly what it was.
func TestSeriesTitleMatchesUnchangedWithoutAliases(t *testing.T) {
	plain := series.Series{Title: "Bleach", SeriesType: "anime"}
	if seriesTitleMatches("BLEACH Thousand-Year Blood War S02E02 1080p WEB-GRP", plain) {
		t.Error("an arc title matched a series with no aliases — behaviour changed for everyone")
	}
	if !seriesTitleMatches("Bleach S04E04 1080p BluRay x264-GRP", plain) {
		t.Error("the series' own title stopped matching")
	}
}
