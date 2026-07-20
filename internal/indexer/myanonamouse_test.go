package indexer

import (
	"strings"
	"testing"
)

func TestJoinMAMNames(t *testing.T) {
	cases := map[string]string{
		`{"123":"Brandon Sanderson"}`:               "Brandon Sanderson",
		`{"1":"Michael Kramer","2":"Kate Reading"}`: "", // order-dependent; checked below
		``:   "",
		`[]`: "",
		`{}`: "",
		`{"9":"Neil Gaiman &amp; Terry Pratchett"}`: "Neil Gaiman & Terry Pratchett",
	}
	for in, want := range cases {
		if want == "" && in == `{"1":"Michael Kramer","2":"Kate Reading"}` {
			// Two-name map: just assert both names are present (map order is random).
			got := joinMAMNames(in)
			if got == "" || !strings.Contains(got, "Michael Kramer") || !strings.Contains(got, "Kate Reading") {
				t.Errorf("joinMAMNames(%q) = %q, want both narrators", in, got)
			}
			continue
		}
		if got := joinMAMNames(in); got != want {
			t.Errorf("joinMAMNames(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestJoinMAMSeries(t *testing.T) {
	if got := joinMAMSeries(`{"42":["The Stormlight Archive","1"]}`); got != "The Stormlight Archive #1" {
		t.Errorf("series = %q", got)
	}
	if got := joinMAMSeries(`{"42":["Mistborn"]}`); got != "Mistborn" {
		t.Errorf("series (no number) = %q", got)
	}
	if got := joinMAMSeries(``); got != "" {
		t.Errorf("empty series = %q", got)
	}
}

func TestParseHumanSize(t *testing.T) {
	mib := float64(int64(1) << 20)
	gib := float64(int64(1) << 30)
	cases := map[string]int64{
		"358.12 MB": int64(358.12 * mib),
		"1.09 GB":   int64(1.09 * gib),
		"512 KB":    512 << 10,
		"bogus":     0,
		"":          0,
		"10":        0,
	}
	for in, want := range cases {
		if got := parseHumanSize(in); got != want {
			t.Errorf("parseHumanSize(%q) = %d, want %d", in, got, want)
		}
	}
}
