package attention

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tristenlammi/arrmada/internal/eventbus"
	"github.com/tristenlammi/arrmada/internal/notify"
	"github.com/tristenlammi/arrmada/internal/store"
)

// emitCall is one EmitOnce the alerter made.
type emitCall struct {
	event, dedupe string
	data          map[string]any
	queued        bool // a new dedupe key: notify would have queued it
}

// fakeEmitter records EmitOnce calls and dedupes them the way notify does.
type fakeEmitter struct {
	mu    sync.Mutex
	calls []emitCall
	seen  map[string]bool
	fail  error
}

func newEmitter() *fakeEmitter { return &fakeEmitter{seen: map[string]bool{}} }

func (e *fakeEmitter) EmitOnce(_ context.Context, key, dedupe string, data map[string]any) (int, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.fail != nil {
		return 0, e.fail
	}
	c := emitCall{event: key, dedupe: dedupe, data: data, queued: !e.seen[dedupe]}
	e.seen[dedupe] = true
	e.calls = append(e.calls, c)
	if c.queued {
		return 1, nil
	}
	return 0, nil
}

// sent is every message that would have been queued, as "event count".
func (e *fakeEmitter) sent() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	var out []string
	for _, c := range e.calls {
		if c.queued {
			out = append(out, fmt.Sprintf("%s %v", c.event, c.data["count"]))
		}
	}
	return out
}

func (e *fakeEmitter) last() emitCall {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.calls[len(e.calls)-1]
}

type fakeFlags struct {
	mu sync.Mutex
	m  map[string]bool
}

func (f *fakeFlags) GetBool(_ context.Context, key string, def bool) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if v, ok := f.m[key]; ok {
		return v
	}
	return def
}

func (f *fakeFlags) SetBool(_ context.Context, key string, v bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.m[key] = v
	return nil
}

// alertEnv is a DB, a clock and the settings flag shared by every Alerter a test starts
// on it (each one is a process start).
type alertEnv struct {
	t     *testing.T
	st    *store.Store
	clk   *clock
	flags *fakeFlags
}

func newAlertEnv(t *testing.T, seededAlready bool) *alertEnv {
	st, _ := seeded(t)
	return &alertEnv{t: t, st: st, clk: &clock{t: time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)},
		flags: &fakeFlags{m: map[string]bool{SeededKey: seededAlready}}}
}

// start is a process start now: a fresh Alerter whose boot grace begins at this moment.
func (e *alertEnv) start(em Emitter) *Alerter {
	a := NewAlerter(e.st.DB(), em, e.flags, quiet)
	a.now, a.bootAt = e.clk.now, e.clk.now()
	return a
}

// started is a process that has been up past its boot grace.
func (e *alertEnv) started(em Emitter) *Alerter {
	a := e.start(em)
	a.bootAt = e.clk.now().Add(-time.Hour)
	return a
}

// diff advances the clock by d and runs one diff.
func (e *alertEnv) diff(a *Alerter, d time.Duration, items ...Item) {
	e.t.Helper()
	e.clk.add(d)
	if err := a.Diff(context.Background(), items); err != nil {
		e.t.Fatalf("diff: %v", err)
	}
}

// diffs runs n refreshes 30 s apart with the same items.
func (e *alertEnv) diffs(a *Alerter, n int, items ...Item) {
	e.t.Helper()
	for i := 0; i < n; i++ {
		e.diff(a, 30*time.Second, items...)
	}
}

func dl(hash string) Item {
	return Item{Key: KindDownload + ":" + hash, Kind: KindDownload, Level: LevelError, Name: hash,
		Title: hash + " errored in the download client", Link: "/downloads?show=problems"}
}

func hl(key, msg string) Item {
	return Item{Key: KindHealth + ":" + key, Kind: KindHealth, Level: LevelError, Title: msg, Link: "/settings/status"}
}

func wantSent(t *testing.T, em *fakeEmitter, want ...string) {
	t.Helper()
	got := em.sent()
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("sent %q, want %q", got, want)
	}
}

