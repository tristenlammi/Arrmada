package subtitles

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// fakeOpenSubtitles answers the three calls the credentials Test makes. Only the key
// "good" and the account me/secret are accepted.
func fakeOpenSubtitles(t *testing.T) (*httptest.Server, *atomic.Value) {
	t.Helper()
	var last atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		last.Store(r.Header.Get("Api-Key"))
		if r.Header.Get("Api-Key") != "good" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		switch r.URL.Path {
		case "/infos/formats":
			_, _ = io.WriteString(w, `{"data":{"output_formats":["srt"]}}`)
		case "/login":
			var b map[string]string
			_ = json.NewDecoder(r.Body).Decode(&b)
			if b["username"] != "me" || b["password"] != "secret" {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = io.WriteString(w, `{"message":"Invalid username/password","status":401}`)
				return
			}
			_, _ = io.WriteString(w, `{"token":"tok","status":200}`)
		case "/infos/user":
			if r.Header.Get("Authorization") != "Bearer tok" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			_, _ = io.WriteString(w, `{"data":{"allowed_downloads":20,"remaining_downloads":17}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &last
}

func TestOpenSubtitlesVerify(t *testing.T) {
	srv, last := fakeOpenSubtitles(t)
	ctx := context.Background()
	mk := func(key, user, pass string) *OpenSubtitles {
		o := NewOpenSubtitles(key, user, pass)
		o.baseURL = srv.URL
		return o
	}

	got, err := mk("good", "me", "secret").Verify(ctx, "")
	if err != nil || got != "OK: signed in as me, 17 downloads left today." {
		t.Errorf("full account: %q, %v", got, err)
	}

	// Missing credentials are named; search alone still works.
	_, err = mk("good", "me", "").Verify(ctx, "")
	if err == nil || !strings.Contains(err.Error(), "needs the OpenSubtitles password") {
		t.Errorf("no password: %v", err)
	}
	_, err = mk("good", "", "").Verify(ctx, "")
	if err == nil || !strings.Contains(err.Error(), "username and password") {
		t.Errorf("no account: %v", err)
	}

	if _, err := mk("good", "me", "wrong").Verify(ctx, ""); err == nil || !strings.Contains(err.Error(), "rejected the username or password") {
		t.Errorf("wrong password: %v", err)
	}

	// A candidate key is used in place of the saved one, and the cached token is untouched.
	o := mk("bad-saved", "me", "secret")
	if _, err := o.Verify(ctx, "good"); err != nil {
		t.Errorf("good candidate over a bad saved key: %v", err)
	}
	if last.Load() != "good" {
		t.Errorf("last request carried key %q, want the candidate", last.Load())
	}
	if o.token != "" {
		t.Errorf("Verify cached a download token: %q", o.token)
	}
	if _, err := o.Verify(ctx, ""); err == nil || !strings.Contains(err.Error(), "rejected the API key") {
		t.Errorf("bad saved key: %v", err)
	}
}
