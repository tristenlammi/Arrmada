package audiobook

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// Exercises the real ffmpeg on real files. Skipped unless ARRMADA_MERGE_DIR points at a
// folder of fixtures, so CI without media stays green.
func TestMergeAgainstRealFFmpeg(t *testing.T) {
	dir := os.Getenv("ARRMADA_MERGE_DIR")
	if dir == "" || !Available() {
		t.Skip("set ARRMADA_MERGE_DIR and have ffmpeg on PATH")
	}
	ctx := context.Background()
	for _, c := range []struct {
		name     string
		files    []string
		wantCopy bool
	}{
		{"matching AAC", []string{"aac1.m4a", "aac2.m4a"}, true},
		{"mixed MP3", []string{"m1.mp3", "m2.mp3"}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			paths := make([]string, len(c.files))
			for i, f := range c.files {
				paths[i] = filepath.Join(dir, f)
			}
			p := PlanFor(ctx, paths)
			if p.Copy != c.wantCopy {
				t.Errorf("copy = %v, want %v (plan %+v)", p.Copy, c.wantCopy, p)
			}
			out := filepath.Join(dir, "out-"+c.name+".m4b")
			if err := Merge(ctx, paths, out); err != nil {
				t.Fatalf("merge: %v", err)
			}
			fi, err := os.Stat(out)
			if err != nil || fi.Size() == 0 {
				t.Fatalf("no output produced: %v", err)
			}
			t.Logf("plan %+v → %d bytes", p, fi.Size())
		})
	}
}
