package automation

import (
	"testing"

	"github.com/tristenlammi/arrmada/internal/parser"
)

func TestShowEnded(t *testing.T) {
	cases := map[string]bool{
		"Ended":            true,
		"ended":            true,
		"Canceled":         true,
		"Cancelled":        true,
		"Returning Series": false,
		"In Production":    false,
		"Planned":          false,
		"":                 false,
	}
	for status, want := range cases {
		if got := showEnded(status); got != want {
			t.Errorf("showEnded(%q) = %v, want %v", status, got, want)
		}
	}
}

func TestIsPackTier(t *testing.T) {
	// Season packs are always eligible; multi-season / complete-show only for ended shows.
	tests := []struct {
		kind  parser.Kind
		ended bool
		want  bool
	}{
		{parser.KindSeasonPack, false, true},
		{parser.KindSeasonPack, true, true},
		{parser.KindMultiSeason, false, false}, // running show: no multi-season packs
		{parser.KindMultiSeason, true, true},
		{parser.KindCompleteShow, false, false}, // running show: no whole-show packs
		{parser.KindCompleteShow, true, true},
		{parser.KindEpisode, true, false},
		{parser.KindMovie, true, false},
	}
	for _, tc := range tests {
		if got := isPackTier(tc.kind, tc.ended); got != tc.want {
			t.Errorf("isPackTier(%v, ended=%v) = %v, want %v", tc.kind, tc.ended, got, tc.want)
		}
	}
}
