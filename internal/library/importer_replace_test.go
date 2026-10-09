package library

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// These pin what an import does when its target already exists, all on synthetic files
// in temp dirs. Before the content check, a different release of exactly the same size
// counted as "already imported": it was never placed, yet its name and quality were
// recorded against the old file.

// writeBytes writes data to p, creating its folder.
func writeBytes(t *testing.T, p string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

// patterned returns size bytes filled with b, with marker written at off — two files
// built with different markers differ only there.
func patterned(size int, b byte, off int, marker string) []byte {
	out := bytes.Repeat([]byte{b}, size)
	copy(out[off:], marker)
	return out
}

func readBytes(t *testing.T, p string) []byte {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// forceEXDEV makes every hardlink fail the way it does across filesystems, for the rest
// of the test.
func forceEXDEV(t *testing.T) {
	t.Helper()
	orig := linkFn
	linkFn = func(oldname, newname string) error {
		return &os.LinkError{Op: "link", Old: oldname, New: newname, Err: syscall.EXDEV}
	}
	t.Cleanup(func() { linkFn = orig })
}

func noTempLitter(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".arrmada-tmp") {
			t.Errorf("temp file left behind: %s", e.Name())
		}
	}
}

// (a) Same size, different content: the new release replaces the library file and the
// old one goes to the recycle bin. Covers both a small file (compared in full) and a large
// one (compared by samples) whose only difference is in the middle sample.
func TestReplaceSameSizeDifferentContent(t *testing.T) {
	for _, tc := range []struct {
		name string
		size int
		off  int
	}{
		{"small", 4096, 2000},
		{"large", 8 << 20, 4 << 20},
		{"large-tail", 8 << 20, 8<<20 - 10},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir, bin := t.TempDir(), t.TempDir()
			src := filepath.Join(dir, "dl", "new.mkv")
			dst := filepath.Join(dir, "lib", "movie.mkv")
			newData := patterned(tc.size, 0x11, tc.off, "NEW-RELEASE")
			oldData := patterned(tc.size, 0x11, tc.off, "OLD-RELEASE")
			writeBytes(t, src, newData)
			writeBytes(t, dst, oldData)

			im := NewImporter(dir, quiet())
			im.SetRecycleDir(bin)
			method, err := im.linkOrCopy(src, dst)
			if err != nil {
				t.Fatal(err)
			}
			if method != "hardlink" {
				t.Errorf("method = %q, want hardlink after recycling the old file", method)
			}
			if !bytes.Equal(readBytes(t, dst), newData) {
				t.Error("the library file still holds the old release")
			}
			if !bytes.Equal(readBytes(t, filepath.Join(bin, "lib", "movie.mkv")), oldData) {
				t.Error("the old release isn't in the recycle bin")
			}
		})
	}
}

// (b) A previous copy with identical content is left alone: "already", nothing recycled.
func TestReimportOverIdenticalCopyIsNoop(t *testing.T) {
	dir, bin := t.TempDir(), t.TempDir()
	src := filepath.Join(dir, "dl", "new.mkv")
	dst := filepath.Join(dir, "lib", "movie.mkv")
	data := patterned(4<<20, 0x22, 100, "SAME")
	writeBytes(t, src, data)
	writeBytes(t, dst, data) // a separate copy, not a hardlink

	im := NewImporter(dir, quiet())
	im.SetRecycleDir(bin)
	method, err := im.linkOrCopy(src, dst)
	if err != nil || method != "already" {
		t.Fatalf("method = %q, err = %v; want already", method, err)
	}
	if entries, _ := os.ReadDir(bin); len(entries) != 0 {
		t.Errorf("something was recycled: %v", entries)
	}
	if !bytes.Equal(readBytes(t, dst), data) {
		t.Error("the library file changed")
	}
}

// (c) A hardlink of the source (the same inode) is "already" — never re-copied, which
// would truncate the seeding torrent along with it.
func TestReimportOverHardlinkIsNoop(t *testing.T) {
	dir, bin := t.TempDir(), t.TempDir()
	src := filepath.Join(dir, "dl", "new.mkv")
	dst := filepath.Join(dir, "lib", "movie.mkv")
	data := patterned(4096, 0x33, 0, "LINKED")
	writeBytes(t, src, data)
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(src, dst); err != nil {
		t.Skipf("hardlinks unsupported here: %v", err)
	}
	im := NewImporter(dir, quiet())
	im.SetRecycleDir(bin)
	method, err := im.linkOrCopy(src, dst)
	if err != nil || method != "already" {
		t.Fatalf("method = %q, err = %v; want already", method, err)
	}
	if !bytes.Equal(readBytes(t, src), data) || !bytes.Equal(readBytes(t, dst), data) {
		t.Error("source or library file lost its data")
	}
	if entries, _ := os.ReadDir(bin); len(entries) != 0 {
		t.Errorf("something was recycled: %v", entries)
	}
}

