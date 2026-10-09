package httpapi

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tristenlammi/arrmada/internal/recyclebin"
	"github.com/tristenlammi/arrmada/internal/settings"
	"github.com/tristenlammi/arrmada/internal/store"
)

// Empty takes an optional bin: none (or no body at all, as older pages send) empties
// every bin; an unknown one is refused rather than emptying everything.
func TestRecycleEmptyOneBinOrAll(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	bin := t.TempDir()
	item := filepath.Join(bin, "Film", "film.mkv")
	if err := os.MkdirAll(filepath.Dir(item), 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(item, []byte("data"), 0o644)
	a := &api{deps: Deps{Log: log, Recycle: recyclebin.New(bin, settings.NewService(st.DB()), log)}}

	post := func(body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		var rd io.Reader
		if body != "" {
			rd = strings.NewReader(body)
		}
		a.handleRecycleEmpty(w, httptest.NewRequest(http.MethodPost, "/api/v1/recycle/empty", rd))
		return w
	}
	if w := post(`{"bin":"0123456789"}`); w.Code != http.StatusBadRequest {
		t.Fatalf("unknown bin: HTTP %d %s", w.Code, w.Body.String())
	}
	if _, err := os.Stat(item); err != nil {
		t.Fatal("an unknown bin emptied the real one")
	}
	if w := post(`{"bin":`); w.Code != http.StatusBadRequest {
		t.Errorf("broken body: HTTP %d", w.Code)
	}
	if w := post(""); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"freed_bytes":4`) {
		t.Fatalf("no body: HTTP %d %s", w.Code, w.Body.String())
	}
	if _, err := os.Stat(item); !os.IsNotExist(err) {
		t.Error("emptying every bin left the item")
	}
}
