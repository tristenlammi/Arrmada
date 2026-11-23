package jellyfin

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
)

// The same client speaking to Emby. The fixtures in testdata/emby/ follow the response
// models in Emby's REST API reference (dev.emby.media/reference/RestAPI), not a captured
// server: there's no Emby in the live rig. What each test pins is the difference from
// Jellyfin: the /emby root, the X-Emby-* headers, the Query list routes, Emby's own
// hardware fields, its user image route and watch link, and no Quick Connect.

func (f *fake) emby() *Client {
	return NewFor(ServerEmby, f.srv.URL+"/", "the-api-key", "dev-1", "0.9.1", nil)
}

// embyAuthParts reads an X-Emby-Authorization header ("Emby Client=…, …") into its fields.
func embyAuthParts(t *testing.T, h string) map[string]string {
	t.Helper()
	rest, ok := strings.CutPrefix(h, "Emby ")
	if !ok {
		t.Fatalf("X-Emby-Authorization %q doesn't use the Emby scheme", h)
	}
	return authParts(t, "MediaBrowser "+rest)
}

func TestNormalizeServerKind(t *testing.T) {
	for in, want := range map[string]string{"": ServerJellyfin, "jellyfin": ServerJellyfin, "Emby": ServerEmby, " emby ": ServerEmby, "plex": ServerJellyfin} {
		if got := NormalizeServerKind(in); got != want {
			t.Errorf("NormalizeServerKind(%q) = %q, want %q", in, got, want)
		}
	}
	if ServerLabel("emby") != "Emby" || ServerLabel("") != "Jellyfin" {
		t.Error("labels")
	}
	if New("http://x", "", "", "", nil).IsEmby() || !NewFor("EMBY", "http://x", "", "", "", nil).IsEmby() {
		t.Error("kind")
	}
}

