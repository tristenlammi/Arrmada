package httpapi

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/tristenlammi/arrmada/internal/config"
	"github.com/tristenlammi/arrmada/internal/store"
)

func fileAPI(t *testing.T) (*api, string) {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	lib := t.TempDir()
	return &api{deps: Deps{
		Store:  st,
		Log:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		Config: config.Config{LibraryDir: lib, TVDir: filepath.Join(lib, "tvshows")},
	}}, lib
}

// The whole point of the panel: a library file traced back to the torrent it arrived
// in, across the imports → grabs join that nothing had ever walked.
func TestFileSourceJoinsImportToGrab(t *testing.T) {
	a, lib := fileAPI(t)
	ctx := context.Background()
	db := a.deps.Store.DB()
	target := filepath.Join(lib, "tvshows", "Rookie", "Season 01", "S01E01.mkv")

	if _, err := db.ExecContext(ctx,
		`INSERT INTO imports (download_hash, source_path, target_path, title, size_bytes)
		 VALUES ('abc123', '/dl/The.Rookie.S01E01.mkv', ?, 'The Rookie S01E01', 100)`, target); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO grabs (movie_id, title, indexer, quality_profile, info_hash, manual, seed_enabled, seed_ratio, media_type)
		 VALUES (1, 'The.Rookie.S01E01.1080p.WEB-DL-GRP', 'TorrentLeech', '1080p Quality', 'abc123', 1, 1, 2.0, 'series')`); err != nil {
		t.Fatal(err)
	}

	src, note := a.fileSourceFor(ctx, target)
	if src == nil {
		t.Fatalf("no source found (note %q)", note)
	}
	if src.Release != "The.Rookie.S01E01.1080p.WEB-DL-GRP" {
		t.Errorf("release = %q, want the grabbed release name rather than the import's label", src.Release)
	}
	if src.Indexer != "TorrentLeech" || src.InfoHash != "abc123" {
		t.Errorf("got indexer %q hash %q", src.Indexer, src.InfoHash)
	}
	if !src.Manual {
		t.Error("manual flag lost — the UI needs to say the user picked this one")
	}
	if src.FromPack {
		t.Error("flagged as a pack, but the import named this exact file")
	}
}

// A season pack records ONE target_path for many imported episodes, so the other
// episodes have to find the torrent by their folder — and must be labelled as a pack
// rather than implying a grab of that one file.
func TestFileSourceFindsThePackAnEpisodeCameFrom(t *testing.T) {
	a, lib := fileAPI(t)
	ctx := context.Background()
	dir := filepath.Join(lib, "tvshows", "Rookie", "Season 01")
	recorded := filepath.Join(dir, "S01E01.mkv")
	sibling := filepath.Join(dir, "S01E05.mkv")

	if _, err := a.deps.Store.DB().ExecContext(ctx,
		`INSERT INTO imports (download_hash, source_path, target_path, title)
		 VALUES ('packhash', '/dl/The.Rookie.S01.COMPLETE', ?, 'The Rookie S01')`, recorded); err != nil {
		t.Fatal(err)
	}

	src, note := a.fileSourceFor(ctx, sibling)
	if src == nil {
		t.Fatalf("no source for a pack sibling (note %q)", note)
	}
	if src.InfoHash != "packhash" {
		t.Errorf("hash = %q, want the pack's", src.InfoHash)
	}
	if !src.FromPack {
		t.Error("not flagged as a pack — the UI would imply this file was grabbed on its own")
	}
}

// A file the user copied in by hand has no import row. That must read as "we don't
// know", not as an error or an empty panel.
func TestFileSourceSaysWhenThereIsNoRecord(t *testing.T) {
	a, lib := fileAPI(t)
	src, note := a.fileSourceFor(context.Background(), filepath.Join(lib, "tvshows", "Nothing", "here.mkv"))
	if src != nil {
		t.Errorf("invented a source: %+v", src)
	}
	if note == "" {
		t.Error("no explanation given — an empty panel with no reason is the thing being fixed")
	}
}

// The endpoint reads files off the host by path, so it must refuse anything outside a
// configured library root.
func TestFileInfoRefusesPathsOutsideTheLibrary(t *testing.T) {
	a, lib := fileAPI(t)
	for _, p := range []string{
		"/etc/passwd",
		filepath.Join(lib, "..", "elsewhere", "secret.txt"),
		lib + "-other/file.mkv", // prefix of the root, but a different folder
	} {
		if a.underLibraryRoot(p) {
			t.Errorf("%q was accepted as a library path", p)
		}
	}
	// Real library paths still pass, including the root itself.
	if err := os.MkdirAll(filepath.Join(lib, "tvshows"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{lib, filepath.Join(lib, "tvshows", "Show", "ep.mkv")} {
		if !a.underLibraryRoot(p) {
			t.Errorf("%q was rejected, but it's inside the library", p)
		}
	}
}
