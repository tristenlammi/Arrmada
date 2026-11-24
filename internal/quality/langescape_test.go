package quality

import (
	"strconv"
	"testing"
)

// Issue #137: which way an upgrade goes when the profile's Audio language row wants one
// language (Italian) and avoids another (English). A file whose every language is avoided
// may give up resolution for a release in a wanted language — the one downgrade the
// upgrade sweep grabs — and the import gates take what the sweep grabbed. Every other
// lower resolution is still refused.

// escapeFile is a probed file on disk of an English-original film: resolution res, audio
// tracks langs (ISO 639-2, as a probe reports them).
func escapeFile(res string, langs ...string) CurrentFile {
	return CurrentFile{Release: "Film.2021." + res + ".WEB-DL.x264-OLD", SizeGB: 10, RuntimeMin: 120, OrigLanguage: "en",
		Facts: &FileFacts{Resolution: res, Codec: "h264", HDR: "SDR", AudioLangs: langs}}
}

// escapeProfile is a movie profile with the Audio language row row and trigger, allowing
// 720p to 2160p, upgrades on.
func escapeProfile(t *testing.T, s *Service, row map[string]string, trigger string) string {
	t.Helper()
	return upgradeProfile(t, s, StoredProfile{Name: "lang escape " + trigger + " " + strconv.Itoa(len(row)),
		AllowedResolutions: []string{"720p", "1080p", "2160p"}, UpgradeTrigger: trigger,
		Ideal: &IdealFile{Language: row}})
}

var (
	itaWantEngAvoid = map[string]string{"it": PrefWant, "en": PrefAvoid}
	escITA1080      = "Film.2021.iTALiAN.1080p.WEB-DL.x264-SPQR"
	escITA2160      = "Film.2021.iTALiAN.2160p.WEB-DL.x265-SPQR"
	escITAENG2160   = "Film.2021.ITA.ENG.2160p.WEB-DL.x265-PAIR"
	escENG2160      = "Film.2021.2160p.WEB-DL.x265-BRIT"
	escENGRemux     = "Film.2021.2160p.BluRay.REMUX.HEVC.TrueHD.Atmos-BRIT"
	escMULTi1080    = "Film.2021.MULTi.1080p.WEB-DL.x264-MIX"
)

// The seven cases of the issue, in both rankings, on the sweep and at the import gate the
// download would reach.
func TestLanguageUpgradeDirection(t *testing.T) {
	cases := []struct {
		name string
		cur  CurrentFile
		cand string
		gb   float64
		want bool
	}{
		{"ENG 1080p → ITA 1080p", escapeFile("1080p", "eng"), escITA1080, 8, true},
		{"ENG 1080p → ITA 2160p", escapeFile("1080p", "eng"), escITA2160, 20, true},
		{"ENG 2160p → ITA 1080p (#137)", escapeFile("2160p", "eng"), escITA1080, 8, true},
		{"ITA 1080p → ITA 2160p", escapeFile("1080p", "ita"), escITA2160, 20, true},
		{"ITA+ENG 1080p → ITA+ENG 2160p", escapeFile("1080p", "ita", "eng"), escITAENG2160, 20, true},
		{"ITA 1080p → ENG 2160p", escapeFile("1080p", "ita"), escENG2160, 20, false},
		{"ITA+ENG 1080p → ENG Remux 2160p", escapeFile("1080p", "ita", "eng"), escENGRemux, 60, false},
	}
	forEachRanking(t, func(t *testing.T, mode string) {
		s, ctx := testService(t)
		setRanking(t, s, mode)
		ref := escapeProfile(t, s, itaWantEngAvoid, TriggerAny)
		for _, tc := range cases {
			cand := enCand(tc.cand, tc.gb)
			pick, key, ok := s.UpgradePick(ctx, ref, tc.cur, []Candidate{cand})
			if ok != tc.want || (ok && pick.Name != tc.cand) {
				t.Errorf("%s: sweep ok=%v (%s, %s), want %v", tc.name, ok, pick.Name, key, tc.want)
				continue
			}
			curRes := resRank[tc.cur.release().Resolution]
			candRes := resRank[cand.Release.Resolution]
			switch {
			case candRes < curRes:
				// wantsEpisodeFile / MarkImportedTo refuse a lower resolution unless this says yes.
				if got := s.IsLanguageUpgrade(ctx, ref, tc.cand, tc.gb, tc.cur); got != tc.want {
					t.Errorf("%s: lower-resolution import gate = %v, want %v", tc.name, got, tc.want)
				}
				if tc.want && key != KeyPreferences {
					t.Errorf("%s: decided on %q, want preferences", tc.name, key)
				}
			case candRes == curRes:
				if got := s.IsQualityUpgrade(ctx, ref, tc.cand, tc.gb, tc.cur); got != tc.want {
					t.Errorf("%s: equal-resolution import gate = %v, want %v", tc.name, got, tc.want)
				}
			default:
				// A higher resolution: the ordinary verdict decides, and it must agree with
				// the sweep — the Italian releases are upgrades, the English ones never are
				// (an English 2160p is avoided where the Italian file isn't: intoAvoid).
				if got := s.IsQualityUpgrade(ctx, ref, tc.cand, tc.gb, tc.cur); got != tc.want {
					t.Errorf("%s: higher-resolution quality verdict = %v, want %v", tc.name, got, tc.want)
				}
			}
		}
	})
}

