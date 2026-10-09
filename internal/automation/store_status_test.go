package automation

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The grabs table drives what is re-grabbed, stall-checked and deleted at seed time, so
// its status words live in one place (grabstatus.go) and every query goes through the
// sets there. A hand-typed IN-list that misses a status is exactly how a held release got
// re-grabbed, or a dismissed one deleted. This fails on a raw grab-status literal in SQL,
// or on setGrabStatus called with a literal, anywhere in automation or requests outside
// grabstatus.go.
var (
	rawGrabStatusRe = regexp.MustCompile(`status\s*(=|!=|<>)\s*'(grabbed|held|imported|dismissed|failed|removed|seeded|cancelled|orphaned)'|status\s+(NOT\s+)?IN\s*\(`)
	rawSetStatusRe  = regexp.MustCompile(`setGrabStatus(ByHash)?\([^)]*"[a-z]+"\s*\)`)
)

func TestNoRawGrabStatusLiterals(t *testing.T) {
	for _, dir := range []string{".", filepath.Join("..", "requests")} {
		files, err := filepath.Glob(filepath.Join(dir, "*.go"))
		if err != nil {
			t.Fatal(err)
		}
		if len(files) == 0 {
			t.Fatalf("no Go files found in %s", dir)
		}
		for _, f := range files {
			if strings.HasSuffix(f, "_test.go") || filepath.Base(f) == "grabstatus.go" {
				continue
			}
			raw, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			for i, line := range strings.Split(string(raw), "\n") {
				if strings.HasPrefix(strings.TrimSpace(line), "//") {
					continue // prose may name a status
				}
				if rawGrabStatusRe.MatchString(line) || rawSetStatusRe.MatchString(line) {
					t.Errorf("%s:%d: raw grab status — use the sets and constants in grabstatus.go:\n\t%s",
						f, i+1, strings.TrimSpace(line))
				}
			}
		}
	}
}
