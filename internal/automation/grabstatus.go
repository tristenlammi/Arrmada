package automation

// The life of a grab row. Only grabStatusGrabbed counts as in flight: stall detection and
// requester progress read status = 'grabbed', so every other status is out of their way by
// construction. The per-title "don't grab this release again" guards read 'removed' as
// well — see pendingTitleWhere.
const (
	grabStatusGrabbed  = "grabbed"  // handed to the download client, not imported yet
	grabStatusImported = "imported" // its file is in the library (may still be seeding)
	grabStatusFailed   = "failed"   // stalled, blocklisted or thrown away as junk
	// grabStatusRemoved: the user removed the torrent from the client themselves. Not a
	// failure — nothing is blocklisted and nothing re-grabs it on their behalf.
	grabStatusRemoved = "removed"
	// grabStatusCancelled: the media was deleted and its torrent removed with it.
	grabStatusCancelled = "cancelled"
	// grabStatusOrphaned: the media was deleted but its torrent was left in the client.
	grabStatusOrphaned = "orphaned"
)

// pendingTitleWhere is the grab-row filter behind the per-title re-grab guards (movies,
// series, books, music). A 'grabbed' row holds its release for a day, so a grab stuck
// there forever can't block that release for good. A 'removed' row holds it for 30: the
// user took that release out of the client on purpose, and the title is usually still
// monitored and missing, so without this every search and RSS sweep would pick the same
// top-ranked release and grab it straight back. The window runs from grabbed_at (the only
// timestamp a grab has), hence the generous length.
const pendingTitleWhere = `((status = 'grabbed' AND grabbed_at > datetime('now', '-1 day'))
		   OR (status = 'removed' AND grabbed_at > datetime('now', '-30 days')))`
