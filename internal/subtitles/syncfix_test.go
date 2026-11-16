package subtitles

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fixFixture is syncFixture with a movie resolved for on-demand checks, and a helper that
// writes its English sidecar.
func fixFixture(t *testing.T) (s *Service, speech []cue, side string, write func(text string)) {
	t.Helper()
	s, video, speech, _, _ := syncFixture(t, &fakeProvider{}, &fakeAI{})
	s.resolve = func(context.Context, *Job) (fileRef, bool) {
		return fileRef{Path: video, Title: "Movie", MediaKey: "m:1"}, true
	}
	side = strings.TrimSuffix(video, filepath.Ext(video)) + ".en.srt"
	write = func(text string) {
		t.Helper()
		if err := os.WriteFile(side, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return s, speech, side, write
}

func firstCue(t *testing.T, path string) time.Duration {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return parseSRT(string(raw))[0].start
}

// The case this exists for: a subtitle a few hundred milliseconds late (too little for the
// download gate's 300 ms, plain to anyone watching) is moved onto the speech; a second fix
// finds it in sync and leaves it alone.
func TestFixSyncMovesASmallOffset(t *testing.T) {
	m := MediaRef{Kind: "movie", MovieID: 1}
	for _, offMs := range []int{250, 450, 800, -600} {
		s, speech, side, write := fixFixture(t)
		base := subtitleFor(speech, 5)
		write(formatSRT(shiftCues(base, offMs, 1)))
		fix, err := s.FixSync(context.Background(), m, "en", "")
		if err != nil || fix.Outcome != FixFixed {
			t.Fatalf("%d ms: outcome %q err %v (%+v)", offMs, fix.Outcome, err, fix.Sync)
		}
		if d := firstCue(t, side) - base[0].start; d < -100*time.Millisecond || d > 100*time.Millisecond {
			t.Errorf("%d ms: first cue is %v off after the fix, want within 100 ms", offMs, d)
		}
		f, ok, _ := s.ledger.LiveFile(context.Background(), side)
		if !ok || f.SyncShiftMs == nil || abs(*f.SyncShiftMs+offMs) > 100 || f.SyncOffsetMs == nil || *f.SyncOffsetMs != 0 {
			t.Errorf("%d ms: row %+v ok=%v, want the shift recorded and the offset cleared", offMs, f, ok)
		}
		if h := healthOf(f); h.State != HealthOK || !strings.HasPrefix(h.Note, "shifted ") {
			t.Errorf("%d ms: health %+v", offMs, h)
		}
		before, _ := os.ReadFile(side)
		again, err := s.FixSync(context.Background(), m, "en", "")
		if err != nil || again.Outcome != FixInSync {
			t.Errorf("%d ms: second fix = %q, %v (%+v); want in sync", offMs, again.Outcome, err, again.Sync)
		}
		if after, _ := os.ReadFile(side); string(after) != string(before) {
			t.Errorf("%d ms: the second fix rewrote a file already in sync", offMs)
		}
	}
}

// What a fix leaves alone: a subtitle already in sync, one that doesn't fit this audio
// (the measurement isn't trusted), and a video a subtitle job is working on.
func TestFixSyncLeavesAlone(t *testing.T) {
	m := MediaRef{Kind: "movie", MovieID: 1}
	ctx := context.Background()

	s, speech, side, write := fixFixture(t)
	inSync := formatSRT(subtitleFor(speech, 6))
	write(inSync)
	fix, err := s.FixSync(ctx, m, "en", "")
	if err != nil || fix.Outcome != FixInSync {
		t.Fatalf("in sync: %q, %v (%+v)", fix.Outcome, err, fix.Sync)
	}
	if raw, _ := os.ReadFile(side); string(raw) != inSync {
		t.Error("a subtitle in sync was rewritten")
	}

	unrelated := formatSRT(subtitleFor(synthSpeech(909, 4*time.Minute), 3))
	write(unrelated)
	fix, err = s.FixSync(ctx, m, "en", "")
	if err != nil || fix.Outcome != FixUnsure || !strings.Contains(fix.Words, "isn't sure enough") {
		t.Fatalf("unrelated: %q %q, %v (%+v)", fix.Outcome, fix.Words, err, fix.Sync)
	}
	if raw, _ := os.ReadFile(side); string(raw) != unrelated {
		t.Error("a subtitle the measurement couldn't place was rewritten")
	}

	s.mu.Lock()
	if s.busy == nil {
		s.busy = map[string]*Job{}
	}
	s.busy["m:1"] = &Job{}
	s.mu.Unlock()
	if _, err := s.FixSync(ctx, m, "en", ""); !errors.Is(err, ErrFileBusy) {
		t.Errorf("busy video: %v, want ErrFileBusy", err)
	}
}

func TestSecondsWords(t *testing.T) {
	for ms, want := range map[int]string{250: "0.25 s", 450: "0.45 s", 500: "0.5 s", 3000: "3.0 s", 1250: "1.2 s", 80: "0.08 s"} {
		if got := secondsWords(ms); got != want {
			t.Errorf("secondsWords(%d) = %q, want %q", ms, got, want)
		}
	}
	if got := signedSeconds(-450); got != "−0.45 s" {
		t.Errorf("signedSeconds(-450) = %q", got)
	}
}

// The show-wide loop counts each outcome, notes what it changed or left, reports progress,
// and passes over a language with no subtitle.
func TestFixSyncAllCountsAndNotes(t *testing.T) {
	s, speech, _, write := fixFixture(t)
	write(formatSRT(shiftCues(subtitleFor(speech, 5), 450, 1)))
	m := MediaRef{Kind: "movie", MovieID: 1}
	var seen []string
	got, err := s.fixSyncAll(context.Background(), []MediaRef{m, m}, []string{"en", "fr"}, func(done, total int, last string) {
		seen = append(seen, last)
	})
	if err != nil || got.Subtitles != 2 || got.Fixed != 1 || got.InSync != 1 || got.Unsure != 0 || got.Failed != 0 {
		t.Fatalf("got %+v, %v; want the first pass fixed and the second in sync (French has no subtitle)", got, err)
	}
	if len(got.Notes) != 1 || !strings.HasPrefix(got.Notes[0], "movie en: shifted −0.4") {
		t.Errorf("notes %q", got.Notes)
	}
	if len(seen) != 2 || !strings.Contains(seen[1], "already in sync") {
		t.Errorf("progress %q", seen)
	}
	if w := got.Words(); w != "1 fixed, 1 already in sync." {
		t.Errorf("words %q", w)
	}
}
