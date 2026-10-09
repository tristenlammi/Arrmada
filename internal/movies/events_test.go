package movies

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/tristenlammi/arrmada/internal/eventbus"
	"github.com/tristenlammi/arrmada/internal/outbox"
)

// changeRecorder wires a real outbox into the fixture's service with fake consumers of
// movie.changed and movie.imported, recording each payload they're handed.
type changeRecorder struct {
	box     *outbox.Outbox
	changed []outbox.MovieChanged
}

func recordChanges(t *testing.T, f *deleteFixture) *changeRecorder {
	t.Helper()
	r := &changeRecorder{box: outbox.New(f.svc.repo.db, f.svc.log)}
	r.box.Register(outbox.TopicMovieChanged, "convert", func(_ context.Context, raw json.RawMessage) error {
		var p outbox.MovieChanged
		if err := json.Unmarshal(raw, &p); err != nil {
			return err
		}
		r.changed = append(r.changed, p)
		return nil
	})
	f.svc.SetOutbox(r.box)
	return r
}

func (r *changeRecorder) drain(t *testing.T) {
	t.Helper()
	if _, err := r.box.Drain(context.Background()); err != nil {
		t.Fatal(err)
	}
}

// next waits briefly for an event on ch.
func next(t *testing.T, ch <-chan eventbus.Event, topic string) map[string]any {
	t.Helper()
	select {
	case ev := <-ch:
		data, ok := ev.Data.(map[string]any)
		if !ok {
			t.Fatalf("%s payload is %T", topic, ev.Data)
		}
		return data
	case <-time.After(time.Second):
		t.Fatalf("no %s published", topic)
	}
	return nil
}

// A rename announces both paths on the bus and queues the index update with them.
func TestRenamePublishesPaths(t *testing.T) {
	f := newDeleteFixture(t, filepath.Join(t.TempDir(), "bin"))
	rec := recordChanges(t, f)
	ctx := context.Background()
	m, err := f.svc.repo.Create(ctx, Movie{TMDBID: 77, Title: "Arrival", Year: 2016, Monitored: true})
	if err != nil {
		t.Fatal(err)
	}
	old := writeFixture(t, filepath.Join(f.root, "incoming", "arrival.2016.1080p.mkv"))
	if err := f.svc.repo.SetFile(ctx, m.ID, old); err != nil {
		t.Fatal(err)
	}
	events, cancel := f.bus.Subscribe("movie.renamed")
	defer cancel()

	if err := f.svc.Rename(ctx, m.ID); err != nil {
		t.Fatal(err)
	}
	got, _ := f.svc.Get(ctx, m.ID)
	if got.MovieFilePath == old {
		t.Fatal("rename didn't move the file — the test needs a misnamed one")
	}
	ev := next(t, events, "movie.renamed")
	if ev["id"] != m.ID || ev["old_path"] != old || ev["new_path"] != got.MovieFilePath {
		t.Fatalf("movie.renamed = %+v, want id %d, %q → %q", ev, m.ID, old, got.MovieFilePath)
	}
	rec.drain(t)
	if len(rec.changed) != 1 {
		t.Fatalf("movie.changed rows run = %d, want 1", len(rec.changed))
	}
	if c := rec.changed[0]; c.Change != outbox.ChangeRenamed || c.OldPath != old || c.Path != got.MovieFilePath {
		t.Fatalf("movie.changed = %+v", c)
	}
}

// Deleting a movie announces movie.deleted (id as int64, its folder) from the service
// itself, and queues the index clean-up in the same transaction as the delete.
func TestDeletePublishesMovieDeleted(t *testing.T) {
	f := newDeleteFixture(t, filepath.Join(t.TempDir(), "bin"))
	rec := recordChanges(t, f)
	events, cancel := f.bus.Subscribe("movie.deleted")
	defer cancel()

	if err := f.svc.Delete(context.Background(), f.id, true); err != nil {
		t.Fatal(err)
	}
	ev := next(t, events, "movie.deleted")
	if id, ok := ev["id"].(int64); !ok || id != f.id {
		t.Fatalf("movie.deleted id = %#v, want int64 %d", ev["id"], f.id)
	}
	if ev["folder"] != filepath.Dir(f.main) {
		t.Fatalf("movie.deleted folder = %v, want %q", ev["folder"], filepath.Dir(f.main))
	}
	rec.drain(t)
	if len(rec.changed) != 1 || rec.changed[0].Change != outbox.ChangeDeleted || rec.changed[0].MovieID != f.id {
		t.Fatalf("movie.changed = %+v, want one 'deleted' for movie %d", rec.changed, f.id)
	}
}

// Deleting a file — the default track's or an extra's — names the file that went, on the
// bus and in the queued index update.
func TestDeleteFilePublishesPath(t *testing.T) {
	f := newDeleteFixture(t, filepath.Join(t.TempDir(), "bin"))
	rec := recordChanges(t, f)
	ctx := context.Background()
	events, cancel := f.bus.Subscribe("movie.file_deleted")
	defer cancel()

	if err := f.svc.DeleteVersionFile(ctx, f.id, f.vid); err != nil {
		t.Fatal(err)
	}
	ev := next(t, events, "movie.file_deleted")
	if ev["path"] != f.extra || ev["version_id"] != f.vid {
		t.Fatalf("extra delete = %+v, want path %q version %d", ev, f.extra, f.vid)
	}
	if err := f.svc.DeleteFile(ctx, f.id); err != nil {
		t.Fatal(err)
	}
	ev = next(t, events, "movie.file_deleted")
	if ev["path"] != f.main || ev["version_id"] != int64(0) {
		t.Fatalf("default delete = %+v, want path %q version 0", ev, f.main)
	}
	rec.drain(t)
	// Both changes are about one movie: the waiting row is updated, not doubled, and the
	// handler re-reads the movie anyway.
	if len(rec.changed) != 1 || rec.changed[0].Change != outbox.ChangeFileDeleted || rec.changed[0].Path != f.main {
		t.Fatalf("movie.changed = %+v, want the latest file_deleted", rec.changed)
	}
}

// With every movie.renamed dropped by a full bus, the index update still runs: it never
// depended on the bus.
func TestRenameIndexUpdateSurvivesDroppedBus(t *testing.T) {
	f := newDeleteFixture(t, filepath.Join(t.TempDir(), "bin"))
	rec := recordChanges(t, f)
	ctx := context.Background()
	stuck, cancel := f.bus.Subscribe("movie.renamed")
	defer cancel()
	for len(stuck) < cap(stuck) {
		f.bus.Publish("movie.renamed", map[string]any{})
	}
	m, err := f.svc.repo.Create(ctx, Movie{TMDBID: 78, Title: "Sicario", Year: 2015, Monitored: true})
	if err != nil {
		t.Fatal(err)
	}
	old := writeFixture(t, filepath.Join(f.root, "incoming", "sicario.2015.mkv"))
	if err := f.svc.repo.SetFile(ctx, m.ID, old); err != nil {
		t.Fatal(err)
	}
	before := f.bus.Drops()["movie.renamed"]
	if err := f.svc.Rename(ctx, m.ID); err != nil {
		t.Fatal(err)
	}
	if f.bus.Drops()["movie.renamed"] <= before {
		t.Fatal("the bus delivered movie.renamed — the test needs it dropped")
	}
	rec.drain(t)
	if len(rec.changed) != 1 || rec.changed[0].MovieID != m.ID {
		t.Fatalf("index update after a dropped event = %+v", rec.changed)
	}
}
