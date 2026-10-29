package download

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// fakeIncompleteQbit is a qBittorrent that saves to save and, when on, downloads into
// temp first. info is its torrent list.
func fakeIncompleteQbit(t *testing.T, save, temp string, on *bool, mu *sync.Mutex, info string) string {
	t.Helper()
	var logins int32
	srv := qbitTestServer(t, &logins, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch r.URL.Path {
		case "/api/v2/app/preferences":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"save_path": save, "temp_path": temp, "temp_path_enabled": *on, "listen_port": 6881,
			})
		case "/api/v2/torrents/info":
			fmt.Fprint(w, info)
		case "/api/v2/app/version":
			fmt.Fprint(w, "v5.0.0")
		default:
			http.NotFound(w, r)
		}
	})
	return srv.URL
}

// A qBittorrent with "Default Download Path (incomplete torrents)" on: Check paths lists
// the incomplete folder as advisory and names the setting, but the report stays OK —
// imports wait for the move out of it, so Arrmada needn't see it. A torrent mid-move isn't
// taken for the newest finished download (its content path still names the incomplete
// folder, which would read as a download Arrmada can't see). With the setting off, there's
// no such line.
func TestPathCheckNamesTheIncompleteFolder(t *testing.T) {
	ctx := context.Background()
	save := t.TempDir() // the save folder, at the same path in both containers
	if err := os.MkdirAll(filepath.Join(save, "Film.2020"), 0o755); err != nil {
		t.Fatal(err)
	}
	info := fmt.Sprintf(`[
	  {"hash":"aaa","name":"Film.2020","progress":1.0,"state":"stalledUP","category":"arrmada",
	   "amount_left":0,"added_on":100,"content_path":%q},
	  {"hash":"bbb","name":"Show.S01.1080p","progress":1.0,"state":"moving","category":"arrmada-tv",
	   "amount_left":0,"added_on":200,"content_path":"/incomplete/Show.S01.1080p"}
	]`, filepath.Join(save, "Film.2020"))

	var mu sync.Mutex
	on := true
	svc, _ := newTestService(t)
	url := fakeIncompleteQbit(t, save, "/incomplete", &on, &mu, info)
	c, err := svc.Create(ctx, Client{Name: "qBittorrent", Kind: KindQbittorrent, URL: url, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}

	rep, err := svc.TestPaths(ctx, c.ID, PathEnv{})
	if err != nil {
		t.Fatal(err)
	}
	if !rep.OK {
		t.Fatalf("an incomplete folder Arrmada can't see must not fail the check: %+v", rep)
	}
	if len(rep.Checks) != 3 {
		t.Fatalf("checks = %+v, want save folder, newest download, incomplete folder", rep.Checks)
	}
	if nd := rep.Checks[1]; nd.What != "newest_download" || nd.Remote != filepath.Join(save, "Film.2020") || !nd.Visible {
		t.Errorf("newest download = %+v, want the seeding film (not the torrent mid-move)", nd)
	}
	inc := rep.Checks[2]
	if inc.What != "incomplete_folder" || inc.Remote != "/incomplete" || inc.Visible || !inc.Advisory {
		t.Errorf("incomplete folder = %+v", inc)
	}
	for _, want := range []string{"downloads into /incomplete first", "Default Download Path (incomplete torrents)", "Arrmada waits for that move"} {
		if !strings.Contains(rep.Summary, want) {
			t.Errorf("summary %q doesn't say %q", rep.Summary, want)
		}
	}

	// Turned off: torrents finish where Arrmada imports from, and nothing is said.
	mu.Lock()
	on = false
	mu.Unlock()
	rep, err = svc.TestPaths(ctx, c.ID, PathEnv{})
	if err != nil {
		t.Fatal(err)
	}
	for _, pc := range rep.Checks {
		if pc.What == "incomplete_folder" {
			t.Errorf("incomplete folder reported while it's off: %+v", rep.Checks)
		}
	}
	if strings.Contains(rep.Summary, "incomplete") {
		t.Errorf("summary mentions the incomplete folder while it's off: %q", rep.Summary)
	}
}
