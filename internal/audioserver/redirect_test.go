package audioserver

import (
	"context"
	"testing"
	"time"

	"github.com/tristenlammi/arrmada/internal/store"
)

// After a merge, an app still holding the dropped book's item id keeps working: play,
// sync, progress updates, offline uploads and bookmarks for the old id land on the kept
// book's item, in either id shape.
func TestMergedItemIDsRedirect(t *testing.T) {
	h := newHarness(t)
	h.signIn()
	ctx := context.Background()
	uid := h.readerID()
	key := itemKeyFor(h.book.ID, 0)
	const old = "b999" // a book merged into h.book (its row is gone)
	release := h.srv.listen.HoldReports()
	err := store.WithTx(ctx, h.srv.db, func(tx *store.Transaction) error {
		_, err := h.srv.listen.RekeyItemsTx(ctx, tx, map[string]string{old: key})
		return err
	})
	release()
	if err != nil {
		t.Fatal(err)
	}

	// A progress PATCH for the old id.
	p := h.json("PATCH", "/api/me/progress/"+old, map[string]any{"currentTime": 600, "duration": 36000})
	if p["libraryItemId"] != key || p["currentTime"].(float64) != 600 {
		t.Fatalf("PATCH old id answered %v", p)
	}
	if got, ok, _ := h.srv.listen.Progress(ctx, uid, key); !ok || got.Position != 600 {
		t.Fatalf("place on the kept item = %+v %v", got, ok)
	}
	if _, ok, _ := h.srv.listen.Progress(ctx, uid, old); ok {
		t.Fatal("a place was written under the old id")
	}
	// Read back by the old id, in its UUID form too.
	if got := h.json("GET", "/api/me/progress/"+old, nil)["currentTime"].(float64); got != 600 {
		t.Fatalf("GET old id = %v", got)
	}
	if got := h.json("GET", "/api/me/progress/"+packUUID(markItem, 999, 0), nil)["currentTime"].(float64); got != 600 {
		t.Fatalf("GET old id as a UUID = %v", got)
	}

	// A play session opened on the old id syncs onto the kept item.
	sid := h.play(old)
	if sess, err := h.srv.listen.GetSession(ctx, uid, sid); err != nil || sess.ItemKey != key {
		t.Fatalf("session = %+v %v", sess, err)
	}
	if code, out := h.do("POST", "/api/session/"+sid+"/sync", map[string]any{"currentTime": 640, "timeListened": 10}, nil); code != 200 {
		t.Fatalf("sync: HTTP %d %s", code, out)
	}
	if got, _, _ := h.srv.listen.Progress(ctx, uid, key); got.Position != 640 {
		t.Fatalf("after sync: %v", got.Position)
	}

	// An offline upload made before the merge, by the old id (UUID form).
	now := time.Now()
	res := list1(t, h.json("POST", "/api/session/local-all", map[string]any{"sessions": []any{map[string]any{
		"id": "offline-1", "libraryItemId": packUUID(markItem, 999, 0), "currentTime": 900, "duration": 36000,
		"timeListening": 260, "startTime": 640, "startedAt": now.Add(-4 * time.Minute).UnixMilli(), "updatedAt": now.Add(time.Minute).UnixMilli(),
	}}})["results"])
	if len(res) != 1 || obj1(t, res[0])["success"] != true {
		t.Fatalf("offline upload by the old id: %v", res)
	}
	if got, _, _ := h.srv.listen.Progress(ctx, uid, key); got.Position != 900 {
		t.Fatalf("after offline upload: %v", got.Position)
	}

	// Batch updates and bookmarks.
	if code, out := h.do("PATCH", "/api/me/progress/batch/update", []any{map[string]any{"libraryItemId": old, "isFinished": true}}, nil); code != 200 {
		t.Fatalf("batch: HTTP %d %s", code, out)
	}
	if got, _, _ := h.srv.listen.Progress(ctx, uid, key); !got.Finished {
		t.Fatal("batch update by the old id didn't land")
	}
	h.json("POST", "/api/me/item/"+old+"/bookmark", map[string]any{"time": 42, "title": "mark"})
	if bms, _ := h.srv.listen.Bookmarks(ctx, uid, key); len(bms) != 1 {
		t.Fatalf("bookmarks on the kept item = %+v", bms)
	}
	// The item itself answers under the kept key.
	if it := h.json("GET", "/api/items/"+old, nil); it["id"] != key {
		t.Fatalf("GET item by old id answered id %v", it["id"])
	}
	// An id that was never merged is still not found.
	if code, _ := h.do("GET", "/api/items/b998", nil, nil); code != 404 {
		t.Fatalf("unknown item: HTTP %d", code)
	}
}
