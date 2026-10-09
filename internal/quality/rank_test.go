package quality

import (
	"strconv"
	"strings"
	"testing"
)

// evalOf scores a release name under a profile the way the upgrade paths do (no seeder
// term, a fixed size).
func evalOf(sp StoredProfile, name string) Evaluation {
	normalize(&sp)
	return sp.Engine().Evaluate(sp.ToProfile(), NewCandidate(name, 8, 1_000_000))
}

func TestImprovementKey(t *testing.T) {
	sp := StoredProfile{
		MediaType: MediaMovie, Name: "k",
		FormatScores: map[string]int{"HEVC": 50, "TrueHD": 20},
		Keywords:     []Keyword{{Term: "IMAX", Score: 30}},
	}
	cur := "Film.2021.1080p.WEB-DL.x264-GRP"
	cases := []struct {
		cand string
		want RankKey
	}{
		{"Film.2021.2160p.WEB-DL.x264-GRP", KeyResolution},
		{"Film.2021.1080p.WEB-DL.x265-GRP", KeyPreferences},
		{"Film.2021.1080p.WEB-DL.IMAX.x264-GRP", KeyCustom},
		{"Film.2021.1080p.WEB-DL.TrueHD.x264-GRP", KeyCustom},
		{"Film.2021.1080p.BluRay.x264-GRP", KeySource},
		{"Film.2021.1080p.WEB-DL.x264.PROPER-GRP", KeyProper},
		{"Film.2021.1080p.WEB-DL.x264-OTHER", ""},
	}
	base := evalOf(sp, cur)
	for _, tc := range cases {
		if got := improvementKey(evalOf(sp, tc.cand), base); got != tc.want {
			t.Errorf("%s: improvementKey = %q, want %q", tc.cand, got, tc.want)
		}
	}
	// A resolution gain is named first even when the release also brings a preferred format.
	if got := improvementKey(evalOf(sp, "Film.2021.2160p.BluRay.x265-GRP"), base); got != KeyResolution {
		t.Errorf("2160p HEVC BluRay over 1080p: got %q, want resolution", got)
	}
}

func TestTriggerAllowsMatrix(t *testing.T) {
	keys := []RankKey{KeyResolution, KeyPreferences, KeyCustom, KeySource, KeyProper, ""}
	want := map[string][]bool{
		TriggerAny:        {true, true, true, true, true, true},
		"":                {true, true, true, true, true, true},
		TriggerSource:     {true, true, true, true, false, false},
		TriggerFormat:     {true, true, true, false, false, false},
		TriggerResolution: {true, false, false, false, false, false},
		"nonsense":        {true, true, true, true, true, true}, // read as any
	}
	for trig, row := range want {
		for i, k := range keys {
			if got := triggerAllows(trig, k); got != row[i] {
				t.Errorf("triggerAllows(%q, %q) = %v, want %v", trig, k, got, row[i])
			}
		}
	}
}

// The split never changes the score: target + custom is the whole format score, waived
// bonuses included.
func TestScoreSplitKeepsTotal(t *testing.T) {
	sp := StoredProfile{
		MediaType: MediaMovie, Name: "split",
		FormatScores: map[string]int{"HEVC": 50, "Atmos": 50, "TrueHD": 20, "Dolby Vision": -50},
		Keywords:     []Keyword{{Term: "IMAX", Score: 30}, {Term: "YTS", Score: -40}},
	}
	for _, name := range []string{
		"Film.2021.2160p.BluRay.REMUX.DV.HDR10.TrueHD.Atmos.x265.IMAX-GRP",
		"Film.2021.1080p.WEB-DL.DDP5.1.x264-YTS",
		"Film.2021.1080p.WEB-DL.x264-GRP",
	} {
		ev := evalOf(sp, name)
		if ev.TargetScore+ev.CustomScore != ev.FormatScore {
			t.Errorf("%s: target %d + custom %d != format %d", name, ev.TargetScore, ev.CustomScore, ev.FormatScore)
		}
	}
	normalize(&sp)
	// A 0.5 GB HEVC file next to a 20 GB one over two hours: its preference bonus is waived.
	d := sp.Engine().Decide(sp.ToProfile(), []Candidate{
		NewCandidate("Film.2021.1080p.WEB-DL.Atmos.x265.IMAX-GRP", 0.5, 10).WithRuntime(120),
		NewCandidate("Film.2021.1080p.BluRay.x264-GRP", 20, 10).WithRuntime(120),
	})
	for _, ev := range append(d.Eligible, d.Rejected...) {
		if ev.TargetScore+ev.CustomScore != ev.FormatScore {
			t.Errorf("%s (waived %v): target %d + custom %d != format %d", ev.Candidate.Name, ev.BonusWaived, ev.TargetScore, ev.CustomScore, ev.FormatScore)
		}
	}
}

