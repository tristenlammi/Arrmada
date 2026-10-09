package download

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

const qbitInfoSample = `[
  {"hash":"aaa","name":"Dune.Part.Two.2024.2160p","size":25769803776,"progress":0.62,
   "dlspeed":19283712,"upspeed":0,"eta":240,"state":"downloading","ratio":0.0,
   "category":"arrmada","completed":15977221324,"amount_left":9792582452},
  {"hash":"bbb","name":"Shogun.S01","size":19000000000,"progress":1.0,
   "dlspeed":0,"upspeed":3355443,"eta":8640000,"state":"stalledUP","ratio":2.41,
   "category":"arrmada","completed":19000000000,"amount_left":0}
]`

func TestParseTorrentsInfo(t *testing.T) {
	items, err := parseTorrentsInfo([]byte(qbitInfoSample))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(items))
	}

	d := items[0]
	if d.State != "downloading" {
		t.Errorf("state = %q, want downloading", d.State)
	}
	if d.Progress != 0.62 || d.DownSpeed != 19283712 || d.ETASeconds != 240 {
		t.Errorf("unexpected download fields: %+v", d)
	}
	if d.DownloadedBytes != 15977221324 || d.SizeBytes != 25769803776 {
		t.Errorf("bytes = %d/%d", d.DownloadedBytes, d.SizeBytes)
	}

	// A stalledUP torrent normalizes to "seeding".
	if items[1].State != "seeding" {
		t.Errorf("state[1] = %q, want seeding", items[1].State)
	}
	if items[1].Ratio != 2.41 {
		t.Errorf("ratio = %v", items[1].Ratio)
	}
}

// A dead torrent and a live one both normalize to "downloading"; only the raw state and
// the swarm numbers tell them apart, so the parse has to keep them.
func TestParseTorrentsInfoKeepsRawStateAndSwarm(t *testing.T) {
	const payload = `[
	  {"hash":"ccc","name":"Rare.Film.1971.1080p","size":8000000000,"progress":0.03,
	   "dlspeed":0,"upspeed":0,"eta":8640000,"state":"stalledDL","ratio":0,
	   "category":"arrmada","completed":240000000,"amount_left":7760000000,
	   "num_seeds":0,"num_leechs":2,"num_complete":1,"num_incomplete":7,
	   "last_activity":1759900000,"added_on":1759800000,"availability":0.41}
	]`
	items, err := parseTorrentsInfo([]byte(payload))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	it := items[0]
	if it.State != "downloading" {
		t.Errorf("State = %q — existing callers still expect downloading", it.State)
	}
	if it.RawState != "stalledDL" || it.Phase() != "stalled" {
		t.Errorf("RawState = %q, Phase = %q; want stalledDL / stalled", it.RawState, it.Phase())
	}
	if it.Seeds != 0 || it.Peers != 2 || it.SwarmSeeds != 1 || it.SwarmPeers != 7 {
		t.Errorf("swarm = seeds %d peers %d swarm %d/%d", it.Seeds, it.Peers, it.SwarmSeeds, it.SwarmPeers)
	}
	if it.LastActivity != 1759900000 || it.AddedOn != 1759800000 || it.Availability != 0.41 {
		t.Errorf("activity = last %d added %d avail %v", it.LastActivity, it.AddedOn, it.Availability)
	}
}

// qbitTestServer is an httptest qBittorrent stub. Every /api/v2/auth/login
// bumps logins and sets a fresh SID cookie; handle serves everything else.
func qbitTestServer(t *testing.T, logins *int32, handle http.HandlerFunc) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v2/auth/login", func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(logins, 1)
		http.SetCookie(w, &http.Cookie{Name: "SID", Value: fmt.Sprintf("sid-%d", n), Path: "/"})
		fmt.Fprint(w, "Ok.")
	})
	mux.HandleFunc("/", handle)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// A 403 on an authenticated call (expired SID) must trigger one re-login and a
