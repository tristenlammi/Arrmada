package convert

import (
	"context"
	"strings"
	"testing"
)

// "Leave them alone" keeps a Dolby Vision file's picture as it is, says why, and still
// tidies its tracks; "drop" (the default) converts it by its bitrate as before.
func TestDolbyVisionPolicyDecides(t *testing.T) {
	mi := dvP8(60000)
	drop := prefs{dolbyVision: DVDrop, imageSubs: ImageSubsKeep}
	if _, n := drop.planFor(mi, "", "", nil); !n.Video || !n.Worth {
		t.Fatalf("drop: %+v, want a worthwhile re-encode", n)
	}
	skip := prefs{dolbyVision: DVSkip, imageSubs: ImageSubsKeep}
	plan, n := skip.planFor(mi, "", "", nil)
	if n.Video || n.Worth || n.Why != dvLeftAloneWhy || plan.VideoCodec != "" {
		t.Fatalf("skip: %+v (codec %q), want the picture left alone with the reason", n, plan.VideoCodec)
	}

	// Its tracks are still tidied.
	comm := aud("ac3", "eng", 2)
	comm.Commentary = true
	withComm := dvP8(60000)
	withComm.Audio = append(withComm.Audio, comm)
	withComm.Audio[1].AudIndex = 1
	skip.dropCommentary, skip.tidyTracks = true, true
	plan, n = skip.planFor(withComm, "", "", nil)
	if n.Video || !n.Audio || !n.Worth || plan.VideoCodec != "" {
		t.Errorf("skip with a commentary track: %+v, want a tracks-only tidy", n)
	}

	// Not Dolby Vision: the choice doesn't apply.
	if _, n := skip.planFor(film("h264", 1920, 1080, 30000, aud("ac3", "eng", 6)), "", "", nil); !n.Video {
		t.Error("the Dolby Vision choice must not touch other files")
	}
	if drop.cacheKey() == (prefs{dolbyVision: DVSkip, imageSubs: ImageSubsKeep}).cacheKey() {
		t.Error("the cached views must be rebuilt when the choice changes")
	}
	if dvPolicy("") != DVDrop || dvPolicy("bogus") != DVDrop || dvPolicy(DVSkip) != DVSkip || dvPolicy(DVKeep) != DVKeep {
		t.Error("unset or unknown must read as today's behaviour (drop)")
	}
}

// The setting saves, refuses nonsense, and switching it changes the Library, the stats and
// the Switch-on counts at once — no rescan.
func TestDolbyVisionPolicySetting(t *testing.T) {
	ctx := context.Background()
	s := newTestService(t)
	if got := s.GetSettings(ctx).DolbyVision; got != DVSkip {
		t.Fatalf("a new install's default = %q, want skip", got)
	}
	drop := DVDrop
	if _, err := s.UpdateSettings(ctx, SettingsPatch{DolbyVision: &drop}); err != nil {
		t.Fatal(err)
	}
	bad := "bogus"
	if _, err := s.UpdateSettings(ctx, SettingsPatch{DolbyVision: &bad}); err == nil {
		t.Fatal("an unknown choice must be refused")
	}
	keep := DVKeep
	if _, err := s.UpdateSettings(ctx, SettingsPatch{DolbyVision: &keep}); err == nil {
		t.Fatal("keep must be refused while dovi_tool isn't installed")
	}
	indexMovie(t, s, 1, "DV", dvP8(60000))
	indexMovie(t, s, 2, "DV Two", dvP8(60000))
	indexMovie(t, s, 3, "Plain", film("h264", 1920, 1080, 30000, aud("ac3", "eng", 6)))

	st, err := s.LibraryStats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if st.Total.Convertible != 3 || st.Total.DolbyVisionFiles != 2 || st.Total.DolbyVision != 2 {
		t.Fatalf("drop: convertible %d, DV files %d, convertible DV %d", st.Total.Convertible, st.Total.DolbyVisionFiles, st.Total.DolbyVision)
	}
	if p := s.AutoPreview(ctx, PreviewFilter{Season: -1}); p.DVFiles != 2 {
		t.Errorf("drop: %d files lose Dolby Vision, want 2", p.DVFiles)
	}

	choice := DVSkip
	saved, err := s.UpdateSettings(ctx, SettingsPatch{DolbyVision: &choice})
	if err != nil || saved.DolbyVision != DVSkip {
		t.Fatalf("save: %v %+v", err, saved)
	}
	st, _ = s.LibraryStats(ctx)
	if st.Total.Convertible != 1 || st.Total.DolbyVisionFiles != 2 {
		t.Errorf("skip: convertible %d (want 1), DV files %d (want 2 — the choice's count)", st.Total.Convertible, st.Total.DolbyVisionFiles)
	}
	if p := s.AutoPreview(ctx, PreviewFilter{Season: -1}); p.DVFiles != 0 || p.Files != 1 {
		t.Errorf("skip: %+v, want only the plain file and nobody losing Dolby Vision", p)
	}
	lib, _ := s.Library(ctx)
	for _, c := range lib {
		if c.MovieID == 1 && (c.Worth || c.Needs.Why != dvLeftAloneWhy) {
			t.Errorf("skip: the DV row = %+v, want not worth with the reason", c.Needs)
		}
	}
	if st := s.Status(ctx); st.Remaining != 1 {
		t.Errorf("skip: %d files in Up next, want 1", st.Remaining)
	}
}

