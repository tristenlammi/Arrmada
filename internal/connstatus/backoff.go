package connstatus

import "time"

// ladder is how long background work leaves an integration alone after its nth failure
// in a row (index n-1). The first failure costs nothing: a public tracker that times out
// once in a while is still worth asking. From the second on, the pause grows, so an
// expired login or a dead Prowlarr stops being retried on every search of every sweep.
var ladder = []time.Duration{0, 5 * time.Minute, 15 * time.Minute, time.Hour, 3 * time.Hour}

// maxBackoff caps the ladder: a fixed integration is noticed within six hours even if
// nobody presses Test.
const maxBackoff = 6 * time.Hour

// maxRetryAfter caps a server's Retry-After. A tracker asking for a week off would
// otherwise hide itself for a week without anyone noticing.
const maxRetryAfter = 24 * time.Hour

// backoffFor is the pause after the nth consecutive failure: none for the first, then
// 5m, 15m, 1h, 3h, and 6h from then on.
func backoffFor(n int) time.Duration {
	if n <= 1 {
		return 0
	}
	if n-1 < len(ladder) {
		return ladder[n-1]
	}
	return maxBackoff
}

// pauseFor is the pause for the nth failure when the server also said how long to wait:
// the longer of the two wins, so a 429 with "Retry-After: 600" is honoured even on the
// first failure.
func pauseFor(n int, retryAfter time.Duration) time.Duration {
	d := backoffFor(n)
	if retryAfter > maxRetryAfter {
		retryAfter = maxRetryAfter
	}
	if retryAfter > d {
		d = retryAfter
	}
	return d
}
