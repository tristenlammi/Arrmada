package indexer

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// tlStandIn plays TorrentLeech: a login form that accepts one password, and a browse API
// whose next few replies a test can script (a Cloudflare 403, a login page, ...).
type tlStandIn struct {
	logins     atomic.Int32
	searches   atomic.Int32
	loginDelay time.Duration

	mu       sync.Mutex
	password string
	script   []func(w http.ResponseWriter) // next browse replies, then JSON
}

const tlLoggedIn = `<html><a href="/user/account/logout">Logout</a></html>`

func newTLStandIn(t *testing.T) (*tlStandIn, *TorrentLeechSearcher) {
	t.Helper()
	st := &tlStandIn{password: "right"}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/user/account/login/":
			st.logins.Add(1)
			time.Sleep(st.loginDelay)
			_ = r.ParseForm()
			st.mu.Lock()
			ok := r.PostForm.Get("password") == st.password
			st.mu.Unlock()
			if ok {
				_, _ = w.Write([]byte(tlLoggedIn))
				return
			}
			_, _ = w.Write([]byte(`<form class="login-form"><div class="text-danger">Invalid</div></form>`))
		case strings.HasPrefix(r.URL.Path, "/torrents/browse/list"):
			st.searches.Add(1)
			st.mu.Lock()
			var next func(http.ResponseWriter)
			if len(st.script) > 0 {
				next, st.script = st.script[0], st.script[1:]
			}
			st.mu.Unlock()
			if next != nil {
				next(w)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(tlSample))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	tl := NewTorrentLeechSearcher(nil)
	tl.base, tl.delay = srv.URL, 0
	return st, tl
}

func (st *tlStandIn) then(replies ...func(w http.ResponseWriter)) {
	st.mu.Lock()
	st.script = append(st.script, replies...)
	st.mu.Unlock()
}

func cloudflare(code int) func(http.ResponseWriter) {
	return func(w http.ResponseWriter) {
		w.WriteHeader(code)
		_, _ = w.Write([]byte("<html>Just a moment...</html>"))
	}
}

var tlIdx = Indexer{ID: 7, Name: "TorrentLeech", Kind: KindTorrentLeech, Username: "me", Password: "right"}

// A Cloudflare 403 or 503, a login page, or a 200 that isn't JSON drops the session; the
// search logs in once more and succeeds, with no restart.
func TestTorrentLeechRecoversStaleSession(t *testing.T) {
	ctx := context.Background()
	for name, reply := range map[string]func(http.ResponseWriter){
		"403":        cloudflare(http.StatusForbidden),
		"503":        cloudflare(http.StatusServiceUnavailable),
		"login page": func(w http.ResponseWriter) { _, _ = w.Write([]byte(`<form class="login-form">`)) },
		"non-JSON":   func(w http.ResponseWriter) { _, _ = w.Write([]byte(`<html>maintenance</html>`)) },
	} {
		st, tl := newTLStandIn(t)
		if _, err := tl.Search(ctx, tlIdx, SearchQuery{Text: "Dune"}); err != nil {
			t.Fatalf("%s: first search: %v", name, err)
		}
		st.then(reply)
		rel, err := tl.Search(ctx, tlIdx, SearchQuery{Text: "Dune"})
		if err != nil || len(rel) != 2 {
			t.Fatalf("%s: search after a stale session = %d releases, %v", name, len(rel), err)
		}
		if st.logins.Load() != 2 || st.searches.Load() != 3 {
			t.Fatalf("%s: logins %d, searches %d; want 2 and 3", name, st.logins.Load(), st.searches.Load())
		}
	}

	// Retried exactly once: a second stale reply is the search's error.
	st, tl := newTLStandIn(t)
	st.then(cloudflare(http.StatusForbidden), cloudflare(http.StatusForbidden))
	if _, err := tl.Search(ctx, tlIdx, SearchQuery{Text: "Dune"}); !errors.Is(err, errTLSession) {
		t.Fatalf("err = %v", err)
	}
	if st.logins.Load() != 2 || st.searches.Load() != 2 {
		t.Fatalf("logins %d, searches %d; want 2 and 2", st.logins.Load(), st.searches.Load())
	}
}

