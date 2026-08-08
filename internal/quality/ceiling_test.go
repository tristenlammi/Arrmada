package quality

import (
	"strconv"
	"testing"
)

// A file that already sits at the profile's best allowed resolution and its maximum
// source can't be beaten by anything the profile would accept — searching for it again
// every sweep is traffic spent on a guaranteed rejection.
func TestAtCeiling(t *testing.T) {
	s, ctx := testService(t)

	capped, err := s.Create(ctx, StoredProfile{
		MediaType:          MediaSeries,
		Name:               "1080p WEB-DL",
		AllowedResolutions: []string{"1080p"},
		MaxSource:          "WEB-DL",
		UpgradesEnabled:    true,
	})
	if err != nil {
		t.Fatal(err)
	}
	ref := "custom:" + strconv.FormatInt(capped.ID, 10)

	cases := []struct {
		release string
		want    bool
		why     string
	}{
		{"Show.S01E01.1080p.WEB-DL.DDP5.1.H.264-NTb", true, "top allowed resolution and max source"},
		{"Show.S01E01.1080p.BluRay.x264-GRP", true, "source above the cap still can't be improved on"},
		{"Show.S01E01.720p.WEB-DL.H.264-NTb", false, "a 1080p release would beat it"},
		{"Show.S01E01.1080p.HDTV.x264-GRP", false, "WEB-DL would beat HDTV at the same resolution"},
		{"", false, "no recorded release — let the normal path decide"},
	}
	for _, tc := range cases {
		if got := s.AtCeiling(ctx, ref, tc.release); got != tc.want {
			t.Errorf("AtCeiling(%q) = %v, want %v — %s", tc.release, got, tc.want, tc.why)
		}
	}

	// An open-ended profile has no ceiling to reach: with any resolution allowed and no
	// maximum source, something better can always show up, so nothing is ever skipped.
	open, err := s.Create(ctx, StoredProfile{
		MediaType: MediaSeries, Name: "anything", UpgradesEnabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if s.AtCeiling(ctx, "custom:"+strconv.FormatInt(open.ID, 10), "Show.S01E01.2160p.Remux-GRP") {
		t.Error("an unbounded profile must never report a ceiling")
	}
}