func TestEmbyPrefixAndHeaders(t *testing.T) {
	f := newFake(t)
	f.json("GET /emby/System/Info", "emby/system_info.json")
	si, err := f.emby().SystemInfo(bg)
	if err != nil {
		t.Fatal(err)
	}
	if si.ID != "5D6E0C7B8A9F4E2D9C1B0A3F7E6D5C4B" || si.ServerName != "emby-den" || si.Version != "4.8.10.0" || si.ProductName != "" {
		t.Errorf("system info = %+v", si)
	}
	r, _ := f.last()
	if r.Header.Get("Authorization") != "" {
		t.Errorf("Emby got Jellyfin's Authorization header %q", r.Header.Get("Authorization"))
	}
	if r.Header.Get("X-Emby-Token") != "the-api-key" {
		t.Errorf("X-Emby-Token = %q", r.Header.Get("X-Emby-Token"))
	}
	got := embyAuthParts(t, r.Header.Get("X-Emby-Authorization"))
	for k, v := range map[string]string{"Token": "the-api-key", "Client": "Arrmada", "Device": "Arrmada", "DeviceId": "dev-1", "Version": "0.9.1"} {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
	if strings.Contains(r.URL.RawQuery, "the-api-key") {
		t.Errorf("key in the query: %q", r.URL.RawQuery)
	}

	// An address saved with /emby already on it isn't given a second one.
	c := NewFor(ServerEmby, f.srv.URL+"/emby/", "the-api-key", "dev-1", "", nil)
	if _, err := c.SystemInfo(bg); err != nil {
		t.Fatalf("address ending in /emby: %v", err)
	}
	if r, _ := f.last(); r.URL.Path != "/emby/System/Info" {
		t.Errorf("path = %q", r.URL.Path)
	}

	// Public info needs no key and sends no token header.
	f.json("GET /emby/System/Info/Public", "emby/system_info_public.json")
	if _, err := f.emby().WithToken("").PublicSystemInfo(bg); err != nil {
		t.Fatal(err)
	}
	if r, _ := f.last(); r.Header.Get("X-Emby-Token") != "" || strings.Contains(r.Header.Get("X-Emby-Authorization"), "Token=") {
		t.Errorf("anonymous call carried a token: %v", r.Header)
	}
}

func TestEmbyVirtualFolders(t *testing.T) {
	f := newFake(t)
	f.json("GET /emby/Library/VirtualFolders/Query", "emby/virtual_folders_query.json")
	vfs, err := f.emby().VirtualFolders(bg)
	if err != nil {
		t.Fatal(err)
	}
	if len(vfs) != 2 || vfs[0].Name != "Movies" || vfs[0].CollectionType != "movies" || vfs[0].ItemID != "4" ||
		len(vfs[1].Locations) != 1 || vfs[1].Locations[0] != "/data/tv" {
		t.Errorf("folders = %+v", vfs)
	}

	// An older Emby without the Query route answers the bare list.
	old := newFake(t)
	old.routes["GET /emby/Library/VirtualFolders"] = func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[{"Name":"Movies","Locations":["/m"],"CollectionType":"movies","ItemId":"4"}]`))
	}
	vfs, err = old.emby().VirtualFolders(bg)
	if err != nil || len(vfs) != 1 || vfs[0].Locations[0] != "/m" {
		t.Errorf("fallback = %+v, %v", vfs, err)
	}

	// Any other failure isn't retried on the old route.
	bad := newFake(t)
	bad.status("GET /emby/Library/VirtualFolders/Query", http.StatusUnauthorized)
	if _, err := bad.emby().VirtualFolders(bg); !errors.Is(err, ErrUnauthorized) {
		t.Errorf("401 = %v", err)
	}
	if len(bad.seen) != 1 {
		t.Errorf("%d requests, want 1", len(bad.seen))
	}
}

func TestEmbyItemsAndProviderIDs(t *testing.T) {
	f := newFake(t)
	f.json("GET /emby/Items", "emby/items_index.json")
	items, err := f.emby().AllItems(bg, ItemsQuery{IncludeItemTypes: []string{TypeMovie, TypeSeries}, Recursive: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("items = %+v", items)
	}
	m, s := items[0], items[1]
	if m.ID != "1201" || m.ProviderIDs.TMDB != 603 || m.ProviderIDs.IMDB != "tt0133093" || m.DateCreated.IsZero() || m.Type != TypeMovie {
		t.Errorf("movie = %+v", m)
	}
	if s.ProviderIDs.TVDB != 371980 || s.ProviderIDs.TMDB != 95396 || s.Path != "/data/tv/Severance" {
		t.Errorf("show = %+v", s)
	}
}

func TestEmbySessionsHardware(t *testing.T) {
	f := newFake(t)
	f.json("GET /emby/Sessions", "emby/sessions_transcode.json")
	ss, err := f.emby().Sessions(bg, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(ss) != 2 || !ss[0].Playing() || ss[1].Playing() {
		t.Fatalf("sessions = %+v", ss)
	}
	s := ss[0]
	ti := s.TranscodingInfo
	if ti == nil || ti.HardwareAccelerationType != "QuickSync" || ti.Bitrate != 8_000_000 || ti.VideoCodec != "h264" || ti.Height != 1080 {
		t.Errorf("transcode = %+v", ti)
	}
	if s.PlayState.PlayMethod != "Transcode" || s.PlayState.MediaSourceID != "mediasource_1201" || s.Client != "Emby Web" || s.RemoteEndPoint != "203.0.113.7" {
		t.Errorf("session = %+v", s)
	}
	if v := s.NowPlayingItem.VideoStream(); v == nil || v.BitDepth != 10 || v.Codec != "hevc" {
		t.Errorf("video stream = %+v", v)
	}

	// Software, and a hardware decoder with a software encoder.
	for _, c := range []struct {
		ti   TranscodingInfo
		want string
	}{
		{TranscodingInfo{}, "none"},
		{TranscodingInfo{VideoDecoderIsHardware: true, VideoDecoderHwAccel: "VAAPI"}, "VAAPI"},
		{TranscodingInfo{VideoEncoderIsHardware: true}, "hardware"},
	} {
		if got := c.ti.embyHardware(); got != c.want {
			t.Errorf("%+v → %q, want %q", c.ti, got, c.want)
		}
	}

	// A Jellyfin client never fills the field in from Emby's.
	jf := newFake(t)
	jf.routes["GET /Sessions"] = func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[{"Id":"x","NowPlayingItem":{"Id":"i","Type":"Movie"},"TranscodingInfo":{"VideoEncoderIsHardware":true,"VideoEncoderHwAccel":"qsv"}}]`))
	}
	js, err := jf.client().Sessions(bg, 0)
	if err != nil || js[0].TranscodingInfo.HardwareAccelerationType != "" {
		t.Errorf("jellyfin session = %+v, %v", js, err)
	}
}

