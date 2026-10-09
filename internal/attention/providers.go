package attention

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/tristenlammi/arrmada/internal/automation"
	"github.com/tristenlammi/arrmada/internal/download"
	"github.com/tristenlammi/arrmada/internal/health"
	"github.com/tristenlammi/arrmada/internal/requests"
)

// groupLinks is where a group's "see them" link goes, by kind. Health has none of its own:
// each finding links to its own fix, and the group to the Status page.
var groupLinks = map[string]*health.Fix{
	KindRequest:  health.FixRequests,
	KindReview:   health.FixReview,
	KindDownload: health.FixDownloadProblems,
	KindStalled:  health.FixDownloadProblems,
	KindImport:   health.FixDownloadProblems,
	KindWrongCat: health.FixDownloadProblems,
	KindSearch:   health.FixDownloadsSearching,
	KindHealth:   health.FixStatus,
}

func linkTo(it *Item, f *health.Fix) {
	if f != nil {
		it.Link, it.LinkKey, it.LinkLabel = f.Path, f.Key, f.Label
	}
}

// QueueFrame builds the refresh's frame from the shared download-queue snapshot (2-second
// cache, so this is at most one client read however often it runs) and the acquisition
// record. Either may fail: an unreadable queue is !OK, an unreadable record is a nil Acq.
func QueueFrame(
	snapshot func(ctx context.Context) (download.Snapshot, error),
	live func(ctx context.Context) (map[string]automation.Acquisition, []automation.Acquisition, error),
) FrameFunc {
	return func(ctx context.Context) *Frame {
		f := &Frame{}
		if snapshot != nil {
			if snap, err := snapshot(ctx); err == nil {
				f.OK, f.Whole, f.Queue = true, snap.Complete, snap.Items
			}
		}
		if live != nil {
			if byHash, _, err := live(ctx); err == nil {
				f.Acq = byHash
			}
		}
		return f
	}
}

// --- requests ------------------------------------------------------------------------

// PendingRequests is the requests source.
type PendingRequests interface {
	Pending(ctx context.Context, limit int) ([]requests.Request, error)
}

type requestsProvider struct{ src PendingRequests }

// Requests reports each request waiting for approval.
func Requests(src PendingRequests) Provider { return requestsProvider{src} }

func (requestsProvider) Name() string { return "requests" }

func (p requestsProvider) Collect(ctx context.Context, _ *Frame) ([]Item, error) {
	if p.src == nil {
		return nil, nil
	}
	list, err := p.src.Pending(ctx, MaxItems)
	if err != nil {
		return nil, err
	}
	out := make([]Item, 0, len(list))
	for _, rq := range list {
		who := rq.RequestedByName
		if who == "" {
			who = "Someone"
		}
		it := Item{
			Key: KindRequest + ":" + strconv.FormatInt(rq.ID, 10), Kind: KindRequest, Level: LevelWarning,
			Title:  who + " requested " + titleYear(rq.Title, rq.Year),
			Detail: mediaWord(rq.MediaType),
			Since:  sqliteMillis(rq.CreatedAt),
		}
		linkTo(&it, health.FixRequests)
		out = append(out, it)
	}
	return out, nil
}

// --- reviews -------------------------------------------------------------------------

// HeldReviews is the Review source.
type HeldReviews interface {
	ListReviews(ctx context.Context) ([]automation.Review, error)
}

type reviewsProvider struct{ src HeldReviews }

// Reviews reports each finished download held in Review.
func Reviews(src HeldReviews) Provider { return reviewsProvider{src} }

func (reviewsProvider) Name() string { return "reviews" }

func (p reviewsProvider) Collect(ctx context.Context, _ *Frame) ([]Item, error) {
	if p.src == nil {
		return nil, nil
	}
	list, err := p.src.ListReviews(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Item, 0, len(list))
	for _, rv := range list {
		name := rv.ExpectedTitle
		if name == "" {
			name = rv.Name
		}
		level := LevelWarning
		if rv.ReasonCode == automation.ReasonImportFailed {
			level = LevelError // a folder it can't write or a full disk: everything else will fail too
		}
		it := Item{
			Key: KindReview + ":" + strconv.FormatInt(rv.ID, 10), Kind: KindReview, Level: level,
			Title:  name + " is held: " + reviewReason(rv.ReasonCode),
			Detail: clip(rv.Reason, 200),
			Since:  sqliteMillis(rv.CreatedAt),
		}
		linkTo(&it, health.FixReview)
		out = append(out, it)
	}
	return out, nil
}

