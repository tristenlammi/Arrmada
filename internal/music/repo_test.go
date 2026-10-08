package music

import (
	"context"
	"testing"

	"github.com/tristenlammi/arrmada/internal/store"
)

func testRepo(t *testing.T) (*Repo, context.Context) {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return NewRepo(st.DB()), context.Background()
}

// HasArtists is what keeps Music on for an install already using it.
func TestHasArtists(t *testing.T) {
	r, ctx := testRepo(t)
	if has, err := r.HasArtists(ctx); err != nil || has {
		t.Fatalf("empty library: has=%v err=%v, want false", has, err)
	}
	if _, err := r.CreateArtist(ctx, Artist{MBID: "a-1", Name: "Radiohead"}); err != nil {
		t.Fatal(err)
	}
	if has, err := r.HasArtists(ctx); err != nil || !has {
		t.Fatalf("after adding an artist: has=%v err=%v, want true", has, err)
	}
}
