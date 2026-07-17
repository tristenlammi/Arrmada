package library

import "testing"

// TestIsSampleName guards the fix for a real episode being skipped because its title
// contained "sample" as a substring (e.g. "Chuck Versus the Nacho Sampler").
func TestIsSampleName(t *testing.T) {
	cases := []struct {
		name string
		want bool
	}{
		{"Chuck - S03E06 - Chuck Versus the Nacho Sampler Bluray-1080p.mkv", false}, // "Sampler"
		{"Some.Show.Free.Samples.S01E01.mkv", false},                               // "Samples" episode title
		{"Movie.2024.sample.mkv", true},
		{"sample.mkv", true},
		{"RARBG-sample.mp4", true},
		{"Movie.2024.1080p.mkv", false},
	}
	for _, c := range cases {
		if got := isSampleName(c.name); got != c.want {
			t.Errorf("isSampleName(%q) = %v, want %v", c.name, got, c.want)
		}
	}
}
