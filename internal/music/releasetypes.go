package music

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Release types and new-release monitoring (MUS-14).
//
// Which release groups an artist lists is two sets of MusicBrainz types, and a release
// group must pass BOTH: its primary type is allowed AND every one of its secondary types
// is. Lidarr joins them with OR, so "albums, plus live albums" there also pulls in every
// live single and live compilation; here a live single needs Single ticked too, and a live
// compilation needs Compilation. A release group with no primary type is never hidden for
// it (MusicBrainz leaves it blank on some old entries); its secondary types still count.

// Settings for the library-wide defaults (Settings → Media → Music: new releases). An
// artist with its own choice ignores them; one without follows them, changes included.
const (
	KeyMonitorNew   = "music_monitor_new"
	KeyReleaseTypes = "music_release_types"
)

// What happens to an album MusicBrainz newly lists for an artist.
const (
	MonitorNewAll  = "all"  // wanted, if the artist is monitored
	MonitorNewNone = "none" // listed, not wanted
)

// What the add dialog may ask for an artist's existing albums.
const (
	MonitorAll    = "all"    // every listed album wanted
	MonitorFuture = "future" // none of today's albums; new ones as they come
	MonitorLatest = "latest" // the newest released album, and new ones
	MonitorNone   = "none"   // nothing, now or later
)

// ValidMonitor reports whether m is one of the add dialog's choices.
func ValidMonitor(m string) bool {
	switch m {
	case MonitorAll, MonitorFuture, MonitorLatest, MonitorNone:
		return true
	}
	return false
}

// ValidMonitorNew reports whether m is a monitor_new value (” = the library default).
func ValidMonitorNew(m string) bool {
	return m == "" || m == MonitorNewAll || m == MonitorNewNone
}

// PrimaryTypes and SecondaryTypes are MusicBrainz's release-group types, in the order the
// options list them.
var (
	PrimaryTypes   = []string{"Album", "EP", "Single", "Broadcast", "Other"}
	SecondaryTypes = []string{"Compilation", "Soundtrack", "Live", "Remix", "DJ-mix", "Mixtape/Street",
		"Demo", "Spokenword", "Interview", "Audiobook", "Audio drama", "Field recording"}
)

// ReleaseTypes are the release groups an artist lists.
type ReleaseTypes struct {
	Primary   []string `json:"primary"`
	Secondary []string `json:"secondary"`
}

// DefaultReleaseTypes is what Music has always listed: albums and EPs, no live albums,
// compilations or remixes.
func DefaultReleaseTypes() ReleaseTypes {
	return ReleaseTypes{Primary: []string{"Album", "EP"}, Secondary: []string{}}
}

// canonType finds a type in list ignoring case, returning MusicBrainz's spelling.
func canonType(list []string, t string) (string, bool) {
	t = strings.TrimSpace(t)
	for _, c := range list {
		if strings.EqualFold(c, t) {
			return c, true
		}
	}
	return "", false
}

// ParseReleaseTypes reads a stored or sent value: the JSON shape, or the older
// comma-separated list of labels ("Album,EP,Live": a label that is a secondary type is
// allowed as one). Types are put in MusicBrainz's spelling and order; an unknown one is an
// error.
func ParseReleaseTypes(s string) (ReleaseTypes, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return DefaultReleaseTypes(), nil
	}
	var raw ReleaseTypes
	if strings.HasPrefix(s, "{") {
		if err := json.Unmarshal([]byte(s), &raw); err != nil {
			return ReleaseTypes{}, fmt.Errorf("release types: %w", err)
		}
	} else {
		for _, p := range SplitList(s) {
			if _, ok := canonType(SecondaryTypes, p); ok {
				raw.Secondary = append(raw.Secondary, p)
			} else {
				raw.Primary = append(raw.Primary, p)
			}
		}
	}
	return raw.Normalize()
}

// Normalize checks every type and puts both lists in MusicBrainz's spelling and order,
// each type once. At least one primary type is needed: without one nothing is listed.
func (t ReleaseTypes) Normalize() (ReleaseTypes, error) {
	pick := func(list, in []string, kind string) ([]string, error) {
		have := map[string]bool{}
		for _, v := range in {
			c, ok := canonType(list, v)
			if !ok {
				return nil, fmt.Errorf("%q isn't a MusicBrainz %s type", v, kind)
			}
			have[c] = true
		}
		out := []string{}
		for _, c := range list {
			if have[c] {
				out = append(out, c)
			}
		}
		return out, nil
	}
	p, err := pick(PrimaryTypes, t.Primary, "primary")
	if err != nil {
		return ReleaseTypes{}, err
	}
	if len(p) == 0 {
		return ReleaseTypes{}, errors.New("pick at least one primary type (Album, EP, Single, Broadcast or Other)")
	}
	s, err := pick(SecondaryTypes, t.Secondary, "secondary")
	if err != nil {
		return ReleaseTypes{}, err
	}
	return ReleaseTypes{Primary: p, Secondary: s}, nil
}

