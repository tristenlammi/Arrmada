package httpapi

import (
	"errors"
	"net/http"
	"strings"

	"github.com/tristenlammi/arrmada/internal/automation"
)

// tokenize gives every release in list an opaque token (see automation/releasetokens.go)
// scoped to the title, the kind of grab and the person searching. The download link
// stays on the server; RankedRelease never serialises it. A release with no link gets no
// token — there would be nothing to grab.
func (a *api) tokenize(r *http.Request, list *automation.ReleaseList, scope automation.ReleaseRef) {
	if a.deps.Automation == nil {
		return
	}
	scope.UserID = requestUserID(r)
	for i := range list.Releases {
		rel := &list.Releases[i]
		if rel.DownloadURL == "" {
			continue
		}
		ref := scope
		ref.Indexer, ref.DownloadURL, ref.Title, ref.InfoHash = rel.Indexer, rel.DownloadURL, rel.Title, rel.InfoHash
		if scope.MediaKind == automation.ReleaseKindBook {
			ref.VersionID = rel.VersionID
		}
		rel.Token = a.deps.Automation.IssueReleaseToken(ref)
	}
}

// resolveRelease turns a grab body's token back into the release it stands for, checking
// it was issued for this kind of grab, this title and this person. On failure it has
// already written the response: 400 for a missing or someone else's token, 410 for one
// that has expired (or predates a restart), so the modal can say "search again".
func (a *api) resolveRelease(w http.ResponseWriter, r *http.Request, token, kind string, mediaID int64) (automation.ReleaseRef, bool) {
	if token == "" {
		a.writeError(w, http.StatusBadRequest, "token is required")
		return automation.ReleaseRef{}, false
	}
	if a.deps.Automation == nil {
		a.writeError(w, http.StatusServiceUnavailable, "grabbing isn't available")
		return automation.ReleaseRef{}, false
	}
	ref, err := a.deps.Automation.ResolveReleaseToken(token, kind, mediaID, requestUserID(r))
	switch {
	case errors.Is(err, automation.ErrReleaseExpired):
		a.writeError(w, http.StatusGone, "This search result has expired — search again")
		return automation.ReleaseRef{}, false
	case errors.Is(err, automation.ErrReleaseScope):
		a.writeError(w, http.StatusBadRequest, "That result belongs to a different title")
		return automation.ReleaseRef{}, false
	case err != nil:
		a.writeError(w, http.StatusBadRequest, err.Error())
		return automation.ReleaseRef{}, false
	}
	return ref, true
}

// writeGrabError reports a failed grab as 502. The download link the token stood for is
// cut out of the message: a client or tracker error that quotes it would otherwise hand
// the browser the very link the token keeps from it.
func (a *api) writeGrabError(w http.ResponseWriter, err error, ref automation.ReleaseRef) {
	msg := err.Error()
	if ref.DownloadURL != "" {
		msg = strings.ReplaceAll(msg, ref.DownloadURL, "the release link")
	}
	a.writeError(w, http.StatusBadGateway, msg)
}

// requestUserID is the signed-in user's ID, or 0 when there is none (the routes that use
// it all require a role, so 0 never matches a real issue).
func requestUserID(r *http.Request) int64 {
	if u, ok := userFrom(r); ok && u != nil {
		return u.ID
	}
	return 0
}
