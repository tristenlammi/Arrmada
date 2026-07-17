package plex

import "testing"

func TestBufferCause(t *testing.T) {
	cases := []struct {
		name string
		s    Session
		want string
	}{
		{"cpu transcode slow", Session{Transcoding: true, TranscodeSpeed: 0.6, TranscodeHW: false}, "transcode_cpu"},
		{"hw transcode slow", Session{Transcoding: true, TranscodeSpeed: 0.8, TranscodeHW: true}, "transcode"},
		{"healthy transcode local", Session{Transcoding: true, TranscodeSpeed: 1.4, Local: true}, "transcode"},
		{"remote direct play, low bw", Session{Local: false, Bandwidth: 3000, SrcBitrate: 8000}, "bandwidth"},
		{"remote generic", Session{Local: false}, "bandwidth"},
		{"local direct play", Session{Local: true}, "unknown"},
	}
	for _, c := range cases {
		if got, _ := c.s.BufferCause(); got != c.want {
			t.Errorf("%s: got %q want %q", c.name, got, c.want)
		}
	}
}
