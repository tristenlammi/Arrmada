package adultfilter

import "testing"

// Matches must block porn while leaving mainstream titles that merely contain
// suggestive words alone — the whole reason bare "sex"/"xxx" are not on the list.
func TestMatches(t *testing.T) {
	for name, want := range map[string]bool{
		// Adult studios / sites, in release-name and plain forms.
		"Brazzers.22.03.04.Scene.XXX.1080p": true,
		"Reality_Kings - something":         true,
		"naughtyamerica 2020":               true,
		"Some.Title.Creampie.1080p":         true,
		"A Gangbang Story":                  true,
		// Mainstream titles that must survive.
		"Sex Education":                                      false,
		"Sex and the City":                                   false,
		"Sex, Lies, and Videotape":                           false,
		"xXx: Return of Xander Cage":                         false,
		"Gonzo: The Life and Work of Dr. Hunter S. Thompson": false,
		"Analyze This":                                       false, // must not trip "anal"
		"The Vixen and the Hare":                             true,  // studio term — accepted false positive
		"Casino Royale":                                      false,
		"Affair at the Nuns' Temple":                         false, // TMDB adult flag / vote floor catch this
	} {
		if got := Matches(name); got != want {
			t.Errorf("Matches(%q) = %v, want %v", name, got, want)
		}
	}
}
