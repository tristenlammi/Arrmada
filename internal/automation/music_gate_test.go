package automation

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/tristenlammi/arrmada/internal/eventbus"
	"github.com/tristenlammi/arrmada/internal/library"
	"github.com/tristenlammi/arrmada/internal/metadata"
	"github.com/tristenlammi/arrmada/internal/music"
	"github.com/tristenlammi/arrmada/internal/store"
)

// gateTestCoord is a coordinator with a music library but no indexers or download
// client: anything that gets past the module gate and reaches for them panics.
func gateTestCoord(t *testing.T, on bool) (*Coordinator, *music.Service) {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	svc := music.NewService(st.DB(), metadata.NewMusicBrainz(), slog.Default())
	c := &Coordinator{music: svc, db: st.DB(), log: slog.Default(), bus: eventbus.New(slog.Default()),
		imp: library.NewImporter(t.TempDir(), slog.Default())}
	c.SetModuleGate(func(_ context.Context, module string) bool {
		if module != "music" {
			t.Errorf("gate asked about %q, want music", module)
		}
		return on
	})
	return c, svc
}

// Switched off, the sweep and the importer return before touching the indexers or the
// download client (both nil here, so reaching them would panic).
func TestMusicJobsStopWhenTheModuleIsOff(t *testing.T) {
	c, _ := gateTestCoord(t, false)
	ctx := context.Background()
	c.SearchMusicMissing(ctx)
	c.ImportMusicDownloads(ctx)
}

// The manual actions refuse with ErrModuleOff, which the API turns into a 404.
func TestMusicActionsRefuseWhenTheModuleIsOff(t *testing.T) {
	c, _ := gateTestCoord(t, false)
	ctx := context.Background()
	if err := c.GrabDiscography(ctx, 1); !errors.Is(err, ErrModuleOff) {
		t.Errorf("GrabDiscography = %v, want ErrModuleOff", err)
	}
	if _, err := c.ScanMusicLibrary(ctx); !errors.Is(err, ErrModuleOff) {
		t.Errorf("ScanMusicLibrary = %v, want ErrModuleOff", err)
	}
}

// No gate wired (tests, older call sites) means on.
func TestNilModuleGateIsOn(t *testing.T) {
	c := &Coordinator{}
	if !c.moduleOn(context.Background(), "music") {
		t.Error("a nil gate must read as on")
	}
	on, _ := gateTestCoord(t, true)
	if _, err := on.ScanMusicLibrary(context.Background()); err != nil {
		t.Errorf("scan of an empty library with Music on = %v, want nil", err)
	}
}
