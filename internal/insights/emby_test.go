package insights

import (
	"testing"

	"github.com/tristenlammi/arrmada/internal/jellyfin"
)

// An Emby session through the Jellyfin mapping: Emby numbers its items, so the play is
// recorded under an "emby-" key (never a bare number a Plex rating key could match) and its
// poster travels in the strict Emby image form.
func TestEmbySessionMapping(t *testing.T) {
	js := jellyfin.Session{
		ID: "8c2f1e0d9b7a4c3e8f6d5a4b3c2d1e0f", UserID: "a1b2c3d4e5f60718293a4b5c6d7e8f90", UserName: "sam",
		Client: "Emby for Android", DeviceName: "Pixel", RemoteEndPoint: "203.0.113.7",
		NowPlayingItem: &jellyfin.NowPlayingItem{
			ID: "2290", Name: "Good News About Hell", Type: jellyfin.TypeEpisode,
			SeriesID: "1310", SeriesName: "Severance", ParentIndexNumber: 1, IndexNumber: 1,
		},
		TranscodingInfo: &jellyfin.TranscodingInfo{HardwareAccelerationType: "QuickSync", Bitrate: 8_000_000},
	}
	s, ok := jellyfinSession(js, 12000)
	if !ok {
		t.Fatal("not mapped")
	}
	if s.RatingKey != "emby-2290" || s.ShowRatingKey != "emby-1310" {
		t.Errorf("keys = %q / %q", s.RatingKey, s.ShowRatingKey)
	}
	if s.Thumb != "emby:/Items/2290/Images/Primary" || s.ShowThumb != "emby:/Items/1310/Images/Primary" || !IsServerImage(s.Thumb) {
		t.Errorf("images = %q / %q", s.Thumb, s.ShowThumb)
	}
	if s.Platform != "Android" || !s.TranscodeHW || s.HWTitle != "quicksync" || s.Bandwidth != 8000 {
		t.Errorf("session = %+v", s)
	}

	// Jellyfin's ids are untouched.
	if jellyfinRatingKey("dbc9717de8df66b09eed7dacda39fcc1") != "dbc9717de8df66b09eed7dacda39fcc1" || jellyfinPlatform("Jellyfin Web") != "Web" {
		t.Error("jellyfin mapping changed")
	}

	// Strict Emby image paths only.
	for _, bad := range []string{"emby:/Items/12a/Images/Primary", "emby:/Items/1/Images/Backdrop", "emby:/Items/../System/Info", "emby:/Items/12345678901234567890/Images/Primary", "emby:/Items/1/Images/Primary?x=1"} {
		if IsEmbyImage(bad) || IsServerImage(bad) {
			t.Errorf("%q accepted", bad)
		}
	}
	if !IsEmbyImage("emby:/Users/42/Images/Primary") {
		t.Error("user picture refused")
	}

	// An Emby key never reaches the Plex index lookup.
	var svc Service
	it := LibraryItem{RatingKey: "emby-2290"}
	svc.identify(&it)
	if it.TMDB != 0 {
		t.Errorf("identified %+v", it)
	}
}
