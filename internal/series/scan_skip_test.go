package series

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/tristenlammi/arrmada/internal/store"
)

// The TV library scan never offers a show from the recycle bin: every folder it looks at
// is searched for, and nothing in .arrmada-recycle may be.
func TestSeriesScanSkipsRecycleDir(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	root := t.TempDir()
	for _, p := range []string{
		filepath.Join(root, "Kept Show (2020)", "Season 01", "Kept Show - S01E01.mkv"),
		filepath.Join(root, ".arrmada-recycle", "Gone Show (2019)", "Gone Show - S01E01.mkv"),
	} {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		f, err := os.Create(p)
		if err != nil {
			t.Fatal(err)
		}
		_ = f.Truncate(200 << 20) // sparse: big enough to count as an episode, no disk used
		_ = f.Close()
	}
	svc := NewService(st.DB(), &fakeMeta{}, "", slog.New(slog.NewTextHandler(io.Discard, nil)))
	svc.SetRootFunc(func() string { return root })
	res, err := svc.ScanLibrary(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	// fakeMeta finds nothing, so every folder scanned ends up unmatched.
	if len(res.Unmatched) != 1 || res.Unmatched[0].Folder != "Kept Show (2020)" {
		t.Fatalf("unmatched = %+v, want only the kept show (the bin must not be scanned)", res.Unmatched)
	}
}
