package automation

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/tristenlammi/arrmada/internal/store"
)

func manualTestCoordinator(t *testing.T) (*Coordinator, context.Context) {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return &Coordinator{db: st.DB(), log: slog.New(slog.NewTextHandler(io.Discard, nil))}, context.Background()
}

// The import gate compares a finished download against what's on disk and skips anything
// that scores worse. Right for the automation; wrong for a release the user picked out of
// the interactive search — they saw the options and chose that one, so a lower-scoring
// pick is the answer, not a mistake to be corrected.
func TestGrabManualFlag(t *testing.T) {
	c, ctx := manualTestCoordinator(t)
	const auto, manual = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

	for _, h := range []string{auto, manual} {
		if _, err := c.db.ExecContext(ctx,
			`INSERT INTO grabs (movie_id, title, indexer, info_hash, media_type) VALUES (1, 'X', 'i', ?, 'series')`, h); err != nil {
			t.Fatal(err)
		}
	}

	// Nothing is manual until it's said to be — the automation's grabs stay gated.
	if c.grabWasManual(ctx, auto) {
		t.Error("an ordinary grab must not read as manual")
	}
	c.markGrabManual(ctx, manual)
	if !c.grabWasManual(ctx, manual) {
		t.Error("a grab marked manual must read back as manual")
	}
	if c.grabWasManual(ctx, auto) {
		t.Error("marking one grab must not flag the others")
	}

	// Matching is on hash alone. An unknown hash is not manual — erring the other way
	// would let any untracked download overwrite a better file.
	if c.grabWasManual(ctx, "cccccccccccccccccccccccccccccccccccccccc") {
		t.Error("an unrecorded hash must not read as manual")
	}
	if c.grabWasManual(ctx, "") {
		t.Error("an empty hash must not read as manual")
	}
	// Case is not identity: qBittorrent and the indexer disagree on it constantly.
	if !c.grabWasManual(ctx, "BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB") {
		t.Error("hash matching must be case-insensitive")
	}
}
