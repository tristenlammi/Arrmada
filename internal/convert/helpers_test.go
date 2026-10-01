package convert

import (
	"context"
	"testing"

	"github.com/tristenlammi/arrmada/internal/settings"
	"github.com/tristenlammi/arrmada/internal/store"
)

// newTestService is a Service over a real, migrated database with working CPU encoders and
// no movie/series modules — enough for the decision layer, the stores and the runner.
func newTestService(t *testing.T) *Service {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	db := st.DB()
	return &Service{
		db: db, settings: settings.NewService(db), log: testLogger(),
		failures: &failureStore{db: db}, cache: &probeCache{db: db}, logs: &logStore{db: db},
		index: &libraryIndex{db: db}, skips: &skipStore{db: db}, requests: &requestStore{db: db},
		choices: &choiceStore{db: db}, pending: map[string]*Job{}, wake: make(chan struct{}, 1),
		encoders: workingCPUEncoders(),
	}
}

func workingCPUEncoders() []Encoder {
	return []Encoder{
		{Codec: "hevc", Name: "libx265", Kind: "cpu", Label: "CPU (x265)", Available: true},
		{Codec: "av1", Name: "libsvtav1", Kind: "cpu", Label: "CPU (SVT-AV1)", Available: true},
	}
}

func set(t *testing.T, s *Service, kv map[string]string) {
	t.Helper()
	for k, v := range kv {
		if err := s.settings.Set(context.Background(), k, v); err != nil {
			t.Fatal(err)
		}
	}
}

// film is a 2-hour 1080p24 file at the given total bitrate, with the given audio tracks.
func film(codec string, w, h, kbps int, audio ...AudioStream) *MediaInfo {
	mi := &MediaInfo{VideoCodec: codec, Width: w, Height: h, FrameRate: 23.976, BitrateKbps: kbps,
		DurationSec: 7200, HDR: "SDR", Audio: audio, AudioTracks: len(audio)}
	mi.SizeBytes = int64(kbps) * 1000 / 8 * 7200
	for i := range mi.Audio {
		mi.Audio[i].AudIndex = i
	}
	return mi
}

func aud(codec, lang string, ch int) AudioStream {
	return AudioStream{Codec: codec, Lang: lang, Channels: ch}
}

func withSubs(mi *MediaInfo, subs ...SubStream) *MediaInfo {
	for i := range subs {
		subs[i].SubIndex = i
	}
	mi.Subs, mi.SubTracks = subs, len(subs)
	return mi
}

func pgs(lang string) SubStream { return SubStream{Codec: "hdmv_pgs_subtitle", Lang: lang} }
func srt(lang string) SubStream { return SubStream{Codec: "subrip", Lang: lang, Text: true} }
