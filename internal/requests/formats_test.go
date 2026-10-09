package requests

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/tristenlammi/arrmada/internal/automation"
	"github.com/tristenlammi/arrmada/internal/books"
	"github.com/tristenlammi/arrmada/internal/metadata"
	"github.com/tristenlammi/arrmada/internal/quality"
)

// presetRef returns the ref of the seeded book preset with this name ("Ebook",
// "Audiobook", "Ebook + Audiobook").
func presetRef(t *testing.T, s *Service, name string) string {
	t.Helper()
	list, err := s.quality.ListStored(context.Background(), quality.MediaBook)
	if err != nil {
		t.Fatal(err)
	}
	for _, sp := range list {
		if sp.Name == name {
			return "custom:" + strconv.FormatInt(sp.ID, 10)
		}
	}
	t.Fatalf("no book preset %q", name)
	return ""
}

func duneCatalogue() catalogue {
	return catalogue{byKey: map[string]metadata.BookResult{"OL1W": {Key: "OL1W", Title: "Dune", Author: "Frank Herbert"}}}
}

// A requester's Read / Listen / Both choice is stored and puts the request on the
// matching preset; no choice takes the owner's default; anything else is refused.
func TestCreateBookFormatsMapToPreset(t *testing.T) {
	s, _, _, ctx := bookLinkFixture(t, duneCatalogue())
	for i, tc := range []struct{ formats, want, preset string }{
		{FormatsAudiobook, FormatsAudiobook, "Audiobook"},
		{FormatsBoth, FormatsBoth, "Ebook + Audiobook"},
		{FormatsEbook, FormatsEbook, "Ebook"},
		{"", FormatsEbook, "Ebook"}, // the default book profile is the Ebook preset
	} {
		req, _, err := s.Create(ctx, Request{MediaType: "book", OLKey: "OL" + strconv.Itoa(100+i) + "W",
			Title: "Book " + strconv.Itoa(i), Author: "Someone", RequestedBy: 7, Formats: tc.formats}, false)
		if err != nil {
			t.Fatal(err)
		}
		if req.Formats != tc.want || req.QualityProfile != presetRef(t, s, tc.preset) {
			t.Errorf("formats %q: got formats %q profile %q, want %q on %s", tc.formats, req.Formats, req.QualityProfile, tc.want, tc.preset)
		}
	}
	if _, _, err := s.Create(ctx, Request{MediaType: "book", OLKey: "OL9W", Title: "X", RequestedBy: 7, Formats: "paperback"}, false); !errors.Is(err, ErrBadFormats) {
		t.Errorf("formats=paperback: err = %v, want ErrBadFormats", err)
	}
	// A profile the requester named stands in for the choice when none is given.
	req, _, err := s.Create(ctx, Request{MediaType: "book", OLKey: "OL8W", Title: "Y", RequestedBy: 7,
		QualityProfile: presetRef(t, s, "Audiobook")}, false)
	if err != nil {
		t.Fatal(err)
	}
	if req.Formats != FormatsAudiobook {
		t.Errorf("profile Audiobook, no formats: formats = %q, want audiobook", req.Formats)
	}
}

// A second requester asking for the other format of a pending request is subscribed,
// and the request now asks for both.
func TestAttachUnionsFormats(t *testing.T) {
	s, _, _, ctx := bookLinkFixture(t, duneCatalogue())
	first, _, err := s.Create(ctx, Request{MediaType: "book", OLKey: "OL1W", Title: "Dune", Author: "Frank Herbert", RequestedBy: 7, Formats: FormatsEbook}, false)
	if err != nil {
		t.Fatal(err)
	}
	got, sub, err := s.Create(ctx, Request{MediaType: "book", OLKey: "OL1W", Title: "Dune", Author: "Frank Herbert", RequestedBy: 8, Formats: FormatsAudiobook}, false)
	if err != nil {
		t.Fatal(err)
	}
	if !sub || got.ID != first.ID {
		t.Fatalf("got request %d subscribed=%v, want subscribed to %d", got.ID, sub, first.ID)
	}
	if got.Formats != FormatsBoth || got.QualityProfile != presetRef(t, s, "Ebook + Audiobook") {
		t.Errorf("after the listener joined: formats %q profile %q, want both", got.Formats, got.QualityProfile)
	}
	// Asking again for what is already asked for changes nothing.
	again, _, _ := s.Create(ctx, Request{MediaType: "book", OLKey: "OL1W", Title: "Dune", Author: "Frank Herbert", RequestedBy: 9, Formats: FormatsEbook}, false)
	if again.Formats != FormatsBoth {
		t.Errorf("formats narrowed to %q", again.Formats)
	}
}

