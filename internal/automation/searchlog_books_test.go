package automation

import (
	"testing"

	"github.com/tristenlammi/arrmada/internal/metadata"
	"github.com/tristenlammi/arrmada/internal/quality"
)

func bookAttempts(t *testing.T, h *bookRSSHarness, id int64) map[string]Attempt {
	t.Helper()
	all, err := h.c.SearchAttempts(h.ctx, AttemptBook, id, 0, 20)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]Attempt{}
	for _, a := range all {
		if _, seen := out[a.Scope]; !seen {
			out[a.Scope] = a // newest per scope
		}
	}
	return out
}

// A book search records one attempt per edition: the ebook pass found only another
// author's book, the audiobook pass only a release the profile rejects.
func TestBookSearchRecordsOneAttemptPerEdition(t *testing.T) {
	h := newBookRSSHarness(t)
	createDefault(t, h.c.quality, quality.StoredProfile{
		MediaType: quality.MediaBook, Name: "Both",
		FormatScores: map[string]int{"EPUB": 40, "M4B": 40},
		Rejected:     []string{"abridged"},
	})
	b := h.add(t, true, metadata.BookResult{Key: "OL2W", Title: "Dune", Author: "Frank Herbert"})[0]
	h.feed.offer(
		"Brandon Sanderson - Mistborn EPUB",
		"Frank Herbert - Dune Abridged M4B",
	)
	out, err := h.c.SearchBookNow(WithSearchTrigger(h.ctx, TriggerManual), b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if out.Grabbed != 0 || out.WrongTitle != 2 {
		t.Fatalf("book outcome = %+v", out)
	}

	got := bookAttempts(t, h, b.ID)
	eb, ok := got["ebook"]
	if !ok {
		t.Fatalf("no ebook attempt: %v", got)
	}
	if eb.WrongTitle != 1 || eb.Reasons[DropWrongTitle] != 1 || eb.Outcome != OutcomeNoneSuitable || eb.Trigger != TriggerManual {
		t.Fatalf("ebook attempt = %+v", eb)
	}
	if eb.Reasons[DropOutOfScope] != 1 {
		t.Fatalf("the audiobook should count as the other edition on the ebook pass: %v", eb.Reasons)
	}
	ab, ok := got["audiobook"]
	if !ok {
		t.Fatalf("no audiobook attempt: %v", got)
	}
	if ab.Reasons[quality.RejectTerm] != 1 || ab.Reasons[DropWrongTitle] != 1 || ab.Rejected != 1 || ab.Outcome != OutcomeNoneSuitable {
		t.Fatalf("audiobook attempt = %+v", ab)
	}
	if ab.Example == "" || eb.Example == "" {
		t.Fatalf("attempts carry no example: %q / %q", eb.Example, ab.Example)
	}
}

// The sweep's give-up behaviour is unchanged: a search that ran and found nothing is a miss.
func TestBookSweepStillCountsMisses(t *testing.T) {
	h := newBookRSSHarness(t)
	b := h.add(t, true, metadata.BookResult{Key: "OL2W", Title: "Dune", Author: "Frank Herbert"})[0]
	h.feed.offer("Brandon Sanderson - Mistborn EPUB")
	h.c.SearchBooksMissing(h.ctx)
	if _, misses := h.bk.SearchState(h.ctx, b.ID); misses != 1 {
		t.Fatalf("misses = %d, want 1", misses)
	}
	a := bookAttempts(t, h, b.ID)["ebook"]
	if a.Trigger != TriggerSweep || a.Outcome != OutcomeNoneSuitable {
		t.Fatalf("sweep attempt = %+v", a)
	}
}

// An album whose every match is blocklisted records one attempt saying so.
func TestAlbumSearchRecordsBlocklisted(t *testing.T) {
	h := musicTestCoord(t)
	a := h.addArtist(t, "Radiohead", "", []string{"OK Computer"}, nil, "")
	al := h.album(t, a.ID, "OK Computer")
	flac, mp3 := "Radiohead - OK Computer (1997) [FLAC]", "Radiohead - OK Computer (1997) [MP3 320]"
	h.c.addBlockMusic(h.ctx, al.ID, flac, "Fake", "test")
	h.c.addBlockMusic(h.ctx, al.ID, mp3, "Fake", "test")
	h.releases = found(rel(flac), rel(mp3), rel("Muse - Absolution (2003) [FLAC]"))

	if out := h.c.grabAlbum(WithSearchTrigger(h.ctx, TriggerSweep), a, al); out.Code != outcomeBlocked {
		t.Fatalf("outcome = %+v", out)
	}
	all, err := h.c.SearchAttempts(h.ctx, AttemptMusic, al.ID, 0, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 {
		t.Fatalf("%d attempts, want one per album search", len(all))
	}
	at := all[0]
	if at.Scope != ScopeAlbum || at.Blocklisted != 2 || at.WrongTitle != 1 || at.Outcome != OutcomeNoneSuitable || at.TopReason != DropBlocklisted {
		t.Fatalf("attempt = %+v", at)
	}
}
