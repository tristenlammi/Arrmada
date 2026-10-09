package httpapi

import (
	"path/filepath"
	"runtime"
	"testing"
)

// The downloads feed only reports free space it actually measured: a missing or unset
// folder is "no figure", never "0 GB free".
func TestFreeGBField(t *testing.T) {
	if _, ok := freeGBField(""); ok {
		t.Error("an unset folder reported a free-space figure")
	}
	if _, ok := freeGBField(filepath.Join(t.TempDir(), "does-not-exist")); ok {
		t.Error("a missing folder reported a free-space figure")
	}
	if runtime.GOOS != "linux" {
		t.Skip("disk usage is only measured on Linux")
	}
	if gb, ok := freeGBField(t.TempDir()); !ok || gb <= 0 {
		t.Errorf("temp dir: %v GB, ok=%v; want a real reading", gb, ok)
	}
}
