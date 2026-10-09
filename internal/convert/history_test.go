package convert

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A finished conversion writes exactly one ledger row with what the file was, what it
// became, how it scored and which tracks went and why — and the row survives a restart.
func TestLedgerRecordsAConversion(t *testing.T) {
	ctx := context.Background()
	r := newSpaceRig(t)
	src := r.addMovie(t, 1, "Film", 1000)
	if _, err := r.db.Exec(`UPDATE movies SET source_release = ? WHERE id = 1`, "Film.2020.1080p.BluRay.x264-GRP"); err != nil {
		t.Fatal(err)
	}
	commentary := aud("aac", "eng", 2)
	commentary.Title, commentary.Commentary = "Director's Commentary", true
	mi := film("h264", 1920, 1080, 35000, aud("truehd", "eng", 8), commentary)
	mi.SizeBytes = 1000
	plan := Plan{VideoCodec: "hevc", Quality: 18, Audio: AudioPlan{DropCommentary: true}}
	stubProbe(t, &MediaInfo{VideoCodec: "hevc", Width: 1920, Height: 1080, DurationSec: mi.DurationSec, AudioTracks: 1})
	stubDisks(t, r.scratch, 1<<50, 1<<50)
	dst := filepath.Join(r.scratch, "convert-1.mkv")
	if err := os.WriteFile(dst, []byte(strings.Repeat("y", 500)), 0o644); err != nil {
		t.Fatal(err)
	}

	it, _ := parseKey(movieKey(1))
	job := r.claim(it, "Film", true)
	// What process() fills in before the hand-off.
	rec := r.record(job)
	rec.srcPath, rec.srcInfo = src, mi
	rec.srcRelease = r.sourceRelease(ctx, job, src)
	rec.ssimMean, rec.ssimWindows = 0.985, []float64{0.99, 0.98}
	rec.setPlan(plan)
	r.update(job, func(j *Job) { j.SrcBytes = mi.SizeBytes; j.Encoder = "CPU (x265)" })
	r.beginHistory(job)
	r.finalizeOutput(ctx, job, src, dst, mi, plan)
	if job.State != StateDone {
		t.Fatalf("state %s (%s), want done", job.State, job.Note)
	}

	// A new Service over the same database: the ledger is what survives a restart.
	after := &Service{history: &historyStore{db: r.db}}
	rows, next, err := after.History(ctx, HistoryFilter{})
	if err != nil || len(rows) != 1 || next != "" {
		t.Fatalf("want exactly one row, got %d (next %q, err %v)", len(rows), next, err)
	}
	e := rows[0]
	if e.Outcome != OutcomeDone || e.Key != movieKey(1) || e.Codec != "hevc" || e.CRF != 18 || e.Encoder != "CPU (x265)" {
		t.Fatalf("row: %+v", e)
	}
	if e.SrcRelease != "Film.2020.1080p.BluRay.x264-GRP" || e.SrcSize != 1000 || e.OutSize != 500 || e.OutPath != src {
		t.Fatalf("sizes/paths/release: %+v", e)
	}
	if e.SSIMMean != 0.985 || e.SSIMMin != 0.98 || len(e.SSIMWindows) != 2 {
		t.Fatalf("ssim: %v %v %v", e.SSIMMean, e.SSIMMin, e.SSIMWindows)
	}
	if len(e.Dropped) != 1 || e.Dropped[0].Title != "Director's Commentary" || e.Dropped[0].Reason != "commentary" {
		t.Fatalf("dropped: %+v", e.Dropped)
	}
	if len(e.Kept) != 1 || e.Kept[0].Codec != "truehd" {
		t.Fatalf("kept: %+v", e.Kept)
	}
	if e.SrcSpec == "" || e.OutSpec == "" || e.FinishedAt == 0 || e.EncodeSecs <= 0 {
		t.Fatalf("spec/timing: %+v", e)
	}
	full, err := after.HistoryEntry(ctx, e.ID)
	if err != nil || full.SrcInfo == nil || full.OutInfo == nil || full.SrcInfo.AudioTracks != 2 {
		t.Fatalf("detail: %+v, %v", full, err)
	}
	if _, err := after.HistoryEntry(ctx, e.ID+100); err != ErrNoHistory {
		t.Fatalf("missing row: %v", err)
	}
}

