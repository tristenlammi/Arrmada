package parser

import "testing"

// "Series" is the one pack word that is routinely part of a real show title. Stripping it
// unconditionally turned "ARK The Animated Series" into "ARK The Animated", which matched
// no library show — so the pack was held for review on every sweep while its episodes sat
// on disk already.
func TestSeriesIsKeptWhenPartOfTheTitle(t *testing.T) {
	keep := []struct{ release, want string }{
		{"ARK.The.Animated.Series.S01.COMPLETE.1080p.AMZN.WEB-DL.DDP5.1.H264-NTb", "ARK The Animated Series"},
		{"ARK.The.Animated.Series.S01.1080p.AMZN.WEB-DL.DDP5.1.H264-NTb", "ARK The Animated Series"},
		{"Batman.The.Animated.Series.S01.1080p.BluRay.x265-GRP", "Batman The Animated Series"},
		{"Star.Trek.The.Animated.Series.S01E03.1080p.WEB-DL-GRP", "Star Trek The Animated Series"},
	}
	for _, c := range keep {
		if got := Parse(c.release).Title; got != c.want {
			t.Errorf("Parse(%q).Title = %q, want %q", c.release, got, c.want)
		}
	}
}

// The pack sense is spelled "Complete Series", and that must still be stripped — this is
// what the pack-word list was added for, and the reason the fix has to be conditional
// rather than simply removing "series" from the list.
func TestCompleteSeriesIsStillStripped(t *testing.T) {
	strip := []struct{ release, want string }{
		{"Scorpion.Complete.Series.S01-S04.1080p.WEB-DL-GRP", "Scorpion"},
		{"The.Expanse.Complete.Series.S01-S06.1080p.WEB-DL-GRP", "The Expanse"},
		{"Some.Show.Series.Pack.S01-S03.1080p.WEB-DL-GRP", "Some Show Series"},
	}
	for _, c := range strip {
		if got := Parse(c.release).Title; got != c.want {
			t.Errorf("Parse(%q).Title = %q, want %q", c.release, got, c.want)
		}
	}
}

// The other pack words are unaffected — they're not plausible title endings the way
// "Series" is.
func TestOtherPackWordsStillStripped(t *testing.T) {
	for _, c := range []struct{ release, want string }{
		{"The.Expanse.complete.S01-S06.1080p.WEB-DL-GRP", "The Expanse"},
		{"Show.Name.Boxset.S01-S03.1080p.WEB-DL-GRP", "Show Name"},
		{"Show.Name.Collection.S01-S03.1080p.WEB-DL-GRP", "Show Name"},
	} {
		if got := Parse(c.release).Title; got != c.want {
			t.Errorf("Parse(%q).Title = %q, want %q", c.release, got, c.want)
		}
	}
}
