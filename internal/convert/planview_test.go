package convert

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// dvRemuxMP4 is the plan card's hard case: a Dolby Vision profile 8 4K HEVC remux in MP4
// with embedded closed captions, a commentary track, a French dub, a full and a forced
// English PGS and a French SRT, and a full English .srt beside it.
func dvRemuxMP4(t *testing.T, s *Service) (string, *MediaInfo) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "Remux (2020).mp4")
	for _, n := range []string{"Remux (2020).mp4", "Remux (2020).en.srt"} {
		if err := os.WriteFile(filepath.Join(dir, n), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	comm := aud("ac3", "eng", 2)
	comm.Commentary, comm.Title = true, "Director's commentary"
	mi := withSubs(film("hevc", 3840, 2160, 60000, aud("truehd", "eng", 8), comm, aud("aac", "fre", 2)),
		pgs("eng"), forcedPGS("eng"), srt("fre"))
	mi.Container, mi.Resolution, mi.TenBit, mi.HasCC = "MP4", "2160p", true, true
	mi.HDR, mi.DVProfile, mi.DVBase = "Dolby Vision", 8, "HDR10"
	if err := s.index.upsert(context.Background(), indexRow{Path: path, MediaType: "movie", MovieID: 7, Title: "Remux",
		PosterURL: "https://image.tmdb.org/t/p/w500/x.jpg", SizeBytes: mi.SizeBytes, Codec: mi.VideoCodec, Info: mi}); err != nil {
		t.Fatal(err)
	}
	return path, mi
}

func TestPlanViewDVRemux(t *testing.T) {
	ctx := context.Background()
	s := newTestService(t)
	set(t, s, map[string]string{keyKeepAudioLangs: "en", keyDropCommentary: "true", keyKeepSubLangs: "en", keyImageSubs: ImageSubsWhenText, keyDolbyVision: DVDrop})
	dvRemuxMP4(t, s)

	v, err := s.PlanView(ctx, movieKey(7))
	if err != nil {
		t.Fatal(err)
	}
	if !v.Needs.Video || v.Video.ToCodec != "HEVC" || v.Video.FromCodec != "HEVC" || v.Video.FromHDR != "Dolby Vision" || v.Video.ToHDR != "HDR10" {
		t.Errorf("video = %+v, want a DV → HDR10 HEVC re-encode", v.Video)
	}
	if v.Video.EstBytes <= 0 || v.Video.EstBytes >= v.SizeBytes {
		t.Errorf("est %d of %d: want a smaller estimate", v.Video.EstBytes, v.SizeBytes)
	}
	if v.Container != (PlanContainer{From: "MP4", To: "MKV"}) {
		t.Errorf("container = %+v", v.Container)
	}
	if v.PosterURL == "" || v.Title != "Remux" {
		t.Errorf("title/poster missing: %q %q", v.Title, v.PosterURL)
	}
	audio := map[string]TrackDecision{}
	for _, d := range v.Audio {
		audio[d.Codec] = d
	}
	if d := audio["truehd"]; !d.Keep {
		t.Errorf("the English TrueHD must stay: %+v", d)
	}
	if d := audio["ac3"]; d.Keep || d.Reason != "commentary" || d.Title != "Director's commentary" {
		t.Errorf("commentary: %+v", d)
	}
	if d := audio["aac"]; d.Keep || d.Reason != "language not kept" || d.Lang != "fre" {
		t.Errorf("French dub: %+v", d)
	}
	if len(v.Subs) != 3 {
		t.Fatalf("subs = %+v", v.Subs)
	}
	if d := v.Subs[0]; d.Keep || !d.Image || d.Reason != "image subtitle covered by text" {
		t.Errorf("full English PGS (covered by the sidecar): %+v", d)
	}
	if d := v.Subs[1]; !d.Keep || !d.Forced {
		t.Errorf("forced English PGS must stay: %+v", d)
	}
	if d := v.Subs[2]; d.Keep || d.Reason != "language not kept" {
		t.Errorf("French SRT: %+v", d)
	}
	w := strings.Join(v.Warnings, " | ")
	for _, want := range []string{"closed captions", "Dolby Vision layer dropped — kept as HDR10", "from MP4 to MKV", "an .srt beside the file covers"} {
		if !strings.Contains(w, want) {
			t.Errorf("warnings %q: missing %q", w, want)
		}
	}
	for _, not := range []string{"forced subtitles", "8-bit", "black bars are checked"} {
		if strings.Contains(w, not) {
			t.Errorf("warnings %q: must not mention %q", w, not)
		}
	}
	if v.Video.CropNote != "black bars are kept (crop is off)" {
		t.Errorf("crop note = %q", v.Video.CropNote)
	}
	if v.EtaSec == nil || *v.EtaSec <= 0 {
		t.Error("the plan carries the time forecast")
	}

	// Crop on, and the forced PGS stays: the bars stay with it.
	set(t, s, map[string]string{keyCrop: "true"})
	if v, _ := s.PlanView(ctx, movieKey(7)); !strings.Contains(v.Video.CropNote, "image subtitles stay") {
		t.Errorf("crop note = %q", v.Video.CropNote)
	}
}

// A file that's already right has nothing to change and nothing to warn about.
func TestPlanViewNothingToDo(t *testing.T) {
	ctx := context.Background()
	s := newTestService(t)
	mi := film("hevc", 1920, 1080, 5000, aud("eac3", "eng", 6))
	mi.Container = "MP4"
	indexMovie(t, s, 3, "Efficient", mi)
	v, err := s.PlanView(ctx, movieKey(3))
	if err != nil {
		t.Fatal(err)
	}
	if v.Needs.Any() || v.Video.ToCodec != "" || len(v.Warnings) != 0 || v.Container.To != "MP4" {
		t.Errorf("plan = %+v, want nothing to change", v)
	}
	if !strings.Contains(v.Video.Why, "already efficient") {
		t.Errorf("why = %q", v.Video.Why)
	}
	if _, err := s.PlanView(ctx, movieKey(99)); !errors.Is(err, ErrNotIndexed) {
		t.Errorf("unknown file: %v", err)
	}
	if _, err := s.PlanView(ctx, "song:1"); !errors.Is(err, ErrInvalidItem) {
		t.Errorf("bad key: %v", err)
	}
}

// An 8-bit source encoded on the CPU says it becomes 10-bit — under "always 10-bit" only;
// "match the source" keeps it 8-bit and says nothing (CONV-26).
func TestPlanViewWarnsTenBit(t *testing.T) {
	s := newTestService(t)
	set(t, s, map[string]string{keyTenBit: TenBitAlways})
	indexMovie(t, s, 4, "BluRay", film("h264", 1920, 1080, 30000, aud("ac3", "eng", 6)))
	v, err := s.PlanView(context.Background(), movieKey(4))
	if err != nil {
		t.Fatal(err)
	}
	if v.Video.FromBits != 8 || v.Video.ToBits != 10 || !strings.Contains(strings.Join(v.Warnings, "|"), "8-bit picture is encoded as 10-bit") {
		t.Errorf("bits %d → %d, warnings %v", v.Video.FromBits, v.Video.ToBits, v.Warnings)
	}
	set(t, s, map[string]string{keyTenBit: TenBitMatchSource})
	v, err = s.PlanView(context.Background(), movieKey(4))
	if err != nil {
		t.Fatal(err)
	}
	if v.Video.ToBits != 8 || strings.Contains(strings.Join(v.Warnings, "|"), "10-bit") {
		t.Errorf("match source: bits %d → %d, warnings %v", v.Video.FromBits, v.Video.ToBits, v.Warnings)
	}
}

// The card's keep flags are keptAudio/keptSubs themselves, for every track fixture.
func TestPlanViewKeepsMatchTheRunner(t *testing.T) {
	ctx := context.Background()
	s := newTestService(t)
	comm := aud("ac3", "eng", 2)
	comm.Commentary = true
	forcedSRT := srt("eng")
	forcedSRT.Forced = true
	files := []*MediaInfo{
		film("h264", 1920, 1080, 12000, aud("truehd", "eng", 8), aud("aac", "jpn", 2), aud("aac", "und", 2), comm),
		withSubs(film("h264", 1920, 1080, 12000, aud("ac3", "eng", 6)), pgs("eng"), srt("eng"), pgs("fre"), srt("ger")),
		withSubs(film("h264", 1920, 1080, 12000, aud("ac3", "eng", 6)), forcedPGS("eng"), pgs("eng"), srt("eng")),
		withSubs(film("h264", 1920, 1080, 12000, comm), forcedPGS(""), forcedSRT, pgs("")),
	}
	plans := []Plan{
		{},
		{Audio: AudioPlan{KeepLangs: []string{"en"}, DropCommentary: true}, Subs: SubPlan{KeepLangs: []string{"en"}, ImageSubs: ImageSubsWhenText}},
		{Audio: AudioPlan{KeepLangs: []string{"en"}, OriginalLang: "ja"}, Subs: SubPlan{ImageSubs: ImageSubsRemove}},
		{Audio: AudioPlan{KeepLangs: []string{"fr"}}, Subs: SubPlan{KeepLangs: []string{"jpn"}, ImageSubs: ImageSubsKeep}},
		{Subs: SubPlan{ImageSubs: ImageSubsWhenText, TextSidecarLangs: []string{"fr"}, TextSidecarForcedLangs: []string{"en"}}},
	}
	p := s.prefs(ctx)
	for fi, mi := range files {
		for pi, plan := range plans {
			n := needsOf(mi, plan)
			v := s.planView(ctx, p, indexRow{MediaType: "movie", MovieID: 1, Info: mi}, plan, n)
			keptA, keptS := map[int]bool{}, map[int]bool{}
			for _, a := range keptAudio(mi, plan) {
				keptA[a.AudIndex] = true
			}
			for _, x := range keptSubs(mi, plan) {
				keptS[x.SubIndex] = true
			}
			if len(v.Audio) != len(mi.Audio) || len(v.Subs) != len(mi.Subs) {
				t.Fatalf("file %d plan %d: %d/%d tracks listed, want every track", fi, pi, len(v.Audio), len(v.Subs))
			}
			for _, d := range v.Audio {
				if d.Keep != keptA[d.Index] {
					t.Errorf("file %d plan %d: audio %d keep=%v, runner says %v", fi, pi, d.Index, d.Keep, keptA[d.Index])
				}
				if !d.Keep && d.Reason == "" {
					t.Errorf("file %d plan %d: audio %d dropped without a reason", fi, pi, d.Index)
				}
			}
			for _, d := range v.Subs {
				if d.Keep != keptS[d.Index] {
					t.Errorf("file %d plan %d: subtitle %d keep=%v, runner says %v", fi, pi, d.Index, d.Keep, keptS[d.Index])
				}
				if !d.Keep && d.Reason == "" {
					t.Errorf("file %d plan %d: subtitle %d dropped without a reason", fi, pi, d.Index)
				}
			}
		}
	}
}
