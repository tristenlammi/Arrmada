package music

import "testing"

func TestOtherReleaseKind(t *testing.T) {
	studio := Album{Title: "No Prayer for the Dying", PrimaryType: "Album"}
	live := Album{Title: "Rock in Rio", PrimaryType: "Album", SecondaryTypes: "Live"}
	cases := []struct {
		name, release, artist string
		al                    Album
		other                 bool
	}{
		{"the album itself", "Iron Maiden - No Prayer for the Dying (1990) [FLAC]", "Iron Maiden", studio, false},
		{"a remaster", "Iron Maiden - No Prayer for the Dying (2015 Remaster) [FLAC]", "Iron Maiden", studio, false},
		{"a live release with its name", "Iron Maiden - No Prayer for the Dying (Live at Donington) [FLAC]", "Iron Maiden", studio, true},
		{"demos", "Green Day - Dookie Demos [MP3]", "Green Day", Album{Title: "Dookie"}, true},
		{"a best-of", "Iron Maiden - The Very Best of incl No Prayer for the Dying", "Iron Maiden", studio, true},
		{"remixes", "Disturbed - Believe Remixes [FLAC]", "Disturbed", Album{Title: "Believe"}, true},
		{"a live album may be live", "Iron Maiden - Rock in Rio (Live) [FLAC]", "Iron Maiden", live, false},
		{"the title says live", "Hole - Live Through This (1994) [FLAC]", "Hole", Album{Title: "Live Through This"}, false},
		{"the artist is Live", "Live - Throwing Copper (1994) [FLAC]", "Live", Album{Title: "Throwing Copper"}, false},
		{"alive isn't live", "Kiss - Alive II [FLAC]", "Kiss", Album{Title: "Alive II"}, false},
		{"an anniversary remix is the album", "Disturbed - Believe (2019 Remix) [FLAC]", "Disturbed", Album{Title: "Believe"}, false},
		{"remixed and remastered", "Disturbed - Believe (Remixed & Remastered) [FLAC]", "Disturbed", Album{Title: "Believe"}, false},
		{"a deluxe with live bonus tracks", "Iron Maiden - No Prayer for the Dying (Deluxe incl. Live Tracks) [FLAC]", "Iron Maiden", studio, false},
		{"plus demos", "Green Day - Dookie + Demos [FLAC]", "Green Day", Album{Title: "Dookie"}, false},
		{"live outside the bonus part", "Iron Maiden - No Prayer for the Dying (Live) incl. Bonus [FLAC]", "Iron Maiden", studio, true},
		{"an old album type label", "Iron Maiden - Rock in Rio (Live) [FLAC]", "Iron Maiden", Album{Title: "Rock in Rio", AlbumType: "Live"}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if why := OtherReleaseKind(c.release, c.artist, c.al); (why != "") != c.other {
				t.Errorf("OtherReleaseKind(%q) = %q, want other kind = %v", c.release, why, c.other)
			}
		})
	}
}
