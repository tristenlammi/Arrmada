package pathguard

import (
	"os"
	"path/filepath"
	"testing"
)

// symlink makes a link or skips the test where the OS won't allow it (Windows without
// developer mode); CI runs on Linux, where it always works.
func symlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks not permitted here: %v", err)
	}
}

func TestWithin(t *testing.T) {
	base := t.TempDir()
	lib := filepath.Join(base, "library")
	old := filepath.Join(base, "library-old")
	outside := filepath.Join(base, "outside")
	for _, d := range []string{lib, old, outside, filepath.Join(lib, "movies")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	cases := []struct {
		name string
		p    string
		want bool
	}{
		{"inside root", filepath.Join(lib, "movies"), true},
		{"the root itself", lib, true},
		{"missing file inside root", filepath.Join(lib, "movies", "new", "film.mkv"), true},
		{"sibling sharing a prefix", old, false},
		{"file in sibling sharing a prefix", filepath.Join(old, "x.mkv"), false},
		{"dot-dot escape", filepath.Join(lib, "..", "outside"), false},
		{"dot-dot escape written raw", lib + string(filepath.Separator) + ".." + string(filepath.Separator) + "outside", false},
		{"parent of root", base, false},
		{"empty path", "", false},
	}
	for _, c := range cases {
		if got := Within(c.p, lib); got != c.want {
			t.Errorf("%s: Within(%q) = %v, want %v", c.name, c.p, got, c.want)
		}
	}

	// Empty roots never contain anything, and don't stop later roots matching.
	if Within(filepath.Join(lib, "movies"), "") {
		t.Error("an empty root contained a path")
	}
	if !Within(filepath.Join(lib, "movies"), "", "  ", lib) {
		t.Error("an empty root stopped a later root from matching")
	}
	if !Under(filepath.Join(lib, "movies"), lib) || Under(outside, lib) {
		t.Error("Under disagrees with Within")
	}
}

func TestWithinFollowsSymlinks(t *testing.T) {
	base := t.TempDir()
	downloads := filepath.Join(base, "downloads")
	secret := filepath.Join(base, "secret")
	for _, d := range []string{downloads, secret} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(secret, "passwd"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	// A link inside downloads that points out of it is outside, whatever its name says.
	escape := filepath.Join(downloads, "escape")
	symlink(t, secret, escape)
	if Within(escape, downloads) {
		t.Error("a symlink pointing out of the root passed as inside it")
	}
	if Within(filepath.Join(escape, "passwd"), downloads) {
		t.Error("a file behind an escaping symlink passed as inside the root")
	}
	// A missing tail can't hide the hop: the existing parent is resolved first.
	if Within(filepath.Join(escape, "not", "there", "yet.mkv"), downloads) {
		t.Error("a missing path under a symlinked parent passed as inside the root")
	}
	// A dangling link is followed to where it would land.
	dangling := filepath.Join(downloads, "dangling")
	symlink(t, filepath.Join(secret, "nothing-here"), dangling)
	if Within(dangling, downloads) {
		t.Error("a dangling symlink pointing out of the root passed as inside it")
	}

	// A symlinked root is fine: both sides resolve to the same real folder.
	linkedRoot := filepath.Join(base, "dl-link")
	symlink(t, downloads, linkedRoot)
	if !Within(filepath.Join(downloads, "film.mkv"), linkedRoot) {
		t.Error("a path inside a symlinked root was rejected")
	}
	if !Within(filepath.Join(linkedRoot, "film.mkv"), downloads) {
		t.Error("a path reached through a symlinked root was rejected")
	}
}

func TestResolveMissingTail(t *testing.T) {
	base := t.TempDir()
	got, err := Resolve(filepath.Join(base, "a", "b", "c.txt"))
	if err != nil {
		t.Fatal(err)
	}
	realBase, _ := filepath.EvalSymlinks(base)
	if want := filepath.Join(realBase, "a", "b", "c.txt"); got != want {
		t.Errorf("Resolve = %q, want %q", got, want)
	}
}