// triggerProfile saves a movie profile that prefers HEVC with the given trigger.
func triggerProfile(t *testing.T, s *Service, trigger string) string {
	t.Helper()
	sp, err := s.Create(t.Context(), StoredProfile{
		MediaType: MediaMovie, Name: "trigger " + trigger, UpgradesEnabled: true, UpgradeTrigger: trigger,
		FormatScores: map[string]int{"HEVC": 50},
	})
	if err != nil {
		t.Fatal(err)
	}
	return "custom:" + strconv.FormatInt(sp.ID, 10)
}

func TestUpgradeTriggerGatesUpgrades(t *testing.T) {
	s, ctx := testService(t)
	cur := "Film.2021.1080p.WEB-DL.x264-GRP"
	// A +10 source step, a +50 Prefer HEVC, and a PROPER of the same group, resolution
	// and source.
	source := "Film.2021.1080p.BluRay.x264-OTHER"
	prefer := "Film.2021.1080p.WEB-DL.x265-OTHER"
	proper := "Film.2021.1080p.WEB-DL.x264.PROPER-GRP"
	properOther := "Film.2021.1080p.WEB-DL.x264.PROPER-OTHER"
	res := "Film.2021.2160p.WEB-DL.x264-OTHER"
	cases := []struct {
		trigger string
		cand    string
		want    bool
	}{
		{TriggerAny, source, true},
		{TriggerAny, properOther, true},
		{TriggerSource, source, true},
		{TriggerSource, properOther, false},
		{TriggerFormat, source, false},
		{TriggerFormat, prefer, true},
		{TriggerFormat, proper, true},
		{TriggerFormat, properOther, false},
		{TriggerResolution, prefer, false},
		{TriggerResolution, res, true},
		{TriggerResolution, proper, true},
	}
	refs := map[string]string{}
	for _, tc := range cases {
		ref, ok := refs[tc.trigger]
		if !ok {
			ref = triggerProfile(t, s, tc.trigger)
			refs[tc.trigger] = ref
		}
		_, got := s.UpgradeCandidate(ctx, ref, cf(cur, 8, 120), []Candidate{NewCandidate(tc.cand, 8, 100).WithRuntime(120)})
		if got != tc.want {
			t.Errorf("UpgradeCandidate %s ← %s = %v, want %v", tc.trigger, tc.cand, got, tc.want)
		}
		if gate := s.IsQualityUpgrade(ctx, ref, tc.cand, 8, cf(cur, 8, 0)); gate != tc.want {
			t.Errorf("IsQualityUpgrade %s ← %s = %v, want %v", tc.trigger, tc.cand, gate, tc.want)
		}
	}
}

// "Higher resolution only" still takes a bitrate upgrade when a percentage step is set:
// the trigger governs quality gains, not the separate bitrate rule.
func TestResolutionTriggerKeepsBitrateUpgrades(t *testing.T) {
	s, ctx := testService(t)
	sp, err := s.Create(ctx, StoredProfile{
		MediaType: MediaMovie, Name: "res only", UpgradesEnabled: true,
		UpgradeTrigger: TriggerResolution, UpgradeMinPercent: 25,
	})
	if err != nil {
		t.Fatal(err)
	}
	ref := "custom:" + strconv.FormatInt(sp.ID, 10)
	cur := "Film.2021.1080p.WEB-DL.x264-GRP"
	cand := "Film.2021.1080p.WEB-DL.x264-OTHER"
	if _, ok := s.UpgradeCandidate(ctx, ref, cf(cur, 4, 120), []Candidate{NewCandidate(cand, 12, 100).WithRuntime(120)}); !ok {
		t.Error("a 3x heavier same-quality release was refused under 'resolution only' with a +25% step")
	}
}