// (d) Across filesystems the hardlink fails with EXDEV and the import copies instead:
// the library gets the exact bytes, and no temp file is left behind.
func TestCrossDeviceImportCopies(t *testing.T) {
	forceEXDEV(t)
	dir := t.TempDir()
	src := filepath.Join(dir, "dl", "new.mkv")
	dst := filepath.Join(dir, "lib", "movie.mkv")
	data := patterned(5<<20, 0x44, 12345, "CROSS-DEVICE")
	writeBytes(t, src, data)
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatal(err)
	}

	im := NewImporter(dir, quiet())
	method, err := im.linkOrCopy(src, dst)
	if err != nil {
		t.Fatal(err)
	}
	if method != "copy" {
		t.Errorf("method = %q, want copy", method)
	}
	if !bytes.Equal(readBytes(t, dst), data) {
		t.Error("the copy's bytes differ from the source")
	}
	if !bytes.Equal(readBytes(t, src), data) {
		t.Error("the source changed")
	}
	if a, b := mustStat(t, src), mustStat(t, dst); os.SameFile(a, b) {
		t.Error("the copy branch produced a hardlink")
	}
	noTempLitter(t, filepath.Dir(dst))

	// The copy replaces an older file the same way, through the bin.
	bin := t.TempDir()
	im.SetRecycleDir(bin)
	newer := patterned(5<<20, 0x44, 12345, "NEWER-RELEASE")
	writeBytes(t, src, newer)
	if method, err := im.linkOrCopy(src, dst); err != nil || method != "copy" {
		t.Fatalf("replacement: method = %q, err = %v", method, err)
	}
	if !bytes.Equal(readBytes(t, dst), newer) {
		t.Error("the replacement copy didn't land")
	}
	if !bytes.Equal(readBytes(t, filepath.Join(bin, "lib", "movie.mkv")), data) {
		t.Error("the replaced copy isn't in the bin")
	}
	noTempLitter(t, filepath.Dir(dst))
}

// (e) A copy that fails part-way returns the error, leaves the old library file exactly
// as it was, and cleans up its temp file. The source is a folder, which opens but can't
// be read, so the failure lands mid-copy on every OS.
func TestFailedCopyLeavesTargetIntact(t *testing.T) {
	forceEXDEV(t)
	dir := t.TempDir()
	src := filepath.Join(dir, "dl", "broken.mkv")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dir, "lib", "movie.mkv")
	old := patterned(1000, 0x55, 0, "OLD")
	writeBytes(t, dst, old)

	im := NewImporter(dir, quiet()) // bin off: the copy would overwrite in place by rename
	if _, err := im.linkOrCopy(src, dst); err == nil {
		t.Fatal("a copy from an unreadable source reported success")
	}
	if !bytes.Equal(readBytes(t, dst), old) {
		t.Error("the library file changed although the copy failed")
	}
	noTempLitter(t, filepath.Dir(dst))
}

// (f) A subtitle that differs from the one already in place, though it's the same size,
// is placed (beside it, as the next numbered variant) instead of being taken for the
// same file; an identical one is still skipped.
func TestSubtitleSameSizeDifferentContentIsPlaced(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "lib", "Movie (2020)", "Movie (2020) Bluray-1080p")
	existing := base + ".en.srt"
	writeBytes(t, existing, []byte("1\n00:00:01,000 --> 00:00:02,000\nHello there.\n"))

	src := filepath.Join(dir, "dl", "Movie.en.srt")
	different := []byte("1\n00:00:01,000 --> 00:00:02,000\nHullo there.\n")
	if len(different) != len(readBytes(t, existing)) {
		t.Fatal("setup: the subtitles must be the same size")
	}
	writeBytes(t, src, different)

	im := NewImporter(dir, quiet())
	im.placeSub(src, base, "en")
	placed := base + ".en.2.srt"
	if got, err := os.ReadFile(placed); err != nil || !bytes.Equal(got, different) {
		t.Fatalf("the different subtitle wasn't placed at %s: %v", filepath.Base(placed), err)
	}

	// The same subtitle again finds itself already there and adds nothing.
	im.placeSub(src, base, "en")
	if _, err := os.Stat(base + ".en.3.srt"); err == nil {
		t.Error("an identical subtitle was stacked as a duplicate")
	}
}

// sameContent itself: empty files never match, and a size mismatch never reads.
func TestSameContentEdges(t *testing.T) {
	dir := t.TempDir()
	a, b := filepath.Join(dir, "a"), filepath.Join(dir, "b")
	writeBytes(t, a, nil)
	writeBytes(t, b, nil)
	if same, err := sameContent(a, b, mustStat(t, a), mustStat(t, b)); same || err != nil {
		t.Errorf("two empty files: same=%v err=%v, want different", same, err)
	}
	writeBytes(t, b, []byte("x"))
	if same, _ := sameContent(a, b, mustStat(t, a), mustStat(t, b)); same {
		t.Error("different sizes matched")
	}
}

func mustStat(t *testing.T, p string) os.FileInfo {
	t.Helper()
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	return fi
}
