package requests

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"testing"

	"github.com/tristenlammi/arrmada/internal/movies"
	"github.com/tristenlammi/arrmada/internal/outbox"
	"github.com/tristenlammi/arrmada/internal/store"
)

// readyFixture is a requests service over a scratch DB with a movie (tmdb 123) that user
// 7 asked for, and an outbox whose requests.ready consumer is the real NotifyMovieReady.
func readyFixture(t *testing.T) (*Service, *outbox.Outbox, int64) {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	mv := movies.NewService(st.DB(), nil, nil, t.TempDir(), "", nil, log)
	s := &Service{repo: NewRepo(st.DB()), movies: mv, log: log}
	ctx := context.Background()
	m, err := movies.NewRepo(st.DB()).Create(ctx, movies.Movie{TMDBID: 123, Title: "Dune", Year: 2021, Monitored: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.repo.Create(ctx, Request{MediaType: "movie", TMDBID: 123, Title: "Dune", Status: StatusApproved, RequestedBy: 7}); err != nil {
		t.Fatal(err)
	}
	box := outbox.New(st.DB(), log)
	box.Register(outbox.TopicMovieImported, "requests.ready", func(ctx context.Context, raw json.RawMessage) error {
		var p outbox.MovieImported
		if err := json.Unmarshal(raw, &p); err != nil {
			return err
		}
		return s.NotifyMovieReady(ctx, p.MovieID)
	})
	return s, box, m.ID
}

// The requester hears from the outbox row alone — no bus, no ten-minute sweep — and a
// second row for the same movie (a retried attach, an upgrade) doesn't ping them twice.
func TestReadyNotificationFromOutboxRow(t *testing.T) {
	s, box, movieID := readyFixture(t)
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		if err := box.Enqueue(ctx, s.repo.db, outbox.TopicMovieImported, outbox.MovieImported{MovieID: movieID}, ""); err != nil {
			t.Fatal(err)
		}
		if _, err := box.Drain(ctx); err != nil {
			t.Fatal(err)
		}
	}
	inbox, err := s.repo.listUserNotifications(ctx, 7)
	if err != nil {
		t.Fatal(err)
	}
	if len(inbox) != 1 || inbox[0].Ref != "movie:123" {
		t.Fatalf("inbox = %+v, want one ready notification", inbox)
	}
	if st, _ := box.Stats(ctx); st.Pending != 0 || st.Failed != 0 {
		t.Fatalf("rows left behind: %+v", st)
	}
}

// When the inbox can't be written the handler reports it, so the row is retried instead
// of the requester silently never hearing; a movie deleted meanwhile is simply done.
func TestNotifyMovieReadyErrorsAreRetryable(t *testing.T) {
	s, _, movieID := readyFixture(t)
	ctx := context.Background()
	if _, err := s.repo.db.Exec(`CREATE TRIGGER inbox_down BEFORE INSERT ON user_notifications BEGIN SELECT RAISE(ABORT, 'disk full'); END`); err != nil {
		t.Fatal(err)
	}
	if err := s.NotifyMovieReady(ctx, movieID); err == nil {
		t.Fatal("a failed inbox write was reported as success; the outbox would drop the message")
	}
	if _, err := s.repo.db.Exec(`DROP TRIGGER inbox_down`); err != nil {
		t.Fatal(err)
	}
	if err := s.NotifyMovieReady(ctx, movieID); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if err := s.NotifyMovieReady(ctx, 9999); err != nil {
		t.Fatalf("a movie that no longer exists should be done, got %v", err)
	}
}