// Failed logins pause the next one: credentials refused go straight to 6 hours and, refused
// again, hold logins until the indexer is edited (Reset) or Tested; other failures climb
// 15 minutes, doubling, to 6 hours. A paused search never contacts the site.
func TestTorrentLeechLoginBackoff(t *testing.T) {
	ctx := context.Background()
	st, tl := newTLStandIn(t)
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.Local)
	tl.now = func() time.Time { return now }
	wrong := tlIdx
	wrong.Password = "wrong"

	if _, err := tl.Search(ctx, wrong, SearchQuery{Text: "x"}); !errors.Is(err, errTLBadLogin) {
		t.Fatalf("first login = %v", err)
	}
	_, err := tl.Search(ctx, wrong, SearchQuery{Text: "x"})
	if err == nil || !strings.Contains(err.Error(), "TorrentLeech login paused until 18:00 after: login failed — check username/password") {
		t.Fatalf("paused = %v", err)
	}
	if st.logins.Load() != 1 {
		t.Fatalf("a paused login reached the site: %d", st.logins.Load())
	}

	now = now.Add(6*time.Hour + time.Minute)
	_, _ = tl.Search(ctx, wrong, SearchQuery{Text: "x"})
	if st.logins.Load() != 2 {
		t.Fatalf("logins after the pause = %d, want 2", st.logins.Load())
	}
	now = now.Add(24 * time.Hour)
	_, err = tl.Search(ctx, wrong, SearchQuery{Text: "x"})
	if err == nil || !strings.Contains(err.Error(), "edit the indexer or press Test") || st.logins.Load() != 2 {
		t.Fatalf("held: %v (logins %d)", err, st.logins.Load())
	}

	// Edited: the hold is gone and the new password logs in at once.
	tl.Reset(tlIdx.ID)
	if _, err := tl.Search(ctx, tlIdx, SearchQuery{Text: "x"}); err != nil || st.logins.Load() != 3 {
		t.Fatalf("after Reset: %v (logins %d)", err, st.logins.Load())
	}

	// Other failures climb the ladder.
	tl.Reset(tlIdx.ID)
	tl.sessMu.Lock()
	for _, want := range []time.Duration{15 * time.Minute, 30 * time.Minute, time.Hour, 2 * time.Hour, 4 * time.Hour, 6 * time.Hour, 6 * time.Hour} {
		tl.failLocked(tlIdx.ID, errNoFlareSolverr)
		if got := tl.backoff[tlIdx.ID].until.Sub(now); got != want || tl.backoff[tlIdx.ID].held {
			t.Errorf("failure %d paused %v, want %v", tl.backoff[tlIdx.ID].n, got, want)
		}
	}
	tl.sessMu.Unlock()
}

// Test ignores the pause, and a pass clears it and keeps its session for the next search.
func TestTorrentLeechTestBypassesBackoff(t *testing.T) {
	ctx := context.Background()
	st, tl := newTLStandIn(t)
	wrong := tlIdx
	wrong.Password = "wrong"
	_, _ = tl.Search(ctx, wrong, SearchQuery{Text: "x"})
	if err := tl.Test(ctx, wrong); !errors.Is(err, errTLBadLogin) || st.logins.Load() != 2 {
		t.Fatalf("Test while paused = %v (logins %d)", err, st.logins.Load())
	}
	if err := tl.Test(ctx, tlIdx); err != nil {
		t.Fatalf("Test = %v", err)
	}
	if _, err := tl.Search(ctx, tlIdx, SearchQuery{Text: "x"}); err != nil {
		t.Fatalf("search after a passing Test = %v", err)
	}
	if st.logins.Load() != 3 {
		t.Fatalf("the search logged in again instead of using Test's session: %d", st.logins.Load())
	}

	// Unsaved settings (id 0) leave no session or pause behind.
	unsaved := wrong
	unsaved.ID = 0
	_ = tl.Test(ctx, unsaved)
	tl.sessMu.Lock()
	_, paused := tl.backoff[0]
	tl.sessMu.Unlock()
	if paused {
		t.Fatal("testing unsaved settings paused logins")
	}
}

// Concurrent searches share one login.
func TestTorrentLeechConcurrentSearchesLogInOnce(t *testing.T) {
	st, tl := newTLStandIn(t)
	st.loginDelay = 200 * time.Millisecond
	var wg sync.WaitGroup
	errs := make(chan error, 5)
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := tl.Search(context.Background(), tlIdx, SearchQuery{Text: "x"}); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	if n := st.logins.Load(); n != 1 {
		t.Fatalf("5 concurrent searches made %d login POSTs, want 1", n)
	}
}

// A login slower than the search that started it still finishes and is kept: the search
// times out, the next one uses the session, and the outcome is reported since nobody was
// left waiting for it.
func TestTorrentLeechSlowLoginOutlivesSearch(t *testing.T) {
	st, tl := newTLStandIn(t)
	st.loginDelay = 300 * time.Millisecond
	reported := make(chan error, 1)
	tl.onLogin = func(_ Indexer, err error) { reported <- err }

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := tl.Search(ctx, tlIdx, SearchQuery{Text: "x"}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("first search = %v", err)
	}
	select {
	case err := <-reported:
		if err != nil {
			t.Fatalf("login reported %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the detached login never finished")
	}
	if _, err := tl.Search(context.Background(), tlIdx, SearchQuery{Text: "x"}); err != nil {
		t.Fatal(err)
	}
	if st.logins.Load() != 1 {
		t.Fatalf("logins = %d, want 1", st.logins.Load())
	}
}

// A Reset while a login is under way discards its session: it was made with the old
// credentials.
func TestTorrentLeechResetDiscardsLoginInFlight(t *testing.T) {
	st, tl := newTLStandIn(t)
	st.loginDelay = 200 * time.Millisecond
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = tl.Search(context.Background(), tlIdx, SearchQuery{Text: "x"})
	}()
	time.Sleep(50 * time.Millisecond)
	tl.Reset(tlIdx.ID)
	<-done
	tl.sessMu.Lock()
	_, kept := tl.sessions[tlIdx.ID]
	tl.sessMu.Unlock()
	if kept {
		t.Fatal("a login started before Reset was kept")
	}
}
