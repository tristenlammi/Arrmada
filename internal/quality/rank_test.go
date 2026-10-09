package quality

import (
	"strconv"
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
