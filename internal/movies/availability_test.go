package movies

import (
	"testing"
	"time"
)

func TestAvailable(t *testing.T) {
	now := time.Date(2026, 7, 16, 0, 0, 0, 0, time.UTC)
	rel := func(date string) *MovieExtra { return &MovieExtra{ReleaseDate: date} }

	cases := []struct {
		name string
		m    Movie
		want bool
	}{
		{"announced always searchable", Movie{MinAvailability: "announced"}, true},
		{"released + status Released", Movie{MinAvailability: "released", Status: "Released"}, true},
		{"released + status Released but future date → not yet", Movie{MinAvailability: "released", Status: "Released", Extra: rel("2026-12-25")}, false},
		{"released + future date → not yet", Movie{MinAvailability: "released", Extra: rel("2026-12-25")}, false},
		{"released + past date → available", Movie{MinAvailability: "released", Extra: rel("2025-01-01")}, true},
		{"released + today → available", Movie{MinAvailability: "released", Extra: rel("2026-07-16")}, true},
		{"released, no date, not Released → strict skip", Movie{MinAvailability: "released", Status: "Post Production"}, false},
		{"inCinemas, no date, not Released → lenient allow", Movie{MinAvailability: "inCinemas", Status: "Post Production"}, true},
		{"empty min avail → default allow", Movie{}, true},
	}
	for _, tc := range cases {
		if got := available(tc.m, now); got != tc.want {
			t.Errorf("%s: available = %v, want %v", tc.name, got, tc.want)
		}
	}
}
