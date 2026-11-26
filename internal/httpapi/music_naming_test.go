package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/tristenlammi/arrmada/internal/auth"
	"github.com/tristenlammi/arrmada/internal/settings"
)

// MUS-34: the music naming preview renders the samples as typed and explains a broken
// template without failing the request; it 404s while Music is off.
func TestMusicNamingPreview(t *testing.T) {
	h, set, session := musicGateServer(t)
	q := url.Values{"folder": {"{Album Artist}/{Album Title} ({Original Year})"}, "track": {"{Disc Prefix}{Track:00} - {Track Title}"},
		"compilation": {"{Disc Prefix}{Track:00} - {Track Artist} - {Track Title}"}}
	if rec := musicGateCall(h, "GET", "/api/v1/music/naming/preview?"+q.Encode(), session); rec.Code != http.StatusNotFound {
		t.Fatalf("music off: HTTP %d", rec.Code)
	}
	if err := set.SetBool(context.Background(), settings.KeyModuleMusic, true); err != nil {
		t.Fatal(err)
	}
	rec := musicGateCall(h, "GET", "/api/v1/music/naming/preview?"+q.Encode(), session)
	if rec.Code != http.StatusOK {
		t.Fatalf("preview: HTTP %d %s", rec.Code, rec.Body.String())
	}
	var got struct {
		Samples []struct{ Paths []string }
		Error   *struct{ Field, Message string }
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Error != nil || len(got.Samples) == 0 || got.Samples[0].Paths[0] != "Radiohead/OK Computer (1997)/01 - Airbag.flac" {
		t.Errorf("preview = %+v", got)
	}
	q.Set("folder", "{Album Artist}")
	rec = musicGateCall(h, "GET", "/api/v1/music/naming/preview?"+q.Encode(), session)
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || rec.Code != http.StatusOK || got.Error == nil || got.Error.Field != "folder" {
		t.Errorf("broken folder: HTTP %d %s", rec.Code, rec.Body.String())
	}
	if rec := musicGateCall(h, "GET", "/api/v1/music/naming/presets", session); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "disc-folders") {
		t.Errorf("presets: HTTP %d %s", rec.Code, rec.Body.String())
	}
}

// A broken music template, or an extras list that's too long, is refused before anything
// is saved; a manager may change them (media management, not an admin setting); the
// extras list is stored tidy.
func TestMusicSettingsAreChecked(t *testing.T) {
	a := settingsAPI(t)
	for _, body := range []string{`{"naming_music_folder":"{Album Artist}"}`, `{"naming_music_track":"{Track Title}"}`,
		`{"naming_music_compilation":""}`, `{"music_extras":"` + strings.Repeat(".x ", 400) + `"}`} {
		if w := putSettingsAs(t, a, auth.RoleManager, body); w.Code != http.StatusBadRequest {
			t.Errorf("PUT %s: HTTP %d %s", body, w.Code, w.Body.String())
		}
	}
	if w := putSettingsAs(t, a, auth.RoleManager, `{"music_write_tags":false,"music_extras":"PDF, jpg  .lrc pdf","music_transcode":true,"music_transcode_format":"MP3","music_transcode_quality":"320","music_transcode_lossless_only":false}`); w.Code != http.StatusOK {
		t.Fatalf("PUT: HTTP %d %s", w.Code, w.Body.String())
	}
	_, got := getSettings(t, a)
	if got["music_write_tags"] != false || got["music_extras"] != ".pdf .jpg .lrc" {
		t.Errorf("saved %v / %q", got["music_write_tags"], got["music_extras"])
	}
	if got["music_transcode"] != true || got["music_transcode_format"] != "mp3" || got["music_transcode_quality"] != "320" || got["music_transcode_lossless_only"] != false {
		t.Errorf("transcode saved = %#v", got)
	}
}

func TestMusicTranscodeSettingsValidation(t *testing.T) {
	a := settingsAPI(t)
	for _, body := range []string{
		`{"music_transcode_format":"aac"}`,
		`{"music_transcode_quality":"v1"}`,
		`{"music_transcode_quality":"flac"}`,
	} {
		if w := putSettingsAs(t, a, auth.RoleManager, body); w.Code != http.StatusBadRequest {
			t.Errorf("PUT %s: HTTP %d %s", body, w.Code, w.Body.String())
		}
	}
}
