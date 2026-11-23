package quality

import (
	"strings"
	"testing"

	"github.com/tristenlammi/arrmada/internal/langs"
)

// Issue #58, second report: an Italian Must / English Avoid / Original Avoid series
// profile, judged against the names Italian trackers and international groups really use.
// Italian trackers post new episodes before the Italian dub exists as "ENG Sub ITA" —
// English audio, Italian subtitles. Those were read as Italian audio and grabbed, and the
// files that arrived had only an English track.
func TestItalianMustSceneNames(t *testing.T) {
	row := map[string]string{"en": PrefAvoid, "it": PrefMust, LangOriginal: PrefAvoid}
	cases := []struct {
		name  string
		multi bool // the profile's "MULTi and Dual count" switch
		ok    bool
		why   string
	}{
		{"Show.S01E03.MULTi.1080p.WEB.H264-GRP", false, false, "Doesn't name Italian audio"},
		{"Show.S01E03.DUAL.1080p.WEB.H264-GRP", false, false, "Doesn't name Italian audio"},
		{"Show.S01E03.1080p.AMZN.WEB-DL.DDP5.1.H.264-GRP", false, false, "Missing Italian audio"},
		{"Show.S01E03.iTA.ENG.1080p.WEB.H264-GRP", false, true, ""},
		{"Show.S01E03.ITA-ENG.1080p.WEB.H264-GRP", false, true, ""},
		{"Show.S01E03.1080p.WEB-DL.ITA.ENG.DDP5.1.H264-GRP", false, true, ""},
		{"Show.S01E03.iTALiAN.1080p.WEB.H264-GRP", false, true, ""},
		{"Show.S01E03.MULTi.ITA.1080p.WEB.H264-GRP", false, true, ""},
		// Subtitles only: never Italian audio.
		{"Show.S01E03.1080p.WEB.ENG.Sub.ITA.H264-GRP", false, false, "Missing Italian audio"},
		{"Show.S01E03.1080p.WEB-DL.ENG.SUB-ITA.H264-GRP", false, false, "Missing Italian audio"},
		{"Show.S01E03.1080p.AMZN.WEB-DL.DDP5.1.H.264.Sub.ITA-GRP", false, false, "Missing Italian audio"},
		{"Show.S01E03.1080p.WEB-DL.ENG.Subs.ITA.ENG.H264-GRP", false, false, "Missing Italian audio"},
		{"Show.S01E03.1080p.WEB.MULTi.Sub.ITA.H264-GRP", false, false, "Missing Italian audio"},
		// …but a name that says Italian audio as well as Italian subtitles still has it.
		{"Show.S01E03.1080p.WEB-DL.ITA.ENG.Sub.ITA.ENG.H264-GRP", false, true, ""},
		// Season packs read the same way.
		{"Show.S01.1080p.WEB.ENG.Sub.ITA.H264-GRP", false, false, "Missing Italian audio"},
		{"Show.S01.iTA.ENG.1080p.WEB.H264-GRP", false, true, ""},
		{"Show.S01.MULTi.1080p.WEB.H264-GRP", false, false, "Doesn't name Italian audio"},
		// The switch on: MULTi and Dual naming no language are let through as "may carry
		// Italian" — the opposite of what the reporter wanted from setting it to 1.
		{"Show.S01E03.MULTi.1080p.WEB.H264-GRP", true, true, ""},
		{"Show.S01E03.DUAL.1080p.WEB.H264-GRP", true, true, ""},
		{"Show.S01E03.1080p.AMZN.WEB-DL.DDP5.1.H.264-GRP", true, false, "Missing Italian audio"},
		{"Show.S01E03.1080p.WEB.ENG.Sub.ITA.H264-GRP", true, false, "Missing Italian audio"},
	}
	for _, tc := range cases {
		forEachRanking(t, func(t *testing.T, mode string) {
			p := Profile{Languages: normLanguageRow(row), MultiCountsLanguage: tc.multi}
			ev := engineIn(mode).Evaluate(p, NewCandidate(tc.name, 2, 50).WithLanguage("en"))
			if ev.Eligible != tc.ok {
				t.Errorf("%s (multi %v): eligible %v %q langs %v, want %v", tc.name, tc.multi, ev.Eligible, ev.RejectReason, ev.AudioLangs, tc.ok)
				return
			}
			if !tc.ok && (ev.RejectCode != RejectMissingLanguage || !strings.HasPrefix(ev.RejectReason, tc.why)) {
				t.Errorf("%s (multi %v): %s %q, want %q…", tc.name, tc.multi, ev.RejectCode, ev.RejectReason, tc.why)
			}
		})
	}
}

// The replace path (trigger=replace, the upgrade sweep and its import gate): a file probed
// as English only is short of the Must, and is replaced only by a release that says Italian
// audio — never by one with Italian subtitles, or a MULTi that doesn't name it.
func TestItalianMustReplace(t *testing.T) {
	row := map[string]string{"en": PrefAvoid, "it": PrefMust, LangOriginal: PrefAvoid}
	forEachRanking(t, func(t *testing.T, mode string) {
		s, ctx := testService(t)
		setRanking(t, s, mode)
		ref := upgradeProfile(t, s, langStored(row, TriggerAny))
		cur := onDisk("Show.S01E03.1080p.AMZN.WEB-DL.DDP5.1.H.264-OLD", englishOnly)
		if v, _ := s.GetStored(ctx, ref); v.JudgeFile(cur).Eligible {
			t.Fatal("an English-only file fits an Italian Must")
		}
		subs := enCand("Show.S01E03.1080p.WEB.ENG.Sub.ITA.H264-SUB", 3)
		multi := enCand("Show.S01E03.MULTi.1080p.WEB.H264-MUL", 3)
		if pick, _, ok := s.UpgradePick(ctx, ref, cur, []Candidate{subs, multi}); ok {
			t.Errorf("replaced an English file with %s", pick.Name)
		}
		for _, n := range []string{subs.Name, multi.Name} {
			if s.IsQualityUpgrade(ctx, ref, n, 3, cur) {
				t.Errorf("import gate took %s as an upgrade", n)
			}
		}
		// A file that came in under an "ENG Sub ITA" name is English: it no longer passes the
		// Must (it used to, read as Italian), so the sweep can replace it — probed as an
		// English track, or not read yet.
		stored, _ := s.GetStored(ctx, ref)
		for _, facts := range []*FileFacts{englishOnly, nil} {
			in := onDisk("Show.S01E03.1080p.WEB.ENG.Sub.ITA.H264-SUB", facts)
			if v := stored.JudgeFile(in); v.Eligible {
				t.Errorf("ENG Sub ITA file (probed %v) fits an Italian Must", facts != nil)
			}
			if fit := stored.LanguageFit(in); fit == nil || fit.Status != langs.AudioFitBad {
				t.Errorf("ENG Sub ITA file (probed %v): badge %+v, want red", facts != nil, fit)
			}
		}
		ita := enCand("Show.S01E03.iTA.ENG.1080p.WEB.H264-ITA", 3)
		pick, _, ok := s.UpgradePick(ctx, ref, cur, []Candidate{subs, multi, ita})
		if !ok || pick.Name != ita.Name {
			t.Errorf("upgrade = %v %s, want the iTA.ENG release", ok, pick.Name)
		}
	})
}