// Among releases that may replace an English-only 2160p file, ordinary ranking still
// decides: the highest resolution in a wanted language, never the avoided remux, never a
// MULTi that doesn't say it has Italian.
func TestLanguageEscapePicksTheBestRelease(t *testing.T) {
	forEachRanking(t, func(t *testing.T, mode string) {
		s, ctx := testService(t)
		setRanking(t, s, mode)
		ref := escapeProfile(t, s, itaWantEngAvoid, TriggerAny)
		cands := []Candidate{
			enCand("Film.2021.iTALiAN.720p.WEB-DL.x264-SPQR", 4),
			enCand(escMULTi1080, 8),
			enCand(escENGRemux, 60),
			enCand(escITA1080, 8),
		}
		pick, key, ok := s.UpgradePick(ctx, ref, escapeFile("2160p", "eng"), cands)
		if !ok || pick.Name != escITA1080 || key != KeyPreferences {
			t.Fatalf("picked %v %s %s, want %s on preferences", ok, pick.Name, key, escITA1080)
		}
	})
}

// After the swap the Italian 1080p file is never replaced back by an English 2160p one,
// and an Italian 2160p release is an ordinary upgrade of it. A MULTi 2160p is an ordinary
// resolution upgrade of it too (as before #137) — and once that MULTi is placed and its
// probe finds English alone, the escape doesn't take another Italian 1080p: the chain
// ends at the 2160p, as it always did. Its name doesn't say it carries only avoided
// languages, and nothing whose name does replaces it either.
func TestLanguageEscapeDoesNotLoop(t *testing.T) {
	const (
		escMULTi2160    = "Film.2021.MULTi.2160p.WEB-DL.x265-MIX"
		escUntagged2160 = "Film.2021.2160p.BluRay.x265-GRP"
		escITA1080Other = "Film.2021.iTALiAN.1080p.BluRay.x264-ROMA"
	)
	forEachRanking(t, func(t *testing.T, mode string) {
		s, ctx := testService(t)
		setRanking(t, s, mode)
		ref := escapeProfile(t, s, itaWantEngAvoid, TriggerAny)
		// The escape's file, read by its name and, as a probe may well find a mislabelled
		// track, by an English-only probe too.
		for _, swapped := range []CurrentFile{
			{Release: escITA1080, SizeGB: 8, RuntimeMin: 120, OrigLanguage: "en"},
			{Release: escITA1080, SizeGB: 8, RuntimeMin: 120, OrigLanguage: "en",
				Facts: &FileFacts{Resolution: "1080p", Codec: "h264", HDR: "SDR", AudioLangs: []string{"eng"}}},
		} {
			probed := swapped.Facts != nil
			for _, name := range []string{escENG2160, escENGRemux, escUntagged2160} {
				if pick, _, ok := s.UpgradePick(ctx, ref, swapped, []Candidate{enCand(name, 40)}); ok {
					t.Errorf("probed=%v: the Italian file would be replaced back by %s", probed, pick.Name)
				}
				if s.IsLanguageUpgrade(ctx, ref, name, 40, swapped) || s.IsQualityUpgrade(ctx, ref, name, 40, swapped) {
					t.Errorf("probed=%v: an import gate takes %s over the Italian file", probed, name)
				}
			}
			if s.IsLanguageUpgrade(ctx, ref, escITA1080, 8, swapped) {
				t.Errorf("probed=%v: the escape took the file's own release", probed)
			}
			// Nothing escapes from it: not a lower resolution, not another Italian 1080p.
			const ita720 = "Film.2021.iTALiAN.720p.WEB-DL.x264-SPQR"
			if pick, key, ok := s.UpgradePick(ctx, ref, swapped, []Candidate{enCand(ita720, 4)}); ok {
				t.Errorf("probed=%v: escaped again to %s (%s)", probed, pick.Name, key)
			}
			for _, name := range []string{escITA1080Other, ita720} {
				if s.IsLanguageUpgrade(ctx, ref, name, 6, swapped) {
					t.Errorf("probed=%v: the lower-resolution gate takes %s", probed, name)
				}
			}
			if _, key, ok := s.UpgradePick(ctx, ref, swapped, []Candidate{enCand(escITA2160, 20)}); !ok || key != KeyResolution {
				t.Errorf("probed=%v: Italian 2160p over the Italian 1080p file: %v %s, want a resolution upgrade", probed, ok, key)
			}
			// A MULTi 2160p isn't avoided (it may carry Italian) and wins on resolution.
			if pick, key, ok := s.UpgradePick(ctx, ref, swapped, []Candidate{enCand(escMULTi2160, 20)}); !ok || pick.Name != escMULTi2160 || key != KeyResolution {
				t.Errorf("probed=%v: MULTi 2160p over the Italian 1080p file: %v %s %s, want a resolution upgrade", probed, ok, pick.Name, key)
			}
		}

		// The MULTi 2160p is placed, and its probe finds an English track alone: the file
		// is all-avoided again, but its name isn't.
		multi := CurrentFile{Release: escMULTi2160, SizeGB: 20, RuntimeMin: 120, OrigLanguage: "en",
			Facts: &FileFacts{Resolution: "2160p", Codec: "hevc", HDR: "SDR", AudioLangs: []string{"eng"}}}
		italians := []Candidate{enCand(escITA1080, 8), enCand(escITA1080Other, 8), enCand("Film.2021.iTALiAN.720p.WEB-DL.x264-SPQR", 4)}
		if pick, key, ok := s.UpgradePick(ctx, ref, multi, italians); ok {
			t.Errorf("the MULTi 2160p that probed English was replaced by %s (%s)", pick.Name, key)
		}
		for _, c := range italians {
			// The import gates ask this of every lower resolution (wantsEpisodeFile,
			// MarkImportedToAllowing).
			if s.IsLanguageUpgrade(ctx, ref, c.Name, c.SizeGB, multi) {
				t.Errorf("the lower-resolution import gate takes %s over the MULTi 2160p", c.Name)
			}
		}
		// Nor does a release whose name says only avoided languages replace it: that one
		// would be eligible for the escape where the MULTi isn't.
		for _, name := range []string{escENGRemux, escUntagged2160, "Film.2021.ENG.2160p.BluRay.REMUX.HEVC-BRIT"} {
			if pick, key, ok := s.UpgradePick(ctx, ref, multi, []Candidate{enCand(name, 60)}); ok {
				t.Errorf("the MULTi 2160p that probed English was replaced by %s (%s)", pick.Name, key)
			}
			if s.IsQualityUpgrade(ctx, ref, name, 60, multi) {
				t.Errorf("the import gate takes %s over the MULTi 2160p", name)
			}
		}
	})
}

