package convert

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// Encoder is an available (compiled-in) ffmpeg video encoder for a target codec.
type Encoder struct {
	Codec    string `json:"codec"`    // "hevc" | "av1"
	Name     string `json:"name"`     // ffmpeg encoder, e.g. "hevc_nvenc" | "libx265"
	Kind     string `json:"kind"`     // "nvenc" | "qsv" | "vaapi" | "videotoolbox" | "cpu"
	Label    string `json:"label"`    // human label
	Hardware bool   `json:"hardware"` // GPU-accelerated
	// Available means VERIFIED: compiled in, its device is present, AND a one-frame test
	// encode actually succeeded at startup. The last part matters — Quick Sync can
	// initialise and then fail, and a broken libx265 build segfaults instantly.
	Available bool `json:"available"`
	// Note carries why an encoder was rejected, so the UI can explain it.
	Note string `json:"note,omitempty"`
}

// knownEncoders lists the encoders we know how to drive, per target codec, best hardware
// first within each codec (R5 — HEVC / H.264 / AV1 targets). The CPU encoder for each codec
// is always the safe fallback.
// VAAPI is listed before QSV within each codec: on Intel both drive the same
// media engine, but VAAPI (via intel-media-driver) is the dependable path on this
// Alpine image, whereas QSV needs the oneVPL runtime. Preferring VAAPI avoids a
// "QSV init fails → fall back to CPU" loop on Intel boxes.
// Order is preference order: the first VERIFIED entry for a codec wins.
//
// VAAPI sits ahead of Quick Sync. I briefly reversed this on the reasoning that QSV's ICQ
// is a better rate-control mode than VAAPI's CQP — true on paper, but QSV fails to
// initialise on at least one real Intel Arc setup (exit 171) where VAAPI encodes happily.
// A theoretically better mode is worth nothing if the encoder doesn't run.
//
// This ordering matters much less now that encoders are verified with a real test encode at
// startup rather than assumed to work, so a broken one is dropped before it's ever chosen.
var knownEncoders = []Encoder{
	// HEVC (the default "save space" target).
	{Codec: "hevc", Name: "hevc_nvenc", Kind: "nvenc", Label: "NVENC (HEVC)", Hardware: true},
	{Codec: "hevc", Name: "hevc_vaapi", Kind: "vaapi", Label: "VAAPI (HEVC)", Hardware: true},
	{Codec: "hevc", Name: "hevc_qsv", Kind: "qsv", Label: "Quick Sync (HEVC)", Hardware: true},
	{Codec: "hevc", Name: "hevc_videotoolbox", Kind: "videotoolbox", Label: "VideoToolbox (HEVC)", Hardware: true},
	{Codec: "hevc", Name: "libx265", Kind: "cpu", Label: "CPU (x265)", Hardware: false},
	// H.264 (maximum compatibility).
	{Codec: "h264", Name: "h264_nvenc", Kind: "nvenc", Label: "NVENC (H.264)", Hardware: true},
	{Codec: "h264", Name: "h264_vaapi", Kind: "vaapi", Label: "VAAPI (H.264)", Hardware: true},
	{Codec: "h264", Name: "h264_qsv", Kind: "qsv", Label: "Quick Sync (H.264)", Hardware: true},
	{Codec: "h264", Name: "h264_videotoolbox", Kind: "videotoolbox", Label: "VideoToolbox (H.264)", Hardware: true},
	{Codec: "h264", Name: "libx264", Kind: "cpu", Label: "CPU (x264)", Hardware: false},
	// AV1 (smallest, newest).
	{Codec: "av1", Name: "av1_nvenc", Kind: "nvenc", Label: "NVENC (AV1)", Hardware: true},
	{Codec: "av1", Name: "av1_vaapi", Kind: "vaapi", Label: "VAAPI (AV1)", Hardware: true},
	{Codec: "av1", Name: "av1_qsv", Kind: "qsv", Label: "Quick Sync (AV1)", Hardware: true},
	{Codec: "av1", Name: "libsvtav1", Kind: "cpu", Label: "CPU (SVT-AV1)", Hardware: false},
}