// retry that succeeds — the caller never sees the expiry.
func TestListReloginOn403(t *testing.T) {
	var logins, infoCalls int32
	srv := qbitTestServer(t, &logins, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v2/torrents/info" {
			t.Errorf("unexpected path %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		if atomic.AddInt32(&infoCalls, 1) == 1 {
			w.WriteHeader(http.StatusForbidden) // simulate idle-expired session
			return
		}
		fmt.Fprint(w, qbitInfoSample)
	})

	q := NewQBittorrent()
	dc := Client{ID: 1, URL: srv.URL, Username: "u", Password: "p"}
	items, err := q.List(context.Background(), dc)
	if err != nil {
		t.Fatalf("List after expiry: %v", err)
	}
	if len(items) != 2 {
		t.Errorf("items = %d, want 2", len(items))
	}
	if got := atomic.LoadInt32(&logins); got != 2 {
		t.Errorf("logins = %d, want 2 (initial + re-login)", got)
	}
	if got := atomic.LoadInt32(&infoCalls); got != 2 {
		t.Errorf("info calls = %d, want 2 (403 + retry)", got)
	}
}

// A second 403 after a fresh login is a real error: exactly one retry, no loop.
func TestListPersistent403IsError(t *testing.T) {
	var logins, infoCalls int32
	srv := qbitTestServer(t, &logins, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&infoCalls, 1)
		w.WriteHeader(http.StatusForbidden)
	})

	q := NewQBittorrent()
	dc := Client{ID: 1, URL: srv.URL, Username: "u", Password: "p"}
	if _, err := q.List(context.Background(), dc); err == nil {
		t.Fatal("List should fail when 403 persists after re-login")
	}
	if got := atomic.LoadInt32(&logins); got != 2 {
		t.Errorf("logins = %d, want 2", got)
	}
	if got := atomic.LoadInt32(&infoCalls); got != 2 {
		t.Errorf("info calls = %d, want exactly 2 (one retry, no loop)", got)
	}
}

// The multipart Add body must be rebuilt for the retry — a consumed reader
// would send an empty body the second time.
func TestAddReloginRebuildsMultipartBody(t *testing.T) {
	var logins, addCalls int32
	srv := qbitTestServer(t, &logins, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v2/torrents/add" {
			http.NotFound(w, r)
			return
		}
		if atomic.AddInt32(&addCalls, 1) == 1 {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Errorf("retried add body unparseable: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if got := r.FormValue("urls"); got != "magnet:?xt=urn:btih:abc" {
			t.Errorf("retried add urls = %q", got)
		}
		if got := r.FormValue("category"); got != "arrmada" {
			t.Errorf("retried add category = %q", got)
		}
		fmt.Fprint(w, "Ok.")
	})

	q := NewQBittorrent()
	dc := Client{ID: 1, URL: srv.URL, Username: "u", Password: "p", Category: "arrmada"}
	err := q.Add(context.Background(), dc, AddRequest{Name: "x", URL: "magnet:?xt=urn:btih:abc"})
	if err != nil {
		t.Fatalf("Add after expiry: %v", err)
	}
	if got := atomic.LoadInt32(&addCalls); got != 2 {
		t.Errorf("add calls = %d, want 2", got)
	}
	if got := atomic.LoadInt32(&logins); got != 2 {
		t.Errorf("logins = %d, want 2", got)
	}
}

// Form-body actions (postForm path, here via Remove) also re-login and resend
// an intact body.
func TestPostFormReloginOn403(t *testing.T) {
	var logins, delCalls int32
	srv := qbitTestServer(t, &logins, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v2/torrents/delete" {
			http.NotFound(w, r)
			return
		}
		if atomic.AddInt32(&delCalls, 1) == 1 {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		if err := r.ParseForm(); err != nil {
			t.Errorf("retried form unparseable: %v", err)
		}
		if got := r.FormValue("hashes"); got != "deadbeef" {
			t.Errorf("retried hashes = %q", got)
		}
		if got := r.FormValue("deleteFiles"); got != "true" {
			t.Errorf("retried deleteFiles = %q", got)
		}
		fmt.Fprint(w, "Ok.")
	})

	q := NewQBittorrent()
	dc := Client{ID: 1, URL: srv.URL, Username: "u", Password: "p"}
	if err := q.Remove(context.Background(), dc, "deadbeef", true); err != nil {
		t.Fatalf("Remove after expiry: %v", err)
	}
	if got := atomic.LoadInt32(&delCalls); got != 2 {
		t.Errorf("delete calls = %d, want 2", got)
	}
}

