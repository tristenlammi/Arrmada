package music

import "testing"

func TestStandInCover(t *testing.T) {
	cases := []struct {
		name   string
		albums []Album
		want   string
	}{
		{"no albums", nil, ""},
		{"no covers", []Album{{ID: 1, PrimaryType: "Album", HaveTracks: 3}}, ""},
		{"held beats newer listed", []Album{
			{ID: 1, CoverURL: "new", PrimaryType: "Album", Year: 2024},
			{ID: 2, CoverURL: "held", PrimaryType: "Album", Year: 1999, HaveTracks: 1},
		}, "held"},
		{"album beats single", []Album{
			{ID: 1, CoverURL: "single", PrimaryType: "Single", Year: 2024},
			{ID: 2, CoverURL: "album", PrimaryType: "Album", Year: 2001},
		}, "album"},
		{"newest album", []Album{
			{ID: 1, CoverURL: "old", PrimaryType: "Album", Year: 1990},
			{ID: 2, CoverURL: "new", PrimaryType: "Album", Year: 2010},
		}, "new"},
		{"tie goes to the first added", []Album{
			{ID: 7, CoverURL: "b", PrimaryType: "Album", Year: 2000},
			{ID: 3, CoverURL: "a", PrimaryType: "Album", Year: 2000},
		}, "a"},
		{"one without a cover is skipped", []Album{
			{ID: 1, PrimaryType: "Album", HaveTracks: 9},
			{ID: 2, CoverURL: "only", PrimaryType: "EP"},
		}, "only"},
	}
	for _, c := range cases {
		if got := standInCover(c.albums); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}
