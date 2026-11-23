package mediaserver

import "testing"

func TestSetJellyfinLabel(t *testing.T) {
	t.Cleanup(func() { SetJellyfinLabel(nil) })
	label := "Emby"
	SetJellyfinLabel(func() string { return label })
	if Jellyfin.Label() != "Emby" || Plex.Label() != "Plex" {
		t.Errorf("labels = %q, %q", Jellyfin.Label(), Plex.Label())
	}
	label = ""
	if Jellyfin.Label() != "Jellyfin" {
		t.Errorf("blank answer = %q", Jellyfin.Label())
	}
	SetJellyfinLabel(nil)
	if Jellyfin.Label() != "Jellyfin" {
		t.Errorf("cleared = %q", Jellyfin.Label())
	}
}
