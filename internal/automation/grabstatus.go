package automation

// The life of a grab row. Only grabStatusGrabbed counts as pending: stall detection, the
// pending-grab guard and requester progress all read status = 'grabbed', so every other
// status is out of their way by construction.
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
