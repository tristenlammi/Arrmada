package automation

// The Needs-you feed (internal/attention) is told when the set of pending reviews
// changes, so its badges catch up in seconds instead of at its next tick. A direct call
// rather than a bus subscription: the bus may drop events, and the feed's own 30-second
// refresh is the backstop for a missed kick either way.

// SetAttentionKick installs the Needs-you feed's refresh request (attention.Service.Kick).
func (c *Coordinator) SetAttentionKick(fn func()) {
	if fn == nil {
		c.attentionKick.Store(nil)
		return
	}
	c.attentionKick.Store(&fn)
}

// reviewsChanged tells the Needs-you feed a review was added or settled.
func (c *Coordinator) reviewsChanged() {
	if fn := c.attentionKick.Load(); fn != nil {
		(*fn)()
	}
}
