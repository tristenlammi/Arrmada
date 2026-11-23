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
	"github.com/tristenlammi/arrmada/internal/library"
	"github.com/tristenlammi/arrmada/internal/requests"
)

// groupLinks is where a group's "see them" link goes, by kind. Health has none of its own:
// each finding links to its own fix, and the group to the Status page.
var groupLinks = map[string]*health.Fix{
	KindRequest:  health.FixRequests,
	KindReview:   health.FixReview,
	KindDownload: health.FixDownloadProblems,
	KindStalled:  health.FixDownloadProblems,
	KindMoving:   health.FixDownloadProblems,
	KindImport:   health.FixImportProblems,
	KindWrongCat: health.FixWrongCategory,
	KindSearch:   health.FixDownloadsSearching,
	KindHealth:   health.FixStatus,
	KindConvert:  health.FixConvertProblems,
	KindIssue:    health.FixIssues,
	KindSubs:     health.FixSubtitles,
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
	trying func() map[string]time.Time,
) FrameFunc {
	return func(ctx context.Context) *Frame {
		f := &Frame{}
		if trying != nil {
			f.Trying = trying()
		}
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

// scanFoldersTitle is the Needs-you line for the library scan's held folders.
func scanFoldersTitle(n int) string {
	if n == 1 {
		return "1 music folder from the library scan needs matching"
	}
	return fmt.Sprintf("%d music folders from the library scan need matching", n)
}

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
	// Folders the music library scan couldn't place are one item, however many: a first
	// scan of a big collection can hold hundreds, and each is the same decision (MUS-37).
	scanned, since := 0, int64(0)
	for _, rv := range list {
		if automation.IsLibraryScanReview(rv) {
			scanned++
			if t := sqliteMillis(rv.CreatedAt); since == 0 || (t > 0 && t < since) {
				since = t
			}
		}
	}
	if scanned > 0 {
		it := Item{
			Key: KindReview + ":music-scan", Kind: KindReview, Level: LevelWarning, Name: "Music library scan",
			Title:  scanFoldersTitle(scanned),
			Detail: "The scan couldn't tell which album they are. Match each to an album in Reviews; nothing on disk changes.",
			Since:  since,
		}
		linkTo(&it, health.FixReview)
		out = append(out, it)
	}
	for _, rv := range list {
		if automation.IsLibraryScanReview(rv) {
			continue
		}
		name := rv.ExpectedTitle
		if name == "" {
			name = rv.Name
		}
		level := LevelWarning
		if rv.ReasonCode == automation.ReasonImportFailed {
			level = LevelError // a folder it can't write or a full disk: everything else will fail too
		}
		it := Item{
			Key: KindReview + ":" + strconv.FormatInt(rv.ID, 10), Kind: KindReview, Level: level, Name: name,
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
	case automation.ReasonSameName:
		return "two shows share its name"
	case automation.ReasonUnmatched:
		return "it matches nothing in the library"
	case automation.ReasonNumbering:
		return "its episode numbers couldn't be read"
	case automation.ReasonTitleConflict:
		return "the release's episode numbers and titles disagree"
	case automation.ReasonImportFailed:
		return "the import keeps failing"
	case automation.ReasonNoMedia:
		return "nothing inside can be imported"
	case automation.ReasonReplaceCheck:
		return "the smaller release isn't what its name said"
	}
	return "it needs a decision"
}

// --- downloads -----------------------------------------------------------------------

// StalledAfter is how long a download must have had nobody sending it data before it
// needs someone. The stall fail-over replaces Arrmada's own grabs on a longer clock; this
// is the earlier "worth a look".
const StalledAfter = time.Hour

// MovingStuckAfter is how long qBittorrent may spend moving a torrent (out of its
// incomplete folder, most often) before it needs someone. Imports wait for the move
// (download.Item.ReadyToImport), so a move that never finishes means a download that never
// imports and nothing saying why. A move across disks is a full copy, and qBittorrent does
// one at a time with the rest queued as "moving" too: a few 100 GB season packs finishing
// together on a busy disk or a gigabit share take the best part of an hour. Two hours
// leaves room for that, and is still the same day for someone waiting on an episode.
// Nothing acts on it; it's only said.
const MovingStuckAfter = 2 * time.Hour

type downloadsProvider struct {
	mu sync.Mutex
	// stalledSince is when each torrent was first seen stuck (stalled, or waiting for its
	// file list) in this run, by lowercased hash: the clock for a torrent no grab knows. An
	// Arrmada grab goes by its stall clock (progress_at), so a restart doesn't reset its hour.
	stalledSince map[string]time.Time
	// movingSince is when each torrent was first seen being moved by qBittorrent in this
	// run, by lowercased hash. The client doesn't say when a move began; a restart starts
	// the clock again, which only delays the item.
	movingSince map[string]time.Time
}

// Downloads reports errored torrents, ones stalled for StalledAfter or longer, and ones
// qBittorrent has been moving for MovingStuckAfter or longer, from the frame's queue read.
// With the client unreadable it reports nothing: the health check says the client is down,
// which is the real problem.
func Downloads() Provider {
	return &downloadsProvider{stalledSince: map[string]time.Time{}, movingSince: map[string]time.Time{}}
}

func (*downloadsProvider) Name() string { return "downloads" }

func (p *downloadsProvider) Collect(_ context.Context, f *Frame) ([]Item, error) {
	if !f.OK {
		return nil, nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []Item
	stalledNow, movingNow := map[string]bool{}, map[string]bool{}
	for _, it := range f.Queue {
		h := strings.ToLower(it.Hash)
		if h == "" {
			continue
		}
		name := displayName(it, f)
		switch phase := it.Phase(); {
		case it.Moving():
			movingNow[h] = true
			first, ok := p.movingSince[h]
			if !ok {
				first = f.Now
				p.movingSince[h] = first
			}
			if f.Now.Sub(first) < MovingStuckAfter {
				continue
			}
			// Its own kind, not a stall: nobody needs to send it data, so the stall alert, the
			// "has stalled" count and the card's Reannounce / Recheck would all be wrong. Not
			// Grabbed: there's nothing to fail over to. The download is here; it's the
			// move that's stuck, and only qBittorrent (or the disk) can finish it.
			x := Item{Key: KindMoving + ":" + h, Kind: KindMoving, Level: LevelWarning, Name: name,
				Title:  "qBittorrent has been moving " + name + " for over " + roughDuration(f.Now.Sub(first)),
				Detail: "It won't import until the move finishes. Check qBittorrent and the disk it's moving to (full, slow, or gone).",
				Since:  first.UnixMilli(), Hash: h}
			linkTo(&x, health.FixDownloadProblems)
			out = append(out, x)
		case phase == "error" && it.Transport == download.TransportUsenet:
			if x, ok := usenetFailure(it, name, h, f); ok {
				out = append(out, x)
			}
		case phase == "error":
			x := Item{Key: KindDownload + ":" + h, Kind: KindDownload, Level: LevelError, Name: name,
				Title: name + " errored in the download client", Detail: clientStateWords(it.RawState), Hash: h, Grabbed: f.grabbed(h)}
			linkTo(&x, health.FixDownloadProblems)
			out = append(out, x)
		case !it.Complete():
			// The Activity queue's stall rule (automation.QueueStall), so every download
			// listed here is also under the queue's Stalled pill; only the threshold
			// differs (an hour, not half an hour). A torrent no grab knows is watched from
			// the first time this run saw it stuck, or the coordinator saw it trying,
			// whichever is later.
			var clocks automation.StallClocks
			if a, ok := f.Acq[h]; ok {
				clocks.Grab = a.ProgressAt
			}
			if phase == "stalled" || phase == "metadata" {
				stalledNow[h] = true
				first, ok := p.stalledSince[h]
				if !ok {
					first = f.Now
					p.stalledSince[h] = first
				}
				clocks.Trying = first
			}
			if at := f.Trying[h]; at.After(clocks.Trying) {
				clocks.Trying = at
			}
			since, stalled := automation.QueueStall(it, clocks, false, f.Now)
			if !stalled || f.Now.Sub(since) < StalledAfter {
				continue
			}
			detail := "Nobody has sent it any data for " + roughDuration(f.Now.Sub(since))
			if phase == "metadata" {
				detail = "It has been waiting for its file list for " + roughDuration(f.Now.Sub(since))
			}
			x := Item{Key: KindStalled + ":" + h, Kind: KindStalled, Level: LevelWarning, Name: name,
				Title: name + " has stalled", Detail: detail, Since: since.UnixMilli(), Hash: h, Grabbed: f.grabbed(h)}
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
	for h := range p.movingSince {
		if movingNow[h] || (!f.Whole && !present[h]) {
			continue
		}
		delete(p.movingSince, h)
	}
	return out, nil
}

// usenetFailure is the item for a usenet job SABnzbd failed. One Arrmada grabbed that
// failed for the release's sake has none: the next stall check blocklists it and searches
// for another on its own, and an alert in between would only announce what's already being
// handled. One that failed where SABnzbd writes (a full disk, a folder it can't write) is
// an error with the fix in plain words — nothing retries it until someone does. Anything
// else (a job Arrmada didn't grab) is shown with SABnzbd's own reason.
func usenetFailure(it download.Item, name, h string, f *Frame) (Item, bool) {
	fail := automation.ClassifyUsenetFailure(it.FailMessage)
	grabbed := f.grabbed(h)
	x := Item{Key: KindDownload + ":" + h, Kind: KindDownload, Level: LevelError, Name: name, Hash: h, Grabbed: grabbed}
	switch {
	case fail.Environment() && fail.Fetch:
		x.Title = "SABnzbd couldn't fetch the NZB for " + name + ": " + fail.Reason
		x.Detail = clip(fail.Fix+". SABnzbd said: "+it.FailMessage, 300)
	case fail.Environment():
		x.Title = "SABnzbd couldn't save " + name + ": " + fail.Reason
		x.Detail = clip(fail.Fix+". SABnzbd said: "+it.FailMessage, 300)
	case grabbed:
		return Item{}, false
	default:
		x.Title = name + " failed in SABnzbd"
		x.Detail = clip(it.FailMessage, 200)
	}
	linkTo(&x, health.FixDownloadProblems)
	return x, true
}

// grabbed reports whether an Arrmada grab recorded the torrent (lowercased hash h) and
// is still downloading it — what Needs you can try another release for.
func (f *Frame) grabbed(h string) bool {
	a, ok := f.Acq[h]
	return ok && a.Downloading()
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

// --- imports that keep failing ------------------------------------------------------

// Import failure thresholds: a second failed try is worth a look; five tries over at
// least a quarter of an hour (the movie importer backs off 1, 2, 4, 8 minutes; the series
// sweep tries every 30 seconds) is broken.
const (
	importWarnAfter  = 2
	importErrorAfter = 5
	importErrorFor   = 15 * time.Minute
)

type importsProvider struct {
	failures func() []library.FailureInfo
	reviews  HeldReviews
}

// Imports reports finished downloads whose import keeps failing — a folder it can't
// write, a full disk — from the importers' own retry records. One already held in Review
// is left to its review, so a movie stuck after five tries shows once, as the review.
func Imports(failures func() []library.FailureInfo, reviews HeldReviews) Provider {
	return importsProvider{failures, reviews}
}

func (importsProvider) Name() string { return "imports" }

func (p importsProvider) Collect(ctx context.Context, f *Frame) ([]Item, error) {
	if p.failures == nil {
		return nil, nil
	}
	list := p.failures()
	if len(list) == 0 {
		return nil, nil
	}
	held := map[string]bool{}
	if p.reviews != nil {
		rvs, err := p.reviews.ListReviews(ctx)
		if err != nil {
			return nil, err // without it a held download would show twice; keep the last answer
		}
		for _, rv := range rvs {
			held[strings.ToLower(rv.Hash)] = true
		}
	}
	var out []Item
	for _, fl := range list {
		h := strings.ToLower(fl.Hash)
		if h == "" || fl.Attempts < importWarnAfter || held[h] || !f.InQueue(h) {
			continue
		}
		level := LevelWarning
		if fl.Attempts >= importErrorAfter && !fl.Since.IsZero() && f.Now.Sub(fl.Since) >= importErrorFor {
			level = LevelError
		}
		name := fl.Name
		if a, ok := f.Acq[h]; ok && a.Title != "" {
			name = a.Title
		}
		it := Item{Key: KindImport + ":" + h, Kind: KindImport, Level: level, Name: name,
			Title:  "Importing " + name + " keeps failing (" + strconv.Itoa(fl.Attempts) + " tries)",
			Detail: clip(fl.LastErr, 200), Hash: h, Grabbed: f.grabbed(h)}
		if !fl.Since.IsZero() {
			it.Since = fl.Since.UnixMilli()
		}
		linkTo(&it, health.FixImportProblems)
		out = append(out, it)
	}
	return out, nil
}

// --- finished TV downloads in the wrong category ------------------------------------

type wrongCatProvider struct{ src func() []download.Item }

// WrongCategory reports finished TV downloads the series import sweep found in a category
// it never imports from, matching a show in the library: they'd wait forever otherwise.
func WrongCategory(src func() []download.Item) Provider { return wrongCatProvider{src} }

func (wrongCatProvider) Name() string { return "wrongcat" }

func (p wrongCatProvider) Collect(_ context.Context, f *Frame) ([]Item, error) {
	if p.src == nil {
		return nil, nil
	}
	var out []Item
	for _, it := range p.src() {
		h := strings.ToLower(it.Hash)
		if h == "" || !f.InQueue(h) {
			continue
		}
		cat := it.Category
		if cat == "" {
			cat = "no category"
		} else {
			cat = "“" + cat + "”"
		}
		x := Item{Key: KindWrongCat + ":" + h, Kind: KindWrongCat, Level: LevelWarning, Name: it.Name, Hash: h, Grabbed: f.grabbed(h),
			Title: it.Name + " finished in " + cat,
			Detail: "Arrmada only imports TV from “" + download.CategoryTV + "”: set the category in qBittorrent, " +
				"or grab it again through Arrmada"}
		linkTo(&x, health.FixWrongCategory)
		out = append(out, x)
	}
	return out, nil
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
