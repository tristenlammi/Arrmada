package movies

import "testing"

func TestSagaBareName(t *testing.T) {
	force := Movie{Title: "Star Wars: The Force Awakens", Year: 2015}
	numbered := Movie{Title: "Star Wars: Episode VII - The Force Awakens", Year: 2015}
	cases := []struct {
		name  string
		m     Movie
		title string
		year  int
		want  bool
	}{
		{"number at the end", force, "Star Wars The Force Awakens Episode VII", 2015, true},
		{"number in the middle", force, "Star Wars Episode VII The Force Awakens", 2015, true},
		{"any case", force, "Star Wars The Force Awakens EPISODE vii", 2015, true},
		{"the film is the one listed with its number", numbered, "Star Wars The Force Awakens Episode VII", 2015, true},
		{"a year off is another film", force, "Star Wars The Force Awakens Episode VII", 2016, false},
		{"no year proves nothing", force, "Star Wars The Force Awakens Episode VII", 0, false},
		{"no number: Match and KnownAs decide", force, "Star Wars The Force Awakens", 2015, false},
		{"another film's name with a number", force, "Star Wars The Last Jedi Episode VIII", 2015, false},
		{"a word, not a numeral", force, "Star Wars The Force Awakens Episode Vision", 2015, false},
		{"an arabic number isn't read", force, "Star Wars The Force Awakens Episode 7", 2015, false},
		{"nothing left but the number", Movie{Title: "A", Year: 2015}, "A Episode I", 2015, false},
		{"a film listed with its number needs the same number", numbered, "Star Wars The Force Awakens Episode VIII", 2015, false},
		{"a film listed with its number, spelled the same", numbered, "Star Wars Episode vii The Force Awakens", 2015, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, got := SagaBareName(c.m, c.title, c.year); got != c.want {
				t.Errorf("SagaBareName(%q, %q, %d) = %v, want %v", c.m.Title, c.title, c.year, got, c.want)
			}
		})
	}
}
