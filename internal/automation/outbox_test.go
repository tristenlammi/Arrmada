package automation

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/tristenlammi/arrmada/internal/eventbus"
	"github.com/tristenlammi/arrmada/internal/library"
	"github.com/tristenlammi/arrmada/internal/outbox"
	"github.com/tristenlammi/arrmada/internal/series"
)

// recorder is a set of fake outbox consumers that note every payload they're handed.
type recorder struct {
	mu   sync.Mutex
	runs map[string][]json.RawMessage // "topic/consumer" → payloads
}

func (r *recorder) handler(key string) outbox.Handler {
	return func(_ context.Context, p json.RawMessage) error {
		r.mu.Lock()
		defer r.mu.Unlock()
		if r.runs == nil {
			r.runs = map[string][]json.RawMessage{}
		}
		r.runs[key] = append(r.runs[key], p)
		return nil
	}
}

func (r *recorder) count(key string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.runs[key])
}

func (r *recorder) last(t *testing.T, key string, into any) {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	runs := r.runs[key]
	if len(runs) == 0 {
		t.Fatalf("%s never ran", key)
	}
	if err := json.Unmarshal(runs[len(runs)-1], into); err != nil {
		t.Fatal(err)
	}
}

// outboxFor registers fake consumers for every import topic, the way main registers the
// real ones, and wires the outbox into the coordinator and the movies service.
func outboxFor(t *testing.T, c *Coordinator) (*outbox.Outbox, *recorder) {
	t.Helper()
	box := outbox.New(c.db, c.log)
	rec := &recorder{}
	for topic, consumers := range map[string][]string{
		outbox.TopicMovieImported:  {"convert", "subtitles", "requests.ready"},
		outbox.TopicSeriesImported: {"convert", "subtitles", "requests.ready"},
		outbox.TopicBookImported:   {"requests.ready", "audioserver.cache"},
	} {
		for _, name := range consumers {
			box.Register(topic, name, rec.handler(topic+"/"+name))
		}
	}
	c.SetOutbox(box)
	if c.movies != nil {
		c.movies.SetOutbox(box)
	}
	return box, rec
}

func drainOutbox(t *testing.T, box *outbox.Outbox) {
	t.Helper()
	if _, err := box.Drain(context.Background()); err != nil {
		t.Fatal(err)
	}
}

// saturate fills a never-read subscriber's buffer on topic, so every later publish of it
// is dropped — the busy moment that used to cost an import its side effects.
func saturate(t *testing.T, bus *eventbus.Bus, topic string) {
	t.Helper()
	stuck, cancel := bus.Subscribe(topic)
	t.Cleanup(cancel)
	for len(stuck) < cap(stuck) {
		bus.Publish(topic, map[string]any{"id": int64(-1)})
	}
}

// With the bus dropping every movie.downloaded, an attached import still reaches Convert,
// Subtitles and the requester — each exactly once, from the outbox row written with the
// file record.
func TestAttachMovieImportSideEffectsSurviveDroppedBus(t *testing.T) {
	ctx := context.Background()
	c, mv, bus, _ := attachTestCoord(t)
	box, rec := outboxFor(t, c)
	saturate(t, bus, "movie.downloaded")
	dropsBefore := bus.Drops()["movie.downloaded"]

	mustExec(t, c, `INSERT INTO movies (id, tmdb_id, title, year, monitored) VALUES (7, 438631, 'Dune', 2021, 1)`)
	target := filepath.Join(t.TempDir(), "Dune (2021)", "Dune (2021).mkv")
	writeVideo(t, target)
	out, err := c.AttachMovieImport(ctx, library.ImportRecord{
		Hash: "h1", TargetPath: target, Title: "Dune", Year: 2021, ReleaseName: "Dune.2021.1080p.WEB-DL",
	})
	if err != nil || out != library.Attached {
		t.Fatalf("attach = %v, %v", out, err)
	}
	if bus.Drops()["movie.downloaded"] <= dropsBefore {
		t.Fatal("the bus delivered movie.downloaded — the test needs it dropped")
	}

	drainOutbox(t, box)
	for _, consumer := range []string{"convert", "subtitles", "requests.ready"} {
		key := outbox.TopicMovieImported + "/" + consumer
		if n := rec.count(key); n != 1 {
			t.Fatalf("%s ran %d times, want exactly once", key, n)
		}
	}
	var p outbox.MovieImported
	rec.last(t, outbox.TopicMovieImported+"/convert", &p)
	if p.MovieID != 7 || p.Path != target {
		t.Fatalf("payload = %+v", p)
	}
	if m, _ := mv.Get(ctx, 7); !m.HasFile {
		t.Fatal("movie not attached")
	}

	// Nothing is left to run again.
	drainOutbox(t, box)
	if n := rec.count(outbox.TopicMovieImported + "/convert"); n != 1 {
		t.Fatalf("convert ran %d times after a second drain", n)
	}
}

