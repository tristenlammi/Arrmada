package automation

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/tristenlammi/arrmada/internal/settings"
	"github.com/tristenlammi/arrmada/internal/store"
)

// A download that leaves the client is failed over (stallfailover.go), but that says
// nothing about the release: it was removed by hand or by another tool, or the client lost
// its state (a qBittorrent container recreated without its data drops every torrent at
// once). Blocklisting it the first time blocked good releases — hundreds at once when one
// update failed over months of stale grab rows. So the first disappearance only fails the
// grab (stamping grabs.vanished_at, migration 0309) and searches for another. The same
// release grabbed again and gone again is a pattern — someone keeps removing it, or the
// client won't keep it — and that one is blocklisted, which ends the grab-and-vanish round.

// vanishedBlockReason is the blocklist reason every fail-over of a vanished download wrote
// from ACQ-05 until the first disappearance stopped being blocklisted. Earlier builds wrote
// "stalled after N min" for a vanished and a genuinely stalled download alike; those rows
// can't be told apart, so they are left alone.
const vanishedBlockReason = "disappeared from the download client"

// vanishedAgainBlockReason is what a release that disappeared, was grabbed again and
// disappeared again is blocklisted with. Distinct from vanishedBlockReason, so the
// one-time clean-up below never touches it.
const vanishedAgainBlockReason = "disappeared from the download client again"

// closeVanished closes grab g and every duplicate folded onto it as failed, stamped as
// vanished. Closing them all at once is what lets a later pending row of the same release
// be read as a new grab of it (vanishedBefore), never a stale copy of this one.
func (c *Coordinator) closeVanished(ctx context.Context, g grab) {
	ids := g.ids()
	defer c.grabsChanged()
	args := make([]any, 0, 2+len(ids))
	args = append(args, grabStatusFailed, c.clock().Unix())
	for _, id := range ids {
		args = append(args, id)
	}
	marks := strings.TrimSuffix(strings.Repeat("?, ", len(ids)), ", ")
	if _, err := c.db.ExecContext(ctx, `UPDATE grabs SET status = ?, vanished_at = ? WHERE id IN (`+marks+`)`, args...); err != nil {
		c.log.Warn("automation: couldn't record the grab's status", "status", grabStatusFailed, "err", err)
	}
}

// vanishedBefore returns the ids of the closed rows for the same download of the same title
// as g (twinOf) that were failed over because their torrent left the client. g's own rows
// are still pending, so any it returns is an earlier grab of this release.
func (c *Coordinator) vanishedBefore(ctx context.Context, g grab) (map[int64]bool, error) {
	rows, err := c.grabsWhere(ctx, `vanished_at > 0 AND media_type = ? AND movie_id = ? AND version_id = ?`,
		g.MediaType, g.MovieID, g.VersionID)
	if err != nil {
		return nil, err
	}
	out := map[int64]bool{}
	for _, r := range rows {
		if twinOf(g, r) {
			out[r.ID] = true
		}
	}
	return out, nil
}

// vanishedPurgeKey marks the one-time removal of vanished-download blocklist rows as done
// (a setting).
const vanishedPurgeKey = "repair:vanished_blocklist_v1"

// PurgeVanishedBlocks removes, once, the blocklist rows written because a download left
// the client. Right after an update, the old stall check failed over hundreds of stale grab
// rows whose torrents had long gone — seeded out, imported, removed — writing a row each
// (nine for one complete pack), and so blocking releases that had done nothing wrong. Only
// rows with exactly vanishedBlockReason go: stalled, client-error, rejected-in-review,
// fake/malware, wrong-title, manual and every other reason stay, as do the rows for a
// release that disappeared again. A settings key stops it running twice; it is saved after
// the delete, so a failure in between means the next boot looks again and, since nothing
// writes that reason any more, finds nothing new.
func PurgeVanishedBlocks(ctx context.Context, db store.Pool, set *settings.Service, log *slog.Logger) (int, error) {
	if _, done := set.Lookup(vanishedPurgeKey); done {
		return 0, nil
	}
	var n int64
	var releases int
	err := store.WithTx(ctx, db, func(tx *store.Transaction) error {
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(DISTINCT norm_title) FROM blocklist WHERE reason = ?`,
			vanishedBlockReason).Scan(&releases); err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, `DELETE FROM blocklist WHERE reason = ?`, vanishedBlockReason)
		if err != nil {
			return err
		}
		n, err = res.RowsAffected()
		return err
	})
	if err != nil {
		return 0, err
	}
	if err := set.Set(ctx, vanishedPurgeKey, "1"); err != nil {
		return int(n), fmt.Errorf("record the clean-up as done: %w", err)
	}
	if log != nil {
		log.Info("automation: removed blocklist entries written only because a download left the client",
			"rows", n, "releases", releases)
	}
	return int(n), nil
}
