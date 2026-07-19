package automation

import "testing"

// TestSortedKeysStable checks the follow-up search picks the same episodes each sweep.
// Map iteration is random in Go, so without sorting, an anime with many gaps would
// query three arbitrary episodes per pass and could take a long time to cover the run.
func TestSortedKeysStable(t *testing.T) {
	in := map[epKey]bool{
		{season: 2, episode: 3}:  true,
		{season: 1, episode: 10}: true,
		{season: 1, episode: 2}:  true,
	}
	want := []epKey{{1, 2}, {1, 10}, {2, 3}}
	for i := 0; i < 20; i++ { // repeat: random map order must not leak through
		got := sortedKeys(in)
		if len(got) != len(want) {
			t.Fatalf("sortedKeys len = %d, want %d", len(got), len(want))
		}
		for j := range want {
			if got[j] != want[j] {
				t.Fatalf("sortedKeys = %+v, want %+v", got, want)
			}
		}
	}
	if got := sortedKeys(map[epKey]bool{}); len(got) != 0 {
		t.Errorf("empty set = %+v, want none", got)
	}
}

func TestContainsKey(t *testing.T) {
	keys := []epKey{{1, 13}, {1, 14}}
	if !containsKey(keys, epKey{1, 14}) {
		t.Error("expected {1,14} to be present")
	}
	if containsKey(keys, epKey{1, 15}) {
		t.Error("{1,15} is not in the set")
	}
	if containsKey(nil, epKey{1, 1}) {
		t.Error("nil set contains nothing")
	}
}
