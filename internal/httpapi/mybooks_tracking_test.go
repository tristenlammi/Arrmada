package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/tristenlammi/arrmada/internal/auth"
	"github.com/tristenlammi/arrmada/internal/books"
	"github.com/tristenlammi/arrmada/internal/download"
	"github.com/tristenlammi/arrmada/internal/movies"
	"github.com/tristenlammi/arrmada/internal/quality"
	"github.com/tristenlammi/arrmada/internal/requests"
	"github.com/tristenlammi/arrmada/internal/series"
)

// BOOK-08: My shelf's "Your requests" says where each request has got to — a book the
// searches keep missing reads "Not found yet" with the next check, not "Searching".
func TestMyBooksRequestCarriesStageAndNote(t *testing.T) {
	root := t.TempDir()
	s := newRouteServer(t, func(d *Deps) {
		db := d.Store.DB()
		d.Books = books.NewService(db, nil, d.Log)
		d.Downloads = download.NewService(db, d.Log) // no clients: an empty queue
		d.Quality = quality.NewService(db)
		mv := movies.NewService(db, nil, nil, root, "", nil, d.Log)
		sr := series.NewService(db, nil, root, d.Log)
		d.Requests = requests.NewService(db, mv, sr, d.Books, nil, d.Quality, nil, "", d.Log)
	})
	ctx := context.Background()
	u, cookie := s.user(t, "reader@example.com", auth.RoleRequester)
	b, err := books.NewRepo(s.st.DB()).Create(ctx, books.Book{OLKey: "OL1W", Title: "Dune", Author: "Frank Herbert", Monitored: true})
	if err != nil {
		t.Fatal(err)
	}
	last := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	if _, err := s.st.DB().Exec(`UPDATE books SET last_search_at = ?, search_misses = 3 WHERE id = ?`,
		last.Format("2006-01-02 15:04:05"), b.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.st.DB().Exec(`INSERT INTO requests (media_type, ol_key, title, author, status, requested_by, book_id)
		VALUES ('book', 'OL1W', 'Dune', 'Frank Herbert', 'approved', ?, ?)`, u.ID, b.ID); err != nil {
		t.Fatal(err)
	}
	rec := s.do("GET", "/api/v1/me/books", cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("HTTP %d: %s", rec.Code, rec.Body)
	}
	var got struct {
		Requests []MyRequest `json:"requests"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	want := last.Add(books.SearchWait(3)).Format(time.RFC3339)
	if len(got.Requests) != 1 {
		t.Fatalf("requests = %+v, want Dune", got.Requests)
	}
	if r := got.Requests[0]; r.Stage != requests.StageSearching || r.Note != "Not found yet" || r.NextCheckAt != want {
		t.Errorf("request = %+v, want searching / Not found yet / %s", r, want)
	}

	// The staff detail API carries the same search state.
	_, adminCookie := s.user(t, "admin@example.com", auth.RoleAdmin)
	rec = s.do("GET", "/api/v1/books/"+strconv.FormatInt(b.ID, 10), adminCookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("book detail: HTTP %d: %s", rec.Code, rec.Body)
	}
	var detail books.Book
	if err := json.Unmarshal(rec.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	if detail.SearchMisses != 3 || detail.LastSearchAt != last.Format(time.RFC3339) || detail.NextSearchAt != want {
		t.Errorf("detail search state = %q / %d / %q", detail.LastSearchAt, detail.SearchMisses, detail.NextSearchAt)
	}
}
