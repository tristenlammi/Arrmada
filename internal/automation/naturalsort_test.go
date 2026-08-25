package automation

import "testing"

// The merge deletes its source files once it succeeds, so the ONE thing it cannot get
// wrong is the order. sort.Strings put "Chapter 10" before "Chapter 2", which shuffled
// any audiobook with ten or more unpadded chapter files into an unrecoverable mess.
func TestSortNaturalOrdersChapters(t *testing.T) {
	cases := []struct {
		name     string
		in, want []string
	}{
		{
			"unpadded chapter numbers",
			[]string{"Chapter 10.mp3", "Chapter 2.mp3", "Chapter 1.mp3", "Chapter 20.mp3", "Chapter 9.mp3"},
			[]string{"Chapter 1.mp3", "Chapter 2.mp3", "Chapter 9.mp3", "Chapter 10.mp3", "Chapter 20.mp3"},
		},
		{
			// Padded files already sort lexically; natural order must not disturb them.
			"zero-padded still works",
			[]string{"track03.mp3", "track01.mp3", "track02.mp3"},
			[]string{"track01.mp3", "track02.mp3", "track03.mp3"},
		},
		{
			"multiple numbers per name",
			[]string{"Part 1 - 10.mp3", "Part 1 - 2.mp3", "Part 2 - 1.mp3"},
			[]string{"Part 1 - 2.mp3", "Part 1 - 10.mp3", "Part 2 - 1.mp3"},
		},
		{
			"case doesn't reorder",
			[]string{"chapter 2.mp3", "Chapter 1.mp3"},
			[]string{"Chapter 1.mp3", "chapter 2.mp3"},
		},
		{
			// Real rips mix padding within one folder.
			"mixed padding sorts by value",
			[]string{"09 - end.mp3", "10 - after.mp3", "1 - start.mp3"},
			[]string{"1 - start.mp3", "09 - end.mp3", "10 - after.mp3"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := append([]string(nil), c.in...)
			sortNatural(got)
			for i := range c.want {
				if got[i] != c.want[i] {
					t.Fatalf("got %v, want %v", got, c.want)
				}
			}
		})
	}
}

// A comparison used for sorting must be a strict weak ordering, or sort.Slice can
// misbehave. Equal values must not compare less than each other in either direction.
func TestNaturalLessIsConsistent(t *testing.T) {
	for _, s := range []string{"a", "Chapter 1.mp3", "01", "1", ""} {
		if naturalLess(s, s) {
			t.Errorf("naturalLess(%q, %q) must be false — a value is not less than itself", s, s)
		}
	}
	pairs := [][2]string{{"Chapter 2.mp3", "Chapter 10.mp3"}, {"a", "b"}, {"1", "2"}}
	for _, p := range pairs {
		if !naturalLess(p[0], p[1]) {
			t.Errorf("%q should sort before %q", p[0], p[1])
		}
		if naturalLess(p[1], p[0]) {
			t.Errorf("%q must not also sort before %q — the order has to be asymmetric", p[1], p[0])
		}
	}
}
