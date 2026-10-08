package automation

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/tristenlammi/arrmada/internal/library"
	"github.com/tristenlammi/arrmada/internal/metadata"
	"github.com/tristenlammi/arrmada/internal/series"
	"github.com/tristenlammi/arrmada/internal/store"
)

// importMeta answers GetSeries with a fixed listing, flagged as a fallback or not.
type importMeta struct{ d metadata.SeriesDetails }

func (m *importMeta) Available() bool { return true }
func (m *importMeta) SearchSeries(context.Context, string) ([]metadata.SeriesResult, error) {
	return nil, nil
}
func (m *importMeta) GetSeries(context.Context, int) (*metadata.SeriesDetails, error) {
	cp := m.d
	return &cp, nil
}

// A new episode's file arrives during a TVmaze outage. The import refreshes the show to
// learn about the episode, but the refresh only got a stand-in listing that doesn't have it
// yet. That is "couldn't check", not "doesn't exist": the file must be retried next sweep
// (failed), not sent to Review (unresolved).
func TestImportRetriesWhenNumberingSourceFails(t *testing.T) {
	for _, tc := range []struct {
		name               string
		fallback           bool
		wantFailed, wantUn int
	}{
		{"numbering source down", true, 1, 0},
		{"source fine, episode really unknown", false, 0, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st, err := store.Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = st.Close() })
			log := slog.New(slog.NewTextHandler(io.Discard, nil))
			ctx := context.Background()
			meta := &importMeta{d: metadata.SeriesDetails{
				SeriesResult:    metadata.SeriesResult{TMDBID: 3, Title: "Show"},
				NumberingSource: "tvmaze",
				Seasons: []metadata.SeasonDetails{{SeasonNumber: 1, Episodes: []metadata.EpisodeDetails{
					{EpisodeNumber: 1}, {EpisodeNumber: 2}, {EpisodeNumber: 3}, {EpisodeNumber: 4},
				}}},
			}}
			root := t.TempDir()
			svc := series.NewService(st.DB(), meta, root, log)
			s, err := svc.Add(ctx, 3, "", true)
			if err != nil {
				t.Fatal(err)
			}
			meta.d.NumberingSource, meta.d.NumberingFallback = "tmdb", tc.fallback

			// A sparse 60 MB file: big enough not to be taken for a sample, no real bytes.
			dl := filepath.Join(t.TempDir(), "Show.S01E05.1080p.WEB-DL-GRP")
			if err := os.MkdirAll(dl, 0o755); err != nil {
				t.Fatal(err)
			}
			f, err := os.Create(filepath.Join(dl, "Show.S01E05.1080p.WEB-DL-GRP.mkv"))
			if err != nil {
				t.Fatal(err)
			}
			if err := f.Truncate(60 << 20); err != nil {
				t.Fatal(err)
			}
			_ = f.Close()

			c := &Coordinator{db: st.DB(), log: log, series: svc, imp: library.NewImporter(root, log)}
			placed, _, unresolved, failed := c.importSeriesInto(ctx, s, dl, forceRule{})
			if len(placed) != 0 || failed != tc.wantFailed || unresolved != tc.wantUn {
				t.Errorf("placed=%d failed=%d unresolved=%d, want 0/%d/%d", len(placed), failed, unresolved, tc.wantFailed, tc.wantUn)
			}
		})
	}
}
