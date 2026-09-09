package subtitles

import (
	"runtime"
	"strings"
	"testing"
)

// The Vulkan build announces its device at startup; a build with no visible device says
// nothing about Vulkan and runs on the CPU. The status panel must tell those apart, since
// "AI ready" on a GPU build that's silently on the CPU is the most likely misconfiguration.
func TestBackendOfReadsWhisperOutput(t *testing.T) {
	for name, tc := range map[string]struct {
		out  string
		want string
	}{
		"vulkan device found":          {"ggml_vulkan: Found 1 Vulkan devices:\nVulkan0: Intel(R) Arc(tm) A380 Graphics (DG2) | uma: 0 | fp16: 1\nwhisper_init_from_file_with_params_no_state: loading model", "vulkan"},
		"vulkan by whisper's own line": {"whisper_backend_init_gpu: using Vulkan0 backend\n", "vulkan"},
		"sycl device found":            {"ggml_sycl_init: SYCL_USE_XMX: yes\nFound 1 SYCL devices:\n| 0| [level_zero:gpu:0]| Intel Arc A380 Graphics| 12.55| 128| 1024| 32| 6001M| 1.6.33276|\nwhisper_backend_init_gpu: using SYCL0 backend\n", "sycl"},
		"gpu build, no device":         {"ggml_sycl_init: SYCL_USE_XMX: yes\nwhisper_backend_init_gpu: no GPU found\nwhisper_init_with_params_no_state: devices = 1", "cpu"},
		"cpu only build":               {"whisper_init_from_file_with_params_no_state: loading model from 'ggml-large-v3-turbo.bin'\nsystem_info: n_threads = 16", "cpu"},
		"empty":                        {"", "cpu"},
	} {
		if got := backendOf([]byte(tc.out)); got != tc.want {
			t.Errorf("%s: backendOf = %q, want %q", name, got, tc.want)
		}
	}
}

// whisper-cli's own default is 4 threads. Passing the host's core count — capped where
// the decoder stops scaling — is the cheapest speedup available.
func TestThreadsUsesTheMachine(t *testing.T) {
	n := threads()
	if n < 1 {
		t.Fatalf("threads() = %d", n)
	}
	if n > 16 {
		t.Errorf("threads() = %d, want capped at 16", n)
	}
	if cpus := runtime.NumCPU(); cpus <= 16 && n != cpus {
		t.Errorf("threads() = %d on a %d-core machine, want all of them", n, cpus)
	}
}

// ggml announces the device it picked on one line; the "matrix cores" field on it is
// what tells a slow Vulkan run apart from a fast one.
func TestDeviceOfReadsGGMLDeviceLine(t *testing.T) {
	out := "ggml_vulkan: Found 1 Vulkan devices:\nggml_vulkan: 0 = Intel(R) Arc(tm) A380 Graphics (DG2) (Intel open-source Mesa driver) | uma: 0 | fp16: 1 | bf16: 0 | fp4: 0 | warp size: 32 | shared memory: 65536 | int dot: 1 | matrix cores: none\nwhisper_init_from_file_with_params_no_state: loading model"
	want := "Intel(R) Arc(tm) A380 Graphics (DG2) (Intel open-source Mesa driver) | uma: 0 | fp16: 1 | bf16: 0 | fp4: 0 | warp size: 32 | shared memory: 65536 | int dot: 1 | matrix cores: none"
	if got := deviceOf([]byte(out)); got != want {
		t.Errorf("deviceOf = %q, want %q", got, want)
	}
	if got := deviceOf([]byte("system_info: n_threads = 16")); got != "" {
		t.Errorf("deviceOf on a CPU run = %q, want empty", got)
	}
	sycl := "ggml_sycl_init: SYCL_USE_XMX: yes\nFound 1 SYCL devices:\n|ID|        Device Type|                                   Name|Version|units  |group   |group|size   |       Driver version|\n|--|-------------------|---------------------------------------|-------|-------|--------|-----|-------|---------------------|\n| 0| [level_zero:gpu:0]|                Intel Arc A380 Graphics|  12.55|    128|    1024|   32|  6001M|            1.6.33276|\nwhisper_backend_init_gpu: using SYCL0 backend"
	if got, want := deviceOf([]byte(sycl)), "Intel Arc A380 Graphics [level_zero:gpu:0] | 128 CUs | 6001M | driver 1.6.33276 | XMX: yes"; got != want {
		t.Errorf("deviceOf(sycl) = %q, want %q", got, want)
	}
}

// The oneAPI build is preferred while it works and retired for the process once it
// doesn't; a note goes out exactly once.
func TestSyclFallbackIsStickyAndNotedOnce(t *testing.T) {
	var notes []string
	w := &whisperGen{bin: "/usr/local/bin/whisper-cli", sycl: "/usr/local/bin/whisper-cli-sycl"}
	w.note = func(m string) { notes = append(notes, m) }
	if got := w.pickBin(); got != w.sycl {
		t.Fatalf("pickBin = %q, want the oneAPI build first", got)
	}
	w.disableSycl("it failed: boom")
	w.disableSycl("again")
	if got := w.pickBin(); got != w.bin {
		t.Errorf("pickBin after a failure = %q, want the portable build", got)
	}
	if len(notes) != 1 || !strings.Contains(notes[0], "boom") {
		t.Errorf("notes = %q, want exactly one carrying the reason", notes)
	}
	if w.SyclNote() != "again" {
		t.Errorf("SyclNote = %q", w.SyclNote())
	}
	if (&whisperGen{bin: "/usr/local/bin/whisper-cli"}).pickBin() != "/usr/local/bin/whisper-cli" {
		t.Error("with no oneAPI build, the portable one runs")
	}
}