// String is the stored JSON.
func (t ReleaseTypes) String() string {
	if t.Primary == nil {
		t.Primary = []string{}
	}
	if t.Secondary == nil {
		t.Secondary = []string{}
	}
	b, _ := json.Marshal(t)
	return string(b)
}

// Allows reports whether a release group with these MusicBrainz types is listed: its
// primary type allowed (or blank) AND every secondary type allowed.
func (t ReleaseTypes) Allows(primary string, secondary []string) bool {
	if strings.TrimSpace(primary) != "" {
		if _, ok := canonType(t.Primary, primary); !ok {
			return false
		}
	}
	for _, s := range secondary {
		if strings.TrimSpace(s) == "" {
			continue
		}
		if _, ok := canonType(t.Secondary, s); !ok {
			return false
		}
	}
	return true
}

// WantsOther reports whether any primary type outside albums and EPs is allowed, which is
// what makes the second (singles, broadcasts, other) browse worth asking.
func (t ReleaseTypes) WantsOther() bool {
	for _, p := range t.Primary {
		if p != "Album" && p != "EP" {
			return true
		}
	}
	return false
}

// Equal reports whether two type sets list the same release groups.
func (t ReleaseTypes) Equal(o ReleaseTypes) bool { return t.String() == o.String() }

// shownFor reports whether an album shows in its artist's album list (issue #122). The
// artist's release types decide, for what was listed before they changed as much as for
// what a refresh brings in: untick EP and the EPs leave the list, tick it again and they
// come back (the rows are kept, nothing is deleted). An album is shown whatever its kind
// when it is wanted (hiding it would hide a search running for it), holds any file (one
// the scan can't see right now included: hiding it would hide music on disk), or didn't
// come from MusicBrainz's lists (the refresh never added it, so the types never decided
// it).
func shownFor(types ReleaseTypes, al Album, hasFile bool) bool {
	if al.Monitored || hasFile || (al.Source != "" && al.Source != "musicbrainz") {
		return true
	}
	p, sec := albumTypes(al)
	return types.Allows(p, sec)
}

// splitTypes reads the stored secondary types ("Compilation,Live").
func splitTypes(s string) []string { return SplitList(s) }

// albumTypes is an album's primary and secondary types as stored, reading the older
// display label when the album predates them.
func albumTypes(al Album) (string, []string) {
	if al.PrimaryType == "" && al.SecondaryTypes == "" {
		p, s := splitAlbumType(al.AlbumType)
		return p, splitTypes(s)
	}
	return al.PrimaryType, splitTypes(al.SecondaryTypes)
}

// releasedBy reports whether a MusicBrainz date ("2026", "2026-10", "2026-10-20") is
// certainly out by now: a partial date counts from the LAST day it could mean, so "2026"
// isn't out until 2026 is over. "Latest" uses it, so it never picks an album that has only
// been announced. An unknown date is not certainly out.
func releasedBy(date string, now time.Time) bool {
	d := strings.TrimSpace(date)
	if d == "" {
		return false
	}
	var last time.Time
	switch len(d) {
	case 4:
		t, err := time.Parse("2006", d)
		if err != nil {
			return false
		}
		last = t.AddDate(1, 0, -1)
	case 7:
		t, err := time.Parse("2006-01", d)
		if err != nil {
			return false
		}
		last = t.AddDate(0, 1, -1)
	default:
		if len(d) > 10 {
			d = d[:10]
		}
		t, err := time.Parse("2006-01-02", d)
		if err != nil {
			return false
		}
		last = t
	}
	y, m, dd := now.Date()
	today := time.Date(y, m, dd, 0, 0, 0, 0, time.UTC)
	return !last.After(today)
}

// releaseDay is the earliest day a MusicBrainz date could mean (zero when unknown), for
// "how old is this?" questions.
func releaseDay(date string) time.Time {
	d := strings.TrimSpace(date)
	for _, layout := range []string{"2006-01-02", "2006-01", "2006"} {
		if len(d) >= len(layout) {
			if t, err := time.Parse(layout, d[:len(layout)]); err == nil {
				return t
			}
		}
	}
	return time.Time{}
}
