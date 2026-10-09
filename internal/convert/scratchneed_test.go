package convert

import (
	"context"
	"testing"
)

// An HEVC HDR10 file may turn out to carry HDR10+, whose pipeline holds the stream twice in
// scratch, so its estimate is doubled; the same file in SDR isn't.
func TestCandScratchDoublesForPossibleHDR10Plus(t *testing.T) {
	sdr := film("hevc", 3840, 2160, 60000, aud("truehd", "eng", 8))
	hdr := film("hevc", 3840, 2160, 60000, aud("truehd", "eng", 8))
	hdr.HDR = "HDR10"
	plan := Plan{VideoCodec: "hevc"}

	one, two := candScratch(*sdr, plan, 0), candScratch(*hdr, plan, 0)
	if want := scratchNeeded(sdr, plan, false); one != want {
		t.Fatalf("SDR need = %d, want %d", one, want)
	}
	if want := scratchNeeded(hdr, plan, true); two != want || two <= one {
		t.Fatalf("HDR10 need = %d, want the doubled %d (SDR %d)", two, want, one)
	}
	// An H.264 HDR10 file can't hold HDR10+ in the first place: not doubled.
	avc := film("h264", 3840, 2160, 60000, aud("truehd", "eng", 8))
	avc.HDR = "HDR10"
	if got := candScratch(*avc, plan, 0); got != scratchNeeded(avc, plan, false) {
		t.Fatalf("H.264 HDR10 need = %d, want the single figure", got)
	}
	// No size in the probe: the indexed size stands in.
	bare := *sdr
	bare.SizeBytes = 0
	if got := candScratch(bare, plan, sdr.SizeBytes); got != one {
		t.Fatalf("size from the index: %d, want %d", got, one)
	}
}

// computeCandidates records each file's scratch need, and ScratchNeed reports the largest
// among the files the runner would actually pick — a file waiting on a skip doesn't count.
func TestScratchNeedSkipsWaitingFiles(t *testing.T) {
	ctx := context.Background()
	s := newTestService(t)
	indexMovie(t, s, 1, "Remux", film("h264", 1920, 1080, 35000, aud("truehd", "eng", 8)))
	indexMovie(t, s, 2, "BluRay", film("h264", 1920, 1080, 12000, aud("ac3", "eng", 6)))

	cands := s.computeCandidates(ctx, s.prefs(ctx))
	if len(cands) != 2 {
		t.Fatalf("candidates = %+v, want both files", cands)
	}
	for _, c := range cands {
		if c.needScratch <= 0 || c.needScratch < c.Size*(100-minSavingPct)/100 {
			t.Errorf("%s: needScratch = %d for a %d-byte file", c.Title, c.needScratch, c.Size)
		}
	}

	need, title := s.ScratchNeed(ctx)
	if title != "Remux" || need != cands[0].needScratch {
		t.Fatalf("ScratchNeed = %d %q, want the remux's %d", need, title, cands[0].needScratch)
	}
	s.skips.record(ctx, movieKey(1), SkipNoScratch, "full")
	s.invalidateLibraryCache()
	if _, title := s.ScratchNeed(ctx); title != "BluRay" {
		t.Fatalf("with the remux waiting, ScratchNeed names %q, want BluRay", title)
	}
}
