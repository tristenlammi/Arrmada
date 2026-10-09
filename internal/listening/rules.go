// Package listening keeps every user's place in every audiobook, the play sessions
// that move it, and a privacy-preserving listening log. It knows nothing about any
// particular app: the Audiobookshelf-compatible server (and any other protocol later)
// translates requests into Reports and hands them here.
//
// The sync rules exist because Audiobookshelf loses people's place:
//   - it forgets play sessions on restart, then drops every progress report for them;
//   - the last report always wins, so one stray "5 seconds" replaced 3¾ hours;
//   - an older report (offline listening, a second device) can replace a newer place.
//
// Here: sessions are durable; a big jump backwards is held until the same session keeps
// playing from the new spot; an offline report only applies if it's newer than the
// saved place; and earlier places are kept so a user can put theirs back.
package listening

import "math"

// Kind says where a report came from, which decides how much it is trusted.
type Kind int

const (
	// Live is a report from a playing session, stamped with server time.
	Live Kind = iota
	// Offline is a session played without a connection and uploaded later, stamped
	// with the phone's clock.
	Offline
	// Manual is an explicit user action — mark finished, set a position, restore an
	// earlier place. Always applied.
	Manual
	// Reported is an app setting a position outside a play session (Audiobookshelf's
	// progress PATCH). Apps send these on their own schedule, sometimes from a stale
	// copy, so they get the same guards as everything else: an older one is ignored and
	// a big jump back is held until a play session carries on from it.
	Reported
)

const (
	// rewindThreshold is how far back (seconds) a live report may jump before it has to
	// prove itself. Skipping back a chapter's worth to re-hear something is normal;
	// anything bigger is either a deliberate restart or a glitch.
	rewindThreshold = 120.0
	// rewindProof is how long (seconds of listening) the same session must carry on
	// from the new spot before a big jump back becomes the saved place. Long enough to
	// outlast a glitchy report, short enough that a real restart sticks quickly; the
	// person can also confirm a held jump themselves in Arrmada.
	rewindProof = 30.0
	// finishedTail: within this many seconds of the end counts as finished.
	finishedTail = 5.0
	// speedAllowance bounds how far one second of listening can move the position —
	// generous for fast playback speeds.
	speedAllowance = 4.0
	// continuitySlack absorbs report timing jitter when checking a held jump is being
	// listened through continuously.
	continuitySlack = 30.0
)

// Progress is a user's place in one item.
type Progress struct {
	UserID     int64   `json:"-"`
	ItemKey    string  `json:"item_key"`
	Position   float64 `json:"position"`
	Duration   float64 `json:"duration"`
	Finished   bool    `json:"finished"`
	FinishedAt int64   `json:"finished_at,omitempty"`
	UpdatedAt  int64   `json:"updated_at"`
	Device     string  `json:"device,omitempty"`
	SessionID  string  `json:"-"`
	Hidden     bool    `json:"hidden,omitempty"`

	// A big jump backwards waiting for proof.
	PendingPosition *float64 `json:"pending_position,omitempty"`
	PendingSession  string   `json:"-"`
	PendingListened float64  `json:"-"`
	PendingAt       int64    `json:"-"`
}

// Fraction is how far through the item the position is (0..1).
func (p Progress) Fraction() float64 {
	if p.Finished {
		return 1
	}
	if p.Duration <= 0 {
		return 0
	}
	return math.Min(1, math.Max(0, p.Position/p.Duration))
}

// Report is one position update from a listening app.
type Report struct {
	Kind      Kind
	Position  float64
	Duration  float64
	Listened  float64 // seconds actually listened since the last report (Live) or in the session (Offline)
	At        int64   // unix ms of the listening this report describes
	SessionID string
	Device    string
	Finished  *bool // explicit finished flag (Manual; for Reported only "not finished" counts)
}

// Decision is what applying a report does to a saved place.
type Decision struct {
	Progress Progress
	Changed  bool   // the saved position (or finished flag) moved
	Dirty    bool   // something needs writing (Changed, or the pending state moved)
	Reason   string // why: start, forward, back, rewind, offline, manual, set, held, older, unproven, no-position
}