// The plan warns when the title's quality profile prefers Dolby Vision.
func TestPlanWarnsProfilePrefersDV(t *testing.T) {
	ctx := context.Background()
	s := newTestService(t)
	set(t, s, map[string]string{keyDolbyVision: DVDrop})
	indexMovie(t, s, 1, "DV", dvP8(60000))
	const warning = "your quality profile prefers Dolby Vision"
	v, err := s.PlanView(ctx, movieKey(1))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(v.Warnings, "|"), warning) {
		t.Error("no profile lookup: no warning")
	}
	var asked string
	s.SetProfilePrefersDV(func(_ context.Context, kind string, id int64) bool { asked = kind; return id == 1 })
	v, _ = s.PlanView(ctx, movieKey(1))
	if !strings.Contains(strings.Join(v.Warnings, "|"), warning) || asked != "movie" {
		t.Errorf("warnings %v (asked %q), want the profile warning", v.Warnings, asked)
	}
	set(t, s, map[string]string{keyDolbyVision: DVSkip})
	v, _ = s.PlanView(ctx, movieKey(1))
	if v.Video.ToCodec != "" || strings.Contains(strings.Join(v.Warnings, "|"), warning) || v.Video.Why != dvLeftAloneWhy {
		t.Errorf("left alone: %+v %v", v.Video, v.Warnings)
	}
}

// A new install leaves Dolby Vision files alone; an install that was already converting keeps
// dropping the layer as it always did. The default is saved the first time it's read, so it
// never changes under the owner — and a new install's first save switching Auto on can't
// make it look like an old one.
func TestDolbyVisionDefaultByInstall(t *testing.T) {
	ctx := context.Background()
	fresh := newTestService(t)
	on := true
	if _, err := fresh.UpdateSettings(ctx, SettingsPatch{Auto: &on}); err != nil {
		t.Fatal(err)
	}
	if got := fresh.prefs(ctx).dolbyVision; got != DVSkip {
		t.Errorf("new install switched on: %q, want skip", got)
	}
	if v, saved := fresh.settings.Lookup(keyDolbyVision); !saved || v != DVSkip {
		t.Errorf("the default must be saved: %q %v", v, saved)
	}

	for _, used := range convertUsedKeys {
		old := newTestService(t)
		set(t, old, map[string]string{used: "true"})
		if got := old.prefs(ctx).dolbyVision; got != DVDrop {
			t.Errorf("install with %s saved: %q, want drop (today's behaviour)", used, got)
		}
	}

	// A saved choice is never touched, whatever the install looks like.
	chose := newTestService(t)
	set(t, chose, map[string]string{keyAuto: "true", keyDolbyVision: DVSkip})
	if got := chose.prefs(ctx).dolbyVision; got != DVSkip {
		t.Errorf("saved skip read as %q", got)
	}
}
