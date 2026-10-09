package library

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// An import that would replace a library file the recycle bin can't take is refused, and
// the old file is left byte-for-byte as it was — overwriting it was a permanent delete
// in disguise. The bin here is a regular file, so creating anything under it fails on
// every OS.
func TestImporterReplacementRefusedWhenBinFails(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(t.TempDir(), "bin")
	if err := os.WriteFile(bin, []byte("a file, not a folder"), 0o644); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(dir, "new.mkv")
	dst := filepath.Join(dir, "lib", "movie.mkv")
	writeFile(t, src, 2000)
	writeFile(t, dst, 1000)
	before, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}

	im := NewImporter(dir, quiet())
	im.SetRecycleDir(bin)
	if _, err := im.linkOrCopy(src, dst); !errors.Is(err, ErrReplacementRefused) {
		t.Fatalf("err = %v, want ErrReplacementRefused", err)
	}
	after, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("the old file is gone: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Error("the old file was overwritten although the bin refused it")
	}
}

// A first import (nothing to replace) doesn't care about the bin at all.
func TestImporterNewFileIgnoresBrokenBin(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(t.TempDir(), "bin")
	if err := os.WriteFile(bin, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(dir, "new.mkv")
	dst := filepath.Join(dir, "lib", "movie.mkv")
	writeFile(t, src, 2000)
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatal(err)
	}
	im := NewImporter(dir, quiet())
	im.SetRecycleDir(bin)
	if _, err := im.linkOrCopy(src, dst); err != nil {
		t.Fatalf("a fresh import must not need the bin: %v", err)
	}
}

// CheckBin passes a bin that is off, and refuses one that can't be created.
func TestCheckBin(t *testing.T) {
	if err := CheckBin(SingleBin(""), "/x/y.mkv"); err != nil {
		t.Errorf("bin off: %v", err)
	}
	if err := CheckBin(SingleBin(filepath.Join(t.TempDir(), "bin")), "/x/y.mkv"); err != nil {
		t.Errorf("good bin: %v", err)
	}
	broken := filepath.Join(t.TempDir(), "bin")
	if err := os.WriteFile(broken, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := CheckBin(SingleBin(broken), "/x/y.mkv"); !errors.Is(err, ErrBinRefused) {
		t.Errorf("broken bin: err = %v, want ErrBinRefused", err)
	}
}
