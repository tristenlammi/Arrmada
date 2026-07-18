package automation

import (
	"os"
	"path/filepath"
	"testing"
)

// TestHasExecutable checks the fake-release guard: a ".scr" (Windows screensaver =
// executable) masquerading as an episode is detected, while a real .mkv is not.
func TestHasExecutable(t *testing.T) {
	dir := t.TempDir()
	scr := filepath.Join(dir, "Silo S03E04 MULTI 1080p WEB H264-HiggsBoson.scr")
	if err := os.WriteFile(scr, []byte("MZ"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !hasExecutable(dir) {
		t.Error("expected .scr to be flagged as executable")
	}
	if !hasExecutable(scr) { // also works when the content path is the file itself
		t.Error("expected .scr file path to be flagged")
	}

	clean := t.TempDir()
	if err := os.WriteFile(filepath.Join(clean, "Show.S01E01.mkv"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if hasExecutable(clean) {
		t.Error("a normal .mkv download must not be flagged")
	}
}