// reviewReason is a review's reason code in words.
func reviewReason(code string) string {
	switch code {
	case automation.ReasonMismatch:
		return "it looks like a different title"
	case automation.ReasonUnmatched:
		return "it matches nothing in the library"
	case automation.ReasonNumbering:
		return "its episode numbers couldn't be read"
	case automation.ReasonImportFailed:
		return "the import keeps failing"
	case automation.ReasonNoMedia:
		return "nothing inside can be imported"
	}
	return "it needs a decision"
}

// --- downloads -----------------------------------------------------------------------

// StalledAfter is how long a download must have had nobody sending it data before it
// needs someone. The stall fail-over replaces Arrmada's own grabs on a longer clock; this
// is the earlier "worth a look".
const StalledAfter = time.Hour

type downloadsProvider struct {
	mu sync.Mutex
	// stalledSince is when each torrent was first seen stalled in this run, by lowercased
	// hash. The acquisition record's stall clock (progress_at) can push it earlier, so a
	// restart doesn't reset an Arrmada grab's hour.
	stalledSince map[string]time.Time
}

// Downloads reports errored torrents and ones stalled for StalledAfter or longer, from
// the frame's queue read. With the client unreadable it reports nothing: the health
// check says the client is down, which is the real problem.
func Downloads() Provider { return &downloadsProvider{stalledSince: map[string]time.Time{}} }

func (*downloadsProvider) Name() string { return "downloads" }

