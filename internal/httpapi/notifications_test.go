package httpapi

import (
	"io"
	"log/slog"
	"net/http"
	"testing"

	"github.com/tristenlammi/arrmada/internal/auth"
	"github.com/tristenlammi/arrmada/internal/eventbus"
	"github.com/tristenlammi/arrmada/internal/notify"
)

func newNotifyServer(t *testing.T) *routeServer {
	t.Helper()
	return newRouteServer(t, func(d *Deps) {
		log := slog.New(slog.NewTextHandler(io.Discard, nil))
		d.Notify = notify.NewService(d.Store.DB(), eventbus.New(log), log)
	})
}

// A typo or an option-looking string is refused on save with the validator's reason,
// instead of being stored and failing when an alert is due.
func TestCreateNotificationRejectsInvalidURL(t *testing.T) {
	s := newNotifyServer(t)
	_, admin := s.user(t, "admin@example.com", auth.RoleAdmin)

	for _, url := range []string{"discord//missing-colon", "-config=/etc/x", "http://evil", "   "} {
		rec := s.doJSON("POST", "/api/v1/notifications", admin, `{"name":"x","url":"`+url+`","enabled":true}`)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("create %q: HTTP %d, want 400: %s", url, rec.Code, rec.Body)
		}
	}
	for _, url := range []string{"discord://123/abc", "ntfys://ntfy.example.com/topic"} {
		rec := s.doJSON("POST", "/api/v1/notifications", admin, `{"name":"ok","url":" `+url+` ","enabled":true}`)
		if rec.Code != http.StatusCreated {
			t.Errorf("create %q: HTTP %d, want 201: %s", url, rec.Code, rec.Body)
		}
	}
}

func TestUpdateNotificationRejectsInvalidURL(t *testing.T) {
	s := newNotifyServer(t)
	_, admin := s.user(t, "admin@example.com", auth.RoleAdmin)
	if rec := s.doJSON("POST", "/api/v1/notifications", admin, `{"name":"ok","url":"discord://123/abc","enabled":true}`); rec.Code != http.StatusCreated {
		t.Fatalf("create: HTTP %d %s", rec.Code, rec.Body)
	}
	rec := s.doJSON("PUT", "/api/v1/notifications/1", admin, `{"name":"ok","url":"discord//missing-colon","enabled":true}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("update: HTTP %d, want 400: %s", rec.Code, rec.Body)
	}
}
