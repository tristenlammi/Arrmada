package diskspace

import (
	"path/filepath"
	"runtime"
	"testing"
)

// Two temp folders share a filesystem; /proc is always its own. Only Linux can tell.
func TestSameDevice(t *testing.T) {
	if runtime.GOOS != "linux" {
		if _, ok := SameDevice(t.TempDir(), t.TempDir()); ok {
			t.Fatal("off Linux the answer must be unknown")
		}
		t.Skip("filesystem ids are only read on Linux")
	}
	base := t.TempDir()
	a, b := filepath.Join(base, "a"), filepath.Join(base, "not", "made", "yet")
	if same, ok := SameDevice(a, b); !ok || !same {
		t.Errorf("two folders in one temp dir: same=%v ok=%v, want same", same, ok)
	}
	if same, ok := SameDevice(base, "/proc/self"); !ok || same {
		t.Errorf("temp dir vs /proc: same=%v ok=%v, want different", same, ok)
	}
	if _, ok := SameDevice("", base); ok {
		t.Error("an empty path can't be judged")
	}
}
