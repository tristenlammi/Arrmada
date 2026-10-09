package automation

// The life of a grab row, and the one place its status words are spelled out.
//
//	grabbed ──► imported ──► seeded          (the normal path)
//	   │  └───► held ──► imported | dismissed | failed   (Review)
//	   └──────► failed | removed | cancelled | orphaned
//	dismissed ──► seeded                     (removed at its goal, files kept)
//
// Every query that filters grabs by status goes through the sets below rather than typing
// the words again: the table drives removal decisions, and a status missed in one hand-typed
// IN-list either grabs a held release a second time or deletes it at seed time.
// store_status_test.go fails the build on a raw status literal anywhere else.
const (
	grabStatusGrabbed = "grabbed" // handed to the download client, not imported yet
	// grabStatusHeld: finished, but held in Review for a person to decide. Never stall-checked
	// (it's complete) and never seed-cleaned (nothing has been imported from it yet).
	grabStatusHeld     = "held"
	grabStatusImported = "imported" // its file is in the library (may still be seeding)
	// grabStatusDismissed: its review was dismissed — the user is handling the files
	// themselves. Seed cleanup removes the torrent at its goal but keeps the files, since
	// Arrmada never imported them and they may be the only copy.
	grabStatusDismissed = "dismissed"
	grabStatusFailed    = "failed" // stalled, blocklisted, rejected in review or thrown away as junk
	// grabStatusRemoved: the user removed the torrent from the client themselves. Not a
	// failure — nothing is blocklisted and nothing re-grabs it on their behalf.
	grabStatusRemoved = "removed"
	grabStatusSeeded  = "seeded" // seed goal met and the torrent removed from the client
	// grabStatusCancelled: the media was deleted and its torrent removed with it.
	grabStatusCancelled = "cancelled"
	// grabStatusOrphaned: the media was deleted but its torrent was left in the client.
	grabStatusOrphaned = "orphaned"
)

// The status sets, as SQL fragments to AND into a WHERE clause.
const (
	// stallWatchWhere: grabs the stall check judges — still downloading as far as we know.
	stallWatchWhere = `status = '` + grabStatusGrabbed + `'`
	// inFlightWhere: grabs not closed out yet — downloading, or finished and waiting in
	// Review. An import or a review decision flips these.
	inFlightWhere = `status IN ('` + grabStatusGrabbed + `', '` + grabStatusHeld + `')`
	// seedCleanupWhere: grabs whose torrent may be removed once it meets its seed goal.
	seedCleanupWhere = `status IN ('` + grabStatusImported + `', '` + grabStatusDismissed + `')`
	// liveWhere: every grab whose torrent may still be in the client under our rule.
	liveWhere = `status IN ('` + grabStatusGrabbed + `', '` + grabStatusHeld + `', '` +
		grabStatusImported + `', '` + grabStatusDismissed + `')`
)

// pendingTitleWhere is the grab-row filter behind the per-title re-grab guards (movies,
// series, books, music).
//   - A 'grabbed' row holds its release for a day, so a grab stuck there forever can't
//     block that release for good.
//   - A 'held' row holds it for as long as the review is pending: the download is already
//     here, waiting on a decision, and fetching it again decides nothing.
//   - A 'removed' or 'dismissed' row holds it for 30: the user took that release out of the
//     client, or said they'd handle it themselves, on purpose. The title is usually still
//     monitored and missing, so without this every search and RSS sweep would pick the same
//     top-ranked release and grab it straight back. The window runs from grabbed_at (the
//     only timestamp a grab has), hence the generous length.
const pendingTitleWhere = `((status = '` + grabStatusGrabbed + `' AND grabbed_at > datetime('now', '-1 day'))
		   OR status = '` + grabStatusHeld + `'
		   OR (status IN ('` + grabStatusRemoved + `', '` + grabStatusDismissed + `') AND grabbed_at > datetime('now', '-30 days')))`
