package convert

import (
	"context"
	"strings"
	"testing"
)

// indexAt indexes a film at a path of your choosing (indexMovie files it under /movies).
func indexAt(t *testing.T, s *Service, id int64, path string, mi *MediaInfo) {
	t.Helper()
	mi.SizeBytes = int64(mi.BitrateKbps) * 1000 / 8 * int64(mi.DurationSec)
	if mi.Resolution == "" {
		mi.Resolution = resolutionLabel(mi.Width, mi.Height)
	}
	if err := s.index.upsert(context.Background(), indexRow{
		Path: path, MediaType: "movie", MovieID: id, Title: path,
		SizeBytes: mi.SizeBytes, Codec: mi.VideoCodec, Info: mi,
	}); err != nil {
		t.Fatal(err)
	}
}

func indexEpisodeAt(t *testing.T, s *Service, seriesID int64, season, episode int, path string, mi *MediaInfo) {
	t.Helper()
	mi.SizeBytes = int64(mi.BitrateKbps) * 1000 / 8 * int64(mi.DurationSec)
	if err := s.index.upsert(context.Background(), indexRow{
		Path: path, MediaType: "episode", SeriesID: seriesID, Season: season, Episode: episode, Title: path,
		SizeBytes: mi.SizeBytes, Codec: mi.VideoCodec, Info: mi,
	}); err != nil {
		t.Fatal(err)
	}
}

// candKeys is the runner's work list, as keys.
func candKeys(s *Service) map[string]bool {
	ctx := context.Background()
	out := map[string]bool{}
	for _, c := range s.autoCandidates(ctx, s.prefs(ctx)) {
		out[c.Key] = true
	}
	return out
}

func bloated() *MediaInfo { return film("h264", 1920, 1080, 35000, aud("truehd", "eng", 8)) }