// A row left in progress by a crash is closed as failed when the runner starts.
func TestLedgerClosesInterruptedRows(t *testing.T) {
	s := newTestService(t)
	it, _ := parseKey(movieKey(3))
	job := s.claim(it, "Film", false)
	s.beginHistory(job)

	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	go func() { s.Run(ctx); close(stopped) }()
	defer func() { cancel(); <-stopped }()

	var rows []HistoryEntry
	for deadline := time.Now().Add(10 * time.Second); len(rows) == 0 && time.Now().Before(deadline); {
		time.Sleep(20 * time.Millisecond)
		var err error
		if rows, _, err = s.History(context.Background(), HistoryFilter{}); err != nil {
			t.Fatal(err)
		}
	}
	if len(rows) != 1 {
		t.Fatalf("rows %+v", rows)
	}
	if rows[0].Outcome != OutcomeFailed || !strings.Contains(rows[0].Note, "restart") || rows[0].FinishedAt == 0 {
		t.Fatalf("row: %+v", rows[0])
	}
}

// Only outcomes are recorded: a skip before any encode is a wait (Problems has it), a test
// encode that keeps the original is an outcome, and a full disk isn't a failure of the file.
func TestLedgerRecordsOutcomesNotWaits(t *testing.T) {
	ctx := context.Background()
	s := newTestService(t)
	claim := func(id int64) *Job {
		it, _ := parseKey(movieKey(id))
		return s.claim(it, "Film", false)
	}
	s.finishSkip(claim(1), SkipHardlinked, "still seeding")
	s.finishSkip(claim(2), SkipAlreadyTarget, "already matches")
	s.finish(claim(3), StateFailed, "encode failed: No space left on device")
	s.finishSkip(claim(4), SkipQualityGate, "test encodes couldn't reach the quality bar")
	s.finish(claim(5), StateFailed, "could not analyze file: bad header")

	rows, _, err := s.History(ctx, HistoryFilter{})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, e := range rows {
		got[e.Key] = e.Outcome + "/" + e.OutcomeKind
	}
	want := map[string]string{movieKey(4): "skipped/quality_gate", movieKey(5): "failed/"}
	if len(got) != len(want) || got[movieKey(4)] != want[movieKey(4)] || got[movieKey(5)] != want[movieKey(5)] {
		t.Fatalf("recorded %v, want %v", got, want)
	}

	// Once the encode has started, any end is recorded — a full disk included, so the
	// in_progress row is never left open.
	job := claim(6)
	s.beginHistory(job)
	s.finish(job, StateFailed, "encode failed: No space left on device")
	if e, err := s.history.get(ctx, s.record(job).historyID); err != nil || e.Outcome != OutcomeFailed {
		t.Fatalf("encode-started failure: %+v, %v", e, err)
	}
}

