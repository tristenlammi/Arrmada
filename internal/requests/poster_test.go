package requests

import (
	"context"
	"log/slog"
	"sync"
	"testing"

	"github.com/tristenlammi/arrmada/internal/push"
	"github.com/tristenlammi/arrmada/internal/store"
)

// imagePush records the poster each Web Push carries.
type imagePush struct {
	mu     sync.Mutex
	images []string
	urls   []string
}

func (p *imagePush) SendToUserAsync(_ int64, m push.Message) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.images = append(p.images, m.Image)
	p.urls = append(p.urls, m.URL)
}

// A ready notice carries its movie's poster on the inbox row, the Web Push (whose tap
// target is unchanged) and the personal Apprise message; a show gets its own poster.
func TestReadyNoticeCarriesPoster(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	s := &Service{repo: NewRepo(st.DB()), log: slog.Default()}
	pushes := &imagePush{}
	s.push = pushes
	var attached []string
	s.userApprise.resolver = fixedResolver{"push.example.com": "93.184.216.34"}
	s.userApprise.send = func(_ context.Context, _, _, _, poster string) error { attached = append(attached, poster); return nil }
	ctx := context.Background()
	if _, err := st.DB().ExecContext(ctx, `INSERT INTO users (id, username, role, password_hash, apprise_url) VALUES (7, 'kid', 'requester', 'x', 'gotify://push.example.com/token')`); err != nil {
		t.Fatal(err)
	}
	movie := "https://image.tmdb.org/t/p/w500/dune.jpg"
	show := "https://image.tmdb.org/t/p/w500/andor.jpg"
	for _, r := range []Request{
		{MediaType: "movie", TMDBID: 1, Title: "Dune", PosterURL: movie, Status: StatusApproved, RequestedBy: 7},
		{MediaType: "series", TMDBID: 2, Title: "Andor", PosterURL: show, Status: StatusApproved, RequestedBy: 7},
	} {
		if _, err := s.repo.Create(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.notifyRequester(ctx, "movie", 1, ""); err != nil {
		t.Fatal(err)
	}
	if err := s.notifyRequester(ctx, "series", 2, ""); err != nil {
		t.Fatal(err)
	}
	inbox, err := s.repo.listUserNotifications(ctx, 7)
	if err != nil || len(inbox) != 2 {
		t.Fatalf("inbox = %+v (%v)", inbox, err)
	}
	got := map[string]string{}
	for _, n := range inbox {
		got[n.Ref] = n.PosterURL
	}
	if got["movie:1"] != movie || got["series:2"] != show {
		t.Errorf("inbox posters = %v", got)
	}
	pushes.mu.Lock()
	defer pushes.mu.Unlock()
	if len(pushes.images) != 2 || pushes.images[0] != movie || pushes.images[1] != show {
		t.Errorf("push images = %v", pushes.images)
	}
	if pushes.urls[0] != "/discover/movie/1" || pushes.urls[1] != "/discover/series/2" {
		t.Errorf("push tap targets changed: %v", pushes.urls)
	}
	if len(attached) != 2 || attached[0] != movie || attached[1] != show {
		t.Errorf("apprise attachments = %v", attached)
	}
}

// Nothing a person shouldn't see, or can't be fetched safely, goes out as a poster: the
// adult filter hides it, only TMDB's image host is allowed, books have none, and a notice
// stored before posters existed reads back without one.
func TestPosterForWithholds(t *testing.T) {
	s := &Service{}
	ctx := context.Background()
	ok := "https://image.tmdb.org/t/p/w500/a.jpg"
	for name, c := range map[string]struct {
		req  Request
		want string
	}{
		"movie":         {Request{MediaType: "movie", Title: "Dune", PosterURL: ok}, ok},
		"adult title":   {Request{MediaType: "movie", Title: "Brazzers Live", PosterURL: ok}, ""},
		"other host":    {Request{MediaType: "movie", Title: "Dune", PosterURL: "https://evil.example.com/a.jpg"}, ""},
		"internal host": {Request{MediaType: "series", Title: "Andor", PosterURL: "https://192.168.1.5/a.jpg"}, ""},
		"plain http":    {Request{MediaType: "movie", Title: "Dune", PosterURL: "http://image.tmdb.org/t/p/w500/a.jpg"}, ""},
		"no poster":     {Request{MediaType: "movie", Title: "Dune"}, ""},
		"book":          {Request{MediaType: "book", Title: "Dune", PosterURL: ok}, ""},
		"private route": {Request{MediaType: "movie", Title: "Dune", PosterURL: "/api/v1/movies/1/poster"}, ""},
	} {
		if got := s.posterFor(ctx, c.req); got != c.want {
			t.Errorf("%s: posterFor = %q, want %q", name, got, c.want)
		}
	}

	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	r := NewRepo(st.DB())
	if _, err := st.DB().ExecContext(ctx, `INSERT INTO user_notifications (user_id, title, body, media_type, ref, created_at) VALUES (7, 'Your request is ready', '“Dune” is ready to watch.', 'movie', 'movie:1', 1)`); err != nil {
		t.Fatal(err)
	}
	inbox, err := r.listUserNotifications(ctx, 7)
	if err != nil || len(inbox) != 1 || inbox[0].PosterURL != "" {
		t.Errorf("old notice = %+v (%v), want no poster", inbox, err)
	}
}
