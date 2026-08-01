package music

import "testing"

// The tier is codec AND bitrate: "MP3" alone says nothing, because 128 and 320 are two and
// a half times apart.
func TestDetectQuality(t *testing.T) {
	cases := []struct {
		name string
		want Quality
	}{
		{"Radiohead - OK Computer (1997) [FLAC]", QualityFLAC},
		{"Radiohead - OK Computer (1997) [FLAC 24bit-96kHz]", QualityFLAC24},
		{"Artist - Album [24-bit FLAC]", QualityFLAC24},
		{"Artist - Album [ALAC]", QualityALAC},
		{"Artist - Album [WAV]", QualityWAV},
		{"Artist - Album (2016) [MP3-320]", QualityMP3320},
		{"Artist - Album 2016 320kbps", QualityMP3320},
		{"Artist - Album [V0]", QualityMP3V0},
		{"Artist - Album [LAME V2]", QualityMP3V2},
		{"Artist - Album [MP3 256]", QualityMP3256},
		{"Artist - Album [AAC 256]", QualityAAC256},
		{"Artist - Album [MP3 192]", QualityMP3192},
		{"Artist - Album [128kbps]", QualityMP3128},
		{"Artist - Album [Opus]", QualityOpus},
		{"Artist - Album (2016)", QualityUnknown},
		// A dual-format upload is worth taking for its better half.
		{"Artist - Album (2016) [FLAC / MP3 320]", QualityFLAC},
		// An untagged MP3 is a coin toss — assume the middle, never the best.
		{"Artist - Album 2016 MP3", QualityMP3192},
	}
	for _, c := range cases {
		if got := DetectQuality(c.name); got != c.want {
			t.Errorf("DetectQuality(%q) = %q, want %q", c.name, got, c.want)
		}
	}
}

// Lossless outranks every lossy tier, and V0 sits above 256 because it averages ~245 kbps.
func TestQualityLadderOrder(t *testing.T) {
	ordered := []Quality{
		QualityFLAC24, QualityFLAC, QualityALAC, QualityWAV,
		QualityMP3320, QualityMP3V0, QualityAAC256, QualityMP3256,
		QualityMP3V2, QualityOpus, QualityOGG, QualityMP3192, QualityMP3128, QualityUnknown,
	}
	for i := 1; i < len(ordered); i++ {
		if QualityRank(ordered[i-1]) <= QualityRank(ordered[i]) {
			t.Errorf("%q should outrank %q", ordered[i-1], ordered[i])
		}
	}
	for _, q := range []Quality{QualityFLAC24, QualityFLAC, QualityALAC, QualityWAV} {
		if !IsLosslessQuality(q) {
			t.Errorf("%q should be lossless", q)
		}
	}
	if IsLosslessQuality(QualityMP3320) {
		t.Error("MP3-320 is not lossless")
	}
	// Every laddered tier must be score-able, or a profile can't express it.
	if len(AllQualities) != 13 {
		t.Errorf("AllQualities has %d entries; keep it in step with the ladder", len(AllQualities))
	}
}

func TestParseRelease(t *testing.T) {
	r := ParseRelease("Radiohead - OK Computer (1997) [FLAC]")
	if r.Artist != "Radiohead" || r.Album != "OK Computer" || r.Year != 1997 || r.Quality != QualityFLAC {
		t.Errorf("parsed = %+v", r)
	}
	// Dot-separated WITH a separator still splits cleanly.
	if got := ParseRelease("Portishead - Dummy - 1994 - MP3-320"); got.Artist != "Portishead" || got.Year != 1994 || got.Quality != QualityMP3320 {
		t.Errorf("dot-separated with separator = %+v", got)
	}
	// Without any " - " there is no reliable artist/album boundary ("Portishead Dummy" could
	// be either). Year and quality still parse; the album is left empty so the caller matches
	// against the library rather than acting on a guess.
	if got := ParseRelease("Portishead.Dummy.1994.MP3-320"); got.Year != 1994 || got.Quality != QualityMP3320 || got.Album != "" {
		t.Errorf("unsplittable name should still yield year+quality with no album, got %+v", got)
	}
	// A dot inside a name must survive when the name already has spaces.
	if got := ParseRelease("Mr. Bungle - Disco Volante (1995) [FLAC]"); got.Artist != "Mr. Bungle" || got.Album != "Disco Volante" {
		t.Errorf("name with a dot = %+v", got)
	}
	// A discography must be recognisable: grabbing one for a single album would fill that
	// album's folder with the artist's entire catalogue.
	if !ParseRelease("Radiohead - Discography (1993-2016) [FLAC]").Discography {
		t.Error("a discography release should be flagged")
	}
	if ParseRelease("Radiohead - OK Computer (1997) [FLAC]").Discography {
		t.Error("a single album must not be flagged as a discography")
	}
	// An unparseable album is empty, not wrong — the caller can refuse rather than guess.
	if got := ParseRelease("somerandomupload"); got.Album != "" {
		t.Errorf("album should be empty when unparseable, got %q", got.Album)
	}
}
