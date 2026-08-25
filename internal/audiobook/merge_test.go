package audiobook

import "testing"

// The merge exists to make ONE file, not a smaller one. Where the container can carry the
// sources untouched it must remux — the audio then survives byte-for-byte.
func TestPlanCopiesWhenTheContainerCanCarryTheSources(t *testing.T) {
	aac := func(rate, ch int) audioInfo {
		return audioInfo{Codec: "aac", BitrateBPS: 128_000, SampleRate: rate, Channels: ch}
	}

	p := planOf([]audioInfo{aac(44100, 2), aac(44100, 2), aac(44100, 2)})
	if !p.Copy {
		t.Error("matching AAC sources must be copied, not re-encoded")
	}

	// Any disagreement means a decoder re-init mid-stream, which plays wrong from that
	// point on — and the sources are deleted afterwards, so there's no second chance.
	for _, mixed := range [][]audioInfo{
		{aac(44100, 2), aac(22050, 2)}, // sample rate changes
		{aac(44100, 2), aac(44100, 1)}, // channel count changes
		{aac(44100, 2), {Codec: "mp3", BitrateBPS: 128_000, SampleRate: 44100, Channels: 2}},
	} {
		if planOf(mixed).Copy {
			t.Errorf("sources that disagree must be re-encoded, not copied: %+v", mixed)
		}
	}

	// MP3 plays inconsistently inside MP4, so a set of them is re-encoded even when every
	// file agrees — a "successful" merge some devices refuse is worse than a re-encode.
	mp3 := audioInfo{Codec: "mp3", BitrateBPS: 128_000, SampleRate: 44100, Channels: 2}
	if planOf([]audioInfo{mp3, mp3}).Copy {
		t.Error("MP3 must not be copied into an m4b")
	}

	// An unreadable file zeroes out — that must never be mistaken for copyable.
	if planOf([]audioInfo{{}, {}}).Copy {
		t.Error("unprobeable sources must not be copied")
	}
}

// The old fixed 64k halved a 128k MP3 and gutted GraphicAudio — full cast, score and
// effects, nothing like the single narrator that number assumes.
func TestEncodeBitrateIsSizedFromTheSource(t *testing.T) {
	at := func(bps int64) audioInfo { return audioInfo{Codec: "mp3", BitrateBPS: bps} }

	// A 128k source must come out at or above its own bitrate, never below.
	if got := encodeBitrate([]audioInfo{at(128_000)}); got < 128_000 {
		t.Errorf("128k source → %d bps, want at least its own bitrate", got)
	}
	// GraphicAudio ships high; the encode has to follow it up, not flatten it.
	if got := encodeBitrate([]audioInfo{at(192_000)}); got <= 128_000 {
		t.Errorf("192k source → %d bps, want more than a spoken-word default", got)
	}
	// The loudest file sets the target — a set is only as good as its best member.
	if got := encodeBitrate([]audioInfo{at(64_000), at(256_000)}); got < 256_000 {
		t.Errorf("mixed set → %d bps, want the peak source honoured", got)
	}
	// Floors and caps.
	if got := encodeBitrate([]audioInfo{at(32_000)}); got != minEncodeBPS {
		t.Errorf("a very low source → %d bps, want the %d floor", got, minEncodeBPS)
	}
	if got := encodeBitrate([]audioInfo{at(1_000_000)}); got != maxEncodeBPS {
		t.Errorf("an enormous source → %d bps, want the %d cap", got, maxEncodeBPS)
	}
	// Nothing readable: still land on the floor rather than 0, which would make ffmpeg
	// pick its own (low) default.
	if got := encodeBitrate([]audioInfo{{}}); got != minEncodeBPS {
		t.Errorf("unknown source → %d bps, want the %d floor", got, minEncodeBPS)
	}
}

// Mixed sources are the reason we're encoding; the output has to be pinned to the best of
// them and resampled, or the join drifts and the chapter timings go with it.
func TestEncodeArgsNormaliseMixedSources(t *testing.T) {
	mixed := []audioInfo{
		{Codec: "mp3", BitrateBPS: 128_000, SampleRate: 22050, Channels: 1},
		{Codec: "mp3", BitrateBPS: 128_000, SampleRate: 44100, Channels: 2},
	}
	args := encodeArgs(mixed)
	joined := ""
	for _, a := range args {
		joined += a + " "
	}
	for _, want := range []string{"-c:a aac", "-ar 44100", "-ac 2", "aresample"} {
		if !contains(joined, want) {
			t.Errorf("args %q missing %q", joined, want)
		}
	}

	// The copy path takes none of that — no bitrate, no resampling, nothing to degrade.
	aac := audioInfo{Codec: "aac", BitrateBPS: 128_000, SampleRate: 44100, Channels: 2}
	copyArgs := encodeArgs([]audioInfo{aac, aac})
	if len(copyArgs) != 2 || copyArgs[0] != "-c:a" || copyArgs[1] != "copy" {
		t.Errorf("copy path args = %v, want exactly -c:a copy", copyArgs)
	}
}

func contains(hay, needle string) bool {
	for i := 0; i+len(needle) <= len(hay); i++ {
		if hay[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
