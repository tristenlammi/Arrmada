package books

import "testing"

// The lead title names the file — not whichever library title is longest.
func TestMatchFileName(t *testing.T) {
	lib := []Book{
		{ID: 1, Title: "Fire & Blood", Author: "George R.R. Martin"},
		{ID: 2, Title: "A Game of Thrones", Author: "George R.R. Martin"},
		{ID: 3, Title: "The Hunger Games", Author: "Suzanne Collins"},
		{ID: 4, Title: "Mockingjay", Author: "Suzanne Collins"},
		{ID: 5, Title: "Red Rising", Author: "Pierce Brown"},
		{ID: 6, Title: "Golden Son", Author: "Pierce Brown"},
		{ID: 7, Title: "Dune", Author: "Frank Herbert"},
		{ID: 8, Title: "Dune Messiah", Author: "Frank Herbert"},
	}
	for name, want := range map[string]int64{
		"Fire & Blood (HBO Tie-in Edition)- 300 Years Before A Game of Thrones [B07CL3F5H2]": 1,
		"Mockingjay (The Final Book of The Hunger Games) - Suzanne Collins":                  4,
		"The Hunger Games - Suzanne Collins":                                                 3,
		"Red Rising 2 - Golden Son":                                                          6,
		"Red Rising":                                                                         5,
		"Frank Herbert - Dune Messiah":                                                       8,
		"Dune 1 - Dune":                                                                      7,
		"A Game of Thrones":                                                                  2,
	} {
		got, ok := matchFileName(lib, name)
		if !ok || got.ID != want {
			t.Errorf("%q → book %d (ok=%v), want %d", name, got.ID, ok, want)
		}
	}
	if _, ok := matchFileName(lib, "01 - Chapter One"); ok {
		t.Error("a generic chapter file names no book")
	}
}