func TestUpgradeTriggerRoundTrip(t *testing.T) {
	s, db, ctx := storeService(t)
	sp, err := s.Create(ctx, StoredProfile{MediaType: MediaMovie, Name: "fmt", UpgradeTrigger: TriggerFormat})
	if err != nil {
		t.Fatal(err)
	}
	if sp.UpgradeTrigger != TriggerFormat {
		t.Errorf("created with %q", sp.UpgradeTrigger)
	}
	sp.UpgradeTrigger = TriggerResolution
	if err := s.Update(ctx, sp.ID, sp); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetStored(ctx, "custom:"+strconv.FormatInt(sp.ID, 10))
	if got.UpgradeTrigger != TriggerResolution {
		t.Errorf("updated trigger reads %q", got.UpgradeTrigger)
	}
	// A blank stored value (a row written by hand, or before the default existed) reads as any.
	mustExec(t, db, `UPDATE quality_profiles SET upgrade_trigger = '' WHERE id = ?`, sp.ID)
	got, _ = s.GetStored(ctx, "custom:"+strconv.FormatInt(sp.ID, 10))
	if got.UpgradeTrigger != TriggerAny {
		t.Errorf("blank trigger reads %q, want any", got.UpgradeTrigger)
	}
	// A profile saved without one is 'any'.
	plain, _ := s.Create(ctx, StoredProfile{MediaType: MediaMovie, Name: "plain"})
	if plain.UpgradeTrigger != TriggerAny {
		t.Errorf("default trigger %q, want any", plain.UpgradeTrigger)
	}
}

