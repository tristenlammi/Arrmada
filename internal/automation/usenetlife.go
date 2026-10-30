package automation

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/tristenlammi/arrmada/internal/download"
	"github.com/tristenlammi/arrmada/internal/indexer"
)

// A usenet job lives differently from a torrent, and the stall check and seed clean-up
// treat it so (docs/roadmap/USENET.md, decisions 3–5):
//
//   - It never stalls for want of peers. Its articles are on the provider or they aren't,
//     and when downloading makes no headway it's almost always the provider or the account
//     — which affects every job alike, so replacing one release with another fixes
//     nothing. No-progress is never a fail-over: the health check says so instead, once
//     SABnzbd has downloaded nothing for UsenetIdleAfter (UsenetIdle).
//   - After the download SABnzbd verifies, repairs and unpacks it. That holds the stall
//     clock: the files aren't ready, but nothing is stuck.
//   - It fails with a reason (sabfail.go). A bad release is blocklisted, its job removed,
//     and another searched for on the stall check that sees it; a problem where SABnzbd
//     writes is left for a person (Needs you), with nothing blocklisted.
//   - It never seeds. Once imported, the job is archived in SABnzbd and the folder it was
//     unpacked into deleted (ManageSeeding) — only inside the folders Arrmada may tidy.
//   - A job that leaves SABnzbd's history before it was imported (archived by hand, or by
//     another tool) is a vanished download, judged exactly as a vanished torrent is.

// UsenetIdleAfter is how long SABnzbd may have jobs waiting to download and download
// nothing before the health check says so.
const UsenetIdleAfter = 30 * time.Minute

// usenetWatch is this process's view of the usenet clients, under mu. Memory only: after a
// restart the idle clock starts again, and failures are reported again once.
type usenetWatch struct {
	mu sync.Mutex
	// reported is the failure message already logged for a grab, so a job left failed (a
	// full disk waiting for a person) is logged once, not every stall check.
	reported map[int64]string
	// idle is, per usenet client, since when its waiting jobs have moved nothing.
	idle map[int64]*usenetIdle
	// leftovers are, per usenet client, the finished jobs whose folders were left behind
	// since the last one Arrmada could tidy up.
	leftovers map[int64][]UsenetLeftover
}

type usenetIdle struct {
	since time.Time        // zero while something moves
	bytes map[string]int64 // each waiting job's downloaded bytes at the last read
	jobs  int
}

// judgeUsenetJob is the stall check for a grab whose usenet job is in its client. Only a
// failure is acted on; see the top of this file.
func (c *Coordinator) judgeUsenetJob(ctx context.Context, g grab, item download.Item, tick *stallTick, target func(ctx context.Context) (stallTarget, bool)) {
	switch item.Phase() {
	case download.PhaseUnpacking:
		c.holdStallClock(ctx, g.ID, item.Progress)
		return
	case "error":
	default:
		return // queued, downloading, paused or finished: never a fail-over
	}
	if item.Complete() {
		return
	}
	f := ClassifyUsenetFailure(item.FailMessage)
	c.reportUsenetFailure(g, item, f)
	if f.Environment() {
		// Another release would fail the same way. Hold the clock: the job stays failed in
		// SABnzbd until someone fixes the cause and retries it (Needs you says how).
		c.holdStallClock(ctx, g.ID, item.Progress)
		return
	}
	t, ok := target(ctx)
	if !ok {
		return
	}
	if !tick.take() {
		return
	}
	if c.failUsenet(ctx, g, item, f, t) && tick != nil {
		tick.failedOver++
	}
}

