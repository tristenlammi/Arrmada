package audioserver

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/tristenlammi/arrmada/internal/books"
	"github.com/tristenlammi/arrmada/internal/metadata"
)

// Apps list the library with different sorts at the same time. The catalogue is cached
// and shared, so a list request must sort its own copy: sorting the cache in place raced
// between requests (go test -race) and reordered it for everyone.
func TestConcurrentListsDontSortTheSharedCatalogue(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	// A few more books, so a sort has something to move.
	for i, title := range []string{"Zebra Days", "Apple Nights", "Middle March"} {
		added, _ := h.srv.books.AddWorks(ctx, []metadata.BookResult{{Key: fmt.Sprintf("hc:%d", 10+i), Title: title,
			Author: fmt.Sprintf("Author %c", 'C'-rune(i))}}, "", true)
		if len(added) != 1 {
			t.Fatal("book not added")
		}
		if err := h.srv.books.MarkImported(ctx, added[0].ID, books.KindAudiobook, h.book.Audiobook.Path, "MP3", 36000, 3); err != nil {
			t.Fatal(err)
		}
	}
	h.signIn()
	before, err := h.srv.items(ctx)
	if err != nil || len(before) != 4 {
		t.Fatalf("catalogue: %d %v", len(before), err)
	}
	order := make([]string, len(before))
	for i, it := range before {
		order[i] = it.Key
	}

	sorts := []string{"media.metadata.title&desc=0", "media.metadata.title&desc=1", "media.metadata.authorName&desc=0", "addedAt&desc=1"}
	var wg sync.WaitGroup
	for i := 0; i < 40; i++ {
		wg.Add(1)
		path := "/api/libraries/" + libraryID + "/items?limit=10&page=0&sort=" + sorts[i%len(sorts)]
		go func() {
			defer wg.Done()
			if code, out := h.do("GET", path, nil, nil); code != 200 {
				t.Errorf("%s: HTTP %d %s", path, code, out)
			}
		}()
	}
	wg.Wait()

	after, _ := h.srv.items(ctx)
	for i, it := range after {
		if it.Key != order[i] {
			t.Fatalf("the cached catalogue was reordered: %v, was %v", after, order)
		}
	}
	// And each reply is sorted as asked.
	res := list1(t, h.json("GET", "/api/libraries/"+libraryID+"/items?sort=media.metadata.title&desc=1", nil)["results"])
	first := obj1(t, obj1(t, obj1(t, res[0])["media"])["metadata"])["title"]
	if first != "Zebra Days" {
		t.Fatalf("title desc starts with %v", first)
	}
}
