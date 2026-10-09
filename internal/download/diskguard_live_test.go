package download

import (
	"sync"
	"testing"

	"github.com/tristenlammi/arrmada/internal/diskspace"
)

// A Downloads folder changed in Settings is the one the guard reports and measures from
// its next pass, with no restart.
func TestDiskGuardFollowsDownloadsSetting(t *testing.T) {
	g, _, set, ctx := guardFixture(t, nil)
	if err := set.Set(ctx, KeyDiskGuard, "true"); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var measured []string
	g.usage = func(p string) (diskspace.Usage, bool) {
		mu.Lock()
		measured = append(measured, p)
		mu.Unlock()
		return diskspace.Usage{UsedPct: 10}, true
	}
	current := "/storage/torrents"
	g.SetDirFunc(func() string { return current })

	if got := g.Status(ctx).Path; got != "/storage/torrents" {
		t.Fatalf("status path = %q, want the live folder", got)
	}
	current = "  /cache/torrents  " // saved in Settings → Library (whitespace is trimmed)
	if got := g.Status(ctx).Path; got != "/cache/torrents" {
		t.Fatalf("status path after the change = %q", got)
	}
	if err := g.Check(ctx); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if last := measured[len(measured)-1]; last != "/cache/torrents" {
		t.Errorf("Check measured %q, want the new folder (all: %q)", last, measured)
	}
}
