package convert

import (
	"context"
	"reflect"
	"testing"
)

func titled(s SubStream, title string) SubStream { s.Title = title; return s }
func forced(s SubStream) SubStream               { s.Forced = true; return s }

// keptNames lists the kept tracks as lang[/title][/forced], so SDH and forced tell apart.
func keptNames(mi *MediaInfo, plan Plan) []string {
	var out []string
	for _, s := range keptSubs(mi, plan) {
		n := s.Lang
		if s.Title != "" {
			n += "/" + s.Title
		}
		if s.Forced {
			n += "/forced"
		}
		out = append(out, n)
	}
	return out
}

// The case in #100: English "Full + forced, Never SDH, only when the audio is another
// language" on an English show keeps the forced track alone; on a French film it keeps the
// plain full track and the forced one, never the SDH.
func TestSameAsSubtitlesFollowsTheTitlesProfile(t *testing.T) {
	s := newTestService(t)
	ctx := context.Background()
	set(t, s, map[string]string{keyKeepSubLangs: SameAsSubtitles})
	s.SetSubtitleLangs(func(context.Context) []string { return []string{"en"} })
	asked := map[int64]int{}
	s.SetSubtitleProfiles(SubtitleProfiles{
		Rules: func(_ context.Context, kind string, movieID, seriesID int64) []SubRule {
			asked[seriesID]++
			return []SubRule{{Lang: "en", Full: true, OnlyIfAudioDiffers: true, NoSDH: true}}
		},
		Key: func(context.Context) string { return "k1" },
	})
	p := s.prefs(ctx)
	show := titleRef{Kind: "episode", SeriesID: 7}
	subs := func() []SubStream {
		return []SubStream{srt("eng"), titled(srt("eng"), "SDH"), forced(srt("eng")), srt("fre")}
	}

	english := withSubs(film("hevc", 3840, 1920, 15000, aud("eac3", "eng", 6)), subs()...)
	plan, needs := p.planForTitle(english, "/tv/Show/S01E01.mkv", show, nil)
	if got := keptNames(english, plan); !reflect.DeepEqual(got, []string{"eng/forced"}) {
		t.Errorf("English audio kept %v, want only the forced track", got)
	}
	if !needs.Subs {
		t.Error("the file should need its subtitles tidied")
	}
	// The reasons the history shows.
	reasons := map[int]string{}
	for _, d := range trackDecisions(english, plan) {
		if d.Type == "subtitle" && !d.Keep {
			reasons[d.Index] = d.Reason
		}
	}
	want := map[int]string{0: "the audio is already this language", 1: "the audio is already this language", 3: "language not kept"}
	if !reflect.DeepEqual(reasons, want) {
		t.Errorf("reasons = %v, want %v", reasons, want)
	}

	french := withSubs(film("hevc", 1920, 1080, 8000, aud("eac3", "fre", 6)), subs()...)
	plan, _ = p.planForTitle(french, "/movies/Film/Film.mkv", titleRef{Kind: "episode", SeriesID: 7}, nil)
	if got := keptNames(french, plan); !reflect.DeepEqual(got, []string{"eng", "eng/forced"}) {
		t.Errorf("French audio kept %v, want the plain full and the forced track", got)
	}
	if asked[7] != 1 {
		t.Errorf("the show's profile was read %d times, want once per prefs read", asked[7])
	}
	if p.cacheKey() == (prefs{}).cacheKey() {
		t.Error("the cache key should change with the profiles")
	}
}

// What the rules never take: an SDH track that is the language's only full one, any track
// when the audio is untagged, a language listed by hand beside "Same as Subtitles", and
// anything at all under an explicit language list.
func TestProfileSubsStaysCautious(t *testing.T) {
	s := newTestService(t)
	ctx := context.Background()
	s.SetSubtitleLangs(func(context.Context) []string { return []string{"en", "de"} })
	s.SetSubtitleProfiles(SubtitleProfiles{Rules: func(context.Context, string, int64, int64) []SubRule {
		return []SubRule{{Lang: "en", Full: true, OnlyIfAudioDiffers: true, NoSDH: true}, {Lang: "de", Full: false}}
	}})
	movie := titleRef{Kind: "movie", MovieID: 3}

	set(t, s, map[string]string{keyKeepSubLangs: SameAsSubtitles})
	p := s.prefs(ctx)
	onlySDH := withSubs(film("hevc", 1920, 1080, 8000, aud("eac3", "jpn", 6)), titled(srt("eng"), "English SDH"), srt("ger"))
	plan, _ := p.planForTitle(onlySDH, "/m/a.mkv", movie, nil)
	if got := keptNames(onlySDH, plan); !reflect.DeepEqual(got, []string{"eng/English SDH"}) {
		t.Errorf("kept %v: the only English track stays though it's SDH; German full goes (forced only)", got)
	}
	untagged := withSubs(film("hevc", 1920, 1080, 8000, aud("eac3", "", 6)), srt("eng"))
	plan, _ = p.planForTitle(untagged, "/m/a.mkv", movie, nil)
	if got := keptNames(untagged, plan); !reflect.DeepEqual(got, []string{"eng"}) {
		t.Errorf("untagged audio kept %v, want the English track", got)
	}
	// No title known: the language list alone, as before.
	english := withSubs(film("hevc", 1920, 1080, 8000, aud("eac3", "eng", 6)), srt("eng"))
	plan, _ = p.planFor(english, "/m/a.mkv", "", nil)
	if got := keptNames(english, plan); !reflect.DeepEqual(got, []string{"eng"}) {
		t.Errorf("no title: kept %v, want eng", got)
	}

	set(t, s, map[string]string{keyKeepSubLangs: SameAsSubtitles + ", en"})
	plan, _ = s.prefs(ctx).planForTitle(english, "/m/a.mkv", movie, nil)
	if got := keptNames(english, plan); !reflect.DeepEqual(got, []string{"eng"}) {
		t.Errorf("English listed by hand: kept %v, want eng", got)
	}

	set(t, s, map[string]string{keyKeepSubLangs: "en"})
	plan, _ = s.prefs(ctx).planForTitle(english, "/m/a.mkv", movie, nil)
	if got := keptNames(english, plan); !reflect.DeepEqual(got, []string{"eng"}) {
		t.Errorf("explicit list: kept %v, want eng", got)
	}
}

