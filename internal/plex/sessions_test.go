package plex

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

// sessionsFrom serves a /status/sessions fixture and reads it through the real client.
//
// The fixtures are modelled on Plex's TranscodeSession fields. They are not yet captured
// from the owner's server: replace them with a real capture of one hardware transcode and
// one forced CPU transcode when one is taken (the field types are undocumented).
func sessionsFrom(t *testing.T, fixture string) []Session {
	t.Helper()
	body, err := os.ReadFile("testdata/" + fixture)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/status/sessions" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	out, err := New(srv.URL, "test-token").Sessions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 {
		t.Fatalf("want 1 session, got %d", len(out))
	}
	return out
}

// The HW flags come from what Plex reports it is using, not from transcodeHwRequested: a
// CPU fallback with hardware switched on must not read as a hardware transcode.
func TestFlattenHWFields(t *testing.T) {
	hw := sessionsFrom(t, "sessions_hw.json")[0]
	if !hw.HWRequested || !hw.HWDecode || !hw.HWEncode || !hw.HWFullPipeline || !hw.TranscodeHW {
		t.Errorf("hardware transcode: %+v", hw)
	}
	if hw.HWTitle != "Intel (QuickSync)" {
		t.Errorf("hw title = %q", hw.HWTitle)
	}

	cpu := sessionsFrom(t, "sessions_cpu_fallback.json")[0]
	if !cpu.HWRequested || cpu.HWDecode || cpu.HWEncode || cpu.HWFullPipeline || cpu.TranscodeHW || cpu.HWTitle != "" {
		t.Errorf("cpu fallback: requested=%v dec=%v enc=%v full=%v hw=%v title=%q",
			cpu.HWRequested, cpu.HWDecode, cpu.HWEncode, cpu.HWFullPipeline, cpu.TranscodeHW, cpu.HWTitle)
	}
	if !cpu.Transcoding || cpu.StreamWidth != 1280 || cpu.SubDecision != "burn" {
		t.Errorf("the rest of the transcode still reads: %+v", cpu)
	}

	direct := sessionsFrom(t, "sessions_direct.json")[0]
	if direct.Transcoding || direct.HWRequested || direct.TranscodeHW {
		t.Errorf("direct play: %+v", direct)
	}
}

func TestFlexStr(t *testing.T) {
	for in, want := range map[string]bool{
		`"qsv"`: true, `"vaapi"`: true, `true`: true, `1`: true, `"1"`: true,
		`false`: false, `null`: false, `""`: false, `"0"`: false, `0`: false, `{}`: false,
	} {
		var f flexStr
		if err := json.Unmarshal([]byte(in), &f); err != nil {
			t.Errorf("%s: %v", in, err)
			continue
		}
		if f.on() != want {
			t.Errorf("%s: on = %v, want %v", in, f.on(), want)
		}
	}
}

// Buffering during a CPU fallback blames the fallback, not the GPU; a GPU decode with a
// CPU encode is still a fallback (the encoder sets the pace).
func TestBufferCauseFallback(t *testing.T) {
	cases := []struct {
		name string
		s    Session
		want string
	}{
		{"hw encode slow", Session{Transcoding: true, TranscodeSpeed: 0.8, HWRequested: true, HWDecode: true, HWEncode: true}, "transcode"},
		{"requested, cpu used", Session{Transcoding: true, TranscodeSpeed: 0.7, HWRequested: true}, "transcode_fallback"},
		{"hw decode only", Session{Transcoding: true, TranscodeSpeed: 0.7, HWRequested: true, HWDecode: true}, "transcode_fallback"},
		{"hardware off", Session{Transcoding: true, TranscodeSpeed: 0.7}, "transcode_cpu"},
	}
	for _, c := range cases {
		if got, _ := c.s.BufferCause(); got != c.want {
			t.Errorf("%s: got %q want %q", c.name, got, c.want)
		}
	}
	got, _ := sessionsFrom(t, "sessions_cpu_fallback.json")[0].BufferCause()
	if got != "transcode_fallback" {
		t.Errorf("captured cpu fallback buffering: got %q", got)
	}
}