// decidingFactor and Decide's comparator share these fixtures: each pair goes through
// Decide, which must pick the expected winner, and the factor must name the step that
// actually separated them. A comparator change that isn't mirrored fails here.
func TestDecidingFactorTracksComparator(t *testing.T) {
	hevc := Profile{FormatScores: map[string]int{"HEVC": 50}}
	custom := Profile{FormatScores: map[string]int{"HEVC": 50}, Keywords: []Keyword{{Term: "IMAX", Score: 30}}}
	avoidDV := Profile{FormatScores: map[string]int{"Dolby Vision": -50}, MinFormatScore: -100}
	lean := Profile{SmallBias: 2}
	cases := []struct {
		name    string
		p       Profile
		winner  Candidate
		loser   Candidate
		wantKey RankKey
		want    string
	}{
		{"avoid", avoidDV,
			NewCandidate("Film.2021.1080p.WEB-DL.x264-GRPA", 8, 10),
			NewCandidate("Film.2021.2160p.BluRay.DV.x265-GRPB", 40, 10),
			KeyAvoid, "it has Dolby Vision, which you avoid"},
		{"resolution", Profile{},
			NewCandidate("Film.2021.2160p.WEB-DL.x264-GRPA", 8, 10),
			NewCandidate("Film.2021.1080p.BluRay.x264-GRPB", 20, 10),
			KeyResolution, "lower resolution"},
		{"low-quality group", Profile{},
			NewCandidate("Film.2021.1080p.WEB-DL.x264-GRPA", 8, 10),
			NewCandidate("Film.2021.2160p.WEB-DL.x264-YTS", 8, 10),
			KeyGroup, "its group is known for over-compressed encodes"},
		{"source", Profile{},
			NewCandidate("Film.2021.1080p.BluRay.x264-GRPA", 8, 10),
			NewCandidate("Film.2021.1080p.WEBRip.x264-GRPB", 20, 10),
			KeySource, "WEBRip, not BluRay"},
		{"proper", Profile{},
			NewCandidate("Film.2021.1080p.WEB-DL.x264.PROPER-GRPA", 8, 10),
			NewCandidate("Film.2021.1080p.WEB-DL.x264-GRPA", 20, 10),
			KeyProper, "the PROPER fix replaces it"},
		{"proper of another group", Profile{},
			NewCandidate("Film.2021.1080p.WEB-DL.x264.REPACK-GRPA", 8, 10),
			NewCandidate("Film.2021.1080p.WEB-DL.x264-GRPB", 20, 10),
			KeyProper, "it isn't a REPACK"},
		{"prefer format", hevc,
			NewCandidate("Film.2021.1080p.WEB-DL.x265-GRPA", 6, 10),
			NewCandidate("Film.2021.1080p.BluRay.x264-GRPB", 20, 10),
			KeyPreferences, "no HEVC, which you prefer"},
		{"custom", custom,
			NewCandidate("Film.2021.1080p.WEB-DL.IMAX.x264-GRPA", 6, 10),
			NewCandidate("Film.2021.1080p.WEB-DL.x264-GRPB", 8, 10),
			KeyCustom, "scores lower on your custom formats"},
		{"small bias", lean,
			NewCandidate("Film.2021.1080p.WEB-DL.x264-GRPA", 4, 10),
			NewCandidate("Film.2021.1080p.WEB-DL.x264-GRPB", 12, 10),
			KeySize, "larger, and you prefer smaller files"},
		{"health band", Profile{},
			NewCandidate("Film.2021.1080p.WEB-DL.x264-GRPA", 9.5, 150),
			NewCandidate("Film.2021.1080p.WEB-DL.x264-GRPB", 10, 2),
			KeyHealth, "fewer seeders"},
		{"zero seeders", Profile{},
			NewCandidate("Film.2021.1080p.WEB-DL.x264-GRPA", 8, 3),
			NewCandidate("Film.2021.2160p.BluRay.x264-GRPB", 30, 0),
			KeyHealth, "it has no seeders"},
		{"magnitude by size", Profile{},
			NewCandidate("Film.2021.1080p.WEB-DL.x264-GRPA", 12, 2),
			NewCandidate("Film.2021.1080p.WEB-DL.x264-GRPB", 8, 150),
			KeyBitrate, "smaller file"},
		{"magnitude by bitrate", Profile{},
			NewCandidate("Film.2021.1080p.WEB-DL.x264-GRPA", 12, 2).WithRuntime(120),
			NewCandidate("Film.2021.1080p.WEB-DL.x264-GRPB", 8, 150).WithRuntime(120),
			KeyBitrate, "lower bitrate"},
		{"seeders tie", Profile{},
			NewCandidate("Film.2021.1080p.WEB-DL.x264-GRPA", 8, 20),
			NewCandidate("Film.2021.1080p.WEB-DL.x264-GRPB", 8, 10),
			KeyHealth, "fewer seeders"},
	}
	e := NewDefaultEngine()
	for _, tc := range cases {
		// Both orders: the comparator, not the input order, decides.
		for _, in := range [][]Candidate{{tc.winner, tc.loser}, {tc.loser, tc.winner}} {
			d := e.Decide(tc.p, in)
			if d.Winner == nil || d.Winner.Candidate.Name != tc.winner.Name || len(d.Eligible) != 2 {
				t.Fatalf("%s: winner = %+v, eligible %d, want %s", tc.name, d.Winner, len(d.Eligible), tc.winner.Name)
			}
			k, why := decidingFactor(tc.p, d.Eligible[0], d.Eligible[1])
			if k != tc.wantKey || why != tc.want {
				t.Errorf("%s: decidingFactor = (%q, %q), want (%q, %q)", tc.name, k, why, tc.wantKey, tc.want)
			}
			if !strings.HasSuffix(d.ChosenOver, "— "+tc.want) {
				t.Errorf("%s: ChosenOver = %q", tc.name, d.ChosenOver)
			}
		}
	}
	// The source tie-break after equal scores and sizes: sourceRank, which orders DVD
	// above HDTV where the score's sourceBonus doesn't — so it is fed directly.
	w := Evaluation{Candidate: NewCandidate("Film.2021.1080p.DVD.x264-GRPA", 8, 10)}
	l := Evaluation{Candidate: NewCandidate("Film.2021.1080p.HDTV.x264-GRPB", 8, 10)}
	if k, why := decidingFactor(Profile{}, w, l); k != KeySource || why != "HDTV, not DVD" {
		t.Errorf("source tie: (%q, %q)", k, why)
	}
}

