package automation

import (
	"testing"

	"github.com/tristenlammi/arrmada/internal/parser"
)

func TestEpisodeRelease(t *testing.T) {
	// Only single-episode releases for the exact S/E qualify (upgrades are surgical).
	tests := []struct {
		title      string
		season, ep int
		want       bool
	}{
		{"Severance S02E01 1080p BluRay REMUX", 2, 1, true},
		{"Severance S02E01 1080p WEB-DL", 2, 2, false},  // wrong episode
		{"Severance S01E01 1080p WEB-DL", 2, 1, false},  // wrong season
		{"Severance S02 1080p WEBRip", 2, 1, false},     // season pack, not an episode release
		{"Severance S01-S02 1080p WEBRip", 2, 1, false}, // multi-season pack
		{"The Matrix 1999 1080p BluRay", 2, 1, false},   // not TV at all
	}
	for _, tc := range tests {
		if got := episodeRelease(parser.Parse(tc.title), tc.season, tc.ep); got != tc.want {
			t.Errorf("episodeRelease(%q, S%d E%d) = %v, want %v", tc.title, tc.season, tc.ep, got, tc.want)
		}
	}
}

func TestReleaseIsForSeries(t *testing.T) {
	tests := []struct {
		relTitle, seriesTitle string
		want                  bool
	}{
		{"Severance S02E01 1080p WEB-DL", "Severance", true},
		{"Severance.S02.1080p.WEBRip", "Severance", true},
		{"Silo S01E05 1080p WEB", "Severance", false},
		{"Severance 2022 S02 1080p", "severance", true}, // case-insensitive
		// A spinoff that shares a title prefix must NOT match the base show.
		{"Below.Deck.Mediterranean.S08.1080p.AMZN.WEB-DL.DDP2.0.H.264-NTb", "Below Deck", false},
		{"Below Deck Down Under S02 1080p WEB-DL", "Below Deck", false},
		{"Below.Deck.S08.1080p.WEB", "Below Deck", true}, // the real show still matches
	}
	for _, tc := range tests {
		if got := releaseIsForSeries(tc.relTitle, tc.seriesTitle); got != tc.want {
			t.Errorf("releaseIsForSeries(%q, %q) = %v, want %v", tc.relTitle, tc.seriesTitle, got, tc.want)
		}
	}
}
