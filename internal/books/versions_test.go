package books

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/tristenlammi/arrmada/internal/store"
)

// A version claims a release when the release mentions one of its terms as whole
// words, however the tracker punctuates it; a version without terms claims nothing.
func TestAudioVersionMatches(t *testing.T) {
	full := AudioVersion{Label: "Full cast", Terms: []string{"GraphicAudio", "graphic audio", "full cast", "dramatized"}}
	for text, want := range map[string]bool{
		"Empire of the Vampire [GraphicAudio] M4B":            true,
		"Empire of the Vampire (Graphic-Audio Full-Cast)":     true,
		"Jay Kristoff Empire of the Vampire Dramatized m4b":   true,
		"Empire of the Vampire - Damian Lynch - Unabridged":   false,
		"Forecasting the weather: a castaway's guide (audio)": false, // "cast" inside a word
	} {
		if got := full.Matches(text); got != want {
			t.Errorf("Matches(%q) = %v, want %v", text, got, want)
		}
	}
	if (AudioVersion{Label: "Mine"}).Matches("anything at all") {
		t.Error("a version without terms must match nothing")
	}
	narr := AudioVersion{ID: 2, Label: "Stephen Fry", Terms: []string{"Stephen Fry"}}
	if v := VersionFor([]AudioVersion{full, narr}, "Harry Potter read by Stephen Fry"); v == nil || v.ID != 2 {
		t.Errorf("VersionFor picked %+v, want the Stephen Fry version", v)
	}
	if v := VersionFor([]AudioVersion{full, narr}, "Harry Potter read by Jim Dale"); v != nil {
		t.Errorf("no version should claim Jim Dale's reading, got %q", v.Label)
	}
}

func TestSplitTermsCleansAndDedupes(t *testing.T) {
	got := CleanTerms([]string{" GraphicAudio ", "graphicaudio", "full   cast", "", "Full cast,dramatised"})
	want := []string{"GraphicAudio", "full cast", "dramatised"}
	if len(got) != len(want) {
		t.Fatalf("CleanTerms = %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("CleanTerms = %q, want %q", got, want)
		}
	}
}

// Versions round-trip through the store, are validated per book, ride along on Get
// and List, and go when their book goes.
func TestAudioVersionLifecycle(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	svc := NewService(st.DB(), nil, slog.Default())
	ctx := context.Background()
	b, err := svc.repo.Create(ctx, Book{OLKey: "hc:1", Title: "Empire of the Vampire", Author: "Jay Kristoff"})
	if err != nil {
		t.Fatal(err)
	}

	v, err := svc.AddAudioVersion(ctx, b.ID, "  Full   cast ", []string{"GraphicAudio", "full cast"}, true)
	if err != nil {
		t.Fatal(err)
	}
	if v.Label != "Full cast" || len(v.Terms) != 2 || !v.Monitored || v.File != nil {
		t.Errorf("created version = %+v", v)
	}
	for _, bad := range []string{"", "full CAST", "Standard", "a/b"} {
		if _, err := svc.AddAudioVersion(ctx, b.ID, bad, nil, true); !errors.Is(err, ErrVersionInvalid) {
			t.Errorf("label %q: err = %v, want ErrVersionInvalid", bad, err)
		}
	}

	if err := svc.SetAudioVersionFile(ctx, v.ID, "/audio/Jay Kristoff/Empire of the Vampire (Full cast)", "M4B", 900, 3); err != nil {
		t.Fatal(err)
	}
	got, err := svc.Get(ctx, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.AudioVersions) != 1 || got.AudioVersions[0].File == nil || got.AudioVersions[0].File.FileCount != 3 {
		t.Fatalf("Get versions = %+v", got.AudioVersions)
	}
	if got.Audiobook != nil {
		t.Error("a version's file must not fill the standard audiobook")
	}
	list, _ := svc.List(ctx)
	if len(list) != 1 || len(list[0].AudioVersions) != 1 {
		t.Fatalf("List versions = %+v", list)
	}

	up, err := svc.UpdateAudioVersion(ctx, b.ID, v.ID, "GraphicAudio", []string{"graphicaudio"}, false)
	if err != nil || up.Label != "GraphicAudio" || up.Monitored || up.File == nil {
		t.Errorf("update = %+v, %v (file must survive a rename)", up, err)
	}
	if _, err := svc.GetAudioVersion(ctx, b.ID+99, v.ID); !errors.Is(err, ErrVersionNotFound) {
		t.Error("a version must only be reachable through its own book")
	}

	if err := svc.Delete(ctx, b.ID); err != nil {
		t.Fatal(err)
	}
	all, _ := svc.repo.AllAudioVersions(ctx)
	if len(all) != 0 {
		t.Errorf("versions outlived their book: %+v", all)
	}
}
