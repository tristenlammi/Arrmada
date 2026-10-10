package main

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"reflect"
	"sort"
	"testing"

	"github.com/tristenlammi/arrmada/internal/outbox"
	"github.com/tristenlammi/arrmada/internal/store"
)

// Every import topic has the consumers the app relies on. A consumer missing here means
// its rows are never written — the side effect silently stops happening — so the list is
// pinned.
func TestImportConsumersCoverEveryTopic(t *testing.T) {
	box := outbox.New(nil, nil)
	importConsumers{}.register(box) // handlers only close over the services; none run here
	want := []string{
		"book.imported → audioserver.cache",
		"book.imported → requests.ready",
		"movie.changed → convert",
		"movie.changed → plex.scan",
		"movie.changed → subtitles",
		"movie.imported → convert",
		"movie.imported → plex.scan",
		"movie.imported → requests.ready",
		"movie.imported → subtitles",
		"series.imported → convert",
		"series.imported → plex.scan",
		"series.imported → requests.ready",
		"series.imported → subtitles",
	}
	if got := box.Topics(); !reflect.DeepEqual(got, want) {
		t.Fatalf("registered consumers:\n got %v\nwant %v", got, want)
	}
}

type recordScans struct{ got []string }

func (r *recordScans) Request(kind, dir string) {
	r.got = append(r.got, kind+"|"+filepath.ToSlash(dir))
}

// Imports, upgrades, renames and deletes each queue a Plex scan of the right folder,
// through the outbox, so a busy moment or a restart can't lose one.
func TestPlexScanConsumers(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()
	box := outbox.New(st.DB(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	rec := &recordScans{}
	importConsumers{
		plex: rec,
		showFolder: func(_ context.Context, id int64) string {
			if id == 7 {
				return filepath.FromSlash("/tv/The Bear (2022)")
			}
			return "" // no files yet
		},
	}.registerPlex(box)

	enqueue := func(topic string, p any) {
		if err := box.Enqueue(ctx, st.DB(), topic, p, ""); err != nil {
			t.Fatal(err)
		}
	}
	enqueue(outbox.TopicMovieImported, outbox.MovieImported{MovieID: 1, Path: filepath.FromSlash("/movies/Heat (1995)/Heat.mkv"), Upgrade: true})
	enqueue(outbox.TopicMovieChanged, outbox.MovieChanged{MovieID: 2, Change: outbox.ChangeRenamed,
		OldPath: filepath.FromSlash("/movies/Old (2001)/a.mkv"), Path: filepath.FromSlash("/movies/New (2001)/a.mkv")})
	enqueue(outbox.TopicMovieChanged, outbox.MovieChanged{MovieID: 3, Change: outbox.ChangeDeleted, Path: filepath.FromSlash("/movies/Gone (1999)/g.mkv")})
	enqueue(outbox.TopicSeriesImported, outbox.SeriesImported{SeriesID: 7, Episodes: []outbox.Episode{{Season: 1, Episode: 1}}})
	enqueue(outbox.TopicSeriesImported, outbox.SeriesImported{SeriesID: 8})
	if _, err := box.Drain(ctx); err != nil {
		t.Fatal(err)
	}
	sort.Strings(rec.got)
	want := []string{
		"movie|/movies/Gone (1999)",
		"movie|/movies/Heat (1995)",
		"movie|/movies/New (2001)",
		"movie|/movies/Old (2001)",
		"show|/tv/The Bear (2022)",
	}
	if !reflect.DeepEqual(rec.got, want) {
		t.Fatalf("scans = %v\nwant %v", rec.got, want)
	}
	if s, err := box.Stats(ctx); err != nil || s.Failed != 0 || s.Pending != 0 {
		t.Fatalf("stats %+v, %v — a Plex scan consumer finishes at once and never fails", s, err)
	}
}
