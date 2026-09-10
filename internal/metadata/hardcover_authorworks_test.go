package metadata

import (
	"testing"
	"time"
)

func hcTestBook(id int, title string, users int, year int, authorID int, role string, lang string) hcBook {
	b := hcBook{ID: id, Title: title, UsersCount: users}
	if year > 0 {
		b.ReleaseYear = &year
	}
	b.Contributions = append(b.Contributions, struct {
		Contribution string `json:"contribution"`
		Author       struct {
			ID   int    `json:"id"`
			Name string `json:"name"`
		} `json:"author"`
	}{Contribution: role})
	b.Contributions[0].Author.ID = authorID
	b.Contributions[0].Author.Name = "Brandon Sanderson"
	if lang != "" {
		b.DefaultPhysicalEdition = &hcEditionLang{Language: &struct {
			Code2 string `json:"code2"`
		}{Code2: lang}}
	}
	return b
}

// An author's listing keeps what they wrote, in English, whole — not forewords,
// translations, split halves, or the unshelved tail (unless it's new).
func TestFilterAuthorWorks(t *testing.T) {
	const me = 7
	now := time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC)
	in := []hcBook{
		hcTestBook(1, "The Way of Kings", 50000, 2010, me, "", "en"),
		hcTestBook(2, "The Way of Kings, Part 1", 900, 2011, me, "", "en"),
		hcTestBook(3, "Words of Radiance: Part Two", 800, 2014, me, "", ""),
		hcTestBook(4, "Words of Radiance", 40000, 2014, me, "Author", "en"),
		hcTestBook(5, "El camino de los reyes", 400, 2012, me, "", "es"),
		hcTestBook(6, "Some Anthology", 3000, 2015, me, "Contributor", "en"),
		hcTestBook(7, "A Foreword Book", 5000, 2018, me, "Foreword", "en"),
		hcTestBook(8, "Obscure Reprint", 1, 2001, me, "", ""),
		hcTestBook(9, "Brand New Novel", 1, 2026, me, "", "en"),
		hcTestBook(10, "Someone Else's Book", 9000, 2019, 8, "", "en"),
		hcTestBook(11, "Mistborn Box Set", 6000, 2016, me, "", "en"),
	}
	got := filterAuthorWorks(in, me, now)
	want := map[string]bool{"The Way of Kings": true, "Words of Radiance": true, "Brand New Novel": true}
	if len(got) != len(want) {
		t.Fatalf("kept %d: %+v, want %v", len(got), got, want)
	}
	for _, r := range got {
		if !want[r.Title] {
			t.Errorf("kept %q", r.Title)
		}
	}
}