// The escape credits only a language the candidate's name states (#137). With the
// original language on Want and English on Avoid, an untagged release of an Italian film
// reads as Italian by assumption — ordinary ranking still credits that — but it never
// lets an English-only file give up resolution. One whose name says iTALiAN does.
func TestLanguageEscapeNeedsAStatedLanguage(t *testing.T) {
	forEachRanking(t, func(t *testing.T, mode string) {
		s, ctx := testService(t)
		setRanking(t, s, mode)
		ref := escapeProfile(t, s, map[string]string{LangOriginal: PrefWant, "en": PrefAvoid}, TriggerAny)
		cur := CurrentFile{Release: "Film.2021.ENG.2160p.WEB-DL.x265-OLD", SizeGB: 20, RuntimeMin: 120, OrigLanguage: "it",
			Facts: &FileFacts{Resolution: "2160p", Codec: "hevc", HDR: "SDR", AudioLangs: []string{"eng"}}}
		itCand := func(name string) Candidate {
			return NewCandidate(name, 8, 50).WithLanguage("it").WithRuntime(120)
		}
		const untagged = "Film.2021.1080p.BluRay.x264-GRP"
		if pick, key, ok := s.UpgradePick(ctx, ref, cur, []Candidate{itCand(untagged)}); ok {
			t.Errorf("assumed Italian: sweep took %s (%s)", pick.Name, key)
		}
		if s.IsLanguageUpgrade(ctx, ref, untagged, 8, cur) {
			t.Error("assumed Italian: the lower-resolution import gate took it")
		}
		// A MULTi carries the original too, but doesn't say so either.
		if s.IsLanguageUpgrade(ctx, ref, "Film.2021.MULTi.1080p.BluRay.x264-GRP", 8, cur) {
			t.Error("MULTi: the lower-resolution import gate took it")
		}
		// The positive control: the name says Italian.
		const stated = "Film.2021.iTALiAN.1080p.BluRay.x264-GRP"
		if pick, key, ok := s.UpgradePick(ctx, ref, cur, []Candidate{itCand(untagged), itCand(stated)}); !ok || pick.Name != stated || key != KeyPreferences {
			t.Errorf("stated Italian: %v %s %s, want %s on preferences", ok, pick.Name, key, stated)
		}
		if !s.IsLanguageUpgrade(ctx, ref, stated, 8, cur) {
			t.Error("stated Italian: the lower-resolution import gate refused it")
		}
		if !s.IsLanguageUpgrade(ctx, ref, "Film.2021.ITA.1080p.BluRay.x264-GRP", 8, cur) {
			t.Error("stated ITA: the lower-resolution import gate refused it")
		}
	})
}

