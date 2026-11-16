package subtitles

import (
	"context"
	"fmt"
	"math/rand"
	"path/filepath"
	"testing"
	"time"
)

// Real-audio check of the line-start refinement (syncrefine.go), on the calibration's
// synthetic films through an AAC round trip: subtitles timed the way people time them
// (in around the first word, lingering after the last, long lines split), in sync and a
// few hundred milliseconds off. It logs the overlap-only reading beside the refined one,
// and holds the refined one to 90 ms of the truth on the clean and film mixes. Under a
// score louder than the dialogue there are too few clean starts: no refinement is the
// right answer there, and a wrong one would be a failure.
func TestSyncRefineIT(t *testing.T) {
	needFFmpegIT(t)
	dir := t.TempDir()
	dur := 20 * time.Minute
	mixes := []mix{
		{name: "clean", music: 0.05, musicN: 2, effects: 4, noise: 0.003},
		{name: "film", music: 0.15, musicN: 8, effects: 15, noise: 0.006},
		{name: "music-heavy", music: 0.3, musicN: 20, effects: 30, noise: 0.01},
		{name: "wall-to-wall", music: 0.6, musicN: 45, effects: 60, noise: 0.05},
	}
	for mi, m := range mixes {
		utts := filmLayout(int64(100+mi), dur)
		wav := filepath.Join(dir, m.name+".wav")
		if err := synthFilm(wav, dur, utts, m, int64(7+mi)); err != nil {
			t.Fatal(err)
		}
		video := filepath.Join(dir, m.name+".mkv")
		ffmpegIT(t, "-f", "lavfi", "-i", fmt.Sprintf("color=c=black:s=160x90:r=24000/1001:d=%d", int(dur.Seconds())),
			"-i", wav, "-map", "0:v", "-map", "1:a", "-c:v", "libx264", "-preset", "ultrafast",
			"-c:a", "aac", "-b:a", "192k", "-ar", "48000", "-ac", "2", "-shortest", video)
		s := &Service{ffmpeg: "ffmpeg"}
		a := s.newJobAudio(video, nil)
		track, err := a.track(context.Background(), -1)
		if err != nil {
			t.Fatal(err)
		}
		sub := subtitlesFor(utts, int64(50+mi), dur)
		jr := rand.New(rand.NewSource(int64(80 + mi)))
		var sloppy []cue
		for _, c := range sub {
			j := func() time.Duration { return time.Duration(jr.Intn(1201)-600) * time.Millisecond }
			st, en := c.start+j(), c.end+j()
			if en > st {
				sloppy = append(sloppy, cue{start: st, end: en, text: c.text})
			}
		}
		for _, style := range []struct {
			name string
			cues []cue
		}{{"timed", sub}, {"sloppy", sloppy}} {
			for _, off := range []int{0, 250, 450, 800, -300} {
				cues := shiftCues(style.cues, off, 1)
				res, judged := track.CheckJudged([]byte(formatSRT(cues)))
				ref, ok := track.refineByStarts(cues, res)
				t.Logf("REFINE %-12s %-6s truth=%5d overlap=%5d refined=%5d ok=%-5v confidence=%3d judged=%v",
					m.name, style.name, off, res.OffsetMs, ref.OffsetMs, ok, res.Confidence, judged)
				if style.name != "timed" {
					continue
				}
				switch m.name {
				case "clean", "film":
					if !ok || abs(ref.OffsetMs-off) > 90 {
						t.Errorf("%s/%s %d ms: refined %d (ok=%v), want within 90 ms", m.name, style.name, off, ref.OffsetMs, ok)
					}
				default:
					if ok && abs(ref.OffsetMs-off) > 120 {
						t.Errorf("%s/%s %d ms: refined to %d, a confident wrong answer", m.name, style.name, off, ref.OffsetMs)
					}
				}
			}
		}
		a.close()
	}
}
