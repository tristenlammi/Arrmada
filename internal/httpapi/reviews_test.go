package httpapi

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/tristenlammi/arrmada/internal/automation"
	"github.com/tristenlammi/arrmada/internal/books"
	"github.com/tristenlammi/arrmada/internal/music"
	"github.com/tristenlammi/arrmada/internal/store"
)

func reviewAPI(t *testing.T) (*api, *store.Store) {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	co := automation.New(nil, nil, nil, nil, st.DB(), nil, log, "")
	co.SetBooks(books.NewService(st.DB(), nil, log))
	co.SetMusic(music.NewService(st.DB(), nil, log))
	return &api{deps: Deps{Store: st, Log: log, Automation: co}}, st
}

func postImport(a *api, id int64, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/reviews/"+strconv.FormatInt(id, 10)+"/import", strings.NewReader(body))
	req.SetPathValue("id", strconv.FormatInt(id, 10))
	if body == "" {
		req.ContentLength = 0
	}
	rec := httptest.NewRecorder()
	a.handleImportReview(rec, req)
	return rec
}

// A review with no item and no target, or a target of the wrong kind, is the user's to
// fix — 422 with a readable reason, never a 500. An empty target_kind (a page loaded
// before kinds existed) is accepted and treated as the review's own kind.
func TestImportReviewStatuses(t *testing.T) {
	a, st := reviewAPI(t)
	res, err := st.DB().Exec(`INSERT INTO import_reviews (hash, name, content_path, media_type, expected_id, reason)
		VALUES ('h', 'Some Album', ?, 'music', 0, 'held')`, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()

	rec := postImport(a, id, "")
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "choose one") {
		t.Errorf("no target: %d %s", rec.Code, rec.Body.String())
	}
	rec = postImport(a, id, `{"target_id": 0, "target_kind": ""}`)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("empty kind, no target: %d %s", rec.Code, rec.Body.String())
	}
	rec = postImport(a, id, `{"target_id": 7, "target_kind": "movie"}`)
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "not a movie") {
		t.Errorf("wrong kind: %d %s", rec.Code, rec.Body.String())
	}
	rec = postImport(a, 9999, "")
	if rec.Code != http.StatusNotFound {
		t.Errorf("missing review: %d %s", rec.Code, rec.Body.String())
	}
}

func TestReviewErrorStatus(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want int
	}{
		{automation.ErrNeedsTarget, http.StatusUnprocessableEntity},
		{automation.ErrWrongTargetKind, http.StatusUnprocessableEntity},
		{automation.ErrNothingToImport, http.StatusUnprocessableEntity},
		{automation.ErrDownloadGone, http.StatusGone},
		{automation.ErrModuleOff, http.StatusConflict},
		{automation.ErrReviewNotFound, http.StatusNotFound},
		{io.ErrUnexpectedEOF, http.StatusInternalServerError},
	} {
		if got := reviewErrorStatus(tc.err); got != tc.want {
			t.Errorf("%v → %d, want %d", tc.err, got, tc.want)
		}
	}
}
