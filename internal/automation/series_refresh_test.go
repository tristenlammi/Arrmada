package automation

import "testing"

func TestSeriesContinuing(t *testing.T) {
	for status, want := range map[string]bool{
		"Returning Series": true, "In Production": true, "Planned": true, "": true,
		"Ended": false, "Canceled": false, "cancelled": false, " ended ": false,
	} {
		if got := seriesContinuing(status); got != want {
			t.Errorf("seriesContinuing(%q) = %v, want %v", status, got, want)
		}
	}
}
