package convert

// Plan is the compiled description of a conversion — what the flow decided to do, separated
// from how it's executed. A flow (Rules v2) builds a Plan by walking its nodes; for now the
// "Save space" preset builds it from the global settings. The compiler (compileOutputArgs)
// turns a Plan into an ffmpeg command, and preview reads the Plan directly — which is what
// makes the preview exact (you preview the literal thing that will run).
type Plan struct {
	// Video.
	VideoCodec  string // target video codec: "hevc" | "h264" | "av1"; "" = copy (remux only)
	Quality     int    // CRF/CQ target; 0 = codec default (hw encoders map internally)
	VFRToCFR    bool   // normalize variable frame rate when present
	ScaleHeight int    // downscale to this height (0 = keep); never upscales

	Audio     AudioPlan
	Container string // "mkv" | "mp4"

	// HealthCheck, with no transcode (VideoCodec == ""), turns the job into a read-only
	// corruption scan that reports issues instead of replacing the file (R5).
	HealthCheck bool
	// ExtraArgs are raw ffmpeg output args appended verbatim — the advanced escape hatch
	// for anything the structured actions don't cover (R5). Empty for the common case.
	ExtraArgs []string
}

// AudioPlan is the audio portion of a Plan.
type AudioPlan struct {
	KeepLangs []string // keep only these languages (empty = keep all)
	AddStereo bool     // add an AAC 2.0 downmix beside surround tracks
	Loudnorm  bool     // EBU R128 loudness normalize (re-encodes to AAC)
}