// A probe alone never makes a file eligible: a MULTi file that probes as English only, and
// one whose name states Italian, are left at their resolution; an untagged file of an
// English film, or one naming only English, may escape.
func TestLanguageEscapeReadsTheRecordedRelease(t *testing.T) {
	forEachRanking(t, func(t *testing.T, mode string) {
		s, ctx := testService(t)
		setRanking(t, s, mode)
		ref := escapeProfile(t, s, itaWantEngAvoid, TriggerAny)
		engProbe := &FileFacts{Resolution: "2160p", Codec: "hevc", HDR: "SDR", AudioLangs: []string{"eng"}}
		for _, tc := range []struct {
			release string
			want    bool
		}{
			{"Film.2021.2160p.WEB-DL.x265-OLD", true},
			{"Film.2021.ENG.2160p.WEB-DL.x265-OLD", true},
			{"Film.2021.MULTi.2160p.WEB-DL.x265-OLD", false},
			{"Film.2021.DUAL.2160p.WEB-DL.x265-OLD", false},
			{"Film.2021.iTALiAN.2160p.WEB-DL.x265-OLD", false},
		} {
			cur := CurrentFile{Release: tc.release, SizeGB: 20, RuntimeMin: 120, OrigLanguage: "en", Facts: engProbe}
			_, _, ok := s.UpgradePick(ctx, ref, cur, []Candidate{enCand(escITA1080, 8)})
			if ok != tc.want || s.IsLanguageUpgrade(ctx, ref, escITA1080, 8, cur) != tc.want {
				t.Errorf("%s probed English: escape = %v, want %v", tc.release, ok, tc.want)
			}
		}
		// The title's original language unknown: an untagged name can't be told, so no escape.
		cur := CurrentFile{Release: "Film.2021.2160p.WEB-DL.x265-OLD", SizeGB: 20, RuntimeMin: 120, Facts: engProbe}
		if s.IsLanguageUpgrade(ctx, ref, escITA1080, 8, cur) {
			t.Error("untagged name, original unknown: the escape took it")
		}
	})
}

