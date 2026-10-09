package httpapi

import (
	"sync"
	"time"
)

// loginLimiter throttles authentication. Cookie auth with bcrypt is the only cost
// otherwise, so an internet-exposed login is an unlimited online brute-force /
// credential-stuffing target.
//
// It keeps two kinds of count:
//   - attempts (allow), for setup and the Plex PIN start, where every call costs
//     something whether it "succeeds" or not;
//   - failures (fail / blocked / delayed / reset), for password sign-in. Only a
//     wrong password counts, and a right one clears the slate, so signing in and out
//     all day never uses up anyone's budget. Counting every attempt used to let a
//     stranger keep the owner's username locked out simply by trying it.
type loginLimiter struct {
	mu      sync.Mutex
	hits    map[string][]int64     // allow keys → attempt unix-nanos within the window
	fails   map[string][]loginFail // failure keys → recent failures, oldest first
	max     int
	window  time.Duration
	lastGC  int64
	nowNano func() int64 // injectable for tests
}

// loginFail is one wrong password. tag is the username it was for, so a success can
// clear exactly that user's failures from an address's record.
type loginFail struct {
	at  int64
	tag string
}

const (
	// failMemory is how long a failure is remembered: the username back-off and the
	// security alert look back an hour.
	failMemory = time.Hour
	// maxFailsKept bounds one key's record; the back-off is long capped by then.
	maxFailsKept = 64

	// The per-username back-off: free tries, then a pause that doubles from
	// userDelayBase after each further failure, capped at userDelayCap.
	userFreeFails = 5
	userDelayBase = 30 * time.Second
	userDelayCap  = 15 * time.Minute
)

func newLoginLimiter(max int, window time.Duration) *loginLimiter {
	return &loginLimiter{
		hits:    map[string][]int64{},
		fails:   map[string][]loginFail{},
		max:     max,
		window:  window,
		nowNano: func() int64 { return time.Now().UnixNano() },
	}
}

// gc drops idle keys so a flood of distinct addresses or usernames can't grow the
// maps forever. Called with mu held.
func (l *loginLimiter) gc(now int64) {
	if now-l.lastGC <= l.window.Nanoseconds() {
		return
	}
	hitCut := now - l.window.Nanoseconds()
	for k, ts := range l.hits {
		if len(ts) == 0 || ts[len(ts)-1] < hitCut {
			delete(l.hits, k)
		}
	}
	failCut := now - l.failHorizon().Nanoseconds()
	for k, fs := range l.fails {
		if len(fs) == 0 || fs[len(fs)-1].at < failCut {
			delete(l.fails, k)
		}
	}
	l.lastGC = now
}

// failHorizon is how far back failures matter: the longer of the hour and the
// per-address window.
func (l *loginLimiter) failHorizon() time.Duration {
	if l.window > failMemory {
		return l.window
	}
	return failMemory
}

// allow records an attempt for key and reports whether it's permitted. When
// denied, retryAfter is how long until the oldest in-window attempt expires.
func (l *loginLimiter) allow(key string) (ok bool, retryAfter time.Duration) {
	now := l.nowNano()
	cutoff := now - l.window.Nanoseconds()
	l.mu.Lock()
	defer l.mu.Unlock()
	l.gc(now)

	kept := l.hits[key][:0]
	for _, t := range l.hits[key] {
		if t >= cutoff {
			kept = append(kept, t)
		}
	}
	if len(kept) >= l.max {
		l.hits[key] = kept
		return false, time.Duration(kept[0] + l.window.Nanoseconds() - now)
	}
	l.hits[key] = append(kept, now)
	return true, 0
}

// recent returns key's failures since cutoff. Called with mu held.
func (l *loginLimiter) recent(key string, cutoff int64) []loginFail {
	fs := l.fails[key]
	i := 0
	for i < len(fs) && fs[i].at < cutoff {
		i++
	}
	return fs[i:]
}

// blocked reports whether key has max or more failures within the window, and if so
// how long until the oldest of them ages out. It records nothing.
func (l *loginLimiter) blocked(key string) (bool, time.Duration) {
	now := l.nowNano()
	l.mu.Lock()
	defer l.mu.Unlock()
	fs := l.recent(key, now-l.window.Nanoseconds())
	if len(fs) < l.max {
		return false, 0
	}
	oldest := fs[len(fs)-l.max]
	return true, time.Duration(oldest.at + l.window.Nanoseconds() - now)
}

// delayed is the per-username rule: after userFreeFails failures within the hour,
// each further try waits 30s, 1m, 2m, 4m … (capped at 15m) from the last failure.
// A slow guesser gets a handful of tries an hour; the owner, who knows the password,
// waits at most one pause.
func (l *loginLimiter) delayed(key string) (bool, time.Duration) {
	now := l.nowNano()
	l.mu.Lock()
	defer l.mu.Unlock()
	fs := l.recent(key, now-failMemory.Nanoseconds())
	if len(fs) < userFreeFails {
		return false, 0
	}
	wait := userDelayBase
	for i := userFreeFails; i < len(fs) && wait < userDelayCap; i++ {
		wait *= 2
	}
	if wait > userDelayCap {
		wait = userDelayCap
	}
	until := fs[len(fs)-1].at + wait.Nanoseconds()
	if now >= until {
		return false, 0
	}
	return true, time.Duration(until - now)
}

// fail records a wrong password against key (tag: the username it was for) and
// returns how many failures key now has within the hour.
func (l *loginLimiter) fail(key, tag string) int {
	now := l.nowNano()
	l.mu.Lock()
	defer l.mu.Unlock()
	l.gc(now)
	fs := append(l.recent(key, now-l.failHorizon().Nanoseconds()), loginFail{at: now, tag: tag})
	if len(fs) > maxFailsKept {
		fs = fs[len(fs)-maxFailsKept:]
	}
	// Copy so the slice doesn't pin an ever-growing backing array.
	l.fails[key] = append([]loginFail(nil), fs...)
	return len(l.recent(key, now-failMemory.Nanoseconds()))
}

// reset clears key's failures after a successful sign-in. With a tag, only the
// failures for that username go: someone who knows one account's password must not be
// able to wipe their address's record of guesses at everyone else's.
func (l *loginLimiter) reset(key, tag string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if tag == "" {
		delete(l.fails, key)
		return
	}
	var kept []loginFail
	for _, f := range l.fails[key] {
		if f.tag != tag {
			kept = append(kept, f)
		}
	}
	if len(kept) == 0 {
		delete(l.fails, key)
		return
	}
	l.fails[key] = kept
}
