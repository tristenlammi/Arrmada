package insights

import (
	"testing"
	"time"

	"github.com/tristenlammi/arrmada/internal/plex"
)

// TestObserveBufferSpells verifies buffering is debounced into one event per spell.
func TestObserveBufferSpells(t *testing.T) {
	t0 := time.Unix(1_700_000_000, 0)
	ls := &liveSession{started: t0, lastTick: t0, state: "playing"}
	states := []string{"playing", "buffering", "buffering", "playing", "buffering", "playing"}
	for i, st := range states {
		ls.observe(plex.Session{State: st, OffsetMS: int64(i) * 1000}, t0.Add(time.Duration(i)*time.Second))
	}
	// Two distinct spells (indices 1-2 and 4), not four.
	if ls.bufCount != 2 {
		t.Fatalf("bufCount = %d, want 2", ls.bufCount)
	}
	if len(ls.bufEvents) != 2 {
		t.Fatalf("bufEvents = %d, want 2", len(ls.bufEvents))
	}
}

// TestObservePausedAccrual verifies paused time accrues across intervals spent paused.
func TestObservePausedAccrual(t *testing.T) {
	t0 := time.Unix(1_700_000_000, 0)
	ls := &liveSession{started: t0, lastTick: t0, state: "playing"}
	// playing @0, paused @10, still paused @25 (15s paused accrues), playing @30.
	ls.observe(plex.Session{State: "paused"}, t0.Add(10*time.Second))
	ls.observe(plex.Session{State: "paused"}, t0.Add(25*time.Second))
	ls.observe(plex.Session{State: "playing"}, t0.Add(30*time.Second))
	// From the 10s tick (state became paused) to the 25s tick: prior state paused → +15s.
	// From 25s to 30s: prior state paused → +5s. Total 20s.
	if ls.pausedMS != 20_000 {
		t.Fatalf("pausedMS = %d, want 20000", ls.pausedMS)
	}
}

// TestRecordTranscode verifies the finalized record captures the decision + source→stream detail.
func TestRecordTranscode(t *testing.T) {
	t0 := time.Unix(1_700_000_000, 0)
	sess := plex.Session{
		SessionKey: "1", UserID: "42", UserName: "Bob", Type: "movie", Title: "Dune",
		SrcVideoCodec: "hevc", SrcResolution: "4k", SrcAudioCodec: "truehd", SrcContainer: "mkv",
		Transcoding: true, VideoDecision: "transcode", AudioDecision: "transcode",
		StreamVideoCodec: "h264", StreamHeight: 1080, StreamAudioCodec: "aac", TranscodeCont: "mp4",
		PublicIP: "8.8.8.8", Location: "wan", OffsetMS: 5000, DurationMS: 9000000,
	}
	ls := &liveSession{started: t0, lastTick: t0, state: "playing", sess: sess}
	ls.observe(sess, t0.Add(3*time.Second))
	rec := ls.record(t0.Add(1 * time.Hour))

	if rec.Decision != "transcode" {
		t.Errorf("decision = %q, want transcode", rec.Decision)
	}
	if rec.VideoSrc != "HEVC 4K" || rec.VideoStream != "H264 1080p" {
		t.Errorf("video = %q → %q, want HEVC 4K → H264 1080p", rec.VideoSrc, rec.VideoStream)
	}
	if rec.AudioSrc != "TRUEHD" || rec.AudioStream != "AAC" {
		t.Errorf("audio = %q → %q, want TRUEHD → AAC", rec.AudioSrc, rec.AudioStream)
	}
	if rec.ContainerSrc != "MKV" || rec.ContainerStream != "MP4" {
		t.Errorf("container = %q → %q, want MKV → MP4", rec.ContainerSrc, rec.ContainerStream)
	}
	if rec.IPAddress != "8.8.8.8" || rec.UserID != "42" || rec.StartedAt != t0.Unix() {
		t.Errorf("meta wrong: ip=%q user=%q started=%d", rec.IPAddress, rec.UserID, rec.StartedAt)
	}
}

// TestDirectPlayRecord verifies a direct-play stream records no transcode target.
func TestDirectPlayRecord(t *testing.T) {
	t0 := time.Unix(1_700_000_000, 0)
	sess := plex.Session{Title: "Scooby-Doo", Type: "movie", SrcVideoCodec: "h264", SrcResolution: "1080"}
	ls := &liveSession{started: t0, lastTick: t0, state: "playing", sess: sess}
	rec := ls.record(t0.Add(time.Hour))
	if rec.Decision != "direct_play" {
		t.Errorf("decision = %q, want direct_play", rec.Decision)
	}
	if rec.VideoStream != "" || rec.AudioStream != "" {
		t.Errorf("direct play should have empty stream targets, got video=%q audio=%q", rec.VideoStream, rec.AudioStream)
	}
}