func excludeTitle(t *testing.T, s *Service, r ExcludeRequest) ExcludeResult {
	t.Helper()
	res, err := s.SetTitleExclusion(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func seasonPtr(n int) *int { return &n }

// With no rule set nothing is left out: the default changes nothing.
func TestNoExclusionsByDefault(t *testing.T) {
	s := newTestService(t)
	indexAt(t, s, 1, "/movies/A/A.mkv", bloated())
	indexEpisodeAt(t, s, 7, 1, 1, "/tv/Show/S01E01.mkv", bloated())
	p := s.prefs(context.Background())
	if p.excl.any() || p.excl.key != "" {
		t.Fatalf("a fresh install has rules: %+v", p.excl)
	}
	if k := candKeys(s); !k[movieKey(1)] || !k[episodeKey(7, 1, 1)] {
		t.Fatalf("both files should be worth converting, got %v", k)
	}
}

// "Don't convert this" on a film, a show and one season: those files never enter the
// work list, read as left out with a reason, are counted per rule, and come back when the
// rule is turned off.
func TestTitleExclusionsLeaveFilesOut(t *testing.T) {
	ctx := context.Background()
	s := newTestService(t)
	indexAt(t, s, 1, "/movies/A/A.mkv", bloated())
	indexAt(t, s, 2, "/movies/B/B.mkv", bloated())
	indexEpisodeAt(t, s, 7, 1, 1, "/tv/Show/S01E01.mkv", bloated())
	indexEpisodeAt(t, s, 7, 2, 1, "/tv/Show/S02E01.mkv", bloated())
	indexEpisodeAt(t, s, 8, 1, 1, "/tv/Other/S01E01.mkv", bloated())

	excludeTitle(t, s, ExcludeRequest{Kind: "movie", MovieID: 1, Excluded: true})
	excludeTitle(t, s, ExcludeRequest{Kind: "series", SeriesID: 7, Season: seasonPtr(2), Excluded: true})
	excludeTitle(t, s, ExcludeRequest{Kind: "series", SeriesID: 8, Excluded: true})

	k := candKeys(s)
	for key, want := range map[string]bool{
		movieKey(1): false, movieKey(2): true,
		episodeKey(7, 1, 1): true, episodeKey(7, 2, 1): false, episodeKey(8, 1, 1): false,
	} {
		if k[key] != want {
			t.Errorf("%s in the work list = %v, want %v", key, k[key], want)
		}
	}

	pv, err := s.PlanView(ctx, movieKey(1))
	if err != nil {
		t.Fatal(err)
	}
	if pv.Needs.Excluded != ExcludeTitle || !strings.Contains(pv.Needs.Why, "this film") || pv.Needs.Any() || pv.Video.ToCodec != "" {
		t.Errorf("plan card for a left-out film: %+v", pv.Needs)
	}
	for _, a := range pv.Audio {
		if !a.Keep {
			t.Errorf("a left-out film's plan removes a track: %+v", a)
		}
	}
	if pv, _ := s.PlanView(ctx, episodeKey(7, 2, 1)); !strings.Contains(pv.Needs.Why, "this season") {
		t.Errorf("season rule: %q", pv.Needs.Why)
	}
	if pv, _ := s.PlanView(ctx, episodeKey(8, 1, 1)); !strings.Contains(pv.Needs.Why, "this show") {
		t.Errorf("show rule: %q", pv.Needs.Why)
	}

	st, err := s.LibraryStats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if st.Total.LeftOut.Title != 3 || st.Movies.LeftOut.Title != 1 || st.TV.LeftOut.Title != 2 || st.Total.LeftOut.SaveBytes <= 0 {
		t.Errorf("left-out counts: %+v", st.Total.LeftOut)
	}
	if st.Total.Convertible != 2 {
		t.Errorf("convertible = %d, want 2 (the left-out files aren't)", st.Total.Convertible)
	}
	rollup, err := s.computeLibraryTVSeries(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_ = rollup // no series module in tests: the roll-up lists nothing, but must not fail

	list, err := s.TitleExclusions(ctx)
	if err != nil || len(list) != 3 {
		t.Fatalf("list = %+v (%v), want 3 titles", list, err)
	}

	// Off again: the film is back in the work list.
	excludeTitle(t, s, ExcludeRequest{Kind: "movie", MovieID: 1, Excluded: false})
	if !candKeys(s)[movieKey(1)] {
		t.Error("turning the rule off didn't bring the film back")
	}
}

// Library folders: a file in a left-out folder is left out; one in a folder inside it that
// isn't left out belongs to that inner folder, not its parent.
func TestFolderExclusions(t *testing.T) {
	ctx := context.Background()
	s := newTestService(t)
	s.SetLibraryFolders(func(context.Context) []LibraryFolder {
		return []LibraryFolder{
			{Key: "movie:0", Media: "movie", Path: "/movies", Label: "Movies"},
			{Key: "movie:3", Media: "movie", Path: "/kids", Label: "Kids"},
			{Key: "movie:4", Media: "movie", Path: "/kids/keep", Label: "Kept"},
			{Key: "series:0", Media: "series", Path: "/tv", Label: "TV"},
		}
	})
	indexAt(t, s, 1, "/movies/A/A.mkv", bloated())
	indexAt(t, s, 2, "/kids/B/B.mkv", bloated())
	indexAt(t, s, 3, "/kids/keep/C/C.mkv", bloated())
	indexAt(t, s, 4, "/kidsfilms/D/D.mkv", bloated()) // a sibling with the same prefix
	indexEpisodeAt(t, s, 7, 1, 1, "/tv/Show/S01E01.mkv", bloated())

	skip := []string{"movie:3"}
	if _, err := s.UpdateSettings(ctx, SettingsPatch{SkipFolders: &skip}); err != nil {
		t.Fatal(err)
	}
	k := candKeys(s)
	if k[movieKey(2)] || !k[movieKey(1)] || !k[movieKey(3)] || !k[movieKey(4)] || !k[episodeKey(7, 1, 1)] {
		t.Fatalf("work list with Kids left out: %v", k)
	}
	if pv, _ := s.PlanView(ctx, movieKey(2)); pv.Needs.Excluded != ExcludeFolder || !strings.Contains(pv.Needs.Why, `"Kids"`) {
		t.Errorf("folder rule: %+v", pv.Needs)
	}
	st, _ := s.LibraryStats(ctx)
	if st.Total.LeftOut.Folder != 1 {
		t.Errorf("folder count = %+v", st.Total.LeftOut)
	}
	got := s.GetSettings(ctx)
	if len(got.SkipFolders) != 1 || got.SkipFolders[0] != "movie:3" || len(got.LibraryFolders) != 4 {
		t.Errorf("settings read back: %+v %+v", got.SkipFolders, got.LibraryFolders)
	}

	// The TV default folder left out too: a series key must not touch films.
	skip = []string{"movie:3", "series:0"}
	if _, err := s.UpdateSettings(ctx, SettingsPatch{SkipFolders: &skip}); err != nil {
		t.Fatal(err)
	}
	if k := candKeys(s); k[episodeKey(7, 1, 1)] || !k[movieKey(1)] {
		t.Fatalf("with TV left out: %v", k)
	}

	bad := []string{"movie:99"}
	if _, err := s.UpdateSettings(ctx, SettingsPatch{SkipFolders: &bad}); err == nil {
		t.Error("a folder that doesn't exist was accepted")
	}
	none := []string{}
	if _, err := s.UpdateSettings(ctx, SettingsPatch{SkipFolders: &none}); err != nil {
		t.Fatal(err)
	}
	if k := candKeys(s); !k[movieKey(2)] || !k[episodeKey(7, 1, 1)] {
		t.Fatalf("clearing the folders didn't bring the files back: %v", k)
	}
}

// Resolutions, by the file's real resolution as the analysis read it.
func TestResolutionExclusions(t *testing.T) {
	ctx := context.Background()
	s := newTestService(t)
	uhd := film("hevc", 3840, 2160, 70000, aud("truehd", "eng", 8))
	indexAt(t, s, 1, "/movies/UHD/UHD.mkv", uhd)
	scope := film("h264", 1920, 800, 35000, aud("truehd", "eng", 8)) // 2.40:1 1080p, only 800 tall
	indexAt(t, s, 2, "/movies/Scope/Scope.mkv", scope)
	indexAt(t, s, 3, "/movies/HD/HD.mkv", film("h264", 1280, 720, 15000, aud("ac3", "eng", 6)))
	if k := candKeys(s); !k[movieKey(1)] || !k[movieKey(2)] || !k[movieKey(3)] {
		t.Fatalf("all three should need work first: %v", k)
	}

	res := []string{"4K"}
	got, err := s.UpdateSettings(ctx, SettingsPatch{SkipResolutions: &res})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.SkipResolutions) != 1 || got.SkipResolutions[0] != "2160p" {
		t.Errorf("saved as %v, want [2160p]", got.SkipResolutions)
	}
	if k := candKeys(s); k[movieKey(1)] || !k[movieKey(2)] || !k[movieKey(3)] {
		t.Fatalf("with 4K left out: %v", k)
	}
	if pv, _ := s.PlanView(ctx, movieKey(1)); pv.Needs.Excluded != ExcludeResolution || !strings.Contains(pv.Needs.Why, "4K files") {
		t.Errorf("resolution rule: %+v", pv.Needs)
	}

	res = []string{"1080p", "720p"}
	if _, err := s.UpdateSettings(ctx, SettingsPatch{SkipResolutions: &res}); err != nil {
		t.Fatal(err)
	}
	if k := candKeys(s); !k[movieKey(1)] || k[movieKey(2)] || k[movieKey(3)] {
		t.Fatalf("with 1080p and 720p left out (the scope film is 1080p by its width): %v", k)
	}
	st, _ := s.LibraryStats(ctx)
	if st.Total.LeftOut.Resolution != 2 {
		t.Errorf("resolution count = %+v", st.Total.LeftOut)
	}

	res = []string{"8K"}
	if _, err := s.UpdateSettings(ctx, SettingsPatch{SkipResolutions: &res}); err == nil {
		t.Error("an unknown resolution was accepted")
	}
}

// The most specific reason wins: a left-out title in a left-out folder reads as the title.
func TestExclusionReasonOrder(t *testing.T) {
	e := exclusions{
		movies:  map[int64]bool{1: true},
		folders: []skipFolder{{LibraryFolder: LibraryFolder{Key: "movie:0", Media: "movie", Path: "/m", Label: "Movies"}, skip: true}},
		res:     map[string]bool{"1080p": true},
	}
	mi := film("h264", 1920, 1080, 35000)
	if rule, _ := e.match(mi, "/m/a.mkv", titleRef{Kind: "movie", MovieID: 1}); rule != ExcludeTitle {
		t.Errorf("rule = %q, want title", rule)
	}
	if rule, _ := e.match(mi, "/m/a.mkv", titleRef{Kind: "movie", MovieID: 2}); rule != ExcludeFolder {
		t.Errorf("rule = %q, want folder", rule)
	}
	if rule, _ := e.match(mi, "/elsewhere/a.mkv", titleRef{Kind: "movie", MovieID: 2}); rule != ExcludeResolution {
		t.Errorf("rule = %q, want resolution (probe without a label: read from the size)", rule)
	}
	if rule, _ := e.match(film("h264", 1280, 720, 9000), "/elsewhere/a.mkv", titleRef{Kind: "movie", MovieID: 2}); rule != "" {
		t.Errorf("a 720p file outside every rule was left out by %q", rule)
	}
}

// Leaving a title out takes its waiting Convert-now requests off the queue, refuses new
// ones, and lets a conversion already running finish (the reply names it).
func TestExcludingATitleHandlesQueuedAndRunning(t *testing.T) {
	ctx := context.Background()
	s := newTestService(t)
	indexEpisodeAt(t, s, 7, 1, 1, "/tv/Show/S01E01.mkv", bloated())
	indexEpisodeAt(t, s, 7, 1, 2, "/tv/Show/S01E02.mkv", bloated())
	indexEpisodeAt(t, s, 7, 2, 1, "/tv/Show/S02E01.mkv", bloated())
	for _, k := range []string{episodeKey(7, 1, 1), episodeKey(7, 2, 1)} {
		if err := s.requests.add(ctx, k, k); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.requests.add(ctx, movieKey(5), "Film"); err != nil {
		t.Fatal(err)
	}
	running, _ := s.claimPending(episodeKey(7, 1, 2), func(id int64) *Job {
		return &Job{ID: id, Kind: "episode", SeriesID: 7, Season: 1, Episode: 2, Title: "Show - S01E02", State: StateEncoding}
	})

	res := excludeTitle(t, s, ExcludeRequest{Kind: "series", SeriesID: 7, Season: seasonPtr(1), Excluded: true})
	if res.Dropped != 1 || res.Running != "Show - S01E02" {
		t.Fatalf("result = %+v, want one request dropped and the running episode named", res)
	}
	left := map[string]bool{}
	for _, q := range s.requests.list(ctx) {
		left[q.Key] = true
	}
	if left[episodeKey(7, 1, 1)] || !left[episodeKey(7, 2, 1)] || !left[movieKey(5)] {
		t.Fatalf("requests left: %v — only season 1's should go", left)
	}
	if running.State != StateEncoding || running.cancelled {
		t.Error("the running conversion was stopped")
	}
	if err := s.Request(ctx, episodeKey(7, 1, 1)); err == nil || !strings.Contains(err.Error(), "left out") {
		t.Errorf("asking for a left-out episode: %v, want a plain refusal", err)
	}

	if _, err := s.SetTitleExclusion(ctx, ExcludeRequest{Kind: "series", Excluded: true}); err != ErrBadExclusion {
		t.Errorf("no id: %v", err)
	}
}
