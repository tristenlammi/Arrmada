package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/tristenlammi/arrmada/internal/auth"
)

// Declining takes an optional reason (at most 280 characters); the requester sees the
// reason but not who declined; asking again without a note is 409 needs_note with the
// reason, and with one it goes back to pending flagged as a re-request.
func TestRequestDecisionsAPI(t *testing.T) {
	s := requestsServer(t)
	alice, aliceCookie := s.user(t, "alice@example.com", auth.RoleRequester)
	_, admin := s.user(t, "admin@example.com", auth.RoleAdmin)
	id := addRequest(t, s, 1, "Tron", "pending", alice.ID, alice.Username)
	path := fmt.Sprintf("/api/v1/requests/%d/decline", id)

	if rec := s.doJSON("POST", path, admin, `{"reason":"`+strings.Repeat("x", 281)+`"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("a 281-character reason: HTTP %d, want 400", rec.Code)
	}
	if rec := s.doJSON("POST", path, admin, `{"reason":" Already on Netflix "}`); rec.Code != http.StatusOK {
		t.Fatalf("decline: HTTP %d %s", rec.Code, rec.Body)
	}
	rec := s.do("GET", fmt.Sprintf("/api/v1/requests/%d", id), aliceCookie)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"decline_reason":"Already on Netflix"`) || strings.Contains(rec.Body.String(), "decided_by_name") {
		t.Errorf("the requester's view: HTTP %d %s", rec.Code, rec.Body)
	}
	if rec := s.do("GET", fmt.Sprintf("/api/v1/requests/%d", id), admin); !strings.Contains(rec.Body.String(), `"decided_by_name":"admin@example.com"`) {
		t.Errorf("staff don't see who declined: %s", rec.Body)
	}

	body := `{"media_type":"movie","tmdb_id":1,"title":"Tron"}`
	rec = s.doJSON("POST", "/api/v1/requests", aliceCookie, body)
	var refused struct {
		Code          string `json:"code"`
		DeclineReason string `json:"decline_reason"`
	}
	if rec.Code != http.StatusConflict || json.Unmarshal(rec.Body.Bytes(), &refused) != nil || refused.Code != "needs_note" || refused.DeclineReason != "Already on Netflix" {
		t.Fatalf("re-request without a note: HTTP %d %s", rec.Code, rec.Body)
	}
	rec = s.doJSON("POST", "/api/v1/requests", aliceCookie, `{"media_type":"movie","tmdb_id":1,"title":"Tron","note":"It's not on Netflix any more"}`)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"rerequest":1`) || !strings.Contains(rec.Body.String(), `"status":"pending"`) {
		t.Errorf("re-request with a note: HTTP %d %s", rec.Code, rec.Body)
	}

	// Someone else asking for it now follows Alice's request: their answer names neither
	// her, her note, nor who decided it last time.
	_, bobCookie := s.user(t, "bob@example.com", auth.RoleRequester)
	rec = s.doJSON("POST", "/api/v1/requests", bobCookie, body)
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "alice") || strings.Contains(rec.Body.String(), "Netflix any more") ||
		strings.Contains(rec.Body.String(), "decided_by_name") || !strings.Contains(rec.Body.String(), `"relation":"subscriber"`) {
		t.Errorf("a follower's create answer: HTTP %d %s", rec.Code, rec.Body)
	}

	// Bulk declines carry the reason too, bounded the same way.
	if rec := s.doJSON("POST", "/api/v1/requests/bulk", admin, fmt.Sprintf(`{"action":"decline","ids":[%d],"reason":"%s"}`, id, strings.Repeat("y", 281))); rec.Code != http.StatusBadRequest {
		t.Errorf("bulk with a long reason: HTTP %d", rec.Code)
	}
	if rec := s.doJSON("POST", "/api/v1/requests/bulk", admin, fmt.Sprintf(`{"action":"decline","ids":[%d],"reason":"Still no"}`, id)); rec.Code != http.StatusOK {
		t.Fatalf("bulk decline: HTTP %d %s", rec.Code, rec.Body)
	}
	if rec := s.do("GET", fmt.Sprintf("/api/v1/requests/%d", id), aliceCookie); !strings.Contains(rec.Body.String(), `"decline_reason":"Still no"`) {
		t.Errorf("after the bulk decline: %s", rec.Body)
	}
}
