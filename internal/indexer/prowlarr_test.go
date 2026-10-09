package indexer

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
)

// fakeProwlarr serves Prowlarr's API from testdata/prowlarr_indexers.json (Prowlarr 2.x's
// IndexerResource shape). Tests change its list between syncs to play out renames,
// disables and removals upstream.
type fakeProwlarr struct {
	mu         sync.Mutex
	list       []map[string]any
	status     int    // when set, /api/v1/indexer answers with this status
	raw        string // when set, /api/v1/indexer answers with this body
	proxies    []map[string]any
	proxyPosts int
	srv        *httptest.Server
}

func newFakeProwlarr(t *testing.T) *fakeProwlarr {
	t.Helper()
	b, err := os.ReadFile("testdata/prowlarr_indexers.json")
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeProwlarr{}
	if err := json.Unmarshal(b, &f.list); err != nil {
		t.Fatal(err)
	}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeProwlarr) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Header.Get("X-Api-Key") == "" {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	switch r.Method + " " + r.URL.Path {
	case "GET /api/v1/indexer":
		if f.status != 0 {
			w.WriteHeader(f.status)
			return
		}
		if f.raw != "" {
			_, _ = w.Write([]byte(f.raw))
			return
		}
		_ = json.NewEncoder(w).Encode(f.list)
	case "GET /api/v1/indexerproxy":
		_ = json.NewEncoder(w).Encode(append([]map[string]any{}, f.proxies...))
	case "GET /api/v1/indexerproxy/schema":
		_, _ = w.Write([]byte(`[{"implementation":"Http","fields":[]},{"implementation":"FlareSolverr","fields":[{"name":"host","value":""},{"name":"requestTimeout","value":60}]}]`))
	case "POST /api/v1/indexerproxy":
		var p map[string]any
		_ = json.NewDecoder(r.Body).Decode(&p)
		f.proxyPosts++
		f.proxies = append(f.proxies, p)
		_ = json.NewEncoder(w).Encode(p)
	case "GET /api/v1/tag":
		_, _ = w.Write([]byte(`[]`))
	case "POST /api/v1/tag":
		_, _ = w.Write([]byte(`{"id":1,"label":"flaresolverr"}`))
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

// posts is how many proxies were added.
func (f *fakeProwlarr) posts() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.proxyPosts
}

// edit changes the upstream indexer with Prowlarr id pid; fn returning false removes it.
func (f *fakeProwlarr) edit(pid int, fn func(m map[string]any) bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := f.list[:0]
	for _, m := range f.list {
		if int(m["id"].(float64)) == pid && !fn(m) {
			continue
		}
		out = append(out, m)
	}
	f.list = out
}

func (f *fakeProwlarr) sync(t *testing.T, s *Service) SyncResult {
	t.Helper()
	res, err := s.SyncProwlarr(context.Background(), ProwlarrSync{URL: f.srv.URL, APIKey: "prowlarr-key"})
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	return res
}

// byProwlarrID lists the rows keyed by their Prowlarr id.
func byProwlarrID(t *testing.T, s *Service) map[int]Indexer {
	t.Helper()
	list, err := s.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	out := map[int]Indexer{}
	for _, idx := range list {
		if idx.ProwlarrID > 0 {
			out[idx.ProwlarrID] = idx
		}
	}
	return out
}

// (e)(h) A first sync brings in the enabled torrent indexers, scoped by their categories
// and with Prowlarr's priority; usenet and disabled ones stay out.
func TestProwlarrSyncImportsTorrentIndexers(t *testing.T) {
	f := newFakeProwlarr(t)
	s, _, _ := healthService(t)
	res := f.sync(t, s)
	if res.Added != 3 || res.SkippedUsenet != 1 || res.Updated != 0 || res.Disabled != 0 {
		t.Fatalf("result = %+v", res)
	}
	rows := byProwlarrID(t, s)
	if len(rows) != 3 {
		t.Fatalf("rows = %+v", rows)
	}
	if _, ok := rows[3]; ok {
		t.Fatal("a usenet indexer was imported")
	}
	if _, ok := rows[5]; ok {
		t.Fatal("an indexer disabled in Prowlarr was imported")
	}
	tl := rows[1]
	if tl.Name != "Prowlarr · TorrentLeech" || tl.Kind != KindTorznab || tl.URL != f.srv.URL+"/1/api" || tl.Priority != 10 || !tl.Enabled {
		t.Fatalf("TorrentLeech row = %+v", tl)
	}
	if !tl.SeedEnabled || tl.SeedHours != DefaultSeedHours {
		t.Fatalf("a new row must seed by default: %+v", tl)
	}
	if want := []string{MediaMovie, MediaSeries}; !reflect.DeepEqual(tl.MediaTypes, want) {
		t.Fatalf("TorrentLeech media = %v, want %v", tl.MediaTypes, want)
	}
	if want := []string{MediaBook}; !reflect.DeepEqual(rows[2].MediaTypes, want) {
		t.Fatalf("an audiobook + ebook tracker is a book indexer, got %v", rows[2].MediaTypes)
	}
	if rows[4].MediaTypes != nil || rows[4].Priority != 25 {
		t.Fatalf("every area = unscoped, and an out-of-range priority = 25: %+v", rows[4])
	}
}

// (a) A re-sync leaves what the owner set alone: scoping, priority, seeder floor, seed
// rules, and a row they switched off.
func TestProwlarrResyncKeepsOwnerSettings(t *testing.T) {
	f := newFakeProwlarr(t)
	s, _, _ := healthService(t)
	ctx := context.Background()
	f.sync(t, s)
	rows := byProwlarrID(t, s)

	tl := rows[1]
	tl.MediaTypes = []string{MediaBook}
	tl.Priority, tl.MinSeeders = 3, 7
	tl.SeedRatio, tl.SeedHours = 2.5, 48
	if err := s.Update(ctx, tl); err != nil {
		t.Fatal(err)
	}
	off := rows[4]
	off.Enabled = false
	if err := s.Update(ctx, off); err != nil {
		t.Fatal(err)
	}

	res := f.sync(t, s)
	if res.Added != 0 || res.Reenabled != 0 || res.Unchanged != 3 {
		t.Fatalf("result = %+v", res)
	}
	after := byProwlarrID(t, s)
	got := after[1]
	if !reflect.DeepEqual(got.MediaTypes, []string{MediaBook}) || got.Priority != 3 || got.MinSeeders != 7 || got.SeedRatio != 2.5 || got.SeedHours != 48 {
		t.Fatalf("owner's settings were overwritten: %+v", got)
	}
	if after[4].Enabled || after[4].DisabledBy != DisabledByUser {
		t.Fatalf("a row the owner switched off was switched back on: %+v", after[4])
	}
}

// (b) Disabled in Prowlarr → off here with the note; enabled again → back on. A row the
// owner switched off stays off either way.
func TestProwlarrUpstreamDisableAndReenable(t *testing.T) {
	f := newFakeProwlarr(t)
	s, _, _ := healthService(t)
	ctx := context.Background()
	f.sync(t, s)

	mine := byProwlarrID(t, s)[4]
	mine.Enabled = false
	if err := s.Update(ctx, mine); err != nil {
		t.Fatal(err)
	}
	for _, pid := range []int{1, 4} {
		f.edit(pid, func(m map[string]any) bool { m["enable"] = false; return true })
	}
	res := f.sync(t, s)
	if res.Disabled != 1 {
		t.Fatalf("result = %+v", res)
	}
	rows := byProwlarrID(t, s)
	if rows[1].Enabled || rows[1].DisabledBy != DisabledByProwlarr || rows[1].ManagedNote != "Disabled in Prowlarr" {
		t.Fatalf("upstream disable: %+v", rows[1])
	}
	if rows[4].DisabledBy != DisabledByUser {
		t.Fatalf("the owner's own switch-off was taken over by the sync: %+v", rows[4])
	}

	for _, pid := range []int{1, 4} {
		f.edit(pid, func(m map[string]any) bool { m["enable"] = true; return true })
	}
	res = f.sync(t, s)
	if res.Reenabled != 1 {
		t.Fatalf("result = %+v", res)
	}
	rows = byProwlarrID(t, s)
	if !rows[1].Enabled || rows[1].DisabledBy != "" || rows[1].ManagedNote != "" {
		t.Fatalf("upstream re-enable: %+v", rows[1])
	}
	if rows[4].Enabled {
		t.Fatal("a row the owner switched off was switched on by the sync")
	}
}

// (c) Removed from Prowlarr → switched off with a note, never deleted.
func TestProwlarrRemovedUpstreamIsDisabledNotDeleted(t *testing.T) {
	f := newFakeProwlarr(t)
	s, _, _ := healthService(t)
	f.sync(t, s)
	f.edit(2, func(map[string]any) bool { return false })
	res := f.sync(t, s)
	if res.Disabled != 1 {
		t.Fatalf("result = %+v", res)
	}
	row, ok := byProwlarrID(t, s)[2]
	if !ok {
		t.Fatal("a row removed upstream was deleted")
	}
	if row.Enabled || row.DisabledBy != DisabledByProwlarr || row.ManagedNote != "Removed from Prowlarr" {
		t.Fatalf("removed upstream: %+v", row)
	}
}

// (d) A rename in Prowlarr renames the same row and the grabs and blocklist rows that
// name it; no duplicate appears.
func TestProwlarrRenameKeepsRowAndRewritesGrabs(t *testing.T) {
	f := newFakeProwlarr(t)
	s, _, _ := healthService(t)
	ctx := context.Background()
	f.sync(t, s)
	before := byProwlarrID(t, s)[1]
	db := s.repo.db
	if _, err := db.ExecContext(ctx, `INSERT INTO grabs (movie_id, title, indexer) VALUES (1, 'Some.Movie.2024.1080p', ?)`, before.Name); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO blocklist (norm_title, title, indexer) VALUES ('x', 'X', ?)`, before.Name); err != nil {
		t.Fatal(err)
	}

	f.edit(1, func(m map[string]any) bool { m["name"] = "TL"; return true })
	res := f.sync(t, s)
	if res.Added != 0 || res.Updated != 1 {
		t.Fatalf("result = %+v", res)
	}
	list, _ := s.List(ctx)
	if len(list) != 3 {
		t.Fatalf("a rename added a row: %d rows", len(list))
	}
	after := byProwlarrID(t, s)[1]
	if after.ID != before.ID || after.Name != "Prowlarr · TL" {
		t.Fatalf("renamed row = %+v (was id %d)", after, before.ID)
	}
	for _, table := range []string{"grabs", "blocklist"} {
		var name string
		if err := db.QueryRowContext(ctx, `SELECT indexer FROM `+table).Scan(&name); err != nil {
			t.Fatal(err)
		}
		if name != "Prowlarr · TL" {
			t.Fatalf("%s still names %q", table, name)
		}
	}
}

// (e) A Newznab row synced before usenet was skipped is switched off with the reason.
func TestProwlarrSwitchesOffSyncedUsenetRow(t *testing.T) {
	f := newFakeProwlarr(t)
	s, _, _ := healthService(t)
	ctx := context.Background()
	old, err := s.Create(ctx, Indexer{Name: "Prowlarr · NZBgeek", Kind: KindNewznab, URL: f.srv.URL + "/3/api", APIKey: "prowlarr-key", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	res := f.sync(t, s)
	if res.SkippedUsenet != 1 || res.Disabled != 1 {
		t.Fatalf("result = %+v", res)
	}
	got, _ := s.Get(ctx, old.ID)
	if got.Enabled || got.ManagedNote != "Needs a usenet download client" || got.ProwlarrID != 3 {
		t.Fatalf("usenet row = %+v", got)
	}
}

// (f) Rows synced before prowlarr_id existed get it from their feed address, and the next
// sync updates them instead of adding duplicates.
func TestProwlarrBackfillLinksOldRows(t *testing.T) {
	f := newFakeProwlarr(t)
	s, _, _ := healthService(t)
	ctx := context.Background()
	old, _ := s.Create(ctx, Indexer{Name: "Prowlarr · TorrentLeech", Kind: KindTorznab, URL: f.srv.URL + "/1/api", APIKey: "prowlarr-key", Enabled: true, MediaTypes: []string{MediaMovie}})
	other, _ := s.Create(ctx, Indexer{Name: "Elsewhere", Kind: KindTorznab, URL: "http://jackett:9117/api/v2.0/indexers/x/results/torznab/api", Enabled: true})
	// Renamed in Prowlarr since, and on a different address: found by name.
	byName, _ := s.Create(ctx, Indexer{Name: "Prowlarr · 1337x", Kind: KindTorznab, URL: "http://old-host:9696/4/api", Enabled: true})

	n, err := s.BackfillProwlarrIDs(ctx, f.srv.URL+"/")
	if err != nil || n != 1 {
		t.Fatalf("backfill = %d, %v", n, err)
	}
	if got, _ := s.Get(ctx, old.ID); got.ProwlarrID != 1 {
		t.Fatalf("backfill didn't link the synced row: %+v", got)
	}
	if got, _ := s.Get(ctx, other.ID); got.ProwlarrID != 0 {
		t.Fatalf("backfill linked an unrelated row: %+v", got)
	}

	res := f.sync(t, s)
	if res.Added != 1 { // only MyAnonamouse is new
		t.Fatalf("result = %+v", res)
	}
	got, _ := s.Get(ctx, old.ID)
	if !reflect.DeepEqual(got.MediaTypes, []string{MediaMovie}) {
		t.Fatalf("a backfilled row lost its scoping: %+v", got)
	}
	if got, _ := s.Get(ctx, byName.ID); got.ProwlarrID != 4 || got.URL != f.srv.URL+"/4/api" {
		t.Fatalf("name fallback = %+v", got)
	}
}

// (g) An external Prowlarr gets no FlareSolverr proxy unless asked, and the result only
// claims one when Prowlarr lists it afterwards.
func TestProwlarrFlareSolverrProxyOnlyWhenAsked(t *testing.T) {
	f := newFakeProwlarr(t)
	s, _, _ := healthService(t)
	ctx := context.Background()
	res, err := s.SyncProwlarr(ctx, ProwlarrSync{URL: f.srv.URL, APIKey: "k", FlareSolverrURL: "http://arrmada-flaresolverr:8191"})
	if err != nil {
		t.Fatal(err)
	}
	if f.posts() != 0 || res.FlareSolverrReady {
		t.Fatalf("external Prowlarr: posts %d, ready %v", f.posts(), res.FlareSolverrReady)
	}

	res, err = s.SyncProwlarr(ctx, ProwlarrSync{URL: f.srv.URL, APIKey: "k", AddFlareSolverrProxy: true})
	if err != nil {
		t.Fatal(err)
	}
	if f.posts() != 0 || res.FlareSolverrReady || len(res.Notes) == 0 {
		t.Fatalf("asked with no FlareSolverr set up: posts %d, %+v", f.posts(), res)
	}

	res, err = s.SyncProwlarr(ctx, ProwlarrSync{URL: f.srv.URL, APIKey: "k", FlareSolverrURL: "http://arrmada-flaresolverr:8191/", AddFlareSolverrProxy: true})
	if err != nil {
		t.Fatal(err)
	}
	if f.posts() != 1 || !res.FlareSolverrReady {
		t.Fatalf("asked: posts %d, ready %v", f.posts(), res.FlareSolverrReady)
	}
	if !isBundledProwlarr("http://arrmada-prowlarr:9696") || isBundledProwlarr(f.srv.URL) {
		t.Fatal("bundled host check")
	}
}

// (i) A Prowlarr that errors, answers garbage or lists nothing while rows from it exist
// changes nothing.
func TestProwlarrFailedOrEmptyListChangesNothing(t *testing.T) {
	f := newFakeProwlarr(t)
	s, _, _ := healthService(t)
	ctx := context.Background()
	f.sync(t, s)
	before, _ := s.List(ctx)

	for _, tc := range []struct {
		name   string
		status int
		raw    string
		want   string
	}{
		{"http 500", http.StatusInternalServerError, "", "HTTP 500"},
		{"garbage", 0, "<html>", "couldn't read"},
		{"empty", 0, "[]", "nothing changed"},
		{"null", 0, "null", "nothing changed"},
	} {
		f.mu.Lock()
		f.status, f.raw = tc.status, tc.raw
		f.mu.Unlock()
		_, err := s.SyncProwlarr(ctx, ProwlarrSync{URL: f.srv.URL, APIKey: "prowlarr-key"})
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%s: err = %v", tc.name, err)
		}
		after, _ := s.List(ctx)
		if !reflect.DeepEqual(before, after) {
			t.Fatalf("%s: rows changed:\n%+v\n%+v", tc.name, before, after)
		}
	}
}

// An empty Prowlarr with nothing synced from it yet isn't an error, just a note.
func TestProwlarrEmptyFirstSyncIsANote(t *testing.T) {
	f := newFakeProwlarr(t)
	f.raw = "[]"
	s, _, _ := healthService(t)
	res := f.sync(t, s)
	if res.Added != 0 || len(res.Notes) == 0 {
		t.Fatalf("result = %+v", res)
	}
}

// Who switched a row off: the owner turning it off is theirs; turning it on clears it;
// saving a row that stays off (a pill click) keeps a sync's mark so the sync can undo it.
func TestRepoUpdateRecordsWhoDisabled(t *testing.T) {
	ctx := context.Background()
	repo := newRepo(t)
	idx, _ := repo.Create(ctx, Indexer{Name: "X", Kind: KindTorznab, URL: "http://x/api", Enabled: true})

	idx.Enabled = false
	if err := repo.Update(ctx, idx); err != nil {
		t.Fatal(err)
	}
	if got, _ := repo.Get(ctx, idx.ID); got.DisabledBy != DisabledByUser {
		t.Fatalf("1→0: %+v", got)
	}
	idx.Enabled = true
	_ = repo.Update(ctx, idx)
	if got, _ := repo.Get(ctx, idx.ID); got.DisabledBy != "" || !got.Enabled {
		t.Fatalf("0→1: %+v", got)
	}

	if err := setManagedState(ctx, repo.db, idx.ID, false, DisabledByProwlarr, "Removed from Prowlarr"); err != nil {
		t.Fatal(err)
	}
	idx.Enabled = false
	idx.MediaTypes = []string{MediaMovie}
	_ = repo.Update(ctx, idx)
	if got, _ := repo.Get(ctx, idx.ID); got.DisabledBy != DisabledByProwlarr || got.ManagedNote != "Removed from Prowlarr" {
		t.Fatalf("0→0 must keep the sync's mark: %+v", got)
	}
	idx.Enabled = true
	_ = repo.Update(ctx, idx)
	if got, _ := repo.Get(ctx, idx.ID); got.DisabledBy != "" || got.ManagedNote != "" {
		t.Fatalf("turning it on clears the mark: %+v", got)
	}
}

func TestProwlarrIDFromURL(t *testing.T) {
	cases := map[string]int{
		"http://p:9696/12/api":  12,
		"http://P:9696/12/api/": 12,
		"http://p:9696/12":      0,
		"http://p:9696/x/api":   0,
		"http://q:9696/12/api":  0,
		"http://p:9696/0/api":   0,
	}
	for in, want := range cases {
		got, ok := prowlarrIDFromURL("http://p:9696/", in)
		if got != want || ok != (want > 0) {
			t.Errorf("%s = %d, %v", in, got, ok)
		}
	}
}
