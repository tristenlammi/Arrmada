package convert

import (
	"context"
	"testing"

	"github.com/tristenlammi/arrmada/internal/store"
)

// TestLogStore verifies the console persists, reads back oldest-first, and trims to maxLogLines.
func TestLogStore(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	l := &logStore{db: st.DB()}
	ctx := context.Background()

	// Empty store → nothing.
	if got := l.recent(ctx, maxLogLines); len(got) != 0 {
		t.Fatalf("expected empty, got %d", len(got))
	}

	// Write more than the cap; only the most recent maxLogLines should survive, oldest-first.
	total := maxLogLines + 250
	for i := 0; i < total; i++ {
		l.append(ctx, LogLine{At: int64(i), Level: "info", Msg: "line"})
	}
	got := l.recent(ctx, maxLogLines)
	if len(got) != maxLogLines {
		t.Fatalf("expected %d rows after trim, got %d", maxLogLines, len(got))
	}
	// Oldest surviving line is the (total-maxLogLines)th appended; newest is the last.
	if got[0].At != int64(total-maxLogLines) {
		t.Errorf("oldest At = %d, want %d", got[0].At, total-maxLogLines)
	}
	if got[len(got)-1].At != int64(total-1) {
		t.Errorf("newest At = %d, want %d", got[len(got)-1].At, total-1)
	}
}
