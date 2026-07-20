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

// The guard rails exist to stop a library churning through an endless ladder of
// barely-better files. Both a proportional floor and an absolute one must be cleared,
// whatever margin the profile asks for.
func TestUpgradeGuardRails(t *testing.T) {
	// A 60-minute episode currently at ~7 Mbps (3 GiB).
	const runtime = 60
	current := Encode{SizeGB: 3.0}

	cases := []struct {
		name   string
		candGB float64
		want   bool
		why    string
	}{
		{"barely bigger", 3.1, false, "~3% better is noise, exactly the churn to prevent"},
		{"just under the floor", 3.5, false, "~17%, still short of the 20% minimum"},
		{"10 percent bigger", 3.3, false, "under the 20% proportional floor"},
		{"25 percent bigger", 3.75, true, "clears both floors — a real step up"},
		{"much bigger", 6.0, true, "obviously better"},
		{"smaller", 2.0, false, "never replace with less"},
		{"identical", 3.0, false, "no change is not an upgrade"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// A tiny configured margin must not defeat the floors.
			if got := passesUpgradeFloors(c.candGB, current.SizeGB, runtime, 1); got != c.want {
				t.Errorf("with a 1%% setting: got %v, want %v — %s", got, c.want, c.why)
			}
		})
	}
}

// At low bitrates the proportional floor alone is a very small number, so the absolute
// floor is what stops churn there.
func TestAbsoluteFloorMattersAtLowBitrates(t *testing.T) {
	// A 30-minute episode at ~0.6 Mbps (0.13 GiB). 20% of that is ~0.12 Mbps.
	const runtime = 30
	if passesUpgradeFloors(0.16, 0.13, runtime, 1) {
		t.Error("a ~0.1 Mbps gain cleared the proportional floor but must fail the absolute one")
	}
	// A genuinely large jump still passes.
	if !passesUpgradeFloors(0.5, 0.13, runtime, 1) {
		t.Error("a several-fold increase should be an upgrade")
	}
}

// passesUpgradeFloors mirrors IsBitrateUpgrade's arithmetic without needing a database,
// so the thresholds themselves can be exercised directly. pct is what the profile asks
// for; the floors apply on top of it.
func passesUpgradeFloors(candGB, curGB float64, runtimeMin int, pct float64) bool {
	candBr := BitrateMbps(candGB, runtimeMin)
	curBr := BitrateMbps(curGB, runtimeMin)
	if pct < MinUpgradePercent {
		pct = MinUpgradePercent
	}
	return candBr >= curBr*(1+pct/100) && candBr >= curBr+MinUpgradeMarginMbps
}

// Codec normalization: a bigger x264 file is not automatically better than a smaller x265
// one. Comparing raw bitrates would swap a good encode for a worse one.
func TestCodecNormalizationInComparisons(t *testing.T) {
	// 4 GiB of x265 is worth ~1.6x its raw bitrate in H.264-equivalent terms.
	x265 := BitrateMbps(4, 60) * codecEfficiency("x265")
	x264 := BitrateMbps(5, 60) * codecEfficiency("x264")
	if x265 <= x264 {
		t.Errorf("4 GiB x265 (%.1f) should out-rank 5 GiB x264 (%.1f) in H.264-equivalent terms", x265, x264)
	}
}
