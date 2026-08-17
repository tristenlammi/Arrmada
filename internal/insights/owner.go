package insights

import (
	"context"
	"strconv"
	"time"

	"github.com/tristenlammi/arrmada/internal/plex"
)

// ownerPlaceholderID is what a Plex Media Server calls its own owner in /status/sessions:
// not an account id at all, just "whoever owns this box". Every other source of watch
// history — Tautulli, plex.tv — uses the owner's real account id, so recording sessions
// under this value files one person as two users, splitting their plays and watch time.
const ownerPlaceholderID = "1"

// keyOwnerID caches the resolved account id so the plex.tv round trip happens once per
// install rather than once per poll.
const keyOwnerID = "insights_owner_account_id"

// ownerLookupBackoff bounds retries when plex.tv can't be reached. Without it an offline
// plex.tv would be dialled every poll — every 5 seconds by default — forever.
const ownerLookupBackoff = 10 * time.Minute

// ownerID returns the owner's real Plex account id, resolving it from plex.tv on first use
// and caching it in settings. Returns "" when it can't be determined, which callers treat
// as "leave the id alone" — a wrong merge is far worse than a duplicate user row.
func (s *Service) ownerID(ctx context.Context) string {
	if id := s.settings.Get(ctx, keyOwnerID, ""); id != "" {
		return id
	}
	token := s.settings.Get(ctx, keyToken, "")
	if token == "" {
		return ""
	}
	if !s.ownerRetryDue() {
		return ""
	}
	acct, err := plex.GetAccount(ctx, s.clientID(ctx), token)
	if err != nil || acct.ID == 0 {
		s.log.Debug("insights: could not resolve the Plex owner account", "err", err)
		return ""
	}
	id := strconv.FormatInt(acct.ID, 10)
	if err := s.settings.Set(ctx, keyOwnerID, id); err != nil {
		s.log.Warn("insights: could not persist the Plex owner account id", "err", err)
	}
	s.log.Info("insights: resolved the Plex owner account", "id", id, "username", acct.Username)
	// Now that the real id is known, fold anything already filed under the placeholder
	// onto it. Migration 0073 handles the case it could infer from usernames; this covers
	// the rest, including a server that has only ever been live-polled.
	s.mergeOwnerSessions(ctx, id, acct.Username, acct.Thumb)
	return id
}

// ownerRetryDue rate-limits plex.tv lookups. Poller-goroutine only, like s.live.
func (s *Service) ownerRetryDue() bool {
	if !s.ownerLookupAt.IsZero() && time.Since(s.ownerLookupAt) < ownerLookupBackoff {
		return false
	}
	s.ownerLookupAt = time.Now()
	return true
}

// canonicalUserID maps the server's owner placeholder onto the real account id. Any other
// id, and any unresolved owner, passes through untouched.
func (s *Service) canonicalUserID(ctx context.Context, id string) string {
	if id != ownerPlaceholderID {
		return id
	}
	if real := s.ownerID(ctx); real != "" {
		return real
	}
	return id
}

// mergeOwnerSessions repoints sessions recorded under the placeholder onto the real account
// row and retires the placeholder. Idempotent: after the first run there is nothing left
// under the placeholder, so subsequent calls are no-ops.
func (s *Service) mergeOwnerSessions(ctx context.Context, realID, username, thumb string) {
	if realID == "" || realID == ownerPlaceholderID {
		return
	}
	// Make sure the destination row exists before pointing sessions at it, or the merge
	// would leave them referencing a user the Users tab can't join to.
	if err := s.repo.upsertUser(ctx, realID, username, thumb, 0); err != nil {
		s.log.Warn("insights: could not create the owner's user row", "err", err)
		return
	}
	n, err := s.repo.repointUser(ctx, ownerPlaceholderID, realID)
	if err != nil {
		s.log.Warn("insights: could not merge the owner's sessions", "err", err)
		return
	}
	if n > 0 {
		s.log.Info("insights: merged sessions recorded under the owner placeholder",
			"sessions", n, "into", realID)
	}
	if err := s.repo.deleteUserIfUnused(ctx, ownerPlaceholderID); err != nil {
		s.log.Debug("insights: could not retire the owner placeholder row", "err", err)
	}
}
