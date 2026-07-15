package indexer

import (
	"context"
	"errors"
	"testing"

	"github.com/tristenlammi/arrmada/internal/store"
)

func newRepo(t *testing.T) *Repo {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return NewRepo(st.DB())
}

func TestIndexerCRUD(t *testing.T) {
	ctx := context.Background()
	repo := newRepo(t)

	created, err := repo.Create(ctx, Indexer{
		Name: "My Tracker", Kind: KindTorznab,
		URL: "https://tracker.example/api", APIKey: "secret",
		Categories: []int{2000, 5000}, Priority: 10, Enabled: true,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if created.ID == 0 {
		t.Fatal("expected an assigned id")
	}

	got, err := repo.Get(ctx, created.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Name != "My Tracker" || got.APIKey != "secret" || got.Priority != 10 {
		t.Errorf("unexpected indexer: %+v", got)
	}
	if len(got.Categories) != 2 || got.Categories[0] != 2000 || got.Categories[1] != 5000 {
		t.Errorf("categories round-trip failed: %v", got.Categories)
	}
	if !got.Enabled {
		t.Error("expected enabled")
	}

	// Disabled indexers are excluded from ListEnabled.
	_, _ = repo.Create(ctx, Indexer{Name: "Off", Kind: KindNewznab, URL: "https://x/api", Enabled: false})
	all, _ := repo.List(ctx)
	if len(all) != 2 {
		t.Errorf("List = %d, want 2", len(all))
	}
	enabled, _ := repo.ListEnabled(ctx)
	if len(enabled) != 1 {
		t.Errorf("ListEnabled = %d, want 1", len(enabled))
	}

	if err := repo.Delete(ctx, created.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := repo.Get(ctx, created.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound after delete, got %v", err)
	}
	if err := repo.Delete(ctx, created.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound deleting twice, got %v", err)
	}
}

func TestDefaultPriority(t *testing.T) {
	repo := newRepo(t)
	created, err := repo.Create(context.Background(), Indexer{
		Name: "NoPrio", Kind: KindTorznab, URL: "https://x/api", Enabled: true,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if created.Priority != 25 {
		t.Errorf("default priority = %d, want 25", created.Priority)
	}
}