func TestEmbyUsers(t *testing.T) {
	f := newFake(t)
	f.json("GET /emby/Users/Query", "emby/users_query.json")
	us, err := f.emby().Users(bg)
	if err != nil {
		t.Fatal(err)
	}
	if len(us) != 2 || !us[0].Policy.IsAdministrator || us[1].Name != "sam" || us[1].PrimaryImageTag != "abc123" || us[1].ID != "a1b2c3d4e5f60718293a4b5c6d7e8f90" {
		t.Errorf("users = %+v", us)
	}

	old := newFake(t)
	old.routes["GET /emby/Users"] = func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[{"Id":"a1b2c3d4e5f60718293a4b5c6d7e8f90","Name":"sam","Policy":{"IsAdministrator":true}}]`))
	}
	us, err = old.emby().Users(bg)
	if err != nil || len(us) != 1 || !us[0].Policy.IsAdministrator {
		t.Errorf("fallback = %+v, %v", us, err)
	}
}

func TestEmbySignIn(t *testing.T) {
	f := newFake(t)
	f.json("POST /emby/Users/AuthenticateByName", "emby/authenticate_by_name.json")
	f.status("POST /emby/Sessions/Logout", http.StatusNoContent)
	c := f.emby()
	res, err := c.AuthenticateByName(bg, "sam", "pa55word")
	if err != nil {
		t.Fatal(err)
	}
	if res.AccessToken != "REDACTED-user-token" || res.User.Name != "sam" || res.User.Policy.IsAdministrator {
		t.Errorf("auth = %+v", res)
	}
	r, body := f.last()
	if r.Header.Get("X-Emby-Token") != "" || strings.Contains(r.Header.Get("X-Emby-Authorization"), "Token=") {
		t.Errorf("sign-in presented the API key: %v", r.Header)
	}
	var sent map[string]string
	if err := json.Unmarshal([]byte(body), &sent); err != nil || sent["Username"] != "sam" || sent["Pw"] != "pa55word" {
		t.Errorf("body = %s", body)
	}

	if err := c.WithToken(res.AccessToken).Logout(bg); err != nil {
		t.Fatal(err)
	}
	if r, _ := f.last(); r.URL.Path != "/emby/Sessions/Logout" || r.Header.Get("X-Emby-Token") != "REDACTED-user-token" {
		t.Errorf("logout = %s %v", r.URL.Path, r.Header)
	}
}

func TestEmbyHasNoQuickConnect(t *testing.T) {
	f := newFake(t)
	c := f.emby()
	if on, err := c.QuickConnectEnabled(bg); on || err != nil {
		t.Errorf("enabled = %v, %v", on, err)
	}
	if _, err := c.QuickConnectInitiate(bg); !errors.Is(err, ErrUnsupported) {
		t.Errorf("initiate = %v", err)
	}
	if _, err := c.QuickConnectConnect(bg, "s"); !errors.Is(err, ErrUnsupported) {
		t.Errorf("connect = %v", err)
	}
	if _, err := c.AuthenticateWithQuickConnect(bg, "s"); !errors.Is(err, ErrUnsupported) {
		t.Errorf("authenticate = %v", err)
	}
	if err := c.QuickConnectAuthorize(bg, "123456"); !errors.Is(err, ErrUnsupported) {
		t.Errorf("authorize = %v", err)
	}
	if len(f.seen) != 0 {
		t.Errorf("%d requests reached the server", len(f.seen))
	}
}

func TestEmbyImages(t *testing.T) {
	f := newFake(t)
	png := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte("png"))
	}
	f.routes["GET /emby/Items/1201/Images/Primary"] = png
	f.routes["GET /emby/Users/a1b2c3d4e5f60718293a4b5c6d7e8f90/Images/Primary"] = png
	c := f.emby()
	resp, err := c.Image(bg, "1201", ImagePrimary, 300)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if r, _ := f.last(); r.Header.Get("X-Emby-Token") != "the-api-key" || r.URL.Query().Get("maxWidth") != "300" {
		t.Errorf("image request = %v %v", r.URL, r.Header)
	}
	resp, err = c.UserImage(bg, "a1b2c3d4e5f60718293a4b5c6d7e8f90")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	for _, bad := range []string{"", "12a", "../System/Info", "123456789012345678901"} {
		if _, err := c.Image(bg, bad, ImagePrimary, 0); err == nil {
			t.Errorf("Image(%q) was sent", bad)
		}
	}
	// A Jellyfin client still accepts only GUIDs.
	if _, err := f.client().Image(bg, "1201", ImagePrimary, 0); err == nil {
		t.Error("jellyfin client accepted a numeric id")
	}
}

func TestEmbyWatchURLAndRefresh(t *testing.T) {
	got := WatchURLFor(ServerEmby, "https://emby.example.com/", "5D6E0C7B8A9F4E2D9C1B0A3F7E6D5C4B", "1201")
	if got != "https://emby.example.com/web/index.html#!/item?id=1201&serverId=5d6e0c7b8a9f4e2d9c1b0a3f7e6d5c4b" {
		t.Errorf("emby watch url = %q", got)
	}
	if WatchURLFor(ServerJellyfin, "https://jf.example.com", "abc", "def") != WatchURL("https://jf.example.com", "abc", "def") {
		t.Error("jellyfin watch url changed")
	}
	if WatchURLFor(ServerEmby, "", "x", "1") != "" || WatchURLFor(ServerEmby, "http://e", "x", "") != "" {
		t.Error("blank inputs make no link")
	}

	f := newFake(t)
	f.status("POST /emby/Library/Media/Updated", http.StatusNoContent)
	f.status("POST /emby/Items/4/Refresh", http.StatusNoContent)
	c := f.emby()
	if err := c.LibraryMediaUpdated(bg, []MediaUpdate{{Path: "/data/movies/Heat (1995)"}}); err != nil {
		t.Fatal(err)
	}
	if _, body := f.last(); body != `{"Updates":[{"Path":"/data/movies/Heat (1995)","UpdateType":"Modified"}]}` {
		t.Errorf("update body = %s", body)
	}
	if err := c.RefreshItem(bg, "4", false); err != nil {
		t.Fatal(err)
	}
	if r, _ := f.last(); r.URL.Query().Has("regenerateTrickplay") || r.URL.Query().Get("recursive") != "true" {
		t.Errorf("refresh query = %q", r.URL.RawQuery)
	}
}