// reportUsenetFailure logs a failed usenet job once per grab and reason. A reason in no
// wording Arrmada knows is logged with SABnzbd's own text: it is treated as the release's
// fault, and the text is how a translated or new wording gets noticed.
func (c *Coordinator) reportUsenetFailure(g grab, item download.Item, f UsenetFailure) {
	c.usenet.mu.Lock()
	if c.usenet.reported == nil {
		c.usenet.reported = map[int64]string{}
	}
	seen := c.usenet.reported[g.ID] == item.FailMessage
	c.usenet.reported[g.ID] = item.FailMessage
	c.usenet.mu.Unlock()
	if seen {
		return
	}
	switch {
	case f.Environment() && f.Fetch:
		c.log.Warn("automation: SABnzbd couldn't fetch a release's NZB from the indexer — left for a person to fix, nothing blocklisted",
			"release", g.Title, "reason", f.Reason, "sabnzbd_says", item.FailMessage)
	case f.Environment():
		c.log.Warn("automation: SABnzbd couldn't save a download — left for a person to fix, nothing blocklisted",
			"release", g.Title, "reason", f.Reason, "sabnzbd_says", item.FailMessage)
	case !f.Known:
		c.log.Warn("automation: SABnzbd failed a download for a reason Arrmada doesn't recognise — treating it as a bad release. If SABnzbd isn't set to English its reasons are translated; English works best (SABnzbd → Config → General)",
			"release", g.Title, "sabnzbd_says", item.FailMessage)
	}
}

// failUsenet blocklists a release SABnzbd couldn't complete, removes its job (and its
// partial files), closes the grab and searches for another. Unlike a stalled torrent there
// is no copy to protect: the job has failed and won't finish, so it goes whether or not
// anything replaces it. It reports whether the grab was closed.
func (c *Coordinator) failUsenet(ctx context.Context, g grab, item download.Item, f UsenetFailure, t stallTarget) bool {
	reason := "usenet download failed: " + f.Reason
	if err := t.block(ctx, reason); err != nil {
		// Without the blocklist row the next sweep could grab it straight back.
		c.log.Warn("automation: couldn't blocklist a failed usenet download — leaving it for the next check", "release", g.Title, "err", err)
		return false
	}
	if err := c.removeStalled(ctx, item.Hash); err != nil {
		c.log.Warn("automation: couldn't remove the failed usenet job", "release", g.Title, "err", err)
	}
	c.closeGrab(ctx, g, grabStatusFailed)
	c.forgetUsenetFailure(g)

	exclude := map[string]bool{normTitle(g.Title): true}
	for _, d := range g.Dups {
		exclude[normTitle(d.Title)] = true
	}
	repl, err := c.replaceRecorded(ctx, t, exclude)
	outage := indexer.IsOutage(err) || errors.Is(err, errSearchUnavailable)
	if err != nil && !outage {
		c.log.Warn("automation: replacement search failed", "kind", t.kind, "title", t.name, "err", err)
	}
	what := "nothing else found yet"
	switch {
	case repl != "":
		what = "replaced by " + repl
	case t.unsearched != "":
		what = t.unsearched + " — not replaced"
	case outage:
		what = "indexers unavailable, searching again later"
	}
	c.log.Info("automation: usenet download failed, blocklisted",
		"kind", t.kind, "title", t.name, "release", g.Title, "reason", f.Reason, "replacement", repl)
	t.event(ctx, fmt.Sprintf("%s failed in SABnzbd (%s) — blocklisted, %s", g.Title, f.Reason, what))
	c.publishStall(map[string]any{
		"kind": t.kind, "id": t.id, "title": t.name, "release": g.Title,
		"usenet": true, "failed": f.Reason, "replaced": repl != "", "replacement": repl,
	})
	return true
}

// pruneUsenetReports forgets the reported failures of grabs no longer pending.
func (c *Coordinator) pruneUsenetReports(pending []grab) {
	keep := make(map[int64]bool, len(pending))
	for _, g := range pending {
		keep[g.ID] = true
	}
	c.usenet.mu.Lock()
	defer c.usenet.mu.Unlock()
	for id := range c.usenet.reported {
		if !keep[id] {
			delete(c.usenet.reported, id)
		}
	}
}

func (c *Coordinator) forgetUsenetFailure(g grab) {
	c.usenet.mu.Lock()
	defer c.usenet.mu.Unlock()
	for _, id := range g.ids() {
		delete(c.usenet.reported, id)
	}
}