// A new problem sends one alert once it has settled, and never again while it lasts.
func TestAlertNewKeyOnce(t *testing.T) {
	e := newAlertEnv(t, true)
	em := newEmitter()
	a := e.started(em)
	item := dl("aaa")

	e.diff(a, 0, item)
	e.diff(a, time.Second, item) // a kick right after: two runs, but not a minute yet
	wantSent(t, em)
	e.diffs(a, 2, item)
	wantSent(t, em, "download.failed 1")
	c := em.last()
	if c.dedupe != "download.failed:download:aaa:"+fmt.Sprint(e.clk.now().Add(-61*time.Second).UnixMilli()) {
		t.Errorf("dedupe = %q", c.dedupe)
	}
	if c.data["title"] != "aaa errored in the download client" || c.data["link"] != "/downloads?show=problems" {
		t.Errorf("payload = %v", c.data)
	}
	e.diffs(a, 20, item)
	wantSent(t, em, "download.failed 1")
	if n := len(em.calls); n != 1 {
		t.Errorf("EmitOnce called %d times, want 1", n)
	}
}

// Something that shows up for one refresh at a time never alerts.
func TestAlertIgnoresFlicker(t *testing.T) {
	e := newAlertEnv(t, true)
	em := newEmitter()
	a := e.started(em)
	for i := 0; i < 10; i++ {
		e.diff(a, 30*time.Second, dl("aaa"))
		e.diff(a, 30*time.Second)
	}
	wantSent(t, em)
}

// Pending requests and the stuck-search count raise nothing here (request.created
// announces requests), and an import retry only alerts once it has become an error.
func TestAlertSkipsRequestsSearchesAndEarlyImportRetries(t *testing.T) {
	e := newAlertEnv(t, true)
	em := newEmitter()
	a := e.started(em)
	req := Item{Key: "request:1", Kind: KindRequest, Level: LevelWarning, Title: "Sam requested Dune"}
	search := Item{Key: "search:stuck", Kind: KindSearch, Level: LevelWarning, Title: "3 titles", Count: 3}
	imp := Item{Key: "import:abc", Kind: KindImport, Level: LevelWarning, Title: "Importing X keeps failing (2 tries)"}
	e.diffs(a, 5, req, search, imp)
	wantSent(t, em)
	imp.Level = LevelError
	e.diffs(a, 3, req, search, imp)
	wantSent(t, em, "import.stuck 1")
	var n int
	if err := e.st.DB().QueryRow(`SELECT COUNT(*) FROM attention_state WHERE kind IN ('request', 'search')`).Scan(&n); err != nil || n != 0 {
		t.Errorf("requests/searches tracked: %d (%v)", n, err)
	}
}

// Restarting sends nothing for what was already announced: not during the boot grace
// (when health checks haven't run and the problem seems to be gone), not after it.
func TestAlertStateSurvivesRestart(t *testing.T) {
	e := newAlertEnv(t, true)
	em := newEmitter()
	a := e.started(em)
	items := []Item{dl("aaa"), hl("qbit", "qBittorrent is unreachable")}
	e.diffs(a, 4, items...)
	wantSent(t, em, "download.failed 1", "health.problem 1")

	// A new process, and a fresh emitter: the alerter itself must decide nothing.
	em2 := newEmitter()
	b := e.start(em2)
	e.diffs(b, 4, dl("aaa")) // health checks haven't run yet
	e.diffs(b, 20, items...)
	if len(em2.calls) != 0 {
		t.Fatalf("restart re-sent: %+v", em2.calls)
	}
}

