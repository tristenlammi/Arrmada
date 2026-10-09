package metadata

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Validate tells a refused key (ErrInvalidKey) from an accepted one, from no key at all,
// and from TMDB being unreachable — and the key never appears in an error.
func TestTMDBValidate(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/configuration" {
			http.NotFound(w, r)
			return
		}
		if r.URL.Query().Get("api_key") != "good" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"images":{}}`))
	}))
	defer srv.Close()

	with := func(key string) error {
		tm := NewTMDB(key)
		tm.base = srv.URL
		return tm.Validate(context.Background())
	}
	if err := with("good"); err != nil {
		t.Errorf("good key: %v", err)
	}
	if err := with("bad"); !errors.Is(err, ErrInvalidKey) {
		t.Errorf("bad key: %v", err)
	}
	if err := with(""); !errors.Is(err, ErrNotConfigured) {
		t.Errorf("no key: %v", err)
	}

	tm := NewTMDB("secret-key-123")
	tm.base = "http://127.0.0.1:1" // nothing listens there
	err := tm.Validate(context.Background())
	if err == nil || errors.Is(err, ErrInvalidKey) {
		t.Errorf("unreachable: %v", err)
	}
	if err != nil && strings.Contains(err.Error(), "secret-key-123") {
		t.Errorf("the key leaked into the error: %v", err)
	}
}
