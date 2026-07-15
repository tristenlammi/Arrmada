package convert

import (
	"context"
	"os"
	"os/exec"
	"strings"
)

// Encoder is an available (compiled-in) ffmpeg video encoder for a target codec.
type Encoder struct {
	Codec     string `json:"codec"`     // "hevc" | "av1"
	Name      string `json:"name"`      // ffmpeg encoder, e.g. "hevc_nvenc" | "libx265"
	Kind      string `json:"kind"`      // "nvenc" | "qsv" | "vaapi" | "videotoolbox" | "cpu"
	Label     string `json:"label"`     // human label
	Hardware  bool   `json:"hardware"`  // GPU-accelerated
	Available bool   `json:"available"` // compiled in AND the device looks present
}

// knownEncoders lists the encoders we know how to drive, per target codec, best hardware
// first within each codec (R5 — HEVC / H.264 / AV1 targets). The CPU encoder for each codec
// is always the safe fallback.
var knownEncoders = []Encoder{
	// HEVC (the default "save space" target).
	{Codec: "hevc", Name: "hevc_nvenc", Kind: "nvenc", Label: "NVENC (HEVC)", Hardware: true},
	{Codec: "hevc", Name: "hevc_qsv", Kind: "qsv", Label: "Quick Sync (HEVC)", Hardware: true},
	{Codec: "hevc", Name: "hevc_vaapi", Kind: "vaapi", Label: "VAAPI (HEVC)", Hardware: true},
	{Codec: "hevc", Name: "hevc_videotoolbox", Kind: "videotoolbox", Label: "VideoToolbox (HEVC)", Hardware: true},
	{Codec: "hevc", Name: "libx265", Kind: "cpu", Label: "CPU (x265)", Hardware: false},
	// H.264 (maximum compatibility).
	{Codec: "h264", Name: "h264_nvenc", Kind: "nvenc", Label: "NVENC (H.264)", Hardware: true},
	{Codec: "h264", Name: "h264_qsv", Kind: "qsv", Label: "Quick Sync (H.264)", Hardware: true},
	{Codec: "h264", Name: "h264_vaapi", Kind: "vaapi", Label: "VAAPI (H.264)", Hardware: true},
	{Codec: "h264", Name: "h264_videotoolbox", Kind: "videotoolbox", Label: "VideoToolbox (H.264)", Hardware: true},
	{Codec: "h264", Name: "libx264", Kind: "cpu", Label: "CPU (x264)", Hardware: false},
	// AV1 (smallest, newest).
	{Codec: "av1", Name: "av1_nvenc", Kind: "nvenc", Label: "NVENC (AV1)", Hardware: true},
	{Codec: "av1", Name: "av1_qsv", Kind: "qsv", Label: "Quick Sync (AV1)", Hardware: true},
	{Codec: "av1", Name: "av1_vaapi", Kind: "vaapi", Label: "VAAPI (AV1)", Hardware: true},
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
	return out
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

func hasCmd(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}