func TestIsSDH(t *testing.T) {
	for title, want := range map[string]bool{
		"SDH": true, "English (SDH)": true, "English [CC]": true, "Hearing Impaired": true, "eng HI": true,
		"English": false, "Full": false, "Chinese": false, "Commentary": false,
	} {
		if got := isSDH(SubStream{Title: title}); got != want {
			t.Errorf("isSDH(%q) = %v, want %v", title, got, want)
		}
	}
	if !isSDH(SubStream{HearingImpaired: true}) {
		t.Error("the disposition flag should count")
	}
}

// The review's cases: none of these may lose a subtitle someone needs.
func TestProfileSubsReviewCases(t *testing.T) {
	s := newTestService(t)
	ctx := context.Background()
	s.SetSubtitleLangs(func(context.Context) []string { return []string{"en"} })
	s.SetSubtitleProfiles(SubtitleProfiles{Rules: func(context.Context, string, int64, int64) []SubRule {
		return []SubRule{{Lang: "en", Full: true, OnlyIfAudioDiffers: true, NoSDH: true}}
	}})
	show := titleRef{Kind: "episode", SeriesID: 1}

	set(t, s, map[string]string{keyKeepSubLangs: SameAsSubtitles})
	p := s.prefs(ctx)
	// A dub beside the original: which one is watched can't be told, so English stays.
	dual := withSubs(film("hevc", 1920, 1080, 8000, aud("aac", "eng", 2), aud("flac", "jpn", 2)), srt("eng"))
	plan, _ := p.planForTitle(dual, "/a.mkv", show, nil)
	if got := keptNames(dual, plan); !reflect.DeepEqual(got, []string{"eng"}) {
		t.Errorf("dual audio kept %v, want the English track", got)
	}
	// An unflagged signs-and-songs track is forced in all but name.
	signs := withSubs(film("hevc", 1920, 1080, 8000, aud("aac", "eng", 2)), titled(srt("eng"), "Signs & Songs"), srt("eng"))
	plan, _ = p.planForTitle(signs, "/a.mkv", show, nil)
	if got := keptNames(signs, plan); !reflect.DeepEqual(got, []string{"eng/Signs & Songs"}) {
		t.Errorf("kept %v, want the signs track", got)
	}
	// A commentary track doesn't stand in for the SDH one.
	comm := withSubs(film("hevc", 1920, 1080, 8000, aud("aac", "fre", 2)), titled(srt("eng"), "Commentary"), titled(srt("eng"), "English SDH"))
	plan, _ = p.planForTitle(comm, "/a.mkv", show, nil)
	if got := keptNames(comm, plan); !reflect.DeepEqual(got, []string{"eng/Commentary", "eng/English SDH"}) {
		t.Errorf("kept %v, want the SDH track kept beside the commentary", got)
	}

	// Image subtitles removed: a PGS that's about to go doesn't stand in for the SDH text.
	set(t, s, map[string]string{keyKeepSubLangs: SameAsSubtitles, keyImageSubs: ImageSubsRemove})
	img := withSubs(film("hevc", 1920, 1080, 8000, aud("aac", "fre", 2)), pgs("eng"), titled(srt("eng"), "SDH"))
	plan, _ = s.prefs(ctx).planForTitle(img, "/a.mkv", show, nil)
	if got := keptNames(img, plan); !reflect.DeepEqual(got, []string{"eng/SDH"}) {
		t.Errorf("kept %v, want the SDH text track (the PGS goes)", got)
	}
}
