package movies

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func sparseVideo(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	if err := f.Truncate(60 << 20); err != nil { // sparse: no real bytes written
		t.Fatal(err)
	}
}

// A listing whose browser has already gone doesn't walk at all.
func TestManualImportCandidatesCancelled(t *testing.T) {
	dir := t.TempDir()
	sparseVideo(t, filepath.Join(dir, "Film.2020.mkv"))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got, _, err := (&Service{}).ManualImportCandidates(ctx, dir, 500)
	if !errors.Is(err, context.Canceled) || len(got) != 0 {
		t.Errorf("cancelled: %d candidates, err %v", len(got), err)
	}
}

func TestManualImportCandidatesCapped(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 4; i++ {
		sparseVideo(t, filepath.Join(dir, fmt.Sprintf("Film.%d.mkv", 2000+i)))
	}
	s := &Service{}
	got, truncated, err := s.ManualImportCandidates(context.Background(), dir, 3)
	if err != nil || !truncated || len(got) != 3 {
		t.Errorf("cap 3: %d, truncated %v, err %v", len(got), truncated, err)
	}
	got, truncated, err = s.ManualImportCandidates(context.Background(), dir, 500)
	if err != nil || truncated || len(got) != 4 {
		t.Errorf("under the cap: %d, truncated %v, err %v", len(got), truncated, err)
	}
}