// The list pages on a (finished_at, id) cursor and applies the filters.
func TestLedgerListPagesAndFilters(t *testing.T) {
	ctx := context.Background()
	s := newTestService(t)
	now := time.Now().Unix()
	add := func(kind, outcome, title string, at int64) {
		s.history.finish(ctx, 0, historyRow{key: kind + ":" + title, kind: kind, title: title, outcome: outcome, finishedAt: at})
	}
	add("movie", OutcomeDone, "Alien", now-10)
	add("movie", OutcomeSkipped, "Aliens", now-20)
	add("episode", OutcomeDone, "Show - S01E01", now-30)
	add("movie", OutcomeFailed, "Heat", now-30) // same second as the episode
	add("movie", OutcomeDone, "Ran 100%_", now-40)
	s.history.begin(ctx, historyRow{key: "movie:9", kind: "movie", title: "Running"})

	var titles []string
	cursor := ""
	for pages := 0; ; pages++ {
		rows, next, err := s.History(ctx, HistoryFilter{Limit: 2, Before: cursor})
		if err != nil || pages > 5 {
			t.Fatalf("page %d: %v", pages, err)
		}
		for _, e := range rows {
			titles = append(titles, e.Title)
		}
		if next == "" {
			break
		}
		cursor = next
	}
	if got := strings.Join(titles, ","); got != "Alien,Aliens,Heat,Show - S01E01,Ran 100%_" {
		t.Fatalf("paged order: %s (an in-progress row must not be listed)", got)
	}

	count := func(f HistoryFilter) int {
		rows, _, err := s.History(ctx, f)
		if err != nil {
			t.Fatal(err)
		}
		return len(rows)
	}
	if n := count(HistoryFilter{Outcome: OutcomeDone}); n != 3 {
		t.Fatalf("done: %d", n)
	}
	if n := count(HistoryFilter{Media: "episode"}); n != 1 {
		t.Fatalf("episodes: %d", n)
	}
	if n := count(HistoryFilter{Q: "alien"}); n != 2 {
		t.Fatalf("q=alien: %d", n)
	}
	if n := count(HistoryFilter{Q: "100%_"}); n != 1 {
		t.Fatalf("a search is literal, not a LIKE pattern: %d", n)
	}
	if n := count(HistoryFilter{Outcome: OutcomeInProgress}); n != 1 {
		t.Fatalf("in progress on request: %d", n)
	}
	if _, _, err := s.History(ctx, HistoryFilter{Before: "nonsense"}); err != ErrHistoryCursor {
		t.Fatalf("bad cursor: %v", err)
	}
}

// Pruning drops old non-conversions and keeps every finished conversion.
func TestLedgerPruneKeepsConversions(t *testing.T) {
	ctx := context.Background()
	s := newTestService(t)
	old := time.Now().AddDate(0, 0, -(historyKeepDays + 10)).Unix()
	recent := time.Now().AddDate(0, 0, -10).Unix()
	s.history.finish(ctx, 0, historyRow{key: "movie:1", kind: "movie", title: "Old skip", outcome: OutcomeSkipped, finishedAt: old})
	s.history.finish(ctx, 0, historyRow{key: "movie:2", kind: "movie", title: "Old done", outcome: OutcomeDone, finishedAt: old})
	s.history.finish(ctx, 0, historyRow{key: "movie:3", kind: "movie", title: "New skip", outcome: OutcomeSkipped, finishedAt: recent})
	s.history.prune(ctx)
	rows, _, _ := s.History(ctx, HistoryFilter{})
	var titles []string
	for _, e := range rows {
		titles = append(titles, e.Title)
	}
	if got := strings.Join(titles, ","); got != "New skip,Old done" {
		t.Fatalf("after prune: %s", got)
	}
}

// Track reasons follow the same keep rules as the encode.
func TestTrackDecisionReasons(t *testing.T) {
	commentary := aud("ac3", "eng", 2)
	commentary.Commentary = true
	mi := withSubs(film("h264", 1920, 1080, 12000, aud("truehd", "eng", 8), aud("ac3", "fra", 6), commentary),
		srt("eng"), pgs("eng"), srt("deu"))
	plan := Plan{Audio: AudioPlan{KeepLangs: []string{"en"}, DropCommentary: true},
		Subs: SubPlan{KeepLangs: []string{"en"}, ImageSubs: ImageSubsWhenText}}
	got := map[string]string{}
	for _, d := range trackDecisions(mi, plan) {
		k := d.Type + ":" + d.Lang
		if d.Type == "audio" && d.Index == 2 {
			k = "audio:commentary"
		}
		if d.Image {
			k += ":image"
		}
		got[k] = d.Reason
		if d.Keep != (d.Reason == "") {
			t.Errorf("%s: keep %v with reason %q", k, d.Keep, d.Reason)
		}
	}
	want := map[string]string{
		"audio:eng": "", "audio:fra": "language not kept", "audio:commentary": "commentary",
		"subtitle:eng": "", "subtitle:eng:image": "image subtitle covered by text", "subtitle:deu": "language not kept",
	}
	for k, w := range want {
		if got[k] != w {
			t.Errorf("%s: reason %q, want %q", k, got[k], w)
		}
	}
	if len(got) != len(want) {
		t.Errorf("decisions: %v", got)
	}
}