// noteUsenetQueue updates each usenet client's idle clock from a queue read: running while
// it has jobs waiting to download and none of them moved, stopped as soon as one does (or
// none is waiting). A job held back on purpose — paused, or waiting out SABnzbd's
// propagation delay — isn't waiting to download, nor is one whose NZB SABnzbd is still
// fetching (a link handed over when the indexer blocked Arrmada: that's the indexer, not the
// news server). Called with every reconcile's snapshot.
func (c *Coordinator) noteUsenetQueue(items []download.Item) {
	now := c.clock()
	waiting := map[int64]map[string]int64{}
	moving := map[int64]bool{}
	for _, it := range items {
		if it.Transport != download.TransportUsenet || it.RawState == "Propagating" || it.RawState == "Grabbing" {
			continue
		}
		switch it.Phase() {
		case "downloading", "queued":
		default:
			continue
		}
		if waiting[it.ClientID] == nil {
			waiting[it.ClientID] = map[string]int64{}
		}
		waiting[it.ClientID][it.Hash] = it.DownloadedBytes
		if it.DownSpeed > 0 {
			moving[it.ClientID] = true
		}
	}
	w := &c.usenet
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.idle == nil {
		w.idle = map[int64]*usenetIdle{}
	}
	for id := range w.idle {
		if waiting[id] == nil {
			delete(w.idle, id)
		}
	}
	for id, jobs := range waiting {
		st := w.idle[id]
		if st == nil {
			st = &usenetIdle{}
			w.idle[id] = st
		}
		moved := moving[id]
		for key, got := range jobs {
			if before, ok := st.bytes[key]; ok && got > before {
				moved = true
			}
		}
		st.bytes, st.jobs = jobs, len(jobs)
		switch {
		case moved:
			st.since = time.Time{}
		case st.since.IsZero():
			st.since = now
		}
	}
}

// UsenetIdleClient is a usenet client that has had jobs waiting and downloaded nothing.
type UsenetIdleClient struct {
	ClientID int64
	Since    time.Time
	Jobs     int
}

// UsenetIdle lists the usenet clients that have downloaded nothing for UsenetIdleAfter or
// longer while jobs were waiting, for the health check.
func (c *Coordinator) UsenetIdle() []UsenetIdleClient {
	now := c.clock()
	c.usenet.mu.Lock()
	defer c.usenet.mu.Unlock()
	var out []UsenetIdleClient
	for id, st := range c.usenet.idle {
		if !st.since.IsZero() && now.Sub(st.since) >= UsenetIdleAfter {
			out = append(out, UsenetIdleClient{ClientID: id, Since: st.since, Jobs: st.jobs})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ClientID < out[j].ClientID })
	return out
}

// UsenetLeftover is a finished job whose folder Arrmada left in place after archiving it.
type UsenetLeftover struct {
	Name   string
	Detail string // why, as the client said it (the folder and the guard it failed)
	At     time.Time
}

// maxLeftoversKept bounds what's remembered per client; the count is what matters.
const maxLeftoversKept = 20

// noteUsenetLeftover remembers a finished job whose folder was left in place.
func (c *Coordinator) noteUsenetLeftover(clientID int64, name string, err error) {
	c.log.Warn("seeding: archived a finished usenet job but left its folder in place", "release", name, "err", err)
	c.usenet.mu.Lock()
	defer c.usenet.mu.Unlock()
	if c.usenet.leftovers == nil {
		c.usenet.leftovers = map[int64][]UsenetLeftover{}
	}
	l := append(c.usenet.leftovers[clientID], UsenetLeftover{Name: name, Detail: err.Error(), At: c.clock()})
	if len(l) > maxLeftoversKept {
		l = l[len(l)-maxLeftoversKept:]
	}
	c.usenet.leftovers[clientID] = l
}

// clearUsenetLeftovers forgets a client's leftovers once a finished job's folder was
// tidied up: whatever kept the others in place has been fixed.
func (c *Coordinator) clearUsenetLeftovers(clientID int64) {
	c.usenet.mu.Lock()
	defer c.usenet.mu.Unlock()
	delete(c.usenet.leftovers, clientID)
}