// Everything the escape must not take: a release whose language can't be told, a file
// that isn't avoided for its language (English neutral, or a low-quality group's), a
// trigger that doesn't count preferences, upgrades off, a dead torrent, and a converted
// file whose original already had the language.
func TestLanguageEscapeRefusals(t *testing.T) {
	forEachRanking(t, func(t *testing.T, mode string) {
		s, ctx := testService(t)
		setRanking(t, s, mode)
		eng2160 := escapeFile("2160p", "eng")
		ita := enCand(escITA1080, 8)
		refused := func(name, ref string, cur CurrentFile, cand Candidate) {
			t.Helper()
			if pick, key, ok := s.UpgradePick(ctx, ref, cur, []Candidate{cand}); ok {
				t.Errorf("%s: sweep took %s (%s)", name, pick.Name, key)
			}
			if s.IsLanguageUpgrade(ctx, ref, cand.Name, cand.SizeGB, cur) {
				t.Errorf("%s: the import gate took %s", name, cand.Name)
			}
		}
		ref := escapeProfile(t, s, itaWantEngAvoid, TriggerAny)

		// Unknown language: a MULTi naming none, and an untagged release of a title whose
		// original language isn't known.
		refused("MULTi 1080p", ref, eng2160, enCand(escMULTi1080, 8))
		noOrig := eng2160
		noOrig.OrigLanguage = ""
		refused("untagged 1080p, original unknown", ref, noOrig,
			NewCandidate("Film.2021.1080p.WEB-DL.x264-GRP", 8, 50).WithRuntime(120))
		// An English 1080p release is as avoided as the file.
		refused("ENG 1080p", ref, eng2160, enCand("Film.2021.1080p.WEB-DL.x264-BRIT", 8))

		// English neutral: the file isn't avoided, so resolution still governs.
		neutral := escapeProfile(t, s, map[string]string{"it": PrefWant}, TriggerAny)
		refused("English neutral", neutral, eng2160, ita)

		// Avoided for something other than its language: a low-quality group's encode.
		yts := CurrentFile{Release: "Film.2021.2160p.WEBRip.x265-YTS", SizeGB: 10, RuntimeMin: 120, OrigLanguage: "en",
			Facts: &FileFacts{Resolution: "2160p", Codec: "hevc", HDR: "SDR", AudioLangs: []string{"eng"}}}
		refused("low-quality group, English neutral", neutral, yts, ita)

		// "Resolution only" counts no language gain.
		refused("trigger resolution", escapeProfile(t, s, itaWantEngAvoid, TriggerResolution), eng2160, ita)

		// Upgrades off.
		sp, err := s.Create(ctx, StoredProfile{MediaType: MediaMovie, Name: "off", AllowedResolutions: []string{"1080p", "2160p"},
			Ideal: &IdealFile{Language: itaWantEngAvoid}})
		if err != nil {
			t.Fatal(err)
		}
		refused("upgrades off", "custom:"+strconv.FormatInt(sp.ID, 10), eng2160, ita)

		// A dead torrent never downloads.
		dead := ita
		dead.Seeders = 0
		if _, _, ok := s.UpgradePick(ctx, ref, eng2160, []Candidate{dead}); ok {
			t.Error("dead torrent: sweep took it")
		}

		// A converted file is measured against its original too: one converted from a
		// dual-audio remux had Italian, so an Italian 1080p is no escape from it.
		conv := eng2160
		conv.Release = "Film.2021.2160p.BluRay.x265-OLD"
		conv.Facts = &FileFacts{Resolution: "2160p", Codec: "hevc", HDR: "SDR", AudioLangs: []string{"eng"}}
		conv.OrigRelease, conv.OrigSizeGB = "Film.2021.ITA.ENG.2160p.BluRay.REMUX.AVC-ORIG", 50
		refused("converted from a release with Italian", ref, conv, ita)
		// One converted from an English-only remux may still escape.
		conv.OrigRelease = "Film.2021.2160p.BluRay.REMUX.AVC-ORIG"
		if _, key, ok := s.UpgradePick(ctx, ref, conv, []Candidate{ita}); !ok || key != KeyPreferences {
			t.Errorf("converted from an English remux: %v %s, want the escape", ok, key)
		}
		if !s.IsLanguageUpgrade(ctx, ref, ita.Name, ita.SizeGB, conv) || !s.IsQualityUpgrade(ctx, ref, ita.Name, ita.SizeGB, conv) {
			t.Error("converted from an English remux: an import gate refused what the sweep grabbed")
		}
		// Never the release it was converted from.
		if s.IsLanguageUpgrade(ctx, ref, conv.OrigRelease, 50, conv) {
			t.Error("the escape took the release the file was converted from")
		}
	})
}

