package convert

import (
	"context"
	"sync/atomic"
	"testing"
)

// The settings page's "can pausing work" flag follows Insights' monitoring switch, not just
// whether Insights exists: with monitoring off nobody polls Plex, so the pause never fires.
func TestSettingsPlexWatchingKnownFollowsCallback(t *testing.T) {
	s := newTestService(t)
	ctx := context.Background()
	if s.GetSettings(ctx).PlexWatching {
		t.Fatal("no Insights wired: pausing can't work")
	}

	var monitoring, watching atomic.Bool
	s.SetWatching(watching.Load, monitoring.Load)
	if s.GetSettings(ctx).PlexWatching {
		t.Error("monitoring off: the hint must say pausing does nothing")
	}
	monitoring.Store(true)
	if !s.GetSettings(ctx).PlexWatching {
		t.Error("monitoring on: pausing works")
	}

	watching.Store(true)
	if !s.isWatching() {
		t.Error("isWatching should ask the watching callback")
	}

	s.SetWatching(nil, nil)
	if s.GetSettings(ctx).PlexWatching || s.isWatching() {
		t.Error("cleared: neither known nor watching")
	}
}
