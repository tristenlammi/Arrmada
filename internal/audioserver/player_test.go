package audioserver

import (
	"context"
	"encoding/json"
	"testing"
)

func mustJSON(t *testing.T, b []byte, dst any) {
	t.Helper()
	if err := json.Unmarshal(b, dst); err != nil {
		t.Fatalf("not JSON: %s", b)
	}
}

// The home screen an app shows and the Listen tab's shelves come from one selection;
// splitting it out mustn't change what the apps get.
func TestPersonalizedUnchangedAfterShelvesRefactor(t *testing.T) {
	h := newHarness(t)
	h.signIn()
	ctx := context.Background()
	key := itemKeyFor(h.book.ID, 0)
	shelves := func() []map[string]any {
		code, out := h.do("GET", "/api/libraries/"+libraryID+"/personalized", nil, nil)
		if code != 200 {
			t.Fatalf("personalized: HTTP %d %s", code, out)
		}
		var list []map[string]any
		mustJSON(t, out, &list)
		return list
	}
	check := func(want ...string) {
		t.Helper()
		got := shelves()
		if len(got) != len(want)/3 {
			t.Fatalf("shelves = %v, want %v", got, want)
		}
		for i, s := range got {
			id, label, labelKey := want[i*3], want[i*3+1], want[i*3+2]
			if s["id"] != id || s["label"] != label || s["labelStringKey"] != labelKey || s["type"] != "book" {
				t.Errorf("shelf %d = %v, want %s/%s/%s", i, s, id, label, labelKey)
			}
			ents := list1(t, s["entities"])
			if len(ents) != 1 || obj1(t, ents[0])["id"] != key || s["total"] != 1.0 {
				t.Errorf("shelf %s entities = %v", id, ents)
			}
		}
		mine, err := h.srv.Shelves(ctx, h.readerID())
		if err != nil {
			t.Fatal(err)
		}
		if len(mine) != len(got) {
			t.Fatalf("Shelves = %+v, want the same shelves as the app's", mine)
		}
		for i, s := range mine {
			if s.ID != got[i]["id"] || s.Label != got[i]["label"] || len(s.Items) != 1 || s.Items[0].Key != key {
				t.Errorf("Shelves[%d] = %+v, want %v", i, s, got[i])
			}
		}
	}

	check("recently-added", "Recently Added", "LabelRecentlyAdded")
	h.json("PATCH", "/api/me/progress/"+key, map[string]any{"currentTime": 3000, "duration": 36000})
	check("continue-listening", "Continue Listening", "LabelContinueListening",
		"recently-added", "Recently Added", "LabelRecentlyAdded")
	h.json("PATCH", "/api/me/progress/"+key, map[string]any{"isFinished": true})
	check("recently-added", "Recently Added", "LabelRecentlyAdded",
		"listen-again", "Listen Again", "LabelListenAgain")
	mine, _ := h.srv.Shelves(ctx, h.readerID())
	if p := mine[1].Items[0].Progress; p == nil || !p.Finished {
		t.Fatalf("Listen Again card's progress = %+v, want finished", p)
	}
}
