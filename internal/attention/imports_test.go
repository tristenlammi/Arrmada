package attention

import (
	"context"
	"testing"
	"time"

	"github.com/tristenlammi/arrmada/internal/automation"
	"github.com/tristenlammi/arrmada/internal/download"
	"github.com/tristenlammi/arrmada/internal/library"
)

type fakeReviews []automation.Review

func (r fakeReviews) ListReviews(context.Context) ([]automation.Review, error) { return r, nil }

// OBS-09: an import that keeps failing is a warning from its second try and an error
// after five over a quarter of an hour. One already held in Review shows once, as the
// review; one whose torrent a complete read no longer lists is gone.
func TestImportsKeepFailing(t *testing.T) {
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	fails := []library.FailureInfo{
		{Hash: "ONCE", Name: "Once.2020", Attempts: 1, Since: now, LastErr: "permission denied"},
		{Hash: "TWICE", Name: "Twice.2020", Attempts: 2, Since: now.Add(-3 * time.Minute), LastErr: "permission denied"},
		{Hash: "STUCK", Name: "Stuck.2020", Attempts: 6, Since: now.Add(-20 * time.Minute), LastErr: "no space left on device"},
		{Hash: "FAST", Name: "Fast.S01", Attempts: 6, Since: now.Add(-3 * time.Minute), LastErr: "2 files couldn't be placed"},
		{Hash: "HELD", Name: "Held.2020", Attempts: 5, Since: now.Add(-time.Hour), LastErr: "permission denied"},
		{Hash: "GONE", Name: "Gone.2020", Attempts: 3, Since: now.Add(-time.Hour), LastErr: "permission denied"},
	}
	queue := []download.Item{{Hash: "once"}, {Hash: "twice"}, {Hash: "stuck"}, {Hash: "fast"}, {Hash: "held"}}
	f := &Frame{Now: now, OK: true, Whole: true, Queue: queue}
	p := Imports(func() []library.FailureInfo { return fails }, fakeReviews{{ID: 9, Hash: "held"}})
	items, err := p.Collect(context.Background(), f)
	if err != nil {
		t.Fatal(err)
	}
	got := byKey(items)
	if len(got) != 3 {
		t.Fatalf("items = %+v, want twice, stuck and fast", items)
	}
	if it := got["import:twice"]; it.Level != LevelWarning || it.Title != "Importing Twice.2020 keeps failing (2 tries)" || it.Detail != "permission denied" || it.Link != "/downloads?show=problems" {
		t.Errorf("twice = %+v", it)
	}
	if it := got["import:stuck"]; it.Level != LevelError {
		t.Errorf("stuck = %+v, want an error", it)
	}
	if it := got["import:fast"]; it.Level != LevelWarning {
		t.Errorf("six fast series sweeps = %+v, want still a warning", it)
	}
	// A partial read can't tell the torrent is gone, so it stays listed.
	f.Whole = false
	items, _ = p.Collect(context.Background(), f)
	if _, ok := byKey(items)["import:gone"]; !ok {
		t.Errorf("partial read dropped a failing import: %+v", items)
	}
}

// A finished TV download in the wrong category is a warning that says which category
// it's in and where it should be; one gone from the client isn't listed.
func TestWrongCategoryItems(t *testing.T) {
	misfiled := []download.Item{
		{Hash: "AAA", Name: "Show.S01E01.1080p", Category: "other"},
		{Hash: "BBB", Name: "Show.S01E02.1080p", Category: ""},
		{Hash: "CCC", Name: "Gone.S01E01", Category: "other"},
	}
	f := &Frame{OK: true, Whole: true, Queue: []download.Item{{Hash: "aaa"}, {Hash: "bbb"}}}
	items, err := WrongCategory(func() []download.Item { return misfiled }).Collect(context.Background(), f)
	if err != nil {
		t.Fatal(err)
	}
	got := byKey(items)
	if len(got) != 2 {
		t.Fatalf("items = %+v", items)
	}
	if it := got["wrongcat:aaa"]; it.Title != "Show.S01E01.1080p finished in “other”" || it.Level != LevelWarning || it.Detail == "" {
		t.Errorf("aaa = %+v", it)
	}
	if it := got["wrongcat:bbb"]; it.Title != "Show.S01E02.1080p finished in no category" {
		t.Errorf("bbb = %+v", it)
	}
	// Counted with imports, in the Downloads pill.
	s := New(staticFrame(*f), nil, quiet, WrongCategory(func() []download.Item { return misfiled }))
	if err := s.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if c := s.Current().Counts; c.Imports != 2 || c.Total != 2 {
		t.Errorf("counts = %+v", c)
	}
}
