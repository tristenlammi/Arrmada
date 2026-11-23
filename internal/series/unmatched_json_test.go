package series

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tristenlammi/arrmada/internal/metadata"
	"github.com/tristenlammi/arrmada/internal/store"
)

// Issue #136: a folder the scan found no candidates for went on the needs-review list with
// "candidates": null, and Settings → Library crashed reading it. The list lives in memory,
// so a reload didn't help — only a restart did. After a scan and a manual pick, the list
// (as the page receives it) has a list of candidates on every folder, and is itself a list
// once it's empty.
func TestUnmatchedListIsNeverNullAfterScanAndPick(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	tv := t.TempDir()
	show := func(id int, title string, year int) metadata.SeriesDetails {
		return metadata.SeriesDetails{SeriesResult: metadata.SeriesResult{TMDBID: id, Title: title, Year: year}, Seasons: listing(1)}
	}
	// Two shows share a name, so a folder named without a year gets candidates to pick
	// from; "Unknown Thing" matches nothing and gets none.
	meta := shelfMeta{shows: map[int]metadata.SeriesDetails{
		1: show(1, "Gamma Show", 2010), 2: show(2, "Gamma Show", 2020), 3: show(3, "Delta Show", 2015),
	}}
	touchEpisode(t, filepath.Join(tv, "Gamma Show", "Season 01", "Gamma Show - S01E01.mkv"))
	touchEpisode(t, filepath.Join(tv, "Unknown Thing", "Season 01", "Unknown Thing - S01E01.mkv"))
	svc := NewService(st.DB(), meta, "", slog.New(slog.NewTextHandler(io.Discard, nil)))
	svc.SetRootFunc(func() string { return tv })

	page := func() string { // what GET /series/unmatched sends
		b, err := json.Marshal(map[string]any{"unmatched": svc.LastUnmatched()})
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	if got := page(); got != `{"unmatched":[]}` {
		t.Errorf("before any scan the list = %s, want []", got)
	}

	res, err := svc.ScanLibrary(context.Background(), "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Unmatched) != 2 {
		t.Fatalf("scan left %d folders to review, want 2: %+v", len(res.Unmatched), res.Unmatched)
	}
	if b, _ := json.Marshal(res); strings.Contains(string(b), "null") {
		t.Errorf("scan result has a null: %s", b)
	}
	if got := page(); strings.Contains(got, "null") {
		t.Errorf("needs-review list after the scan has a null: %s", got)
	}

	// The manual pick for the folder that had candidates; the one without stays listed.
	if err := svc.ImportFolderAs(context.Background(), "", "Gamma Show", 2); err != nil {
		t.Fatal(err)
	}
	got := page()
	if strings.Contains(got, "null") || !strings.Contains(got, `"folder":"Unknown Thing"`) || !strings.Contains(got, `"candidates":[]`) {
		t.Errorf("needs-review list after the pick = %s, want Unknown Thing with candidates []", got)
	}

	if err := svc.ImportFolderAs(context.Background(), "", "Unknown Thing", 3); err != nil {
		t.Fatal(err)
	}
	if got := page(); got != `{"unmatched":[]}` {
		t.Errorf("needs-review list once everything is picked = %s, want []", got)
	}
}
