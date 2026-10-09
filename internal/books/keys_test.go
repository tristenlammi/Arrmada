package books

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/tristenlammi/arrmada/internal/metadata"
	"github.com/tristenlammi/arrmada/internal/store"
)

func keysOf(t *testing.T, r *Repo, ctx context.Context, id int64) map[string]string {
	t.Helper()
	ks, err := r.KeysFor(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, k := range ks {
		out[k.Key] = k.Source
	}
	return out
}

// A new book's key is recorded with it; AddKey adds more, BookIDForKey and AllKeys find
// the book by any of them, a key another book holds is refused, and deleting the book
// drops its keys.
func TestBookKeysRepo(t *testing.T) {
	repo, ctx := historyRepo(t)
	dune, err := repo.Create(ctx, Book{OLKey: "hc:9", Title: "Dune", Author: "Frank Herbert"})
	if err != nil {
		t.Fatal(err)
	}
	other, err := repo.Create(ctx, Book{OLKey: "OL2W", Title: "Neuromancer", Author: "William Gibson"})
	if err != nil {
		t.Fatal(err)
	}
	if got := keysOf(t, repo, ctx, dune.ID); got["hc:9"] != KeySourceHardcover || len(got) != 1 {
		t.Errorf("keys after create = %v, want just hc:9 from hardcover", got)
	}
	if err := repo.AddKey(ctx, "OL1W", dune.ID, ""); err != nil {
		t.Fatal(err)
	}
	if err := repo.AddKey(ctx, "OL1W", dune.ID, ""); err != nil {
		t.Errorf("adding a key the book already has: %v", err)
	}
	if err := repo.AddKey(ctx, "OL1W", other.ID, ""); !errors.Is(err, ErrExists) {
		t.Errorf("a key another book holds: err = %v, want ErrExists", err)
	}
	if err := repo.AddKey(ctx, "OL2W", dune.ID, ""); !errors.Is(err, ErrExists) {
		t.Errorf("another book's current key: err = %v, want ErrExists", err)
	}
	if id, ok := repo.BookIDForKey(ctx, "OL1W"); !ok || id != dune.ID {
		t.Errorf("BookIDForKey(OL1W) = %d %v, want %d", id, ok, dune.ID)
	}
	if _, ok := repo.BookIDForKey(ctx, "OL404W"); ok {
		t.Error("an unknown key resolved")
	}
	all, err := repo.AllKeys(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if all["OL1W"] != dune.ID || all["hc:9"] != dune.ID || all["OL2W"] != other.ID {
		t.Errorf("AllKeys = %v", all)
	}
	// A former key is that book: a second row can't be created on it.
	if _, err := repo.Create(ctx, Book{OLKey: "OL1W", Title: "Dune (again)"}); !errors.Is(err, ErrExists) {
		t.Errorf("create on another book's former key: err = %v, want ErrExists", err)
	}

	if err := repo.Delete(ctx, dune.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok := repo.BookIDForKey(ctx, "OL1W"); ok {
		t.Error("a deleted book's keys should go with it")
	}
	if ks, _ := repo.KeysFor(ctx, dune.ID); len(ks) != 0 {
		t.Errorf("keys left after delete: %v", ks)
	}
}

// A book written before book_keys existed (or by a path that skipped it) is still found
// by its current key.
func TestBookKeysFallBackToCurrentKey(t *testing.T) {
	repo, ctx := historyRepo(t)
	res, err := repo.db.ExecContext(ctx, `INSERT INTO books (ol_key, title) VALUES ('OL7W', 'Old Row')`)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	if got, ok := repo.BookIDForKey(ctx, "OL7W"); !ok || got != id {
		t.Errorf("BookIDForKey = %d %v, want %d", got, ok, id)
	}
	if ks := keysOf(t, repo, ctx, id); ks["OL7W"] != KeySourceOpenLibrary {
		t.Errorf("KeysFor = %v, want the current key", ks)
	}
}

// fixedCatalogue answers GetBook by key from a map; a key it doesn't know comes back as
// itself with no metadata, the way a thin catalogue answer looks.
type fixedCatalogue struct {
	metadata.BookProvider
	byKey map[string]metadata.BookResult
}

func (c fixedCatalogue) GetBook(_ context.Context, key string) (*metadata.BookDetails, error) {
	return &metadata.BookDetails{BookResult: c.byKey[key]}, nil
}

func keysService(t *testing.T, cat fixedCatalogue) (*Service, context.Context) {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return NewService(st.DB(), cat, slog.New(slog.NewTextHandler(io.Discard, nil))), context.Background()
}

// Change match onto the same book in another catalogue keeps the old key; the book is
// still found by it, and re-matching another book onto it is refused.
func TestRematchKeepsOldKeyAsAlias(t *testing.T) {
	s, ctx := keysService(t, fixedCatalogue{byKey: map[string]metadata.BookResult{
		"hc:42": {Key: "hc:42", Title: "Dune", Author: "Frank Herbert"},
		"hc:43": {Key: "hc:43", Title: "Dune", Author: "Frank Herbert"},
	}})
	b, err := s.repo.Create(ctx, Book{OLKey: "OL1W", Title: "Dune", Author: "Frank Herbert"})
	if err != nil {
		t.Fatal(err)
	}
	other, err := s.repo.Create(ctx, Book{OLKey: "OL9W", Title: "Dune", Author: "Frank Herbert"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Rematch(ctx, b.ID, "hc:42", metadata.BookResult{}); err != nil {
		t.Fatal(err)
	}
	if ks := keysOf(t, s.repo, ctx, b.ID); ks["OL1W"] == "" || ks["hc:42"] == "" {
		t.Errorf("keys after Change match = %v, want OL1W and hc:42", ks)
	}
	if found, ok := s.findByKey(ctx, "OL1W"); !ok || found.ID != b.ID {
		t.Errorf("old key finds %+v %v, want book %d", found, ok, b.ID)
	}
	// OL1W is b's now: moving the other row onto it would make two rows of one book.
	if _, err := s.Rematch(ctx, other.ID, "OL1W", metadata.BookResult{Title: "Dune", Author: "Frank Herbert"}); !errors.Is(err, ErrExists) {
		t.Errorf("re-match onto another book's alias: err = %v, want ErrExists", err)
	}
	// Moving b back onto its own former key is fine.
	if _, err := s.Rematch(ctx, b.ID, "OL1W", metadata.BookResult{Title: "Dune", Author: "Frank Herbert"}); err != nil {
		t.Errorf("re-match onto the book's own former key: %v", err)
	}
}

// Change match that corrects a wrong identification drops the wrong key: it named a
// different book, which must stay addable.
func TestRematchCorrectionDropsWrongKey(t *testing.T) {
	s, ctx := keysService(t, fixedCatalogue{byKey: map[string]metadata.BookResult{
		"hc:2": {Key: "hc:2", Title: "Golden Son", Author: "Pierce Brown"},
	}})
	b, err := s.repo.Create(ctx, Book{OLKey: "hc:1", Title: "Red Rising", Author: "Pierce Brown"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Rematch(ctx, b.ID, "hc:2", metadata.BookResult{}); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.findByKey(ctx, "hc:1"); ok {
		t.Error("the wrong key still points at the corrected book")
	}
}

// Add stores both keys when the catalogue answers with a canonical key other than the
// one asked for, and a later Add under either finds the book.
func TestAddCanonicalSwapStoresBothKeys(t *testing.T) {
	s, ctx := keysService(t, fixedCatalogue{byKey: map[string]metadata.BookResult{
		"hc:7": {Key: "hc:8", Title: "Dune", Author: "Frank Herbert"},
	}})
	b, err := s.Add(ctx, "hc:7", "", true, metadata.BookResult{})
	if err != nil {
		t.Fatal(err)
	}
	if b.OLKey != "hc:8" {
		t.Errorf("stored key %q, want the canonical hc:8", b.OLKey)
	}
	if ks := keysOf(t, s.repo, ctx, b.ID); ks["hc:7"] == "" || ks["hc:8"] == "" {
		t.Errorf("keys = %v, want hc:7 and hc:8", ks)
	}
	again, err := s.Add(ctx, "hc:7", "", true, metadata.BookResult{})
	if !errors.Is(err, ErrExists) || again.ID != b.ID {
		t.Errorf("second Add = %d %v, want book %d with ErrExists", again.ID, err, b.ID)
	}
}

// Adding a book the library has under another catalogue's key aliases the new key to it.
func TestAddDuplicateAliasesIncomingKey(t *testing.T) {
	s, ctx := keysService(t, fixedCatalogue{byKey: map[string]metadata.BookResult{
		"OL1W": {Key: "OL1W", Title: "Dune", Author: "Frank Herbert"},
	}})
	held, err := s.repo.Create(ctx, Book{OLKey: "hc:9", Title: "Dune", Author: "Herbert, Frank"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Add(ctx, "OL1W", "", true, metadata.BookResult{})
	if !errors.Is(err, ErrExists) || got.ID != held.ID {
		t.Fatalf("Add = %d %v, want the existing row with ErrExists", got.ID, err)
	}
	if id, ok := s.BookIDForKey(ctx, "OL1W"); !ok || id != held.ID {
		t.Errorf("OL1W resolves to %d %v, want %d", id, ok, held.ID)
	}
}
