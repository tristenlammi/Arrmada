package download

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/tristenlammi/arrmada/internal/connstatus"
	"github.com/tristenlammi/arrmada/internal/store"
)

// Each enabled client's queue read is recorded on its own: the closed one is failing, the
// one that answered is OK, and neither is paused (clients are shown, never skipped).
// Deleting a client forgets it.
func TestQueueCompleteRecordsEachClient(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			_, _ = io.WriteString(w, "Ok.")
		case "/api/v2/torrents/info":
			_, _ = io.WriteString(w, "[]")
		default:
			http.NotFound(w, r)
		}
	}))
	defer up.Close()
	down := httptest.NewServer(http.NotFoundHandler())
	downURL := down.URL
	down.Close()

	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc := NewService(st.DB(), log)
	tr := connstatus.New(st.DB(), log)
	svc.SetStatus(tr)
	ctx := context.Background()
	good, err := svc.Create(ctx, Client{Name: "bundled", Kind: KindQbittorrent, URL: up.URL, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	bad, err := svc.Create(ctx, Client{Name: "seedbox", Kind: KindQbittorrent, URL: downURL, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}

	if _, complete, err := svc.QueueComplete(ctx); err != nil || complete {
		t.Fatalf("QueueComplete: complete=%v err=%v", complete, err)
	}
	gs, _ := tr.Get(connstatus.KindDownloadClient, strconv.FormatInt(good.ID, 10))
	bs, _ := tr.Get(connstatus.KindDownloadClient, strconv.FormatInt(bad.ID, 10))
	if gs.ConsecutiveFailures != 0 || gs.LastOKAt.IsZero() {
		t.Errorf("answering client: %+v", gs)
	}
	if bs.ConsecutiveFailures != 1 || bs.LastError == "" || !bs.BackoffUntil.IsZero() {
		t.Errorf("closed client: %+v", bs)
	}

	if err := svc.Delete(ctx, bad.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok := tr.Get(connstatus.KindDownloadClient, strconv.FormatInt(bad.ID, 10)); ok {
		t.Error("a deleted client's status should be forgotten")
	}
}
