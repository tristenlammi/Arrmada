package subtitles

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const syclTable = `ggml_sycl_init: SYCL_USE_XMX: yes
Found 4 SYCL devices:
|  |                   |                                       |       |Max    |        |Max  |Global |                     |
|ID|        Device Type|                                   Name|Version|units  |group   |group|size   |       Driver version|
|--|-------------------|---------------------------------------|-------|-------|--------|-----|-------|---------------------|
| 0| [level_zero:gpu:0]|                      Intel(R) Graphics|  12.74|     64|    1024|   32| 30000M|            1.6.33578|
| 1| [level_zero:gpu:1]|            Intel(R) Arc(TM) A380 Graphics|  12.55|    128|    1024|   32|  6001M|            1.6.33578|
| 2|     [opencl:gpu:0]|                      Intel(R) Graphics|    3.0|     64|    1024|   32| 30000M|      25.18.33578.15|
| 3|     [opencl:gpu:1]|            Intel(R) Arc(TM) A380 Graphics|    3.0|    128|    1024|   32|  6001M|      25.18.33578.15|
ggml_sycl_set_main_device: using device 1 (Intel(R) Arc(TM) A380 Graphics) as main device
whisper_backend_init_gpu: using SYCL1 backend
`

// With the CPU's graphics enumerated first, the discrete card must still be the one
// chosen, through Level Zero rather than its OpenCL twin; the device line reports the
// device ggml actually used.
func TestSyclDeviceTableAndChoice(t *testing.T) {
	rows := syclDevices([]byte(syclTable))
	if len(rows) != 4 || rows[1].Name != "Intel(R) Arc(TM) A380 Graphics" || rows[1].CUs != 128 || rows[3].Type != "[opencl:gpu:1]" {
		t.Fatalf("rows = %+v", rows)
	}
	if got := chooseSyclDevice(rows); got != 1 {
		t.Errorf("chooseSyclDevice = %d, want 1 (the Arc over the integrated graphics, Level Zero over OpenCL)", got)
	}
	if got := chooseSyclDevice(nil); got != -1 {
		t.Errorf("no rows: %d", got)
	}
	if got := syclMainDevice([]byte(syclTable)); got != 1 {
		t.Errorf("syclMainDevice = %d", got)
	}
	want := "Intel(R) Arc(TM) A380 Graphics [level_zero:gpu:1] | 128 CUs | 6001M | driver 1.6.33578 | XMX: yes"
	if got := deviceOf([]byte(syclTable)); got != want {
		t.Errorf("deviceOf = %q, want %q", got, want)
	}
	if got := backendOf([]byte(syclTable)); got != "sycl" {
		t.Errorf("backendOf = %q", got)
	}
	// A lone integrated GPU is still a GPU.
	if got := chooseSyclDevice(rows[:1]); got != 0 {
		t.Errorf("single device: %d", got)
	}
}

func TestWriteSilentWAV(t *testing.T) {
	p := filepath.Join(t.TempDir(), "s.wav")
	if err := writeSilentWAV(p, time.Second); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	if len(b) != 44+16000*2 || string(b[:4]) != "RIFF" || string(b[8:16]) != "WAVEfmt " {
		t.Errorf("wav = %d bytes, head %q", len(b), b[:16])
	}
	if d := wavDuration(p); d < 990*time.Millisecond || d > 1010*time.Millisecond {
		t.Errorf("wavDuration = %v, want 1s", d)
	}
}

// A crash's useful line is the assertion or exception, not the backtrace under it.
func TestFirstErrorSkipsTheBacktrace(t *testing.T) {
	out := "whisper_init_with_params_no_state: use gpu = 1\nggml_sycl_init: SYCL_USE_XMX: yes\nFound 1 SYCL devices:\n/src/ggml/src/ggml-sycl/ggml-sycl.cpp:1234: GGML_ASSERT(ptr != nullptr) failed\n[0x4dd072]\n/opt/whisper-sycl/whisper-cli[0x4e728a]\n"
	if got := firstError([]byte(out)); !strings.Contains(got, "GGML_ASSERT") {
		t.Errorf("firstError = %q", got)
	}
	if got := firstError([]byte("just\nnoise")); got != "just ⏎ noise" {
		t.Errorf("fallback = %q", got)
	}
}
