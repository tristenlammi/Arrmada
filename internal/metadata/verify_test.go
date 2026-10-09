package metadata

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// TMDB's Test reports where images come from on success, and on a 401 says to use the v3
// key — louder when what was pasted looks like a v4 token (a JWT).
func TestTMDBVerifyKey(t *testing.T) {
	var lastKey atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/configuration" {
			http.NotFound(w, r)
			return
		}
		k := r.URL.Query().Get("api_key")
		lastKey.Store(k)
		if k != "good-v3-key" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, `{"status_code":7,"status_message":"Invalid API key: You must be granted a valid key."}`)
			return
		}
		_, _ = io.WriteString(w, `{"images":{"secure_base_url":"https://image.tmdb.org/t/p/"}}`)
	}))
	defer srv.Close()
	tm := NewTMDBFunc(func() string { return "good-v3-key" })
	tm.base = srv.URL
	ctx := context.Background()

	got, err := tm.VerifyKey(ctx, "")
	if err != nil || got != "OK: images from https://image.tmdb.org/t/p/" {
		t.Errorf("saved key: %q, %v", got, err)
	}

	_, err = tm.VerifyKey(ctx, "  wrong-key ")
	if !errors.Is(err, ErrInvalidKey) || !strings.Contains(err.Error(), "use the v3 API key") || strings.Contains(err.Error(), "v4 token)") {
		t.Errorf("wrong candidate: %v", err)
	}
	if lastKey.Load() != "wrong-key" {
		t.Errorf("candidate sent as %q, want it trimmed and used instead of the saved key", lastKey.Load())
	}

	_, err = tm.VerifyKey(ctx, "eyJhbGciOiJIUzI1NiJ9.e30.sig")
	if !errors.Is(err, ErrInvalidKey) || !strings.Contains(err.Error(), "this looks like a v4 token") {
		t.Errorf("JWT-shaped candidate: %v", err)
	}

	if _, err := NewTMDBFunc(func() string { return "" }).VerifyKey(ctx, ""); !errors.Is(err, ErrNotConfigured) {
		t.Errorf("no key at all: %v, want ErrNotConfigured", err)
	}
}

// A TMDB transport failure must not carry the key, saved or candidate: the error text
// goes back to the browser and into logs.
func TestTMDBVerifyKeyTransportErrorHidesKey(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	base := srv.URL
	srv.Close() // nothing listens there now
	tm := NewTMDBFunc(func() string { return "saved+secret/key" })
	tm.base = base
	for _, cand := range []string{"", "candidate secret+key"} {
		_, err := tm.VerifyKey(context.Background(), cand)
		if err == nil {
			t.Fatal("want a transport error")
		}
		for _, leak := range []string{"saved+secret/key", "saved%2Bsecret%2Fkey", "candidate secret+key", "candidate+secret%2Bkey"} {
			if strings.Contains(err.Error(), leak) {
				t.Errorf("error leaks the key (%q): %v", leak, err)
			}
		}
	}
}

// OMDb's own complaint is passed through as it says it.
func TestOMDbVerifyKey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("apikey") {
		case "good":
			_, _ = io.WriteString(w, `{"Title":"The Shawshank Redemption","Response":"True"}`)
		case "spent":
			_, _ = io.WriteString(w, `{"Response":"False","Error":"Request limit reached!"}`)
		default:
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, `{"Response":"False","Error":"Invalid API key!"}`)
		}
	}))
	defer srv.Close()
	o := NewOMDbFunc(func() string { return "good" })
	o.base = srv.URL + "/"
	ctx := context.Background()

	if got, err := o.VerifyKey(ctx, ""); err != nil || !strings.Contains(got, "Shawshank") {
		t.Errorf("saved key: %q, %v", got, err)
	}
	if _, err := o.VerifyKey(ctx, "nope"); err == nil || !strings.Contains(err.Error(), "Invalid API key!") || !errors.Is(err, ErrInvalidKey) {
		t.Errorf("bad candidate: %v", err)
	}
	if _, err := o.VerifyKey(ctx, "spent"); err == nil || !strings.Contains(err.Error(), "Request limit reached!") {
		t.Errorf("spent key: %v", err)
	}
}

// TheTVDB's Test logs in with the candidate without replacing the token episode lookups
// use, and a saved key that passes leaves the rejected-key backoff.
func TestTVDBVerifyKey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if r.URL.Path != "/login" {
			http.NotFound(w, r)
			return
		}
		if !strings.Contains(string(body), `"good"`) && !strings.Contains(string(body), `"candidate"`) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = io.WriteString(w, `{"data":{"token":"fresh-token"}}`)
	}))
	defer srv.Close()
	tv := NewTVDB(func() string { return "good" })
	tv.base = srv.URL
	tv.token = "cached-token"
	tv.badKey = "good"
	ctx := context.Background()

	if _, err := tv.VerifyKey(ctx, "candidate"); err != nil {
		t.Fatalf("candidate: %v", err)
	}
	if tv.token != "cached-token" || tv.badKey != "good" {
		t.Errorf("a candidate test changed the client's state: token %q badKey %q", tv.token, tv.badKey)
	}
	if _, err := tv.VerifyKey(ctx, "wrong"); !errors.Is(err, ErrInvalidKey) {
		t.Errorf("wrong candidate: %v", err)
	}
	if _, err := tv.VerifyKey(ctx, ""); err != nil {
		t.Fatalf("saved key: %v", err)
	}
	if tv.token != "cached-token" || tv.badKey != "" {
		t.Errorf("after a passing saved-key test: token %q badKey %q, want the token kept and the backoff cleared", tv.token, tv.badKey)
	}
}