// The first start with alerts takes everything it sees in its first five minutes as
// already known: nothing is sent for it, then or later, and it never gets a "Resolved".
// Problems that start afterwards alert as usual.
func TestAlertFirstRunSeedsOnly(t *testing.T) {
	e := newAlertEnv(t, false)
	em := newEmitter()
	a := e.start(em)
	old := []Item{dl("old"), hl("disk", "Downloads are paused: /downloads is 87% full")}
	e.diffs(a, 2, old[0])
	e.diffs(a, 6, old...) // health shows up a minute in, still inside the window
	e.diffs(a, 10, old...)
	wantSent(t, em)
	if !e.flags.GetBool(context.Background(), SeededKey, false) {
		t.Fatal("seeded mark not saved after the first window")
	}
	e.diffs(a, 3, dl("old"), dl("new"))
	wantSent(t, em, "download.failed 1")
	e.diffs(a, 10, dl("old"), dl("new")) // the disk warning clears: it was never announced
	wantSent(t, em, "download.failed 1")

	// A later start never seeds again.
	em2 := newEmitter()
	b := e.start(em2)
	e.diffs(b, 3, dl("old"), dl("new"), dl("newer"))
	wantSent(t, em2, "download.failed 1")
}

// Health problems wait out the boot grace and need two runs; a client that's back up
// within five minutes of a start says nothing at all.
func TestHealthNeedsTwoRunsAndBootGrace(t *testing.T) {
	e := newAlertEnv(t, true)
	em := newEmitter()
	a := e.start(em)
	qbit := hl("qbit", "qBittorrent is unreachable")
	e.diffs(a, 6, qbit) // three minutes: still booting
	e.diffs(a, 20)      // it came up
	wantSent(t, em)

	em2 := newEmitter()
	b := e.start(em2)
	e.diffs(b, 9, qbit) // 4.5 minutes
	wantSent(t, em2)
	e.diffs(b, 2, qbit) // past five
	wantSent(t, em2, "health.problem 1")
	if c := em2.last(); !strings.HasPrefix(c.dedupe, "health.problem:health:qbit:") {
		t.Errorf("dedupe %q", c.dedupe)
	}

	// After the grace, a new health finding still needs two runs a minute apart.
	e.diff(b, 30*time.Second, qbit, hl("plex", "Plex is unreachable"))
	e.diff(b, time.Second, qbit, hl("plex", "Plex is unreachable"))
	wantSent(t, em2, "health.problem 1")
	e.diffs(b, 2, qbit, hl("plex", "Plex is unreachable"))
	wantSent(t, em2, "health.problem 1", "health.problem 1")
}

// An announced health problem that clears sends exactly one "Resolved"; other kinds
// clear quietly.
func TestHealthResolvedOnce(t *testing.T) {
	e := newAlertEnv(t, true)
	em := newEmitter()
	a := e.started(em)
	e.diffs(a, 4, hl("qbit", "qBittorrent is unreachable"), dl("aaa"))
	wantSent(t, em, "download.failed 1", "health.problem 1")

	e.diff(a, 30*time.Second) // one miss isn't enough
	e.diff(a, time.Second)    // two misses, but not two minutes
	wantSent(t, em, "download.failed 1", "health.problem 1")
	e.diffs(a, 10)
	wantSent(t, em, "download.failed 1", "health.problem 1", "health.resolved 1")
	c := em.last()
	m, ok := mustDef(t, notify.EventHealthResolved).Format(c.data)
	if !ok || m.Body != "✅ Resolved: qBittorrent is unreachable" {
		t.Errorf("resolved message = %+v", m)
	}
	e.diffs(a, 10)
	wantSent(t, em, "download.failed 1", "health.problem 1", "health.resolved 1")
}

// Back within six hours of its alert: no second alert, and no second "Resolved" when
// it clears again. Still there six hours on: one new alert.
func TestFlapGuard(t *testing.T) {
	e := newAlertEnv(t, true)
	em := newEmitter()
	a := e.started(em)
	qbit := hl("qbit", "qBittorrent is unreachable")
	e.diffs(a, 4, qbit)
	e.diffs(a, 6)
	wantSent(t, em, "health.problem 1", "health.resolved 1")
	first := em.calls[0].dedupe

	e.diffs(a, 10, qbit) // back an hour after the alert
	e.diffs(a, 6)        // clears again
	e.diffs(a, 10, qbit) // and back
	wantSent(t, em, "health.problem 1", "health.resolved 1")

	e.clk.add(6 * time.Hour)
	e.diffs(a, 2, qbit)
	wantSent(t, em, "health.problem 1", "health.resolved 1", "health.problem 1")
	if em.last().dedupe == first {
		t.Error("the new occurrence reused the first alert's dedupe key")
	}
}