// detectEncoders reports which known encoders are compiled into ffmpeg and whether the
// matching device appears present (so hardware options aren't offered on a box that can't use
// them). The CPU encoder for each codec is always available as the safe fallback.
func detectEncoders(ctx context.Context, ffmpeg string) []Encoder {
	compiled := map[string]bool{}
	if out, err := exec.CommandContext(ctx, ffmpeg, "-hide_banner", "-encoders").Output(); err == nil {
		for _, line := range strings.Split(string(out), "\n") {
			f := strings.Fields(line)
			if len(f) >= 2 {
				compiled[f[1]] = true
			}
		}
	}
	dri := deviceExists("/dev/dri")
	intel, amd, _ := gpuVendors()
	nvidia := deviceExists("/dev/nvidia0") || hasCmd("nvidia-smi")
	var out []Encoder
	for _, e := range knownEncoders {
		if !compiled[e.Name] {
			continue
		}
		switch e.Kind {
		case "cpu":
			e.Available = true
		case "nvenc":
			e.Available = nvidia
		case "qsv": // Intel only
			e.Available = dri && intel
		case "vaapi": // AMD + Intel
			e.Available = dri && (amd || intel)
		case "videotoolbox":
			e.Available = true // presence implies macOS host
		}
		out = append(out, e)
	}
	// "Compiled in and the device exists" is NOT the same as "works". Both assumptions
	// have failed in practice: Quick Sync initialises and then exits 171 on some Intel
	// parts, and Alpine's libx265 segfaults instantly on some newer CPUs. Trusting the
	// cheap check meant every job burned an attempt on an encoder that could never work,
	// and — worse — routing decisions were made around encoders that don't run at all.
	//
	// So actually encode one frame with each. It costs a few seconds at startup and turns
	// a whole class of runtime failure into a fact known before any file is touched.
	verifyEncoders(ctx, ffmpeg, out)
	return out
}

// verifyEncoders test-encodes a single frame with every available encoder and clears
// Available on any that fail. Runs them concurrently so startup isn't serialized.
func verifyEncoders(ctx context.Context, ffmpeg string, encs []Encoder) {
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	for i := range encs {
		if !encs[i].Available {
			continue
		}
		wg.Add(1)
		go func(e *Encoder) {
			defer wg.Done()
			if err := probeEncoder(ctx, ffmpeg, *e); err != nil {
				e.Available = false
				e.Note = firstLine(err.Error())
			}
		}(&encs[i])
	}
	wg.Wait()
}

// probeEncoder encodes one tiny frame, proving the encoder actually runs on this machine.
func probeEncoder(ctx context.Context, ffmpeg string, e Encoder) error {
	args := []string{"-hide_banner", "-loglevel", "error"}
	args = append(args, globalArgs(e, false, "")...)
	args = append(args, "-f", "lavfi", "-i", "testsrc=d=0.1:s=320x240")
	if e.Kind == "vaapi" {
		args = append(args, "-vf", "format=nv12,hwupload")
	}
	args = append(args, "-c:v", e.Name, "-f", "null", "-")
	out, err := exec.CommandContext(ctx, ffmpeg, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, 0x0a); i >= 0 {
		return s[:i]
	}
	return s
}

// encoderFor picks the encoder to use for a target codec: an available hardware encoder if
// present, else that codec's CPU encoder (always available). "" targets HEVC (the default).
func encoderFor(codec string, encs []Encoder) Encoder {
	if codec == "" {
		codec = "hevc"
	}
	for _, e := range encs { // hardware first
		if e.Codec == codec && e.Hardware && e.Available {
			return e
		}
	}
	for _, e := range encs { // detected CPU encoder
		if e.Codec == codec && e.Kind == "cpu" && e.Available {
			return e
		}
	}
	return cpuEncoder(codec) // last-resort fallback
}

// cpuWorks reports whether the CPU encoder for a codec passed startup verification. It is
// NOT a given: Alpine's libx265 segfaults on some newer CPUs, in which case routing HDR or
// 4K to "the safe CPU path" would send every such file to a guaranteed failure.
func cpuWorks(codec string, encs []Encoder) bool {
	want := cpuEncoder(codec).Name
	for _, e := range encs {
		if e.Name == want {
			return e.Available
		}
	}
	return false
}

