package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/tristenlammi/arrmada/internal/auth"
	"github.com/tristenlammi/arrmada/internal/eventbus"
	"github.com/tristenlammi/arrmada/internal/notify"
	"github.com/tristenlammi/arrmada/internal/requests"
)

// sentRecorder stands in for apprise: it records every URL a message went to, and fails
// with the URL quoted (as apprise -v can) when fail is set.
type sentRecorder struct {
	mu   sync.Mutex
	urls []string
	fail bool
}

func (s *sentRecorder) send(_ context.Context, url, _, _ string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.urls = append(s.urls, url)
	if s.fail {
		return errors.New("apprise: exit status 1 (Unparseable URL " + url + ")")
	}
	return nil
}

func (s *sentRecorder) sent() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.urls...)
}

func newNotifyServer(t *testing.T) (*routeServer, *sentRecorder) {
	t.Helper()
	rec := &sentRecorder{}
	s := newRouteServer(t, func(d *Deps) {
		log := slog.New(slog.NewTextHandler(io.Discard, nil))
		d.Notify = notify.NewService(d.Store.DB(), eventbus.New(log), log)
		d.Notify.SetTransport(rec.send)
	})
	return s, rec
}

func listNotifications(t *testing.T, s *routeServer, c *http.Cookie) []map[string]any {
	t.Helper()
	rec := s.do("GET", "/api/v1/notifications", c)
	if rec.Code != http.StatusOK {
		t.Fatalf("list: HTTP %d %s", rec.Code, rec.Body)
	}
	var out struct {
		Notifications []map[string]any `json:"notifications"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out.Notifications
}

// A typo or an option-looking string is refused on save with the validator's reason,
// instead of being stored and failing when an alert is due.
func TestCreateNotificationRejectsInvalidURL(t *testing.T) {
	s, _ := newNotifyServer(t)
	_, admin := s.user(t, "admin@example.com", auth.RoleAdmin)

	for _, url := range []string{"discord//missing-colon", "-config=/etc/x", "http://evil", "   ", "ntfy://a ntfy://b"} {
		rec := s.doJSON("POST", "/api/v1/notifications", admin, `{"name":"x","url":"`+url+`","enabled":true}`)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("create %q: HTTP %d, want 400: %s", url, rec.Code, rec.Body)
		}
	}
	for _, url := range []string{"discord://123/abc", "ntfys://ntfy.example.com/topic", "tgram://123456789:AAHdqTcv/1234"} {
		rec := s.doJSON("POST", "/api/v1/notifications", admin, `{"name":"ok","url":" `+url+` ","enabled":true}`)
		if rec.Code != http.StatusCreated {
			t.Errorf("create %q: HTTP %d, want 201: %s", url, rec.Code, rec.Body)
		}
	}
	if rec := s.doJSON("PUT", "/api/v1/notifications/1", admin, `{"name":"ok","url":"discord//missing-colon","enabled":true}`); rec.Code != http.StatusBadRequest {
		t.Errorf("update with a bad URL: HTTP %d, want 400: %s", rec.Code, rec.Body)
	}
}

// No answer from any alert route carries a saved token or password, including a failed
// Test whose apprise output quoted the URL.
func TestListNotificationsRedactsSecrets(t *testing.T) {
	s, sent := newNotifyServer(t)
	_, admin := s.user(t, "admin@example.com", auth.RoleAdmin)
	_, mgr := s.user(t, "mgr@example.com", auth.RoleManager)
	sent.fail = true

	var bodies []string
	keep := func(what string, code int, body string, want int) {
		t.Helper()
		if code != want {
			t.Fatalf("%s: HTTP %d, want %d: %s", what, code, want, body)
		}
		bodies = append(bodies, what+": "+body)
	}
	rec := s.doJSON("POST", "/api/v1/notifications", admin, `{"name":"Discord","url":"discord://123456/SECRETWEBHOOKTOKEN","enabled":true}`)
	keep("create discord", rec.Code, rec.Body.String(), http.StatusCreated)
	rec = s.doJSON("POST", "/api/v1/notifications", admin, `{"name":"Mail","url":"mailtos://someone:hunter2@smtp.example.com","enabled":true}`)
	keep("create mail", rec.Code, rec.Body.String(), http.StatusCreated)
	rec = s.doJSON("PUT", "/api/v1/notifications/1", admin, `{"name":"Discord 2","enabled":true}`)
	keep("update", rec.Code, rec.Body.String(), http.StatusOK)
	for _, c := range []*http.Cookie{admin, mgr} {
		rec = s.do("GET", "/api/v1/notifications", c)
		keep("list", rec.Code, rec.Body.String(), http.StatusOK)
	}
	for _, id := range []string{"1", "2"} {
		rec = s.doJSON("POST", "/api/v1/notifications/"+id+"/test", admin, ``)
		keep("test "+id, rec.Code, rec.Body.String(), http.StatusOK)
	}
	rec = s.doJSON("POST", "/api/v1/notifications/test", admin, `{"name":"x","url":"discord://999999/ANOTHERSECRET"}`)
	keep("test unsaved", rec.Code, rec.Body.String(), http.StatusOK)

	all := strings.Join(bodies, "\n")
	for _, secret := range []string{"SECRETWEBHOOKTOKEN", "SECRET", "hunter2", "someone", "123456/", "ANOTHERSECRET", `"url":`} {
		if strings.Contains(all, secret) {
			t.Errorf("an API answer contains %q:\n%s", secret, all)
		}
	}
	if !strings.Contains(all, "smtp.example.com") || !strings.Contains(all, `"url_set":true`) {
		t.Errorf("the list should carry a hint and url_set:\n%s", all)
	}
}

// Renaming a connection without retyping its URL keeps the stored URL, and Test on the
// saved card sends through it.
func TestUpdateKeepsStoredURLWhenOmitted(t *testing.T) {
	s, _ := newNotifyServer(t)
	_, admin := s.user(t, "admin@example.com", auth.RoleAdmin)
	if rec := s.doJSON("POST", "/api/v1/notifications", admin, `{"name":"Discord","url":"discord://123/tok","enabled":true}`); rec.Code != http.StatusCreated {
		t.Fatalf("create: HTTP %d %s", rec.Code, rec.Body)
	}
	for _, body := range []string{`{"name":"Renamed","enabled":true}`, `{"name":"Renamed again","url":"","enabled":false}`, `{"name":"Again","url":"   ","enabled":true}`} {
		if rec := s.doJSON("PUT", "/api/v1/notifications/1", admin, body); rec.Code != http.StatusOK {
			t.Fatalf("update %s: HTTP %d %s", body, rec.Code, rec.Body)
		}
	}
	c, err := s.deps.Notify.Get(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if c.URL != "discord://123/tok" || c.Name != "Again" {
		t.Fatalf("stored connection = %q / %q, want the original URL kept", c.Name, c.URL)
	}
	if rec := s.doJSON("PUT", "/api/v1/notifications/1", admin, `{"name":"New","url":"ntfy://newtopic","enabled":true}`); rec.Code != http.StatusOK {
		t.Fatalf("update with a new URL: HTTP %d %s", rec.Code, rec.Body)
	}
	if c, _ = s.deps.Notify.Get(context.Background(), 1); c.URL != "ntfy://newtopic" {
		t.Fatalf("new URL not stored: %q", c.URL)
	}
}

func TestTestNotificationByIDUsesStoredURL(t *testing.T) {
	s, sent := newNotifyServer(t)
	_, admin := s.user(t, "admin@example.com", auth.RoleAdmin)
	if rec := s.doJSON("POST", "/api/v1/notifications", admin, `{"name":"Discord","url":"discord://123/tok","enabled":true}`); rec.Code != http.StatusCreated {
		t.Fatalf("create: HTTP %d %s", rec.Code, rec.Body)
	}
	rec := s.doJSON("POST", "/api/v1/notifications/1/test", admin, ``)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"ok":true`) {
		t.Fatalf("test: HTTP %d %s", rec.Code, rec.Body)
	}
	if got := sent.sent(); len(got) != 1 || got[0] != "discord://123/tok" {
		t.Fatalf("sent to %v, want the stored URL", got)
	}
	if rec := s.doJSON("POST", "/api/v1/notifications/99/test", admin, ``); rec.Code != http.StatusNotFound {
		t.Errorf("unknown id: HTTP %d, want 404", rec.Code)
	}
}

