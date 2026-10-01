package convert

import (
	"context"
	"strings"
	"testing"
)

// Whether a picture is worth re-encoding is decided by how much bitrate it spends for its
// resolution — not by its codec name. These are the cases that matter in a real library.
func TestVideoWorth(t *testing.T) {
	dv5 := film("hevc", 3840, 2160, 60000, aud("truehd", "eng", 8))
	dv5.HDR, dv5.DVProfile, dv5.DVBase = "Dolby Vision", 5, "SDR"
	dv8 := film("hevc", 3840, 2160, 60000, aud("truehd", "eng", 8))
	dv8.HDR, dv8.DVProfile, dv8.DVBase = "Dolby Vision", 8, "HDR10"
	unknown := film("h264", 1920, 1080, 0)
	unknownHEVC := film("hevc", 1920, 1080, 0)

	for name, c := range map[string]struct {
		mi     *MediaInfo
		want   bool
		reason string
	}{
		"H.264 Blu-ray at 12 Mb/s":            {film("h264", 1920, 1080, 12000, aud("ac3", "eng", 6)), true, ""},
		"lean H.264 at 2 Mb/s":                {film("h264", 1920, 1080, 2000, aud("aac", "eng", 2)), false, "already lean"},
		"bloated HEVC 4K remux":               {film("hevc", 3840, 2160, 60000, aud("truehd", "eng", 8)), true, ""},
		"efficient HEVC 4K WEB-DL at 18Mb/s":  {film("hevc", 3840, 2160, 18000, aud("eac3", "eng", 6)), false, "already efficient"},
		"AV1 is never re-encoded":             {film("av1", 3840, 2160, 60000), false, "already AV1"},
		"Dolby Vision profile 5":              {dv5, false, "profile 5"},
		"Dolby Vision 8.1 remux (HDR10 base)": {dv8, true, ""},
		"unknown bitrate, old codec":          {unknown, true, ""},
		"unknown bitrate, modern codec":       {unknownHEVC, false, "already HEVC"},
	} {
		got, why := videoWorth(c.mi)
		if got != c.want {
			t.Errorf("%s: worth = %v, want %v (%s)", name, got, c.want, why)
		}
		if c.reason != "" && !strings.Contains(why, c.reason) {
			t.Errorf("%s: reason %q should mention %q", name, why, c.reason)
		}
	}
}

// Settings saved before the rebuild keep meaning what they meant.
func TestPrefsMigrateLegacySettings(t *testing.T) {
	ctx := context.Background()
	s := newTestService(t)
	if p := s.prefs(ctx); p.allowAV1 || p.imageSubs != ImageSubsWhenText || !p.pauseWatching || !p.keepOriginal {
		t.Fatalf("defaults wrong: %+v", p)
	}
	set(t, s, map[string]string{keyLegacyTarget: "av1", keyLegacyDropImage: "false"})
	if p := s.prefs(ctx); !p.allowAV1 || p.imageSubs != ImageSubsKeep {
		t.Fatalf("legacy av1 target / image-subs-off not carried over: %+v", p)
	}
	set(t, s, map[string]string{keyAllowAV1: "false", keyImageSubs: ImageSubsRemove})
	if p := s.prefs(ctx); p.allowAV1 || p.imageSubs != ImageSubsRemove {
		t.Fatalf("new settings must win over legacy ones: %+v", p)
	}
}

// planFor's codec is the likely target: HEVC unless AV1 is allowed, and HEVC whenever the
// file carries HDR10+ (which only the HEVC pipeline can hold).
func TestPlanForLikelyCodec(t *testing.T) {
	bloated := film("h264", 1920, 1080, 12000, aud("ac3", "eng", 6))
	hdr10p := film("hevc", 3840, 2160, 60000, aud("truehd", "eng", 8))
	hdr10p.HDR = "HDR10+"
	lean := film("h264", 1920, 1080, 2000, aud("aac", "eng", 2))

	if plan, _ := (prefs{}).planFor(bloated, "", "", nil); plan.VideoCodec != "hevc" || plan.Quality != 20 {
		t.Errorf("AV1 off: plan %+v, want HEVC at CRF 20", plan)
	}
	if plan, _ := (prefs{allowAV1: true}).planFor(bloated, "", "", nil); plan.VideoCodec != "av1" || plan.Quality != 24 {
		t.Errorf("AV1 on: plan %+v, want AV1 at CRF 24", plan)
	}
	if plan, _ := (prefs{allowAV1: true}).planFor(hdr10p, "", "", nil); plan.VideoCodec != "hevc" {
		t.Errorf("HDR10+ with AV1 on: codec %q, want hevc", plan.VideoCodec)
	}
	if plan, n := (prefs{}).planFor(lean, "", "", nil); plan.VideoCodec != "" || n.Any() {
		t.Errorf("a lean file with no track work must need nothing: plan %+v needs %+v", plan, n)
	}
}