func (p *downloadsProvider) Collect(_ context.Context, f *Frame) ([]Item, error) {
	if !f.OK {
		return nil, nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []Item
	stalledNow := map[string]bool{}
	for _, it := range f.Queue {
		h := strings.ToLower(it.Hash)
		if h == "" {
			continue
		}
		name := displayName(it, f)
		switch phase := it.Phase(); {
		case phase == "error":
			x := Item{Key: KindDownload + ":" + h, Kind: KindDownload, Level: LevelError,
				Title: name + " errored in the download client", Detail: clientStateWords(it.RawState)}
			linkTo(&x, health.FixDownloadProblems)
			out = append(out, x)
		case (phase == "stalled" || phase == "metadata") && !it.Complete():
			stalledNow[h] = true
			since, ok := p.stalledSince[h]
			if !ok {
				since = f.Now
				p.stalledSince[h] = since
			}
			if a, ok := f.Acq[h]; ok && !a.ProgressAt.IsZero() && a.ProgressAt.Before(since) {
				since = a.ProgressAt // the record has watched it longer than this run has
			}
			if f.Now.Sub(since) < StalledAfter {
				continue
			}
			detail := "Nobody has sent it any data for " + roughDuration(f.Now.Sub(since))
			if phase == "metadata" {
				detail = "It has been waiting for its file list for " + roughDuration(f.Now.Sub(since))
			}
			x := Item{Key: KindStalled + ":" + h, Kind: KindStalled, Level: LevelWarning,
				Title: name + " has stalled", Detail: detail, Since: since.UnixMilli()}
			linkTo(&x, health.FixDownloadProblems)
			out = append(out, x)
		}
	}
	// Forget torrents that aren't stalled any more. On a partial read a missing torrent
	// may only belong to the client that didn't answer, so its clock is kept.
	present := map[string]bool{}
	for _, it := range f.Queue {
		present[strings.ToLower(it.Hash)] = true
	}
	for h := range p.stalledSince {
		if stalledNow[h] || (!f.Whole && !present[h]) {
			continue
		}
		delete(p.stalledSince, h)
	}
	return out, nil
}

// displayName is the release the grab recorded for a torrent, else the client's name.
func displayName(it download.Item, f *Frame) string {
	if a, ok := f.Acq[strings.ToLower(it.Hash)]; ok && a.Title != "" {
		return a.Title
	}
	if it.Name != "" {
		return it.Name
	}
	return it.Hash
}

func clientStateWords(raw string) string {
	switch raw {
	case "missingFiles":
		return "Its files are missing from disk"
	case "error":
		return "The client stopped it with an error"
	}
	return ""
}

// --- health --------------------------------------------------------------------------

// HealthWarnings is the health registry's cached findings.
type HealthWarnings interface {
	Warnings() []health.Warning
}

type healthProvider struct{ src HealthWarnings }

// Health reports each health finding, with its own level and fix link.
func Health(src HealthWarnings) Provider { return healthProvider{src} }

func (healthProvider) Name() string { return "health" }

func (p healthProvider) Collect(context.Context, *Frame) ([]Item, error) {
	if p.src == nil {
		return nil, nil
	}
	ws := p.src.Warnings()
	out := make([]Item, 0, len(ws))
	for _, w := range ws {
		it := Item{Key: KindHealth + ":" + w.Key, Kind: KindHealth, Level: w.Level, Title: w.Message,
			Link: w.Link, LinkKey: w.LinkKey, LinkLabel: w.LinkLabel}
		if !w.Since.IsZero() {
			it.Since = w.Since.UnixMilli()
		}
		out = append(out, it)
	}
	return out, nil
}

// --- searches ------------------------------------------------------------------------

// SearchStuckAfter is how many empty sweeps in a row make a movie or show "still hasn't
// found a release": with the 30-minute-to-12-hour backoff that is a few days of looking.
// Books use their own ladder's monthly point (books.MonthlyAfter).
const SearchStuckAfter = 10

// Counter is one media type's stuck-search count.
type Counter func(ctx context.Context) (int, error)

type searchesProvider struct{ counters []Counter }

// Searches reports one aggregate item for every title whose searches keep coming up
// empty, from cheap COUNT queries (never a list). A counter that fails fails the
// provider, so the last count stands rather than dropping to a smaller one.
func Searches(counters ...Counter) Provider { return searchesProvider{counters} }

func (searchesProvider) Name() string { return "searches" }

func (p searchesProvider) Collect(ctx context.Context, _ *Frame) ([]Item, error) {
	total := 0
	for _, c := range p.counters {
		if c == nil {
			continue
		}
		n, err := c(ctx)
		if err != nil {
			return nil, err
		}
		total += n
	}
	if total == 0 {
		return nil, nil
	}
	it := Item{Key: KindSearch + ":stuck", Kind: KindSearch, Level: LevelWarning, Count: total,
		Title:  groupTitle(KindSearch, total),
		Detail: "Their searches keep coming up empty; each one says what it keeps turning down"}
	linkTo(&it, health.FixDownloadsSearching)
	return []Item{it}, nil
}

// --- helpers -------------------------------------------------------------------------

func titleYear(title string, year int) string {
	if year > 0 {
		return fmt.Sprintf("%s (%d)", title, year)
	}
	return title
}

func mediaWord(kind string) string {
	switch kind {
	case "movie":
		return "Movie"
	case "series":
		return "Series"
	case "book":
		return "Book"
	}
	return ""
}

// sqliteMillis reads a CURRENT_TIMESTAMP value (UTC) as unix ms; 0 when unreadable, so
// the service fills in when it first saw the item.
func sqliteMillis(s string) int64 {
	for _, layout := range []string{"2006-01-02 15:04:05", time.RFC3339Nano} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UnixMilli()
		}
	}
	return 0
}

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

// roughDuration is "2h", "3d": how long, to the unit that matters.
func roughDuration(d time.Duration) string {
	switch {
	case d < time.Hour:
		return strconv.Itoa(int(d.Minutes())) + "m"
	case d < 48*time.Hour:
		return strconv.Itoa(int(d.Hours())) + "h"
	}
	return strconv.Itoa(int(d.Hours()/24)) + "d"
}

func itoa(n int) string { return strconv.Itoa(n) }

func equalHash(a, b string) bool { return a != "" && strings.EqualFold(a, b) }