// More than five of one kind at once become one summary; five go one by one.
func TestBatchingAboveFive(t *testing.T) {
	e := newAlertEnv(t, true)
	em := newEmitter()
	a := e.started(em)
	var six []Item
	for i := 0; i < 6; i++ {
		six = append(six, dl(fmt.Sprintf("t%d", i)))
	}
	e.diffs(a, 3, six...)
	wantSent(t, em, "download.failed 6")
	m, ok := mustDef(t, notify.EventDownloadFailed).Format(em.last().data)
	if !ok || m.Body != "❌ 6 downloads failed: t0, t1, t2 and 3 more" || m.Link != "/downloads?show=problems" {
		t.Errorf("summary = %+v", m)
	}
	e.diffs(a, 10, six...)
	wantSent(t, em, "download.failed 6")

	e2 := newAlertEnv(t, true)
	em2 := newEmitter()
	b := e2.started(em2)
	e2.diffs(b, 3, six[:5]...)
	wantSent(t, em2, "download.failed 1", "download.failed 1", "download.failed 1", "download.failed 1", "download.failed 1")
}

// A trickle doesn't become a flood: past five messages of one kind in fifteen minutes,
// what's new waits and goes out as one summary when the window has room.
func TestFloodBudget(t *testing.T) {
	e := newAlertEnv(t, true)
	em := newEmitter()
	a := e.started(em)
	var on []Item
	for i := 0; i < 5; i++ {
		on = append(on, dl(fmt.Sprintf("a%d", i)))
		e.diffs(a, 3, on...)
	}
	if got := len(em.sent()); got != 5 {
		t.Fatalf("sent %d, want 5", got)
	}
	on = append(on, dl("b0"), dl("b1"), dl("b2"))
	e.diffs(a, 4, on...)
	if got := len(em.sent()); got != 5 {
		t.Fatalf("over budget: sent %d, want 5 still", got)
	}
	e.diffs(a, 30, on...) // the window slides on
	got := em.sent()
	if len(got) != 6 || got[5] != "download.failed 3" {
		t.Fatalf("sent %q, want one summary of the three held", got)
	}
	e.diffs(a, 30, on...)
	if len(em.sent()) != 6 {
		t.Fatal("held items sent twice")
	}
}

// A decision that couldn't be handed to the queue is retried on the next refresh, under
// the same dedupe key; one handed over twice (a crash before it was marked done) is
// still queued once.
func TestAlertPendingIsRetriedUnderTheSameKey(t *testing.T) {
	e := newAlertEnv(t, true)
	em := newEmitter()
	em.fail = errors.New("database is locked")
	a := e.started(em)
	e.clk.add(30 * time.Second)
	_ = a.Diff(context.Background(), []Item{dl("aaa")})
	e.clk.add(30 * time.Second)
	_ = a.Diff(context.Background(), []Item{dl("aaa")})
	e.clk.add(30 * time.Second)
	if err := a.Diff(context.Background(), []Item{dl("aaa")}); err == nil {
		t.Fatal("a failed hand-over should be reported")
	}
	em.mu.Lock()
	em.fail = nil
	em.mu.Unlock()
	e.diffs(a, 3, dl("aaa"))
	wantSent(t, em, "download.failed 1")
	key := em.last().dedupe

	// A crash after queueing, before the row was cleared: the next start hands it over
	// again and notify's dedupe drops it.
	if _, err := e.st.DB().Exec(`UPDATE attention_state SET pending_event = ?, pending_dedupe = ? WHERE key = 'download:aaa'`,
		notify.EventDownloadFailed, key); err != nil {
		t.Fatal(err)
	}
	b := e.started(em)
	e.diffs(b, 2, dl("aaa"))
	wantSent(t, em, "download.failed 1")
	if c := em.last(); c.dedupe != key || c.queued {
		t.Errorf("re-handed %+v, want the same key, not queued again", c)
	}
	var pending int
	_ = e.st.DB().QueryRow(`SELECT COUNT(*) FROM attention_state WHERE pending_dedupe != ''`).Scan(&pending)
	if pending != 0 {
		t.Errorf("%d rows still pending", pending)
	}
}

