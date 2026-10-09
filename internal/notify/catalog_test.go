package notify

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tristenlammi/arrmada/internal/eventbus"
	"github.com/tristenlammi/arrmada/internal/store"
)

// samples are a realistic payload for every built-in event, as its producer publishes it.
var samples = map[string]map[string]any{
	"request.auto_approved": {"id": int64(4), "title": "Dune", "year": 2021, "media_type": "movie", "requested_by": "Sam"},
	"release.grabbed":       {"title": "Dune.2021.2160p.WEB-DL", "indexer": "Tracker"},
	"movie.imported":        {"id": int64(12), "version_id": int64(0), "path": "/m/Dune.mkv", "title": "Dune", "upgrade": true},
	"episodes.imported":     {"title": "Severance", "id": int64(3), "count": 2},
	"book.imported":         {"title": "Dune", "id": int64(9), "edition": "audiobook", "version": "Unabridged"},
	"music.imported":        {"artist_id": int64(1), "album_id": int64(5), "placed": 10, "artist": "Björk", "album": "Homogenic"},
	"plex.stream.started":   {"user": "sam", "title": "Dune", "player": "TV"},
	"plex.buffering":        {"user": "sam", "title": "Dune"},
}

// Every event formats its sample into a message with a title and a body, and says
// "nothing to send" when the payload has no title.
func TestCatalogFormat(t *testing.T) {
	for _, d := range Catalog() {
		sample, ok := samples[d.Key]
		if !ok {
			t.Errorf("%s: no sample payload in this test", d.Key)
			continue
		}
		m, ok := d.Format(sample)
		if !ok || m.Title == "" || m.Body == "" {
			t.Errorf("%s: Format(sample) = %+v, %v", d.Key, m, ok)
		}
		if _, ok := d.Format(map[string]any{}); ok {
			t.Errorf("%s: an empty payload should send nothing", d.Key)
		}
		if d.Group == "" || d.Label == "" {
			t.Errorf("%s: missing group or label", d.Key)
		}
	}
	m, _ := mustLookup(t, "book.imported").Format(samples["book.imported"])
	if m.Body != "📚 Dune (audiobook · Unabridged)" || m.Link != "/books/9" {
		t.Errorf("book.imported = %+v", m)
	}
	m, _ = mustLookup(t, "book.imported").Format(map[string]any{"title": "Emma", "edition": "ebook"})
	if m.Body != "📚 Emma (ebook)" {
		t.Errorf("ebook body = %q", m.Body)
	}
	m, _ = mustLookup(t, "request.auto_approved").Format(samples["request.auto_approved"])
	if m.Body != "✅ Auto-approved: Dune (2021) for Sam" {
		t.Errorf("auto-approved body = %q", m.Body)
	}
	if _, ok := mustLookup(t, "movie.imported").Format(map[string]any{"title": "Dune", "source": "scan"}); ok {
		t.Error("a library scan adopting a file should not alert")
	}
}

func mustLookup(t *testing.T, key string) EventDef {
	t.Helper()
	d, ok := Lookup(key)
	if !ok {
		t.Fatalf("%s not in the catalog", key)
	}
	return d
}

func TestRegisterRefusesDuplicatesAndUnknownGroups(t *testing.T) {
	for name, d := range map[string]EventDef{
		"duplicate":     {Key: "release.grabbed", Group: GroupDownloads, Label: "x", Format: func(map[string]any) (Message, bool) { return Message{}, false }},
		"unknown group": {Key: "x.y", Group: "nope", Label: "x", Format: func(map[string]any) (Message, bool) { return Message{}, false }},
		"no format":     {Key: "x.z", Group: GroupDownloads, Label: "x"},
	} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("%s: Register didn't panic", name)
				}
			}()
			Register(d)
		}()
	}
}

// recorder is a Transport that remembers what went where.
type recorder struct {
	mu   sync.Mutex
	sent []string // "url|title|body"
	fail map[string]bool
}

