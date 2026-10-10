package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"testing"

	"github.com/tristenlammi/arrmada/internal/auth"
	"github.com/tristenlammi/arrmada/internal/books"
	"github.com/tristenlammi/arrmada/internal/jobs"
	"github.com/tristenlammi/arrmada/internal/metadata"
	"github.com/tristenlammi/arrmada/internal/movies"
	"github.com/tristenlammi/arrmada/internal/quality"
	"github.com/tristenlammi/arrmada/internal/requests"
	"github.com/tristenlammi/arrmada/internal/series"
)

// The Plex sign-in default: the saved per-type list; else the old toggle only when the
// owner set it (on: every type, off: none); else movies only.
func TestPlexAutoApprovalDefault(t *testing.T) {
	for name, tc := range map[string]struct {
		saved map[string]string
		want  auth.AutoApproval
	}{
		"untouched":         {nil, auth.AutoApproval{Movie: true}},
		"old toggle on":     {map[string]string{"plex_login_auto_approve": "true"}, auth.AllTypes(true)},
		"old toggle off":    {map[string]string{"plex_login_auto_approve": "false"}, auth.AutoApproval{}},
		"list saved":        {map[string]string{keyPlexAutoApproveTypes: "series", "plex_login_auto_approve": "true"}, auth.AutoApproval{Series: true}},
		"list saved, empty": {map[string]string{keyPlexAutoApproveTypes: ""}, auth.AutoApproval{}},
	} {
		got := plexAutoApprovalFrom(func(k string) (string, bool) { v, ok := tc.saved[k]; return v, ok })
		if got != tc.want {
			t.Errorf("%s: %+v, want %+v", name, got, tc.want)
		}
	}
}

// movieMeta answers any movie id with a fixed title, so an approval can add it.
type movieMeta struct{ metadata.MovieProvider }

func (movieMeta) GetMovie(_ context.Context, id int) (*metadata.MovieDetails, error) {
	return &metadata.MovieDetails{MovieResult: metadata.MovieResult{TMDBID: id, Title: "Heat", Year: 1995}, Status: "Released"}, nil
}

// noJobs takes approval searches and runs nothing.
type noJobs struct{}

func (noJobs) Submit(context.Context, jobs.Spec) (int64, bool, error) { return 1, false, nil }

// A movies-only requester's movie request approves itself; their series request waits.
func TestAutoApprovesByMediaTypeHandler(t *testing.T) {
	root := t.TempDir()
	s := newRouteServer(t, func(d *Deps) {
		db := d.Store.DB()
		d.Books = books.NewService(db, nil, d.Log)
		d.Quality = quality.NewService(db)
		mv := movies.NewService(db, movieMeta{}, nil, root, "", nil, d.Log)
		sr := series.NewService(db, nil, root, d.Log)
		d.Requests = requests.NewService(db, mv, sr, d.Books, nil, d.Quality, nil, "", d.Log)
		d.Requests.SetJobs(noJobs{})
	})
	kid, cookie := s.user(t, "kid@example.com", auth.RoleRequester)
	if err := s.auth.UpdateUser(context.Background(), kid.ID, auth.RoleRequester, auth.AutoApproval{Movie: true}); err != nil {
		t.Fatal(err)
	}
	status := func(body string) string {
		t.Helper()
		rec := s.doJSON("POST", "/api/v1/requests", cookie, body)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: HTTP %d %s", body, rec.Code, rec.Body)
		}
		var out struct {
			Request requests.Request `json:"request"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out.Request.Status
	}
	if got := status(`{"media_type":"movie","tmdb_id":949,"title":"Heat","year":1995}`); got != requests.StatusApproved {
		t.Errorf("movie request = %s, want approved", got)
	}
	if got := status(`{"media_type":"series","tmdb_id":1399,"title":"Show","year":2011}`); got != requests.StatusPending {
		t.Errorf("series request = %s, want pending", got)
	}
	rec := s.do("GET", "/api/v1/requests", cookie)
	var list struct {
		Types string `json:"auto_approve_types"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil || list.Types != "movie" {
		t.Errorf("auto_approve_types = %q (%v)", list.Types, err)
	}
}

// The users API takes and returns the per-type flags; the old bool still sets all three.
func TestUsersAPIAutoApproveTypes(t *testing.T) {
	s := requestsServer(t)
	_, admin := s.user(t, "admin@example.com", auth.RoleAdmin)
	rec := s.doJSON("POST", "/api/v1/users", admin, `{"email":"a@example.com","password":"password123","auto_approve_movie":true,"auto_approve_book":true}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: HTTP %d %s", rec.Code, rec.Body)
	}
	var u auth.User
	if err := json.Unmarshal(rec.Body.Bytes(), &u); err != nil {
		t.Fatal(err)
	}
	if u.AutoApproval() != (auth.AutoApproval{Movie: true, Book: true}) || u.AutoApprove {
		t.Fatalf("created = %+v", u)
	}
	path := "/api/v1/users/" + strconv.FormatInt(u.ID, 10)
	if rec := s.doJSON("PUT", path, admin, `{"auto_approve":true,"auto_approve_series":false}`); rec.Code != http.StatusOK {
		t.Fatalf("update: HTTP %d %s", rec.Code, rec.Body)
	}
	got, err := s.auth.UserByID(context.Background(), u.ID)
	if err != nil || got.AutoApproval() != (auth.AutoApproval{Movie: true, Book: true}) {
		t.Errorf("after update: %+v (%v), want movie and book (the flag overrides the old bool)", got, err)
	}
}