// Rows for problems that cleared a week ago are pruned.
func TestAlertPrunesOldResolvedRows(t *testing.T) {
	e := newAlertEnv(t, true)
	em := newEmitter()
	a := e.started(em)
	e.diffs(a, 3, dl("aaa"))
	e.diffs(a, 6)
	e.clk.add(8 * 24 * time.Hour)
	e.diffs(a, 1)
	var n int
	_ = e.st.DB().QueryRow(`SELECT COUNT(*) FROM attention_state`).Scan(&n)
	if n != 0 {
		t.Errorf("%d rows left", n)
	}
}

// Through the attention service: refreshes from the 30-second task and from kicks in
// between all diff the same state, so a new review is announced once.
func TestRefreshAndKicksSendOnce(t *testing.T) {
	e := newAlertEnv(t, true)
	em := newEmitter()
	a := e.started(em)
	src := &stubProvider{}
	s := New(nil, nil, quiet, src)
	s.now = e.clk.now
	s.SetAlerts(a)
	src.set(Item{Key: "review:4", Kind: KindReview, Level: LevelWarning, Name: "Dune", Title: "Dune is held: it looks like a different title", Link: "/review"})
	ctx := context.Background()
	for i := 0; i < 12; i++ {
		e.clk.add(10 * time.Second)
		if err := s.Refresh(ctx); err != nil {
			t.Fatal(err)
		}
		if err := s.Refresh(ctx); err != nil { // a kick landing right behind it
			t.Fatal(err)
		}
	}
	wantSent(t, em, "import.held 1")
}

// End to end through notify: only a connection subscribed to the event gets a queued
// delivery, and a restart queues nothing more.
func TestAlertsReachSubscribedConnectionsOnly(t *testing.T) {
	e := newAlertEnv(t, true)
	ctx := context.Background()
	bus := eventbus.New(quiet)
	ns := notify.NewService(e.st.DB(), bus, quiet)
	yes, err := ns.Create(ctx, notify.Connection{Name: "admin", URL: "ntfy://admin", Events: []string{notify.EventDownloadFailed}, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ns.Create(ctx, notify.Connection{Name: "family", URL: "ntfy://family", Events: []string{"plex.stream.started"}, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	a := e.started(ns)
	e.diffs(a, 4, dl("aaa"))
	b := e.start(ns)
	e.diffs(b, 20, dl("aaa"))

	rows, err := e.st.DB().Query(`SELECT connection_id, body FROM notification_deliveries`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var id int64
		var body string
		if err := rows.Scan(&id, &body); err != nil {
			t.Fatal(err)
		}
		got = append(got, fmt.Sprintf("%d %s", id, body))
	}
	want := fmt.Sprintf("%d ❌ aaa errored in the download client", yes.ID)
	if len(got) != 1 || got[0] != want {
		t.Errorf("deliveries %q, want [%q]", got, want)
	}
}

type stubProvider struct {
	mu    sync.Mutex
	items []Item
}

func (p *stubProvider) set(items ...Item) { p.mu.Lock(); p.items = items; p.mu.Unlock() }
func (*stubProvider) Name() string        { return "stub" }
func (p *stubProvider) Collect(context.Context, *Frame) ([]Item, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]Item(nil), p.items...), nil
}

func mustDef(t *testing.T, key string) notify.EventDef {
	t.Helper()
	d, ok := notify.Lookup(key)
	if !ok {
		t.Fatalf("%s not in the catalog", key)
	}
	return d
}