// A crash after the import is recorded but before anything consumed it: the rows are in
// the database, and the next process's outbox runs them.
func TestMovieImportSideEffectsSurviveRestart(t *testing.T) {
	ctx := context.Background()
	c, mv, _, _ := attachTestCoord(t)
	_, _ = outboxFor(t, c) // the "first process": enqueues, never drains
	mustExec(t, c, `INSERT INTO movies (id, tmdb_id, title, year, monitored) VALUES (7, 438631, 'Dune', 2021, 1)`)
	target := filepath.Join(t.TempDir(), "Dune.2021.mkv")
	writeVideo(t, target)
	if err := mv.MarkImported(ctx, 7, target, "Dune.2021.1080p"); err != nil {
		t.Fatal(err)
	}

	// The "second process": a fresh outbox with the consumers registered again.
	box, rec := outboxFor(t, c)
	drainOutbox(t, box)
	if n := rec.count(outbox.TopicMovieImported + "/convert"); n != 1 {
		t.Fatalf("convert ran %d times after the restart, want 1", n)
	}
}

// The outbox row and the file record are one transaction: when the row can't be written,
// the movie isn't marked downloaded either, so the attach is retried rather than leaving a
// Downloaded movie that Convert, Subtitles and its requester never hear about.
func TestMarkImportedRollsBackWithoutOutboxRow(t *testing.T) {
	ctx := context.Background()
	c, mv, _, _ := attachTestCoord(t)
	_, _ = outboxFor(t, c)
	mustExec(t, c, `INSERT INTO movies (id, tmdb_id, title, year, monitored) VALUES (7, 438631, 'Dune', 2021, 1)`)
	mustExec(t, c, `CREATE TRIGGER outbox_refuses BEFORE INSERT ON outbox BEGIN SELECT RAISE(ABORT, 'outbox unavailable'); END`)
	target := filepath.Join(t.TempDir(), "Dune.2021.mkv")
	writeVideo(t, target)

	out, err := c.AttachMovieImport(ctx, library.ImportRecord{Hash: "h", TargetPath: target, Title: "Dune", Year: 2021})
	if err == nil || out != library.AttachRetry {
		t.Fatalf("attach = %v, %v; want a retry", out, err)
	}
	m, err := mv.Get(ctx, 7)
	if err != nil {
		t.Fatal(err)
	}
	if m.HasFile || m.MovieFilePath != "" {
		t.Fatalf("the movie was marked downloaded without its outbox row: %+v", m)
	}
	var events int
	_ = c.db.QueryRow(`SELECT COUNT(*) FROM movie_events WHERE movie_id = 7`).Scan(&events)
	if events != 0 {
		t.Fatalf("%d history events written by a rolled-back import", events)
	}
}

// "Import anyway" from Review goes through the same path as an automatic import, so the
// movie gets its Convert/Subtitles/ready rows even though nothing is published by hand.
func TestReviewMovieImportEnqueuesMovieImported(t *testing.T) {
	c, _, ctx := reviewTestCoord(t)
	box, rec := outboxFor(t, c)
	mustExec(t, c, `INSERT INTO movies (id, tmdb_id, title, year, monitored) VALUES (5, 1, 'Arrival', 2016, 1)`)
	dl := t.TempDir()
	src := filepath.Join(dl, "Arrival.2016.1080p.BluRay.mkv")
	f, err := os.Create(src)
	if err != nil {
		t.Fatal(err)
	}
	// Sparse, but over the importer's sample-size floor.
	if err := f.Truncate(60 << 20); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	id := seedReview(t, c, "movie", 5, dl)
	if err := c.ImportReview(ctx, id, 0, ""); err != nil {
		t.Fatal(err)
	}
	drainOutbox(t, box)
	var p outbox.MovieImported
	rec.last(t, outbox.TopicMovieImported+"/requests.ready", &p)
	if p.MovieID != 5 || p.Path == "" {
		t.Fatalf("payload = %+v", p)
	}
	if rec.count(outbox.TopicMovieImported+"/convert") != 1 || rec.count(outbox.TopicMovieImported+"/subtitles") != 1 {
		t.Fatal("Convert and Subtitles weren't both told")
	}
}

// Series imports queue the episodes they placed for every series consumer.
func TestSeriesImportedEnqueuesPlacedEpisodes(t *testing.T) {
	c, _ := removeCoord(t)
	box, rec := outboxFor(t, c)
	c.seriesImported(context.Background(), 12, []series.EpisodeRef{{Season: 1, Episode: 2}, {Season: 1, Episode: 3}})
	drainOutbox(t, box)
	for _, consumer := range []string{"convert", "subtitles", "requests.ready"} {
		if n := rec.count(outbox.TopicSeriesImported + "/" + consumer); n != 1 {
			t.Fatalf("%s ran %d times", consumer, n)
		}
	}
	var p outbox.SeriesImported
	rec.last(t, outbox.TopicSeriesImported+"/subtitles", &p)
	if p.SeriesID != 12 || len(p.Episodes) != 2 || p.Episodes[1] != (outbox.Episode{Season: 1, Episode: 3}) {
		t.Fatalf("payload = %+v", p)
	}
}