// cpuEncoder returns the CPU software encoder for a codec (the guaranteed fallback path).
func cpuEncoder(codec string) Encoder {
	switch codec {
	case "h264":
		return Encoder{Codec: "h264", Name: "libx264", Kind: "cpu", Label: "CPU (x264)", Available: true}
	case "av1":
		return Encoder{Codec: "av1", Name: "libsvtav1", Kind: "cpu", Label: "CPU (SVT-AV1)", Available: true}
	default:
		return Encoder{Codec: "hevc", Name: "libx265", Kind: "cpu", Label: "CPU (x265)", Available: true}
	}
}

// gpuVendors reads the PCI vendor of each DRM render node so we prefer the right encoder
// per GPU (QSV is Intel-only; AMD goes through VAAPI). Empty on non-Linux / no GPU.
func gpuVendors() (intel, amd, any bool) {
	entries, err := os.ReadDir("/sys/class/drm")
	if err != nil {
		return
	}
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), "renderD") {
			continue
		}
		b, err := os.ReadFile("/sys/class/drm/" + e.Name() + "/device/vendor")
		if err != nil {
			continue
		}
		switch strings.TrimSpace(string(b)) {
		case "0x8086":
			intel, any = true, true
		case "0x1002":
			amd, any = true, true
		}
	}
	return
}

// bestHEVC picks the encoder to use: an available hardware encoder if present, else CPU.
func bestHEVC(encs []Encoder) Encoder {
	for _, e := range encs {
		if e.Hardware && e.Available {
			return e
		}
	}
	for _, e := range encs {
		if e.Kind == "cpu" {
			return e
		}
	}
	return Encoder{Codec: "hevc", Name: "libx265", Kind: "cpu", Label: "CPU (x265)", Available: true}
}

func deviceExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// RenderDevice is one /dev/dri/renderD* node the user can point VAAPI at (a box
// with an iGPU + a discrete card has several).
type RenderDevice struct {
	Path   string `json:"path"`   // /dev/dri/renderD128
	PCI    string `json:"pci"`    // 0000:03:00.0
	Vendor string `json:"vendor"` // Intel | AMD | NVIDIA | ...
}

// renderDevices lists the DRM render nodes present, each with its PCI address and
// GPU vendor — enough for the user to tell the iGPU from the discrete Arc.
func renderDevices() []RenderDevice {
	entries, err := os.ReadDir("/dev/dri")
	if err != nil {
		return nil
	}
	var out []RenderDevice
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, "renderD") {
			continue
		}
		d := RenderDevice{Path: "/dev/dri/" + name}
		if link, err := os.Readlink("/sys/class/drm/" + name + "/device"); err == nil {
			d.PCI = filepathBase(link)
		}
		if v, err := os.ReadFile("/sys/class/drm/" + name + "/device/vendor"); err == nil {
			d.Vendor = vendorName(strings.TrimSpace(string(v)))
		}
		out = append(out, d)
	}
	return out
}

func vendorName(id string) string {
	switch id {
	case "0x8086":
		return "Intel"
	case "0x1002":
		return "AMD"
	case "0x10de":
		return "NVIDIA"
	}
	return id
}

func filepathBase(p string) string {
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		return p[i+1:]
	}
	return p
}

func hasCmd(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

// numaPoolsBlocked reports whether x265's NUMA thread-pool setup is denied in this
// environment.
//
// x265 binds its worker pools to NUMA nodes with set_mempolicy(2). Docker's default seccomp
// profile blocks that syscall unless the container has CAP_SYS_NICE, and x265 responds by
// logging "set_mempolicy: Operation not permitted" once per pool — then, on some CPUs
// (hybrid P-core/E-core parts in particular), dying with a segfault partway into the encode.
//
// Rather than guess, probe it: encode a single frame and look for the warning. When it's
// present every subsequent encode gets pools=none, which trades some threading efficiency
// for actually completing. Adding SYS_NICE to the container makes the probe pass and
// restores full threading with no further change.
func numaPoolsBlocked(ctx context.Context, ffmpeg string) bool {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out, _ := exec.CommandContext(ctx, ffmpeg,
		"-hide_banner", "-loglevel", "warning",
		"-f", "lavfi", "-i", "testsrc=d=0.1:s=64x64",
		"-c:v", "libx265", "-preset", "ultrafast", "-f", "null", "-",
	).CombinedOutput()
	return strings.Contains(string(out), "set_mempolicy")
}
