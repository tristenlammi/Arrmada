package httpapi

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

// Saving a new Downloads folder tells the hook (which re-points qBittorrent) in the
// background; saving the folder already in use, or a blank that falls back to it, doesn't.
func TestFoldersChangedHookCalledOnDownloadsChange(t *testing.T) {
	a, base := folderTestAPI(t)
	calls := make(chan []string, 4)
	a.deps.OnFoldersChanged = func(ctx context.Context, changed []string) {
		if ctx.Err() != nil {
			t.Error("the hook got a context that was already done — it must outlive the request")
		}
		calls <- changed
	}
	next := func() []string {
		t.Helper()
		select {
		case c := <-calls:
			return c
		case <-time.After(5 * time.Second):
			t.Fatal("the hook was never called")
			return nil
		}
	}
	none := func() {
		t.Helper()
		select {
		case c := <-calls:
			t.Fatalf("hook called with %v, want no call", c)
		case <-time.After(200 * time.Millisecond):
		}
	}

	newDL := filepath.Join(base, "torrents")
	if err := os.MkdirAll(newDL, 0o755); err != nil {
		t.Fatal(err)
	}
	if w := putPaths(t, a, map[string]any{"downloads": newDL, "tv": a.deps.Config.TVDir}); w.Code != http.StatusOK {
		t.Fatalf("save: %d %s", w.Code, w.Body.String())
	}
	if got := next(); !reflect.DeepEqual(got, []string{"downloads"}) {
		t.Errorf("changed = %v, want only downloads (tv was resent unchanged)", got)
	}

	// The same folder again: nothing moved.
	if w := putPaths(t, a, map[string]any{"downloads": newDL}); w.Code != http.StatusOK {
		t.Fatalf("resave: %d", w.Code)
	}
	none()

	// Blank goes back to the environment's folder, which is a real change.
	if w := putPaths(t, a, map[string]any{"downloads": ""}); w.Code != http.StatusOK {
		t.Fatalf("blank: %d", w.Code)
	}
	if got := next(); !reflect.DeepEqual(got, []string{"downloads"}) {
		t.Errorf("changed = %v, want downloads after going back to the default", got)
	}
	if got := a.roots().Downloads(context.Background()); got != a.deps.Config.DownloadsDir {
		t.Errorf("downloads now %q, want the environment's %q", got, a.deps.Config.DownloadsDir)
	}
}
