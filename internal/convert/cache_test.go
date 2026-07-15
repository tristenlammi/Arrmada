package convert

import (
	"context"
	"testing"

	"github.com/tristenlammi/arrmada/internal/store"
)

// TestProbeCache verifies the cache stores and returns a probe result, and
// invalidates when the file's size or mtime changes.
func TestProbeCache(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	c := &probeCache{db: st.DB()}
	ctx := context.Background()
	path := "/library/movies/Some Movie (2020)/Some Movie (2020).mkv"
	mi := &MediaInfo{Container: "MKV", VideoCodec: "h264", Width: 1920, Height: 1080, SizeBytes: 5_000_000_000}

	// Miss on an empty cache.
	if _, ok := c.get(ctx, path, 5_000_000_000, 1000); ok {
		t.Fatal("expected a miss before anything is stored")
	}

	c.put(ctx, path, 5_000_000_000, 1000, mi)

	// Hit with matching size + mtime.
	got, ok := c.get(ctx, path, 5_000_000_000, 1000)
	if !ok {
		t.Fatal("expected a hit after storing")
	}
	if got.VideoCodec != "h264" || got.Width != 1920 || got.SizeBytes != 5_000_000_000 {
		t.Fatalf("cached info mismatch: %+v", got)
	}

	// Changed mtime → miss (file was modified).
	if _, ok := c.get(ctx, path, 5_000_000_000, 2000); ok {
		t.Error("expected a miss when mtime changed")
	}
	// Changed size → miss (file was replaced).
	if _, ok := c.get(ctx, path, 6_000_000_000, 1000); ok {
		t.Error("expected a miss when size changed")
	}

	// A re-put on the same path updates in place (no duplicate rows / no error).
	mi2 := &MediaInfo{Container: "MKV", VideoCodec: "hevc", SizeBytes: 2_500_000_000}
	c.put(ctx, path, 2_500_000_000, 3000, mi2)
	got2, ok := c.get(ctx, path, 2_500_000_000, 3000)
	if !ok || got2.VideoCodec != "hevc" {
		t.Fatalf("expected updated hevc entry, got ok=%v info=%+v", ok, got2)
	}
}
