package httpapi

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/tristenlammi/arrmada/internal/auth"
)

// A password change needs the current password; wrong guesses are throttled per account;
// the right one changes it, ends the other devices and keeps this one.
func TestChangePasswordRequiresCurrent(t *testing.T) {
	s := newRouteServer(t, nil)
	u, here := s.user(t, "kid@example.com", auth.RoleRequester)
	phoneTok, _, _ := s.auth.CreateSession(context.Background(), u.ID)
	phone := &http.Cookie{Name: sessionCookieName, Value: phoneTok}

	if rec := s.doJSON("POST", "/api/v1/me/password", here, `{"current":"","new":"brandnewpass"}`); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "Current password is wrong") {
		t.Fatalf("no current password: HTTP %d %s", rec.Code, rec.Body)
	}
	if rec := s.doJSON("POST", "/api/v1/me/password", here, `{"current":"password123","new":"short"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("weak new password: HTTP %d %s", rec.Code, rec.Body)
	}
	rec := s.doJSON("POST", "/api/v1/me/password", here, `{"current":"password123","new":"brandnewpass"}`)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"signed_out":1`) {
		t.Fatalf("change: HTTP %d %s", rec.Code, rec.Body)
	}
	if rec := s.do("GET", "/api/v1/auth/me", here); rec.Code != http.StatusOK {
		t.Errorf("this device was signed out: HTTP %d", rec.Code)
	}
	if rec := s.do("GET", "/api/v1/auth/me", phone); rec.Code != http.StatusUnauthorized {
		t.Errorf("the other device is still in: HTTP %d", rec.Code)
	}

	// Ten wrong guesses, then even the right password waits.
	for i := 0; i < 10; i++ {
		if rec := s.doJSON("POST", "/api/v1/me/password", here, `{"current":"guess","new":"anotherpass1"}`); rec.Code != http.StatusBadRequest {
			t.Fatalf("guess %d: HTTP %d", i, rec.Code)
		}
	}
	if rec := s.doJSON("POST", "/api/v1/me/password", here, `{"current":"brandnewpass","new":"anotherpass1"}`); rec.Code != http.StatusTooManyRequests {
		t.Errorf("after ten wrong guesses: HTTP %d, want 429", rec.Code)
	}
	// Another account isn't held up by this one's guesses.
	_, aunt := s.user(t, "aunt@example.com", auth.RoleRequester)
	if rec := s.doJSON("POST", "/api/v1/me/password", aunt, `{"current":"password123","new":"auntsnewpass"}`); rec.Code != http.StatusOK {
		t.Errorf("another account: HTTP %d %s", rec.Code, rec.Body)
	}
}

// An account with no password anyone knows (Plex sign-in) sets its first one without a
// current password, after which it has one and can unlink Plex.
func TestChangePasswordPlexOnlyFirstPassword(t *testing.T) {
	s := newRouteServer(t, nil)
	ctx := context.Background()
	u, err := s.auth.FindOrCreatePlexUser(ctx, "4242", "kidplex", auth.RoleRequester, auth.AutoApproval{})
	if err != nil {
		t.Fatal(err)
	}
	tok, _, _ := s.auth.CreateSession(ctx, u.ID)
	c := &http.Cookie{Name: sessionCookieName, Value: tok}
	if rec := s.do("GET", "/api/v1/me/account", c); !strings.Contains(rec.Body.String(), `"password_set":false`) {
		t.Fatalf("account before: %s", rec.Body)
	}
	if rec := s.doJSON("POST", "/api/v1/me/password", c, `{"current":"","new":"firstpassword"}`); rec.Code != http.StatusOK {
		t.Fatalf("first password: HTTP %d %s", rec.Code, rec.Body)
	}
	if rec := s.do("GET", "/api/v1/me/account", c); !strings.Contains(rec.Body.String(), `"password_set":true`) {
		t.Errorf("account after: %s", rec.Body)
	}
	// Now it has one, the next change asks for it.
	if rec := s.doJSON("POST", "/api/v1/me/password", c, `{"current":"","new":"secondpassword"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("second change without the current password: HTTP %d", rec.Code)
	}
	if rec := s.doCookies("DELETE", "/api/v1/me/plex/link", "", c); rec.Code != http.StatusOK {
		t.Errorf("unlink after setting a password: HTTP %d %s", rec.Code, rec.Body)
	}
}

// "Sign out other devices" keeps this one; an admin changing their own password in Edit
// user isn't signed out mid-save either.
func TestRevokeOthersAndOwnEditKeepThisSession(t *testing.T) {
	s := newRouteServer(t, nil)
	ctx := context.Background()
	admin, here := s.user(t, "owner@example.com", auth.RoleAdmin)
	for i := 0; i < 2; i++ {
		_, _, _ = s.auth.CreateSession(ctx, admin.ID)
	}
	rec := s.doJSON("POST", "/api/v1/me/sessions/revoke-others", here, ``)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"signed_out":2`) {
		t.Fatalf("revoke others: HTTP %d %s", rec.Code, rec.Body)
	}
	other, _, _ := s.auth.CreateSession(ctx, admin.ID)
	if rec := s.doJSON("PUT", "/api/v1/users/"+strconv.FormatInt(admin.ID, 10), here, `{"password":"ownnewpassword"}`); rec.Code != http.StatusOK {
		t.Fatalf("own password in Edit user: HTTP %d %s", rec.Code, rec.Body)
	}
	if rec := s.do("GET", "/api/v1/auth/me", here); rec.Code != http.StatusOK {
		t.Errorf("signed out by changing their own password: HTTP %d", rec.Code)
	}
	if rec := s.do("GET", "/api/v1/auth/me", &http.Cookie{Name: sessionCookieName, Value: other}); rec.Code != http.StatusUnauthorized {
		t.Errorf("their other session survived: HTTP %d", rec.Code)
	}
}
