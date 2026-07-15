package quality

// duneRuntimeMin is Dune: Part Two's runtime — attached to the sample candidates so the preview
// reflects a profile's bitrate ceiling (82 GB ≈ 71 Mbps, 24 GB ≈ 21 Mbps at this length).
const duneRuntimeMin = 166

// SampleCandidates is a fixed set of real-world releases (Dune: Part Two) used
// by the quality-preview endpoint and docs to demonstrate the engine without a
// configured indexer. Mirrors the approved mockup.
func SampleCandidates() []Candidate {
	cands := []Candidate{
		NewCandidate("Dune.Part.Two.2024.2160p.UHD.BluRay.REMUX.DV.HDR.TrueHD.Atmos.7.1-FraMeSToR", 82, 44),
		NewCandidate("Dune.Part.Two.2024.2160p.WEB-DL.DDP5.1.Atmos.DV.HDR.H.265-FLUX", 24, 212),
		NewCandidate("Dune.Part.Two.2024.2160p.WEB-DL.DDP5.1.HDR.H.265-RUMOUR", 19, 61),
		NewCandidate("Dune.Part.Two.2024.1080p.BluRay.x264.DTS-HD.MA.5.1-SPARKS", 16, 180),
		NewCandidate("Dune.Part.Two.2024.1080p.WEB-DL.DDP5.1.H.264-EVO", 9, 340),
		NewCandidate("Dune.Part.Two.2024.1080p.WEBRip.x265.AAC5.1-YTS", 3.8, 990),
		NewCandidate("Dune.Part.Two.2024.720p.HDTV.x264-GALAXY", 1.5, 22),
	}
	for i := range cands {
		cands[i].RuntimeMin = duneRuntimeMin
	}
	return cands
}