type mapResolver map[string]string

func (r mapResolver) LookupIPAddr(_ context.Context, host string) ([]net.IPAddr, error) {
	ip, ok := r[host]
	if !ok {
		return nil, errors.New("no such host")
	}
	return []net.IPAddr{{IP: net.ParseIP(ip)}}, nil
}

// A requester can't aim their personal Apprise URL at the local network; staff keep the
// ordinary rules; and neither ever gets the saved URL back.
func TestMyAppriseKeepsRequestersOffInternalHosts(t *testing.T) {
	s := newRouteServer(t, func(d *Deps) {
		d.Requests = requests.NewService(d.Store.DB(), nil, nil, nil, nil, nil, nil, "", d.Log)
		d.Resolver = mapResolver{"internal.lan": "192.168.1.5", "push.example.com": "93.184.216.34", "qbittorrent": "172.18.0.4"}
	})
	_, kid := s.user(t, "kid@example.com", auth.RoleRequester)
	_, mgr := s.user(t, "mgr@example.com", auth.RoleManager)

	for _, url := range []string{"json://10.0.0.5/hook", "gotify://192.168.1.10/token", "ntfys://qbittorrent/topic", "gotify://internal.lan/tok", "mailtos://a:b@push.example.com?smtp=10.0.0.5"} {
		if rec := s.doJSON("PUT", "/api/v1/me/apprise", kid, `{"url":"`+url+`"}`); rec.Code != http.StatusBadRequest {
			t.Errorf("requester %q: HTTP %d, want 400: %s", url, rec.Code, rec.Body)
		}
	}
	var bodies []string
	for _, tc := range []struct {
		c   *http.Cookie
		url string
	}{{kid, "discord://123456/SECRETWEBHOOKTOKEN"}, {kid, "ntfy://mytopic"}, {mgr, "json://internal.lan/SECRETHOOK"}} {
		rec := s.doJSON("PUT", "/api/v1/me/apprise", tc.c, `{"url":"`+tc.url+`"}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("%q: HTTP %d %s", tc.url, rec.Code, rec.Body)
		}
		bodies = append(bodies, rec.Body.String(), s.do("GET", "/api/v1/me/apprise", tc.c).Body.String())
	}
	all := strings.Join(bodies, "\n")
	for _, secret := range []string{"SECRETWEBHOOKTOKEN", "mytopic", "SECRETHOOK", `"url":`} {
		if strings.Contains(all, secret) {
			t.Errorf("a /me/apprise answer contains %q:\n%s", secret, all)
		}
	}
	if !strings.Contains(all, `"set":true`) {
		t.Errorf("answers should say a URL is set:\n%s", all)
	}
}

// A push connection always targets the admin who made it, whatever the body says, takes
// no URL, and can't be turned into (or out of) an Apprise connection.
func TestCreatePushConnectionTargetsSessionUser(t *testing.T) {
	s, _ := newNotifyServer(t)
	admin, cookie := s.user(t, "admin@example.com", auth.RoleAdmin)
	other, _ := s.user(t, "other@example.com", auth.RoleAdmin)

	body := `{"name":"My phone","kind":"webpush","enabled":true,"config":{"user_id":` + strconv.FormatInt(other.ID, 10) + `}}`
	if rec := s.doJSON("POST", "/api/v1/notifications", cookie, body); rec.Code != http.StatusCreated {
		t.Fatalf("create: HTTP %d %s", rec.Code, rec.Body)
	}
	c, err := s.deps.Notify.Get(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if c.Kind != notify.KindWebPush || c.PushUserID() != admin.ID || c.URL != "" {
		t.Fatalf("stored %+v (user %d), want a push connection for the admin %d", c, c.PushUserID(), admin.ID)
	}
	for _, tc := range []struct{ method, path, body string }{
		{"POST", "/api/v1/notifications", `{"name":"x","kind":"webpush","url":"discord://1/2"}`},
		{"PUT", "/api/v1/notifications/1", `{"name":"x","kind":"webpush","url":"discord://1/2"}`},
		{"PUT", "/api/v1/notifications/1", `{"name":"x","kind":"","url":"discord://1/2"}`},
	} {
		if rec := s.doJSON(tc.method, tc.path, cookie, tc.body); rec.Code != http.StatusBadRequest {
			t.Errorf("%s %s %s: HTTP %d, want 400", tc.method, tc.path, tc.body, rec.Code)
		}
	}
	if rec := s.doJSON("PUT", "/api/v1/notifications/1", cookie, `{"name":"Phone","kind":"webpush","enabled":true,"events":["release.grabbed"]}`); rec.Code != http.StatusOK {
		t.Fatalf("rename: HTTP %d %s", rec.Code, rec.Body)
	}
	if c, _ = s.deps.Notify.Get(context.Background(), 1); c.Name != "Phone" || c.PushUserID() != admin.ID {
		t.Errorf("after rename: %+v", c)
	}
}

// Managers see the redacted list; only admins change connections or make them send.
func TestNotificationWritesAreAdminOnly(t *testing.T) {
	s, sent := newNotifyServer(t)
	_, admin := s.user(t, "admin@example.com", auth.RoleAdmin)
	_, mgr := s.user(t, "mgr@example.com", auth.RoleManager)
	if rec := s.doJSON("POST", "/api/v1/notifications", admin, `{"name":"Discord","url":"discord://123/tok","enabled":true}`); rec.Code != http.StatusCreated {
		t.Fatalf("create: HTTP %d %s", rec.Code, rec.Body)
	}
	for _, tc := range []struct{ method, path, body string }{
		{"POST", "/api/v1/notifications", `{"name":"x","url":"discord://1/2"}`},
		{"PUT", "/api/v1/notifications/1", `{"name":"x"}`},
		{"DELETE", "/api/v1/notifications/1", ``},
		{"POST", "/api/v1/notifications/test", `{"name":"x","url":"discord://1/2"}`},
		{"POST", "/api/v1/notifications/1/test", ``},
	} {
		if rec := s.doJSON(tc.method, tc.path, mgr, tc.body); rec.Code != http.StatusForbidden {
			t.Errorf("manager %s %s: HTTP %d, want 403", tc.method, tc.path, rec.Code)
		}
	}
	if got := sent.sent(); len(got) != 0 {
		t.Errorf("a manager's request sent a message: %v", got)
	}
	if list := listNotifications(t, s, mgr); len(list) != 1 || list[0]["name"] != "Discord" {
		t.Errorf("manager list = %v", list)
	}
}