// UsenetLeftoverReport is a usenet client whose finished jobs' folders keep being left
// behind: Count of them since the last one Arrmada could tidy, the latest as Example.
type UsenetLeftoverReport struct {
	ClientID int64
	Count    int
	Example  UsenetLeftover
}

// LeftoverRepeatAfter is how many folders in a row must be left behind before it's a
// problem worth a person's time: one can be a job moved by hand, two is the setup.
const LeftoverRepeatAfter = 2

// UsenetLeftovers lists the usenet clients whose finished jobs' folders have been left in
// place LeftoverRepeatAfter times or more in a row, for the health check.
func (c *Coordinator) UsenetLeftovers() []UsenetLeftoverReport {
	c.usenet.mu.Lock()
	defer c.usenet.mu.Unlock()
	var out []UsenetLeftoverReport
	for id, l := range c.usenet.leftovers {
		if len(l) >= LeftoverRepeatAfter {
			out = append(out, UsenetLeftoverReport{ClientID: id, Count: len(l), Example: l[len(l)-1]})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ClientID < out[j].ClientID })
	return out
}

// cleanUpUsenetJob is seed clean-up for a finished usenet job: archive it in SABnzbd and,
// once imported (deleteData), delete the folder it was unpacked into. A dismissed one's
// files are kept: Arrmada never imported them.
func (c *Coordinator) cleanUpUsenetJob(ctx context.Context, it download.Item, g grab, deleteData bool) {
	kept, err := c.removeUsenetJob(ctx, it, deleteData)
	if err != nil {
		c.log.Warn("seeding: couldn't clear a finished usenet job from SABnzbd", "release", g.Title, "err", err)
		return
	}
	c.setGrabStatus(ctx, g.ID, grabStatusSeeded) // out of the client: done
	c.closeStaleTwins(ctx, g, grabStatusSeeded)
	what := "its folder deleted"
	switch {
	case !deleteData:
		what = "its files kept in the downloads folder"
	case kept:
		what = "its folder left in place (outside where Arrmada tidies up)"
	}
	c.log.Info("seeding: cleared a usenet download from SABnzbd", "release", g.Title, "delete_data", deleteData, "folder_kept", kept)
	c.addMediaEvent(ctx, &g, "removed", g.Title+" — usenet download cleared from SABnzbd after import, "+what)
}

// clearFailedUsenetJob removes a failed usenet job whose grab was settled as imported
// anyway (what it was for landed another way). Only its partial files go with it.
func (c *Coordinator) clearFailedUsenetJob(ctx context.Context, it download.Item, g grab) {
	if err := c.dropTorrent(ctx, it.Hash, true); err != nil {
		c.log.Warn("seeding: couldn't clear a failed usenet job from SABnzbd", "release", g.Title, "err", err)
		return
	}
	c.setGrabStatus(ctx, g.ID, grabStatusSeeded)
	c.log.Info("seeding: cleared a failed usenet job whose title is already in the library", "release", g.Title)
}

// removeUsenetJob takes a finished (or failed) usenet job out of its client for seed
// clean-up. A finished job's folder goes with it when deleteData — but only from where
// Arrmada may tidy up: a folder outside those is left (and noted), and the job archived
// anyway, so it doesn't come round again every pass. kept reports a folder left behind.
func (c *Coordinator) removeUsenetJob(ctx context.Context, it download.Item, deleteData bool) (kept bool, err error) {
	err = c.dropTorrent(ctx, it.Hash, deleteData)
	switch {
	case err == nil:
		if deleteData && it.Complete() {
			c.clearUsenetLeftovers(it.ClientID)
		}
		return false, nil
	case errors.Is(err, download.ErrLeftoverNotDeleted):
		// Archived already; only the folder stayed.
		c.noteUsenetLeftover(it.ClientID, it.Name, err)
		return true, nil
	case errors.Is(err, download.ErrLeftoverOutside):
		c.noteUsenetLeftover(it.ClientID, it.Name, err)
		return true, c.dropTorrent(ctx, it.Hash, false)
	}
	return false, err
}