func (r *recorder) send(_ context.Context, url, title, body string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sent = append(r.sent, url+"|"+title+"|"+body)
	if r.fail[url] {
		return errors.New("boom at " + url)
	}
	return nil
}

func (r *recorder) got() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.sent...)
}

func newTestService(t *testing.T) (*Service, *recorder, *eventbus.Bus) {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	bus := eventbus.New(log)
	s := NewService(st.DB(), bus, log)
	rec := &recorder{fail: map[string]bool{}}
	s.SetTransport(rec.send)
	return s, rec, bus
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// Publishing book.imported reaches only the connection subscribed to it.
func TestRunDispatchesByTopic(t *testing.T) {
	s, rec, bus := newTestService(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if _, err := s.Create(ctx, Connection{Name: "books", URL: "ntfy://books", Events: []string{"book.imported"}, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create(ctx, Connection{Name: "grabs", URL: "ntfy://grabs", Events: []string{"release.grabbed"}, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create(ctx, Connection{Name: "off", URL: "ntfy://off", Events: []string{"book.imported"}, Enabled: false}); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()
	// Run subscribes asynchronously; keep publishing until it's listening.
	waitFor(t, "the book alert", func() bool {
		bus.Publish("book.imported", map[string]any{"title": "Dune", "id": int64(1), "edition": "audiobook"})
		return len(rec.got()) > 0
	})
	cancel()
	<-done
	for _, g := range rec.got() {
		if g != "ntfy://books|Imported|📚 Dune (audiobook)" {
			t.Errorf("unexpected send %q", g)
		}
	}
}

func TestConnectionCRUDRoundTripsEvents(t *testing.T) {
	s, _, _ := newTestService(t)
	ctx := context.Background()
	c, err := s.Create(ctx, Connection{Name: "a", URL: "ntfy://a", Events: []string{"plex.buffering", "book.imported"}, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(c.Events, ",") != "book.imported,plex.buffering" {
		t.Fatalf("created events = %v", c.Events)
	}
	c.Events = []string{"release.grabbed"}
	if err := s.Update(ctx, c.ID, c); err != nil {
		t.Fatal(err)
	}
	list, err := s.List(ctx)
	if err != nil || len(list) != 1 || strings.Join(list[0].Events, ",") != "release.grabbed" {
		t.Fatalf("list after update = %+v, %v", list, err)
	}
	// The legacy columns follow, for a rolled-back build.
	var grab, buf int
	if err := s.db.QueryRowContext(ctx, `SELECT on_grab, on_buffering FROM notifications WHERE id = ?`, c.ID).Scan(&grab, &buf); err != nil || grab != 1 || buf != 0 {
		t.Errorf("legacy flags = grab %d buffering %d (%v)", grab, buf, err)
	}
	if err := s.Delete(ctx, c.ID); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM notification_subscriptions`).Scan(&n); err != nil || n != 0 {
		t.Errorf("subscriptions left after delete: %d (%v)", n, err)
	}
	if err := s.Update(ctx, c.ID, c); !errors.Is(err, ErrNotFound) {
		t.Errorf("update of a deleted connection: %v", err)
	}
}

// Emit formats a catalog event and dispatches it; Dispatch counts the connections.
func TestEmitAndDispatch(t *testing.T) {
	s, rec, _ := newTestService(t)
	ctx := context.Background()
	if _, err := s.Create(ctx, Connection{Name: "a", URL: "ntfy://a", Events: []string{"request.auto_approved"}, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	n, err := s.Emit(ctx, "request.auto_approved", samples["request.auto_approved"])
	if err != nil || n != 1 {
		t.Fatalf("Emit = %d, %v", n, err)
	}
	if g := rec.got(); len(g) != 1 || !strings.Contains(g[0], "Auto-approved: Dune (2021) for Sam") {
		t.Fatalf("sent %v", g)
	}
	if _, err := s.Emit(ctx, "no.such.event", nil); err == nil {
		t.Error("Emit of an unknown key should fail")
	}
}