// A file whose picture is fine still needs work when its tracks don't match — and gets a
// copy plan for it, not a re-encode.
func TestTrackOnlyWorkCopiesTheVideo(t *testing.T) {
	mi := withSubs(film("hevc", 1920, 1080, 6000, aud("eac3", "eng", 6), aud("eac3", "fre", 6)), srt("eng"), srt("ger"))
	p := prefs{keepAudio: []string{"en"}, keepSubs: []string{"en"}}
	plan, n := p.planFor(mi, "", "", nil)
	if n.Video || !n.Audio || !n.Subs || !n.RemuxOnly() {
		t.Fatalf("needs = %+v, want tracks only", n)
	}
	if plan.VideoCodec != "" {
		t.Fatalf("track-only work must copy the video, got codec %q", plan.VideoCodec)
	}
	if got := trackSummary(mi, plan); got != "audio 2 → 1 · subtitles 2 → 1" {
		t.Fatalf("summary = %q", got)
	}
}

// The estimate is driven by resolution and frame rate: a bloated remux is predicted to
// shrink a lot, a lean encode barely at all.
func TestEstimateTracksBitrate(t *testing.T) {
	remux := film("h264", 1920, 1080, 30000, aud("truehd", "eng", 8))
	plan := Plan{VideoCodec: "hevc"}
	if est := estimatePlanSize(remux, plan); est > remux.SizeBytes/3 {
		t.Errorf("30 Mb/s remux estimated at %d of %d — should shrink by well over half", est, remux.SizeBytes)
	}
	lean := film("h264", 1920, 1080, 3200, aud("aac", "eng", 2))
	if est := estimatePlanSize(lean, plan); est < lean.SizeBytes*8/10 {
		t.Errorf("3.2 Mb/s encode estimated at %d of %d — should barely shrink", est, lean.SizeBytes)
	}
	if est := estimatePlanSize(remux, Plan{VideoCodec: "av1"}); est >= estimatePlanSize(remux, plan) {
		t.Error("AV1 should estimate smaller than HEVC")
	}
}

// Only work that pays off is done automatically and counted.
func TestWorthIt(t *testing.T) {
	// Above the "lean" line, but the estimate says a re-encode would barely shrink it: the
	// picture is left alone rather than burn hours on an encode the 20% rule throws away.
	borderline := film("h264", 1920, 1080, 3200, aud("aac", "eng", 2))
	if ok, _ := videoWorth(borderline); !ok {
		t.Fatal("test setup: should pass the bitrate test")
	}
	_, n := (prefs{}).planFor(borderline, "", "", nil)
	if n.Video || n.Worth || !strings.Contains(n.Why, "would only save") {
		t.Errorf("borderline file: %+v, want left alone with a reason", n)
	}

	// A big H.264 Blu-ray re-encode is worth it, with a real saving attached.
	_, n = (prefs{}).planFor(film("h264", 1920, 1080, 12000, aud("ac3", "eng", 6)), "", "", nil)
	if !n.Video || !n.Worth || n.Save <= 0 {
		t.Errorf("Blu-ray re-encode: %+v, want worth it with a saving", n)
	}

	// Efficient HEVC whose only gap is an image subtitle: rewriting the whole file to shed
	// it isn't worth doing automatically — unless you've asked for tidying.
	pgsOnly := withSubs(film("hevc", 1920, 1080, 5000, aud("eac3", "eng", 6)), pgs("eng"), srt("eng"))
	p := prefs{imageSubs: ImageSubsWhenText}
	if _, n := p.planFor(pgsOnly, "", "", nil); !n.Subs || n.Worth {
		t.Errorf("subtitle-only tidy-up: %+v, want flagged but not worth it", n)
	}
	p.tidyTracks = true
	if _, n := p.planFor(pgsOnly, "", "", nil); !n.Worth {
		t.Errorf("with tidying on, a subtitle-only change should be worth doing: %+v", n)
	}

	// Dropping a big foreign-language audio track frees real space: worth it on its own.
	dub := film("hevc", 1920, 1080, 5000, aud("eac3", "eng", 6), aud("truehd", "ger", 8))
	if _, n := (prefs{keepAudio: []string{"en"}}).planFor(dub, "", "", nil); !n.Audio || !n.Worth || n.Save*100 < dub.SizeBytes*tidySavingPct {
		t.Errorf("dropping a TrueHD dub: %+v, want worth it", n)
	}
}
