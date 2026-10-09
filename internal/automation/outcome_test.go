package automation

import (
	"strings"
	"testing"
)

// searchOutcome runs a manual search of the harness' movie and returns what it reported.
func searchOutcome(t *testing.T, h *stallHarness, mid int64) SearchOutcome {
	t.Helper()
	out, err := h.c.SearchMovie(h.ctx, mid)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestSearchOutcomeGrabbed(t *testing.T) {
	h := newStallHarness(t)
	mid := h.addMovie(t, 1, "Arrival", 2016)
	h.ix.offer(arrivalRelease)
	out := searchOutcome(t, h, mid)
	if out.Reason != ReasonGrabbed || !out.Searched || out.Returned != 1 || out.Matching != 1 || out.Usable != 1 ||
		out.Grabbed != 1 || len(out.GrabbedTitles) != 1 || out.GrabbedTitles[0] != arrivalRelease {
		t.Fatalf("outcome = %+v", out)
	}
	if msg := out.Message("movie"); msg != "Grabbed "+arrivalRelease {
		t.Fatalf("message = %q", msg)
	}
}

func TestSearchOutcomeNoReleases(t *testing.T) {
	h := newStallHarness(t)
	mid := h.addMovie(t, 1, "Arrival", 2016)
	out := searchOutcome(t, h, mid)
	if out.Reason != ReasonNoReleases || !out.Searched || out.Returned != 0 || out.Grabbed != 0 {
		t.Fatalf("outcome = %+v", out)
	}
	if msg := out.Message("movie"); msg != "No releases found" {
		t.Fatalf("message = %q", msg)
	}
}

func TestSearchOutcomeWrongTitle(t *testing.T) {
	h := newStallHarness(t)
	mid := h.addMovie(t, 1, "Arrival", 2016)
	h.ix.offer("Inception.2010.1080p.BluRay.x264-GRP", "Interstellar.2014.2160p.WEB-DL.x265-GRP")
	out := searchOutcome(t, h, mid)
	if out.Reason != ReasonNoneForTitle || out.Returned != 2 || out.Matching != 0 || out.Grabbed != 0 {
		t.Fatalf("outcome = %+v", out)
	}
	if msg := out.Message("movie"); msg != "2 releases found, none for this movie" {
		t.Fatalf("message = %q", msg)
	}
	if h.adds() != 0 {
		t.Fatal("grabbed a different film")
	}
}

func TestSearchOutcomeBlocklisted(t *testing.T) {
	h := newStallHarness(t)
	mid := h.addMovie(t, 1, "Arrival", 2016)
	h.ix.offer(arrivalRelease)
	if err := h.c.Blocklist(h.ctx, mid, arrivalRelease, "Fake", "", "test"); err != nil {
		t.Fatal(err)
	}
	out := searchOutcome(t, h, mid)
	if out.Reason != ReasonBlockedOrBelow || out.Matching != 1 || out.Usable != 0 || out.Grabbed != 0 {
		t.Fatalf("outcome = %+v", out)
	}
	if msg := out.Message("movie"); !strings.Contains(msg, "blocklisted") {
		t.Fatalf("message = %q", msg)
	}
}

func TestSearchOutcomeNothingWanted(t *testing.T) {
	h := newStallHarness(t)
	mid := h.addMovie(t, 1, "Arrival", 2016)
	if _, err := h.c.db.Exec(`UPDATE movies SET has_file = 1, movie_file_path = '/library/Arrival (2016)/Arrival.mkv' WHERE id = ?`, mid); err != nil {
		t.Fatal(err)
	}
	h.ix.offer(arrivalRelease)
	out := searchOutcome(t, h, mid)
	if out.Reason != ReasonNothingWanted || out.Searched || h.ix.searchCount() != 0 {
		t.Fatalf("outcome = %+v, searches = %d", out, h.ix.searchCount())
	}
	if msg := out.Message("movie"); !strings.HasPrefix(msg, "Nothing to search for") {
		t.Fatalf("message = %q", msg)
	}
}

func TestSearchOutcomeMessages(t *testing.T) {
	cases := []struct {
		out  SearchOutcome
		want string
	}{
		{SearchOutcome{Reason: ReasonGrabbed, Grabbed: 2, GrabbedTitles: []string{"A.2020.1080p", "A.2020.2160p"}}, "Grabbed A.2020.1080p and 1 more"},
		{SearchOutcome{Reason: ReasonGrabbed, Grabbed: 3}, "Grabbed 3 releases"},
		{SearchOutcome{Reason: ReasonNoneForTitle, Returned: 12}, "12 releases found, none for this show"},
		{SearchOutcome{Reason: ReasonNoneForTitle, Returned: 1}, "1 release found, none for this show"},
		{SearchOutcome{Reason: ReasonBlockedOrBelow, Returned: 9, Matching: 5, Usable: 4}, "5 releases found, none met the quality profile"},
		{SearchOutcome{Reason: ReasonAlreadySearching}, "Already being searched — that search covers it"},
	}
	for _, c := range cases {
		if got := c.out.Message("show"); got != c.want {
			t.Errorf("%+v: message = %q, want %q", c.out, got, c.want)
		}
	}
}
