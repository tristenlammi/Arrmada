package automation

import (
	"math"
	"testing"

	"github.com/tristenlammi/arrmada/internal/download"
)

// A season pack that had uploaded 4 MB of 3.65 GB was deleted one day into a 28-day seed
// goal, because qBittorrent reported its ratio as the MAX_RATIO sentinel and 9999 clears a
// target of 2 instantly. The ratio now comes from the byte counters.
func TestSeedRatioOfIgnoresTheClientsSentinel(t *testing.T) {
	const gb int64 = 1 << 30
	const hawkBytes int64 = 3919163392 // 3.65 GiB

	// The real case: the client's own Ratio field said "infinite", the bytes say 0.001.
	hawk := download.Item{
		Name:          "The Hawk S01 Complete 1080p WEBRip 10Bit DDP5 1 x265-NeoNoir",
		Ratio:         9999, // qBittorrent's MAX_RATIO
		UploadedBytes: 4 << 20, TransferredBytes: hawkBytes, DownloadedBytes: hawkBytes,
	}
	got := hawk.SeedRatio()
	if want := float64(4<<20) / float64(hawkBytes); math.Abs(got-want) > 1e-9 {
		t.Errorf("ratio = %v, want %v (uploaded ÷ downloaded, not the client's field)", got, want)
	}
	if got >= 2 {
		t.Errorf("ratio %v would still clear a target of 2 — the seed goal is not met", got)
	}

	// Data already on disk: nothing was pulled from peers, so fall back to the completed
	// size. A real denominator keeps the ratio small, which keeps the torrent seeding.
	preexisting := download.Item{Ratio: 9999, UploadedBytes: 1 << 20, TransferredBytes: 0, DownloadedBytes: 2 * gb}
	if r := preexisting.SeedRatio(); r < 0 || r >= 1 {
		t.Errorf("pre-existing data: ratio = %v, want a small positive number", r)
	}

	// No honest denominator at all → refuse to judge, so only the time goal can end it.
	// Erring toward seeding too long costs disk; erring short costs a tracker ban.
	blank := download.Item{Ratio: 9999, UploadedBytes: 5 << 20}
	if r := blank.SeedRatio(); r >= 0 {
		t.Errorf("unknown transfer: ratio = %v, want -1", r)
	}

	// A genuinely well-seeded torrent still reports a ratio that meets the goal.
	seeded := download.Item{UploadedBytes: 8 * gb, TransferredBytes: 4 * gb, DownloadedBytes: 4 * gb}
	if r := seeded.SeedRatio(); r < 2 {
		t.Errorf("well-seeded: ratio = %v, want >= 2", r)
	}
}