// Auth-bypass servers (IP whitelist, no credentials) never 403; calls work
// with empty username/password.
func TestListAuthBypass(t *testing.T) {
	mux := http.NewServeMux()
	var logins int32
	mux.HandleFunc("/api/v2/auth/login", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&logins, 1)
		w.WriteHeader(http.StatusNoContent) // bypassed auth: 204, no SID cookie
	})
	mux.HandleFunc("/api/v2/torrents/info", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, qbitInfoSample)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	q := NewQBittorrent()
	dc := Client{ID: 7, URL: srv.URL}
	items, err := q.List(context.Background(), dc)
	if err != nil {
		t.Fatalf("List with auth bypass: %v", err)
	}
	if len(items) != 2 {
		t.Errorf("items = %d, want 2", len(items))
	}
	// Session is cached: a second call must not log in again.
	if _, err := q.List(context.Background(), dc); err != nil {
		t.Fatalf("second List: %v", err)
	}
	if got := atomic.LoadInt32(&logins); got != 1 {
		t.Errorf("logins = %d, want 1 (session cached)", got)
	}
}

// A wrong password on the re-login surfaces the login error, not a raw 403.
func TestReloginFailureSurfaced(t *testing.T) {
	var logins int32
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v2/auth/login", func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&logins, 1) == 1 {
			http.SetCookie(w, &http.Cookie{Name: "SID", Value: "sid-1", Path: "/"})
			fmt.Fprint(w, "Ok.")
			return
		}
		fmt.Fprint(w, "Fails.") // password changed while we were idle
	})
	mux.HandleFunc("/api/v2/torrents/info", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	q := NewQBittorrent()
	dc := Client{ID: 1, URL: srv.URL, Username: "u", Password: "p"}
	_, err := q.List(context.Background(), dc)
	if err == nil || !strings.Contains(err.Error(), "invalid username or password") {
		t.Fatalf("err = %v, want invalid-credentials error", err)
	}
}

// Forget drops the cached session, so the next call logs in again with whatever is
// stored now — a changed password takes effect without a restart.
func TestForgetDropsCachedSession(t *testing.T) {
	var logins atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			logins.Add(1)
			fmt.Fprint(w, "Ok.")
		case "/api/v2/app/version":
			fmt.Fprint(w, "v5.0.0")
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	q := NewQBittorrent()
	dc := Client{ID: 7, URL: srv.URL, Username: "u", Password: "p"}
	ctx := context.Background()
	for range 2 {
		if err := q.Ping(ctx, dc); err != nil {
			t.Fatal(err)
		}
	}
	if n := logins.Load(); n != 1 {
		t.Fatalf("logins before Forget = %d, want 1 (the session is cached)", n)
	}
	q.Forget(dc.ID)
	if err := q.Ping(ctx, dc); err != nil {
		t.Fatal(err)
	}
	if n := logins.Load(); n != 2 {
		t.Errorf("logins after Forget = %d, want 2", n)
	}
	q.mu.Lock()
	_, kept := q.loginMu[dc.ID]
	q.mu.Unlock()
	if !kept {
		t.Error("Forget dropped the login mutex; an in-flight login would lose its single-flight")
	}
}

func TestNormalizeState(t *testing.T) {
	cases := map[string]string{
		"downloading": "downloading",
		"metaDL":      "downloading",
		"stalledDL":   "downloading",
		"uploading":   "seeding",
		"stalledUP":   "seeding",
		"pausedDL":    "paused",
		"error":       "error",
		"moving":      "checking",
		// A recheck moves no progress, so it must not read as downloading — that let a
		// long recheck run down the stall window.
		"checkingDL":   "checking",
		"forcedMetaDL": "downloading",
	}
	for in, want := range cases {
		if got := normalizeState(in); got != want {
			t.Errorf("normalizeState(%q) = %q, want %q", in, got, want)
		}
	}
}
