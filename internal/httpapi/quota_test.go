package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tristenlammi/arrmada/internal/auth"
	"github.com/tristenlammi/arrmada/internal/requests"
)

// quotaServer is requestsServer with request limits wired as main wires them: a person's
// own limits, else the global settings.
func quotaServer(t *testing.T) *routeServer {
	t.Helper()
	s := requestsServer(t)
	s.deps.Requests.SetQuotaLimits(func(ctx context.Context, uid int64) (requests.Limits, error) {
		own, err := s.auth.Quota(ctx, uid)
		if err != nil {
			return requests.Limits{}, err
		}
		global := requests.GlobalLimits(func(key string) string { return s.deps.Settings.Get(ctx, key, "") })
		return requests.LimitsFrom(global, own.Movies, own.Seasons, own.Books), nil
	})
	return s
}

// Over the limit answers 429 with what was used and when it frees up; /me/quota says how
// much is left; staff aren't limited; a person's own limit overrides the global one.
func TestCreateReturns429(t *testing.T) {
	s := quotaServer(t)
	_, admin := s.user(t, "admin@example.com", auth.RoleAdmin)
	kid, cookie := s.user(t, "kid@example.com", auth.RoleRequester)
	if rec := s.doJSON("PUT", "/api/v1/settings", admin, `{"request_quota_days":7,"request_quota_books":1}`); rec.Code != http.StatusOK {
		t.Fatalf("settings: HTTP %d %s", rec.Code, rec.Body)
	}
	book := func(c *http.Cookie, key string) *httptest.ResponseRecorder {
		return s.doJSON("POST", "/api/v1/requests", c, fmt.Sprintf(`{"media_type":"book","ol_key":"%s","title":"Book %s","author":"A"}`, key, key))
	}
	if rec := book(cookie, "OL1W"); rec.Code != http.StatusOK {
		t.Fatalf("first book: HTTP %d %s", rec.Code, rec.Body)
	}
	rec := book(cookie, "OL2W")
	var over struct {
		Message  string `json:"message"`
		Kind     string `json:"kind"`
		Limit    int    `json:"limit"`
		Used     int    `json:"used"`
		ResetsAt string `json:"resets_at"`
	}
	if rec.Code != http.StatusTooManyRequests || json.Unmarshal(rec.Body.Bytes(), &over) != nil ||
		over.Kind != "book" || over.Limit != 1 || over.Used != 1 || over.ResetsAt == "" ||
		!strings.HasPrefix(over.Message, "You've used your 1 book request this week. The next one frees up ") {
		t.Fatalf("second book: HTTP %d %s", rec.Code, rec.Body)
	}
	var mine requests.QuotaStatus
	if rec := s.do("GET", "/api/v1/me/quota", cookie); rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &mine) != nil ||
		mine.Book.Limit != 1 || mine.Book.Used != 1 || mine.Movie.Limit != 0 || mine.Days != 7 {
		t.Errorf("/me/quota: HTTP %d %s", rec.Code, rec.Body)
	}
	// Staff are never limited.
	for _, key := range []string{"OL3W", "OL4W"} {
		if rec := book(admin, key); rec.Code != http.StatusOK {
			t.Errorf("staff book %s: HTTP %d %s", key, rec.Code, rec.Body)
		}
	}
	// Their own limit (0: unlimited) beats the global one.
	if rec := s.doJSON("PUT", fmt.Sprintf("/api/v1/users/%d", kid.ID), admin, `{"quota":{"movies":-1,"seasons":-1,"books":0}}`); rec.Code != http.StatusOK {
		t.Fatalf("user quota: HTTP %d %s", rec.Code, rec.Body)
	}
	if rec := book(cookie, "OL2W"); rec.Code != http.StatusOK {
		t.Errorf("with their own unlimited books: HTTP %d %s", rec.Code, rec.Body)
	}
	if rec := s.do("GET", "/api/v1/users", admin); !strings.Contains(rec.Body.String(), `"quota":{"movies":-1,"seasons":-1,"books":0}`) {
		t.Errorf("users list doesn't carry their limits: %s", rec.Body)
	}
	// Only an admin sets the global limits, and only sensible ones.
	_, manager := s.user(t, "manager@example.com", auth.RoleManager)
	if rec := s.doJSON("PUT", "/api/v1/settings", manager, `{"request_quota_movies":5}`); rec.Code != http.StatusForbidden {
		t.Errorf("manager changing limits: HTTP %d", rec.Code)
	}
	if rec := s.doJSON("PUT", "/api/v1/settings", admin, `{"request_quota_days":0}`); rec.Code != http.StatusBadRequest {
		t.Errorf("a 0-day window: HTTP %d", rec.Code)
	}
}