func searchSpy(s *Service) chan int64 {
	ch := make(chan int64, 4)
	s.searchBook = func(_ context.Context, id int64) (automation.SearchOutcome, error) {
		ch <- id
		return automation.SearchOutcome{}, nil
	}
	return ch
}

func expectSearch(t *testing.T, ch chan int64, want int64) {
	t.Helper()
	select {
	case id := <-ch:
		if id != want {
			t.Errorf("searched book %d, want %d", id, want)
		}
	case <-time.After(5 * time.Second):
		t.Errorf("no search for book %d", want)
	}
}

// Requesting the audiobook of a book the library holds as an ebook widens the book to
// Ebook + Audiobook and searches; approving a narrower request never narrows a book.
func TestApproveWidensExistingBook(t *testing.T) {
	s, repo, _, ctx := bookLinkFixture(t, duneCatalogue())
	searched := searchSpy(s)
	ebookOnly := presetRef(t, s, "Ebook")
	both := presetRef(t, s, "Ebook + Audiobook")
	b, err := repo.Create(ctx, books.Book{OLKey: "OL1W", Title: "Dune", Author: "Frank Herbert", QualityProfile: ebookOnly, Monitored: true})
	if err != nil {
		t.Fatal(err)
	}
	giveEbook(t, repo, ctx, b.ID)

	req, _, err := s.Create(ctx, Request{MediaType: "book", OLKey: "OL1W", Title: "Dune", Author: "Frank Herbert", RequestedBy: 7, Formats: FormatsAudiobook}, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Approve(ctx, req.ID, ""); err != nil {
		t.Fatal(err)
	}
	got, _ := repo.Get(ctx, b.ID)
	if got.QualityProfile != both {
		t.Errorf("book profile = %q, want widened to %q", got.QualityProfile, both)
	}
	expectSearch(t, searched, b.ID)
	evs, _ := repo.Events(ctx, b.ID, 10)
	if len(evs) == 0 || !strings.Contains(evs[0].Detail, "also wants the audiobook") {
		t.Errorf("timeline = %+v, want the widening noted", evs)
	}

	// An ebook-only request for it leaves the book on both.
	other, err := repo.Create(ctx, books.Book{OLKey: "OL2W", Title: "Children of Dune", Author: "Frank Herbert", QualityProfile: both})
	if err != nil {
		t.Fatal(err)
	}
	s.books = books.NewService(s.repo.db, catalogue{byKey: map[string]metadata.BookResult{
		"OL2W": {Key: "OL2W", Title: "Children of Dune", Author: "Frank Herbert"},
	}}, s.log)
	narrow, _, err := s.Create(ctx, Request{MediaType: "book", OLKey: "OL2W", Title: "Children of Dune", Author: "Frank Herbert", RequestedBy: 7, Formats: FormatsEbook}, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Approve(ctx, narrow.ID, ""); err != nil {
		t.Fatal(err)
	}
	if got, _ := repo.Get(ctx, other.ID); got.QualityProfile != both {
		t.Errorf("an ebook request narrowed the book to %q", got.QualityProfile)
	}
	expectSearch(t, searched, other.ID) // its ebook is missing
}

// A listener joining an approved ebook request widens the library book and searches.
func TestAttachToApprovedWidensBook(t *testing.T) {
	s, repo, _, ctx := bookLinkFixture(t, duneCatalogue())
	searched := searchSpy(s)
	b, err := repo.Create(ctx, books.Book{OLKey: "OL1W", Title: "Dune", Author: "Frank Herbert", QualityProfile: presetRef(t, s, "Ebook")})
	if err != nil {
		t.Fatal(err)
	}
	giveEbook(t, repo, ctx, b.ID)
	first, _, err := s.Create(ctx, Request{MediaType: "book", OLKey: "OL1W", Title: "Dune", Author: "Frank Herbert", RequestedBy: 7, Formats: FormatsEbook}, true)
	if err != nil {
		t.Fatal(err)
	}
	if first.Status != StatusApproved {
		t.Fatalf("auto-approved request is %q", first.Status)
	}
	got, sub, err := s.Create(ctx, Request{MediaType: "book", OLKey: "OL1W", Title: "Dune", Author: "Frank Herbert", RequestedBy: 8, Formats: FormatsAudiobook}, false)
	if err != nil || !sub {
		t.Fatalf("listener: %v subscribed=%v", err, sub)
	}
	if got.Formats != FormatsBoth {
		t.Errorf("formats = %q, want both", got.Formats)
	}
	if bk, _ := repo.Get(ctx, b.ID); bk.QualityProfile != presetRef(t, s, "Ebook + Audiobook") {
		t.Errorf("book profile = %q, want Ebook + Audiobook", bk.QualityProfile)
	}
	expectSearch(t, searched, b.ID)
}

func TestBookReady(t *testing.T) {
	ebook := &books.BookFile{Path: "/l/b.epub"}
	audio := &books.BookFile{Path: "/l/b"}
	version := []books.AudioVersion{{ID: 1, File: &books.BookFile{Path: "/l/b (Full Cast)"}}}
	cases := []struct {
		name    string
		formats string
		b       books.Book
		want    bool
		note    string
	}{
		{"legacy with a file", "", books.Book{Ebook: ebook, HasFile: true}, true, ""},
		{"legacy without", "", books.Book{}, false, ""},
		{"read, ebook here", FormatsEbook, books.Book{Ebook: ebook, HasFile: true}, true, ""},
		{"read, only audio", FormatsEbook, books.Book{Audiobook: audio, HasFile: true}, false, ""},
		{"listen, only ebook", FormatsAudiobook, books.Book{Ebook: ebook, HasFile: true}, false, ""},
		{"listen, audiobook", FormatsAudiobook, books.Book{Audiobook: audio, HasFile: true}, true, ""},
		{"listen, a version", FormatsAudiobook, books.Book{AudioVersions: version}, true, ""},
		{"both, ebook only", FormatsBoth, books.Book{Ebook: ebook, HasFile: true}, false, "Ebook ready · audiobook on the way"},
		{"both, audio only", FormatsBoth, books.Book{Audiobook: audio, HasFile: true}, false, "Audiobook ready · ebook on the way"},
		{"both, both", FormatsBoth, books.Book{Ebook: ebook, Audiobook: audio, HasFile: true}, true, ""},
	}
	for _, tc := range cases {
		if got := bookReady(tc.formats, tc.b); got != tc.want {
			t.Errorf("%s: ready = %v, want %v", tc.name, got, tc.want)
		}
		if got := partialNote(tc.formats, tc.b); got != tc.note {
			t.Errorf("%s: note = %q, want %q", tc.name, got, tc.note)
		}
	}
}

func inboxBodies(t *testing.T, s *Service, uid int64) []string {
	t.Helper()
	inbox, err := s.repo.listUserNotifications(context.Background(), uid)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, n := range inbox {
		out = append(out, n.Ref+" | "+n.Body)
	}
	return out
}

// A Listen request whose ebook arrives first hears nothing; when the audiobook lands it
// is told the audiobook is ready to listen to. A Both request gets one message per
// format, each once. A request from before the choice keeps its one ready message.
func TestReadyMessagesPerFormat(t *testing.T) {
	s, repo, _, ctx := bookLinkFixture(t, duneCatalogue())
	b, _ := repo.Create(ctx, books.Book{OLKey: "OL1W", Title: "Dune", Author: "Frank Herbert"})
	mk := func(key, formats string, uid int64) {
		if _, err := s.repo.Create(ctx, Request{MediaType: "book", OLKey: key, Title: "Dune", Status: StatusApproved,
			RequestedBy: uid, BookID: b.ID, Formats: formats}); err != nil {
			t.Fatal(err)
		}
	}
	mk("OL1W", FormatsAudiobook, 1)
	mk("OL2W", FormatsBoth, 2)
	mk("OL3W", "", 3)

	giveEbook(t, repo, ctx, b.ID)
	if err := s.NotifyBookReady(ctx, b.ID); err != nil {
		t.Fatal(err)
	}
	if got := inboxBodies(t, s, 1); len(got) != 0 {
		t.Errorf("listener told about the ebook: %v", got)
	}
	if got := inboxBodies(t, s, 2); len(got) != 1 || !strings.Contains(got[0], "is ready to read") {
		t.Errorf("both, ebook here: %v", got)
	}
	if got := inboxBodies(t, s, 3); len(got) != 1 || got[0] != "book:OL3W | “Dune” is ready to read." {
		t.Errorf("legacy: %v, want the one old message under the old ref", got)
	}

	if err := repo.SetEdition(ctx, b.ID, books.KindAudiobook, "/library/dune", "M4B", 1, 1); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ { // the import event, then the sweep: still once each
		if err := s.NotifyBookReady(ctx, b.ID); err != nil {
			t.Fatal(err)
		}
		if err := s.SweepReadyRequests(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if got := inboxBodies(t, s, 1); len(got) != 1 || got[0] != "book:OL1W:audiobook | The audiobook of “Dune” is ready to listen to." {
		t.Errorf("listener: %v", got)
	}
	if got := inboxBodies(t, s, 2); len(got) != 2 {
		t.Errorf("both: %v, want one per format", got)
	}
	if got := inboxBodies(t, s, 3); len(got) != 1 {
		t.Errorf("legacy re-notified: %v", got)
	}
}

// List marks a request available only when every format it asked for is here, and
// Track calls a "both" request with one format partial.
func TestListPerFormatReadiness(t *testing.T) {
	s, repo, _, ctx := bookLinkFixture(t, duneCatalogue())
	b, _ := repo.Create(ctx, books.Book{OLKey: "OL1W", Title: "Dune", Author: "Frank Herbert"})
	giveEbook(t, repo, ctx, b.ID)
	mk := func(key, formats string) {
		if _, err := s.repo.Create(ctx, Request{MediaType: "book", OLKey: key, Title: "Dune", Status: StatusApproved,
			RequestedBy: 7, BookID: b.ID, Formats: formats}); err != nil {
			t.Fatal(err)
		}
	}
	mk("OL1W", FormatsBoth)
	mk("OL2W", FormatsAudiobook)
	mk("OL3W", FormatsEbook)
	list, err := s.List(ctx, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	s.Track(ctx, list, nil)
	byFormat := map[string]Request{}
	for _, r := range list {
		byFormat[r.Formats] = r
	}
	if r := byFormat[FormatsBoth]; r.Available || r.Tracking.Stage != StagePartial || r.Tracking.Note != "Ebook ready · audiobook on the way" {
		t.Errorf("both: available=%v tracking=%+v", r.Available, r.Tracking)
	}
	if r := byFormat[FormatsAudiobook]; r.Available || r.Tracking.Stage != StageSearching {
		t.Errorf("listen with only the ebook here: available=%v tracking=%+v", r.Available, r.Tracking)
	}
	if r := byFormat[FormatsEbook]; !r.Available || r.Tracking.Stage != StageAvailable {
		t.Errorf("read: available=%v tracking=%+v", r.Available, r.Tracking)
	}
}
