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

	if plan, _ := (prefs{}).planFor(bloated, "", "", nil); plan.VideoCodec != "hevc" || plan.Quality != 18 {
		t.Errorf("1080p, AV1 off: plan %+v, want HEVC at CRF 18", plan)
	}
	if plan, _ := (prefs{allowAV1: true}).planFor(bloated, "", "", nil); plan.VideoCodec != "av1" || plan.Quality != 22 {
		t.Errorf("1080p, AV1 on: plan %+v, want AV1 at CRF 22", plan)
	}
	sd := film("h264", 1280, 720, 8000, aud("ac3", "eng", 6))
	if q, qa := maxQualityCRF("hevc", sd), maxQualityCRF("av1", sd); q != 20 || qa != 24 {
		t.Errorf("720p CRF = HEVC %d / AV1 %d, want 20 / 24", q, qa)
	}
	if plan, _ := (prefs{allowAV1: true}).planFor(hdr10p, "", "", nil); plan.VideoCodec != "hevc" || plan.Quality != 18 {
		t.Errorf("4K HDR10+ with AV1 on: %+v, want HEVC at CRF 18 (4K gets the tighter target)", plan)
	}
	if q := maxQualityCRF("av1", hdr10p); q != 22 {
		t.Errorf("4K AV1 CRF = %d, want 22", q)
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
	if est := estimatePlanSize(remux, plan); est < remux.SizeBytes*45/100 || est > remux.SizeBytes*65/100 {
		t.Errorf("30 Mb/s H.264 remux estimated at %d%% of its size — want roughly half (the video halves, the audio stays)", est*100/remux.SizeBytes)
	}
	// The case that started this: a grainy 4K HEVC remux at ~71 Mb/s must not be promised a
	// tiny file. Keeping ~55% of the video bitrate puts it around 60% of its size.
	uhd := film("hevc", 3840, 2160, 71000, aud("truehd", "eng", 8))
	if est := estimatePlanSize(uhd, plan); est < uhd.SizeBytes/2 {
		t.Errorf("71 Mb/s 4K remux estimated at %d%% of its size — that's the 15-GB-from-113 fantasy again", est*100/uhd.SizeBytes)
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

// Once a file has been test-encoded, it's judged and shown by what was measured — but
// only while the measurement still describes it: same file, same format, a setting at
// least as tight as today's.
func TestMeasuredSizeReplacesEstimate(t *testing.T) {
	const path = "/movies/Remux (2001)/Remux (2001).mkv"
	mi := film("hevc", 3840, 2160, 71000, aud("truehd", "eng", 8))
	base, _ := (prefs{}).planFor(mi, path, "", nil)
	_, est := (prefs{}).planFor(mi, path, "", nil)
	if est.Measured || !est.Video {
		t.Fatalf("unmeasured remux: %+v, want an estimated re-encode", est)
	}
	audio := keptAudioBytes(mi, Plan{})
	measuredAt := func(m measurement) prefs {
		return prefs{measured: map[string]measurement{measureKey(path, "hevc"): m}}
	}
	// A test encode predicting 40% of the original's video.
	good := measurement{Size: mi.SizeBytes, CRF: base.Quality, VideoBytes: (mi.SizeBytes - audio) * 40 / 100, SSIM: 0.996}
	_, n := measuredAt(good).planFor(mi, path, "", nil)
	if !n.Measured || !n.Worth || n.Save != mi.SizeBytes-(good.VideoBytes+audio) {
		t.Errorf("measured file: %+v, want Save from the measurement (%d)", n, mi.SizeBytes-(good.VideoBytes+audio))
	}
	// Measured at a tighter setting still counts; at a looser one (the target has been
	// raised since), or on a file that's since been replaced, it doesn't.
	tighter := good
	tighter.CRF = base.Quality - 2
	if _, n := measuredAt(tighter).planFor(mi, path, "", nil); !n.Measured {
		t.Error("a measurement at a tighter CRF should count")
	}
	looser := good
	looser.CRF = base.Quality + 2
	if _, n := measuredAt(looser).planFor(mi, path, "", nil); n.Measured {
		t.Error("a measurement at a looser CRF than today's must not count")
	}
	replaced := good
	replaced.Size = mi.SizeBytes - 1
	if _, n := measuredAt(replaced).planFor(mi, path, "", nil); n.Measured {
		t.Error("a measurement of a different file (size changed) must not count")
	}
	// Measured too close to the original: the picture is left alone, and it says why.
	poor := good
	poor.VideoBytes = (mi.SizeBytes - audio) * 90 / 100
	if _, n := measuredAt(poor).planFor(mi, path, "", nil); n.Video || !strings.Contains(n.Why, "test encode measured only") {
		t.Errorf("measured at 10%% smaller: %+v, want the video left alone with the measurement as the reason", n)
	}
}

// Measurements persist, reach the decision layer through prefs, and are dropped once the
// file they describe has been converted.
func TestMeasureStoreRoundTrip(t *testing.T) {
	s := newTestService(t)
	ctx := context.Background()
	s.recordMeasurement(ctx, "/m/a.mkv", "hevc", measurement{Size: 100, CRF: 18, VideoBytes: 40, SSIM: 0.99, Source: "compare"})
	got, ok := s.prefs(ctx).measured[measureKey("/m/a.mkv", "hevc")]
	if !ok || got.VideoBytes != 40 || got.CRF != 18 || got.Source != "compare" {
		t.Fatalf("measurement after a round trip: %+v (found %v)", got, ok)
	}
	s.measured.forget(ctx, "/m/a.mkv")
	if _, ok := s.prefs(ctx).measured[measureKey("/m/a.mkv", "hevc")]; ok {
		t.Error("a converted file's measurement should be forgotten")
	}
}
