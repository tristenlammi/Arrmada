package httpapi

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/tristenlammi/arrmada/internal/auth"
)

// The server normalises the email, so a capitalised one is stored lowercased and a
// second spelling of the same address is a 409.
func TestCreateUserNormalisesEmail(t *testing.T) {
	s := newRouteServer(t, nil)
	_, admin := s.user(t, "owner@example.com", auth.RoleAdmin)

	rec := s.doBody("POST", "/api/v1/users", `{"email":" Mum@Gmail.com ","password":"supersecret","role":"readonly"}`, admin)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: HTTP %d %s", rec.Code, rec.Body)
	}
	var u auth.User
	_ = json.Unmarshal(rec.Body.Bytes(), &u)
	if u.Username != "mum@gmail.com" || u.Role != auth.RoleReadonly {
		t.Errorf("created %+v, want mum@gmail.com as readonly", u)
	}
	rec = s.doBody("POST", "/api/v1/users", `{"email":"MUM@gmail.com","password":"supersecret"}`, admin)
	if rec.Code != http.StatusConflict {
		t.Errorf("case duplicate: HTTP %d %s, want 409", rec.Code, rec.Body)
	}
}
