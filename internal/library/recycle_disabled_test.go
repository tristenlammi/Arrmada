package library_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/tristenlammi/arrmada/internal/library"
)

// A disabled recycle bin used to produce a RELATIVE destination from filepath.Join(""), so
// the file was moved into the process working directory and reported as recycled — losing it
// on the next container update. It must refuse instead, and leave the file alone.
func TestRecycleFileRefusesWhenDisabled(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "Some Movie (1999)", "Some Movie.mkv")
	if err := os.MkdirAll(filepath.Dir(src), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(src, []byte("video"), 0o644); err != nil {
		t.Fatal(err)
	}

	dst, err := library.RecycleFile("", src)
	if !errors.Is(err, library.ErrRecycleDisabled) {
		t.Fatalf("want ErrRecycleDisabled, got dst=%q err=%v", dst, err)
	}
	if dst != "" {
		t.Fatalf("want no destination, got %q", dst)
	}
	if _, err := os.Stat(src); err != nil {
		t.Fatalf("source must be left untouched when the bin is off: %v", err)
	}
	// And nothing may have been created relative to the working directory.
	if _, err := os.Stat("Some Movie (1999)"); err == nil {
		_ = os.RemoveAll("Some Movie (1999)")
		t.Fatal("recycle wrote a relative directory into the working directory")
	}
}
