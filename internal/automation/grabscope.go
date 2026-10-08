package automation

import (
	"fmt"
	"regexp"
	"strconv"

	"github.com/tristenlammi/arrmada/internal/series"
)

// GrabScope is the part of a show a grab was made for: the whole show, one season, or one
// episode. It's recorded on the grab so the import knows where the user's say-so ends —
// a pack picked for Season 3 may carry seasons 1-10, and only Season 3 was asked for.
//
// Season -1 is the whole show. Season 0 is Specials, a real season like any other.
type GrabScope struct {
	Season, Episode int
}

// WholeShow is the scope of a grab that wasn't made for any one season or episode: the
// whole-show search, an uploaded torrent, a manual import, an approved review.
var WholeShow = GrabScope{Season: -1}

// ScopeFor builds the scope for a (season, episode) pair as the API and quick buttons
// carry them: a negative season means the whole show, episode 0 means the whole season.
func ScopeFor(season, episode int) GrabScope {
	if season < 0 {
		return WholeShow
	}
	if episode < 0 {
		episode = 0
	}
	return GrabScope{Season: season, Episode: episode}
}

// String is the form stored in grabs.scope: "", "S03", "S03E04", "S00E05".
func (g GrabScope) String() string {
	switch {
	case g.Season < 0:
		return ""
	case g.Episode > 0:
		return fmt.Sprintf("S%02dE%02d", g.Season, g.Episode)
	default:
		return fmt.Sprintf("S%02d", g.Season)
	}
}

var reGrabScope = regexp.MustCompile(`^S(\d+)(?:E(\d+))?$`)

// parseGrabScope is String's inverse. ok is false for anything String can't produce, so a
// corrupted value is never mistaken for a scope.
func parseGrabScope(s string) (GrabScope, bool) {
	if s == "" {
		return WholeShow, true
	}
	m := reGrabScope.FindStringSubmatch(s)
	if m == nil {
		return GrabScope{}, false
	}
	season, _ := strconv.Atoi(m[1])
	episode := 0
	if m[2] != "" {
		episode, _ = strconv.Atoi(m[2])
		if episode == 0 {
			return GrabScope{}, false // "S03E00" isn't a form String writes
		}
	}
	return GrabScope{Season: season, Episode: episode}, true
}

// Covers reports whether an episode falls inside the scope.
func (g GrabScope) Covers(ref series.EpisodeRef) bool {
	switch {
	case g.Season < 0:
		return true
	case ref.Season != g.Season:
		return false
	case g.Episode > 0:
		return ref.Episode == g.Episode
	default:
		return true
	}
}

// forceRule is whether an import skips the quality gate, and for which episodes. The gate
// stops the automation replacing a good file with a worse one; the user choosing a release
// answers "is it better" for the episodes they chose it for, and only those.
type forceRule struct {
	On    bool
	Scope GrabScope
}

// forceAll skips the gate for every file: the user's own file (an upload, a folder they
// pointed manual import at, a review they approved).
var forceAll = forceRule{On: true, Scope: WholeShow}

// forces reports whether the rule skips the gate for this episode.
func (f forceRule) forces(ref series.EpisodeRef) bool {
	return f.On && f.Scope.Covers(ref)
}

// refsToPlace splits a file's episodes into the ones it should be placed for and, of
// those, the ones placed on the user's say-so. A forced episode goes in whatever it
// scores; every other one must pass wants (the quality gate), so a pack's out-of-scope
// seasons can't silently downgrade the library.
func refsToPlace(refs []series.EpisodeRef, force forceRule, wants func(series.EpisodeRef) bool) (place, forced []series.EpisodeRef) {
	for _, ref := range refs {
		if force.forces(ref) {
			place = append(place, ref)
			forced = append(forced, ref)
			continue
		}
		if wants(ref) {
			place = append(place, ref)
		}
	}
	return place, forced
}
