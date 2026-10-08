package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The first start of this version scrubs book details out of older log lines, once.
func TestScrubLegacyLogsRunsOnce(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "logs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(dir, "arrmada.log.jsonl")
	old := `{"time_ms":1,"level":"INFO","msg":"audiobook server: request","attrs":"method=GET path=/api/items/b45 query=q=dungeon status=200 bytes=1 token=x client=Lissen"}` + "\n"
	if err := os.WriteFile(logPath+".1", []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := scrubLegacyLogs(logPath); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(logPath + ".1")
	if strings.Contains(string(got), "b45") || strings.Contains(string(got), "dungeon") {
		t.Errorf("rotated log still names the book or the search: %s", got)
	}
	if _, err := os.Stat(filepath.Join(dir, scrubLegacyMarker)); err != nil {
		t.Fatalf("no marker written: %v", err)
	}

	// With the marker in place it doesn't run again.
	if err := os.WriteFile(logPath, []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := scrubLegacyLogs(logPath); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(logPath); string(got) != old {
		t.Errorf("the scrub ran a second time")
	}
}
