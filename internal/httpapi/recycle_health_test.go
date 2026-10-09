package httpapi

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tristenlammi/arrmada/internal/library"
	"github.com/tristenlammi/arrmada/internal/recyclebin"
)

// The health panel names an old shared bin still holding files, once, and says nothing
// about a per-library bin that is where it should be.
func TestSystemHealthRecycleBins(t *testing.T) {
	a, base := folderTestAPI(t)
	healthAPI(t, a)
	legacy := filepath.Join(base, "volume", ".recycle")
	if err := os.MkdirAll(filepath.Join(legacy, "Old Film"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "Old Film", "old.mkv"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	movies := a.deps.Config.MoviesDir
	a.deps.Recycle = recyclebin.NewBins(func() []library.BinDir {
		return []library.BinDir{
			{Dir: legacy, Label: "Old shared bin", Legacy: true, Serves: []string{movies}},
			{Dir: filepath.Join(movies, library.RecycleDirName), Label: "Movies", Serves: []string{movies}},
		}
	}, a.deps.Settings, a.deps.Log)

	var legacyLines, copyLines int
	for _, w := range healthWarnings(t, a) {
		if strings.Contains(w.Message, "old shared recycle bin") && strings.Contains(w.Message, legacy) {
			legacyLines++
		}
		if strings.Contains(w.Message, "every delete is a full copy") {
			copyLines++
		}
	}
	if legacyLines != 1 {
		t.Errorf("legacy bin lines = %d, want 1", legacyLines)
	}
	if copyLines != 0 {
		t.Errorf("a per-library bin (or the legacy one) was reported as a copy on every delete")
	}
}
