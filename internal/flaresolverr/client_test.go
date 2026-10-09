package flaresolverr

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fake is a FlareSolverr answering sessions.list with its version, counting requests.
func fake(t *testing.T, status int, body string, hits *atomic.Int32) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits != nil {
			hits.Add(1)
		}
		var req map[string]any
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &req)
		if r.URL.Path != "/v1" || req["cmd"] != "sessions.list" {
			http.Error(w, "unexpected", http.StatusBadRequest)
			return
		}
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestPing(t *testing.T) {
	ok := fake(t, http.StatusOK, `{"status":"ok","message":"","sessions":[],"version":"3.3.21"}`, nil)
	v, err := New(ok.URL).Ping(context.Background())
	if err != nil || v != "3.3.21" {
		t.Fatalf("ok: version %q, err %v", v, err)
	}

	bad := fake(t, http.StatusInternalServerError, `{"status":"error","message":"Error: browser crashed"}`, nil)
	if _, err := New(bad.URL).Ping(context.Background()); err == nil || !strings.Contains(err.Error(), "browser crashed") {
		t.Fatalf("error status: %v", err)
	}

	dead := httptest.NewServer(http.NotFoundHandler())
	deadURL := dead.URL
	dead.Close()
	_, err = New(deadURL).Ping(context.Background())
	if err == nil || !strings.Contains(err.Error(), "isn't answering") || !strings.Contains(err.Error(), "Arrmada-flaresolverr container") {
		t.Fatalf("unreachable: %v", err)
	}

	var none *Client
	if _, err := none.Ping(context.Background()); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("nil client: %v", err)
	}
	if none.Configured() || New("").Configured() || New("  ").Configured() {
		t.Fatal("no URL must read as not configured")
	}
}

// The URL is read on every call, so a change in Settings applies to the next request.
func TestNewFuncReadsTheURLEachCall(t *testing.T) {
	a := fake(t, http.StatusOK, `{"status":"ok","version":"A"}`, nil)
	b := fake(t, http.StatusOK, `{"status":"ok","version":"B"}`, nil)
	var mu sync.Mutex
	url := a.URL
	c := NewFunc(func() string { mu.Lock(); defer mu.Unlock(); return url + "/" })
	if v, _ := c.Ping(context.Background()); v != "A" {
		t.Fatalf("first = %q", v)
	}
	mu.Lock()
	url = b.URL
	mu.Unlock()
	if v, _ := c.Ping(context.Background()); v != "B" {
		t.Fatalf("after the change = %q", v)
	}
}

// Status reuses its answer for a minute, asks again after that, and asks at once for a
// new URL.
func TestStatusCaches(t *testing.T) {
	var hits atomic.Int32
	srv := fake(t, http.StatusOK, `{"status":"ok","version":"3.3.21"}`, &hits)
	now := time.Date(2026, 10, 9, 14, 0, 0, 0, time.UTC)
	url := srv.URL
	c := NewFunc(func() string { return url })
	c.now = func() time.Time { return now }

	st := c.Status(context.Background())
	if !st.Configured || !st.OK || st.Version != "3.3.21" || st.URL != srv.URL {
		t.Fatalf("status = %+v", st)
	}
	now = now.Add(30 * time.Second)
	c.Status(context.Background())
	if hits.Load() != 1 {
		t.Fatalf("asked %d times within the minute", hits.Load())
	}
	now = now.Add(31 * time.Second)
	c.Status(context.Background())
	if hits.Load() != 2 {
		t.Fatalf("asked %d times after the minute", hits.Load())
	}

	var unset *Client
	if st := unset.Status(context.Background()); st.Configured || st.OK {
		t.Fatalf("nil client status = %+v", st)
	}
}

// Get and Ping tell the status tracker how they went.
func TestOnResult(t *testing.T) {
	srv := fake(t, http.StatusOK, `{"status":"ok","version":"1"}`, nil)
	c := New(srv.URL)
	var got []error
	c.OnResult(func(err error) { got = append(got, err) })
	_, _ = c.Ping(context.Background())
	if len(got) != 1 || got[0] != nil {
		t.Fatalf("results = %v", got)
	}
	// A call the caller gave up on isn't FlareSolverr's failure.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _ = c.Ping(ctx)
	if len(got) != 1 {
		t.Fatalf("a cancelled call was reported: %v", got)
	}
}
