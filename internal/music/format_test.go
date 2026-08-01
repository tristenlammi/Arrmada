package music

import "testing"

func TestFormatClassification(t *testing.T) {
	for _, f := range []string{"FLAC", "flac", "ALAC", "WAV"} {
		if !IsLossless(f) || IsLossy(f) {
			t.Errorf("%q should be lossless", f)
		}
	}
	for _, f := range []string{"MP3", "opus", "AAC", "OGG"} {
		if !IsLossy(f) || IsLossless(f) {
			t.Errorf("%q should be lossy", f)
		}
	}
	if IsAudioFormat("MKV") {
		t.Error("MKV is not an audio format")
	}
	if got := FormatOf("/music/a/b/01 - Airbag.FLAC"); got != "FLAC" {
		t.Errorf("FormatOf = %q, want FLAC (extension case shouldn't matter)", got)
	}
	if FormatOf("/music/cover.jpg") != "" || IsAudioFile("/music/cover.jpg") {
		t.Error("a jpg is not an audio file")
	}
}

// The point of the tier: a lossless file is not "a bigger MP3". Lossless always beats
// lossy, lossless never churns against lossless, and within lossy a materially higher
// bitrate is needed — so a 320 doesn't endlessly replace a 319.
func TestBetterFormat(t *testing.T) {
	if !BetterFormat("FLAC", 0, "MP3", 320) {
		t.Error("lossless must beat lossy regardless of bitrate")
	}
	if BetterFormat("MP3", 320, "FLAC", 0) {
		t.Error("lossy must never replace lossless")
	}
	if BetterFormat("FLAC", 0, "ALAC", 0) {
		t.Error("lossless vs lossless is not an upgrade — don't churn")
	}
	if !BetterFormat("MP3", 320, "MP3", 128) {
		t.Error("320 over 128 is a real upgrade")
	}
	if BetterFormat("MP3", 320, "MP3", 319) {
		t.Error("a 1 kbps gain must not trigger a replacement")
	}
	if BetterFormat("MP3", 0, "MP3", 0) {
		t.Error("unknown bitrates give no basis to upgrade")
	}
	if BetterFormat("", 0, "", 0) {
		t.Error("unknown vs unknown is not an upgrade")
	}
}