// Decide applies a report to the current saved place (nil when there is none yet).
// It is pure: the store does the reading and writing around it.
func Decide(cur *Progress, r Report) Decision {
	dur := r.Duration
	if dur <= 0 && cur != nil {
		dur = cur.Duration
	}
	pos := clamp(r.Position, dur)

	if cur == nil {
		p := Progress{ItemKey: "", Position: pos, Duration: dur, UpdatedAt: r.At, Device: r.Device, SessionID: r.SessionID}
		setFinished(&p, r, pos, dur)
		return Decision{Progress: p, Changed: true, Dirty: true, Reason: "start"}
	}

	p := *cur
	if r.Duration > 0 {
		p.Duration = r.Duration
	}

	accept := func(reason string) Decision {
		back := cur.Position - pos
		p.Position = pos
		if r.At > p.UpdatedAt || r.Kind != Offline {
			p.UpdatedAt = maxInt64(r.At, p.UpdatedAt)
		}
		p.Device, p.SessionID = r.Device, r.SessionID
		clearPending(&p)
		if back > rewindThreshold && dur > 0 && pos < dur-finishedTail {
			p.Finished, p.FinishedAt = false, 0 // listening again from earlier
		}
		setFinished(&p, r, pos, dur)
		changed := p.Position != cur.Position || p.Finished != cur.Finished
		return Decision{Progress: p, Changed: changed, Dirty: true, Reason: reason}
	}

	manual := func() Decision {
		p.Position = pos
		p.UpdatedAt = maxInt64(r.At, cur.UpdatedAt)
		p.Device, p.SessionID = r.Device, r.SessionID
		clearPending(&p)
		p.Finished, p.FinishedAt = false, 0
		setFinished(&p, r, pos, dur)
		return Decision{Progress: p, Changed: true, Dirty: true, Reason: "manual"}
	}

	switch r.Kind {
	case Manual:
		return manual()

	case Offline:
		if r.At <= cur.UpdatedAt {
			// Listening that happened before the saved place was set — a phone uploading
			// old offline listening must not drag a newer place back.
			return Decision{Progress: *cur, Reason: "older"}
		}
		if cur.Position-pos > rewindThreshold && r.Listened < rewindProof {
			return Decision{Progress: *cur, Reason: "unproven"}
		}
		return accept("offline")

	case Reported:
		if r.At <= cur.UpdatedAt {
			// Describes a moment before the saved place was set: a stale copy.
			return Decision{Progress: *cur, Reason: "older"}
		}
		if r.Finished != nil && !*r.Finished && cur.Finished {
			// The app marking a finished book not finished: the person's own action, applied
			// at once (it keeps the saved place unless the app said where to pick up).
			return manual()
		}
		// Otherwise the flag says nothing new; the position alone decides.
		r.Finished = nil
		if cur.Position-pos > rewindThreshold {
			if cur.PendingPosition != nil && math.Abs(pos-*cur.PendingPosition) <= rewindThreshold {
				// A jump to about here is already held, perhaps with a play session proving
				// it right now (an app that also PATCHes as it plays, or "Listen again").
				// Leave that hold alone rather than restart its proof.
				return Decision{Progress: *cur, Reason: "held"}
			}
			// Held with no session: the first play session that carries on from here
			// takes it over and proves it, or the person confirms it in Arrmada.
			np := pos
			p.PendingPosition, p.PendingSession, p.PendingListened, p.PendingAt = &np, "", 0, r.At
			return Decision{Progress: p, Dirty: true, Reason: "held"}
		}
		return accept("set")

	default: // Live
		back := cur.Position - pos
		if back <= rewindThreshold {
			if back > 0 {
				return accept("back")
			}
			return accept("forward")
		}
		// A big jump backwards. Is this session already carrying on from a held jump?
		if cur.PendingPosition != nil && r.SessionID != "" {
			last := *cur.PendingPosition
			continuous := pos >= last-continuitySlack && pos <= last+r.Listened*speedAllowance+continuitySlack
			if continuous && cur.PendingSession == "" {
				// A jump an app set without playing (Reported). This session is playing on
				// from it, so it takes the hold over; the proof counts from here, since its
				// listening so far may have been somewhere else.
				np := pos
				p.PendingPosition, p.PendingSession, p.PendingListened = &np, r.SessionID, 0
				return Decision{Progress: p, Dirty: true, Reason: "held"}
			}
			if continuous && cur.PendingSession == r.SessionID {
				p.PendingListened = cur.PendingListened + math.Max(0, r.Listened)
				np := pos
				p.PendingPosition = &np
				if p.PendingListened >= rewindProof {
					return accept("rewind")
				}
				return Decision{Progress: p, Dirty: true, Reason: "held"}
			}
		}
		// Start holding it. The listening in this report happened before the jump, so it
		// doesn't count towards the proof.
		np := pos
		p.PendingPosition, p.PendingSession, p.PendingListened, p.PendingAt = &np, r.SessionID, 0, r.At
		return Decision{Progress: p, Dirty: true, Reason: "held"}
	}
}

func setFinished(p *Progress, r Report, pos, dur float64) {
	if r.Finished != nil {
		p.Finished = *r.Finished
		if p.Finished {
			if dur > 0 {
				p.Position = dur
			}
			if p.FinishedAt == 0 {
				p.FinishedAt = r.At
			}
		} else {
			p.FinishedAt = 0
		}
		return
	}
	if dur > 0 && pos >= dur-finishedTail && !p.Finished {
		p.Finished, p.FinishedAt = true, r.At
	}
}

func clearPending(p *Progress) {
	p.PendingPosition, p.PendingSession, p.PendingListened, p.PendingAt = nil, "", 0, 0
}

func clamp(pos, dur float64) float64 {
	if math.IsNaN(pos) || pos < 0 {
		return 0
	}
	if dur > 0 && pos > dur {
		return dur
	}
	return pos
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
