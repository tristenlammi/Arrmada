package quality

import "testing"

// TestContainsTerm guards the fix for reject terms matching mid-word — e.g. the default "com"
// executable term must not reject "... Season 1 Complete ...".
func TestContainsTerm(t *testing.T) {
	cases := []struct {
		name, term string
		want       bool
	}{
		{"ben 10 2005 season 1 complete 720p web-dl x264 [i_c]", "com", false}, // "Complete"
		{"movie.2024.combat.720p", "bat", false},                               // "Combat"
		{"the.batman.2022.1080p", "bat", false},                                // "Batman"
		{"fake.movie.2024.com", "com", true},                                   // real .com token
		{"movie.2024.exe", "exe", true},
		{"movie.cam.x264", "cam", true},
		{"movie.camera.doc", "cam", false},
		{"", "com", false},
		{"anything", "", false},
	}
	for _, c := range cases {
		if got := containsTerm(c.name, c.term); got != c.want {
			t.Errorf("containsTerm(%q,%q) = %v, want %v", c.name, c.term, got, c.want)
		}
	}
}