// A smaller HEVC release that won on a Prefer is not the "highest bitrate"; the heaviest
// winner is.
func TestWhyReasonsClaimBitrateOnlyWhenTrue(t *testing.T) {
	e := NewDefaultEngine()
	p := Profile{BitrateCapMbps: 40, FormatScores: map[string]int{"HEVC": 50}}
	small := NewCandidate("Film.2021.1080p.WEB-DL.x265-GRPA", 6, 10).WithRuntime(120)
	big := NewCandidate("Film.2021.1080p.WEB-DL.x264-GRPB", 12, 10).WithRuntime(120)
	d := e.Decide(p, []Candidate{small, big})
	if d.Winner == nil || d.Winner.Candidate.Name != small.Name {
		t.Fatalf("winner = %s, want the HEVC release", winnerGroup(d))
	}
	joined := strings.Join(d.Why, " | ")
	if strings.Contains(joined, "Highest bitrate") || !strings.Contains(joined, "HEVC — matched") || !strings.Contains(joined, "Best fit for your profile") {
		t.Errorf("Prefer winner reasons = %q", joined)
	}
	// Both HEVC: the heavier one wins on bitrate and may say so.
	heavy := NewCandidate("Film.2021.1080p.WEB-DL.x265-GRPC", 12, 10).WithRuntime(120)
	d = e.Decide(p, []Candidate{small, heavy})
	if joined := strings.Join(d.Why, " | "); !strings.Contains(joined, "Highest bitrate under your 40 Mbps ceiling") {
		t.Errorf("heaviest winner reasons = %q", joined)
	}
	// An avoided heavier release doesn't take the claim away from the clean winner.
	avoid := Profile{FormatScores: map[string]int{"Dolby Vision": -50}, MinFormatScore: -100}
	d = e.Decide(avoid, []Candidate{NewCandidate("Film.2021.1080p.WEB-DL.x264-GRPA", 8, 10), NewCandidate("Film.2021.1080p.BluRay.DV.x265-GRPB", 30, 10)})
	if joined := strings.Join(d.Why, " | "); !strings.Contains(joined, "Highest bitrate available") {
		t.Errorf("clean winner over an avoided heavier one = %q", joined)
	}
}

// The downgrade prompt's reason: a resolution or Must miss needs a different release, a
// ceiling a smaller one.
func TestWouldRejectReason(t *testing.T) {
	s, ctx := testService(t)
	mk := func(sp StoredProfile) string {
		sp.MediaType = MediaMovie
		got, err := s.Create(ctx, sp)
		if err != nil {
			t.Fatal(err)
		}
		return "custom:" + strconv.FormatInt(got.ID, 10)
	}
	file := cf("Film.2021.1080p.BluRay.x264-GRP", 20, 60) // ≈48 Mb/s
	cases := []struct {
		name        string
		sp          StoredProfile
		wantReason  string
		wantCeiling string
	}{
		{"4k only", StoredProfile{Name: "uhd", AllowedResolutions: []string{"2160p"}}, "Not in profile — 1080p", ""},
		{"must hevc", StoredProfile{Name: "hevc", RequiredFormats: []string{"HEVC"}}, "No HEVC — your profile requires it", ""},
		{"ceiling", StoredProfile{Name: "cap", BitrateCapMbps: 20}, "Over your 20 Mbps ceiling (47.7 Mbps)", "20 Mb/s"},
	}
	for _, tc := range cases {
		rej, ok := s.WouldRejectReason(ctx, mk(tc.sp), file)
		if !ok || rej.Reason != tc.wantReason || rej.Ceiling != tc.wantCeiling {
			t.Errorf("%s: WouldRejectReason = (%+v, %v), want (%q, %q)", tc.name, rej, ok, tc.wantReason, tc.wantCeiling)
		}
	}
	if _, ok := s.WouldRejectReason(ctx, mk(StoredProfile{Name: "open"}), file); ok {
		t.Error("an open profile rejected the file")
	}
}
