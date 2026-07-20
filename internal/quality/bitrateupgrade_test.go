package quality

import "testing"

// The grab path and the import path must agree on what counts as a bitrate upgrade.
// They didn't: the searcher would find a same-resolution higher-bitrate release, grab it,
// download it, and the importer would refuse to place it for not raising the resolution.
func TestBitrateMbpsMatchesTheGrabPathMath(t *testing.T) {
	// 3 GiB over a 60-minute episode.
	got := BitrateMbps(3, 60)
	if got < 6.5 || got > 7.5 {
		t.Errorf("BitrateMbps(3GiB, 60min) = %.2f, expected roughly 7 Mbps", got)
	}
	// Half the size at the same runtime is half the bitrate.
	if half := BitrateMbps(1.5, 60); half < got/2-0.01 || half > got/2+0.01 {
		t.Errorf("BitrateMbps should scale linearly with size: %.2f vs %.2f", half, got)
	}
	// Twice the runtime at the same size is half the bitrate.
	if long := BitrateMbps(3, 120); long < got/2-0.01 || long > got/2+0.01 {
		t.Errorf("BitrateMbps should scale inversely with runtime: %.2f vs %.2f", long, got)
	}
	// Missing runtime can't produce a bitrate.
	if BitrateMbps(3, 0) != 0 {
		t.Error("no runtime means no computable bitrate")
	}
}