// Italian as a Must: the English-only file no longer fits the profile at all, and an
// Italian release at a lower resolution replaces it, alike in both rankings.
func TestLanguageEscapeUnderAMust(t *testing.T) {
	forEachRanking(t, func(t *testing.T, mode string) {
		s, ctx := testService(t)
		setRanking(t, s, mode)
		ref := escapeProfile(t, s, map[string]string{"it": PrefMust, "en": PrefAvoid}, TriggerAny)
		cur := escapeFile("2160p", "eng")
		if _, key, ok := s.UpgradePick(ctx, ref, cur, []Candidate{enCand(escITA1080, 8)}); !ok || key != KeyPreferences {
			t.Errorf("Must Italian: %v %s, want the escape", ok, key)
		}
		if !s.IsLanguageUpgrade(ctx, ref, escITA1080, 8, cur) {
			t.Error("Must Italian: the import gate refused the escape")
		}
	})
}

// A profile with no language on Avoid does the work it did before #137: none of the
// escape's readings are taken, and every lower resolution is refused as it always was —
// on the sweep and at the import gates, for a file that fits and one a Must rules out.
func TestLanguageEscapeSkippedWithoutAnAvoid(t *testing.T) {
	forEachRanking(t, func(t *testing.T, mode string) {
		s, ctx := testService(t)
		setRanking(t, s, mode)
		eng2160 := escapeFile("2160p", "eng")
		conv := eng2160
		conv.Release = "Film.2021.2160p.BluRay.x265-OLD"
		conv.OrigRelease, conv.OrigSizeGB = "Film.2021.2160p.BluRay.REMUX.AVC-ORIG", 50
		for _, row := range []map[string]string{{"it": PrefWant}, {"it": PrefMust}, {LangOriginal: PrefWant, "it": PrefWant}} {
			ref := escapeProfile(t, s, row, TriggerAny)
			p, e := s.Resolve(ctx, ref)
			if p.avoidsALanguage() {
				t.Fatalf("%v: reads as avoiding a language", row)
			}
			for _, cur := range []CurrentFile{eng2160, conv} {
				bar := cur.upgradeBar(p, e)
				if bar.langRules != nil || bar.langAvoided || bar.nameAvoided || bar.origLangAvoided {
					t.Errorf("%v %s: escape readings taken (%+v)", row, cur.Release, bar)
				}
				if bar.langEscape(TriggerAny, e.Evaluate(p, cur.arriving(escITA1080, 8))) {
					t.Errorf("%v %s: langEscape fired", row, cur.Release)
				}
				if pick, key, ok := s.UpgradePick(ctx, ref, cur, []Candidate{enCand(escITA1080, 8)}); ok {
					t.Errorf("%v %s: sweep took the lower resolution %s (%s)", row, cur.Release, pick.Name, key)
				}
				if s.IsLanguageUpgrade(ctx, ref, escITA1080, 8, cur) {
					t.Errorf("%v %s: the lower-resolution import gate took it", row, cur.Release)
				}
			}
		}
	})
}
