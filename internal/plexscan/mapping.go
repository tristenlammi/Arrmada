// Package plexscan tells Plex which folder changed after Arrmada imports, renames,
// deletes or converts a file, so Plex picks the change up within a minute instead of
// waiting for its own watcher (unreliable on Unraid's /mnt/user shares) or its scheduled
// scan.
//
// Arrmada and Plex usually see the same files under different paths (/movies/Heat (1995)
// here, /data/media/movies/Heat (1995) in the Plex container), so every folder is first
// translated to Plex's side (mapping.go), then queued and debounced so a season pack or a
// burst of renames costs one scan per folder (scanner.go).
package plexscan

import (
	"path"
	"regexp"
	"sort"
	"strings"

	"github.com/tristenlammi/arrmada/internal/plex"
)

// Library kinds a scan can be for, matching Plex's section types.
const (
	KindMovie = "movie"
	KindShow  = "show"
)

// How a folder was translated to Plex's side, in the order Resolve tries them.
const (
	// HowDirect: the folder already lies under one of Plex's library folders (both apps
	// mount the media at the same path).
	HowDirect = "direct"
	// HowMapped: a path mapping the owner entered translated it.
	HowMapped = "mapped"
	// HowGuessed: Arrmada's library folder and one of Plex's end in the same name
	// (/movies and /data/media/movies), so the rest of the path was carried across.
	HowGuessed = "guessed"
	// HowSection: no translation was found, so the whole library section is scanned.
	// Correct, just slower on a big library.
	HowSection = "section"
	// HowNone: Plex has no library of this kind at all.
	HowNone = "none"
)

// PathMap translates an Arrmada folder (From) to the same folder as Plex sees it (To).
type PathMap struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// Resolution is where a folder's scan goes.
type Resolution struct {
	SectionKeys []string `json:"-"`
	Sections    []string `json:"sections"`  // their titles, for the settings view
	PlexPath    string   `json:"plex_path"` // "" = scan the whole section
	How         string   `json:"how"`
	Note        string   `json:"note,omitempty"`
}

// Resolve works out which Plex library section(s) a changed Arrmada folder belongs to and
// what that folder is called on Plex's side. kind is KindMovie or KindShow, dir the
// folder as Arrmada sees it, arrRoot Arrmada's library folder for that kind. It tries, in
// order: the folder as is, the owner's mappings (longest From wins), a guess from matching
// folder names, and finally the whole section. Pure: no I/O.
func Resolve(kind, dir, arrRoot string, sections []plex.Library, maps []PathMap) Resolution {
	var mine []plex.Library
	for _, s := range sections {
		if s.Type == kind {
			mine = append(mine, s)
		}
	}
	if len(mine) == 0 {
		return Resolution{How: HowNone, Note: "Plex has no " + kindNoun(kind) + " library"}
	}

	// (a) The folder is already under one of Plex's library folders.
	if sec, p, ok := locate(dir, mine); ok {
		return Resolution{SectionKeys: []string{sec.Key}, Sections: []string{sec.Title}, PlexPath: p, How: HowDirect}
	}

	// (b) The owner's mappings: the longest From the folder lies under.
	var best PathMap
	bestRel, found := "", false
	for _, m := range maps {
		rel, ok := under(dir, m.From)
		if !ok || strings.TrimSpace(m.To) == "" {
			continue
		}
		if !found || len(canon(m.From)) > len(canon(best.From)) {
			best, bestRel, found = m, rel, true
		}
	}
	if found {
		mapped := join(best.To, bestRel)
		if sec, p, ok := locate(mapped, mine); ok {
			return Resolution{SectionKeys: []string{sec.Key}, Sections: []string{sec.Title}, PlexPath: p, How: HowMapped}
		}
		r := wholeSections(mine)
		r.Note = "the path mapping points at " + mapped + ", which isn't inside any Plex " + kindNoun(kind) + " library"
		return r
	}

	// (c) Arrmada's library folder and a Plex one share their last name.
	if rel, ok := under(dir, arrRoot); ok {
		name := lastSegment(arrRoot)
		for _, sec := range mine {
			for _, loc := range sec.Locations {
				if name != "" && strings.EqualFold(lastSegment(loc), name) {
					return Resolution{SectionKeys: []string{sec.Key}, Sections: []string{sec.Title}, PlexPath: join(loc, rel), How: HowGuessed}
				}
			}
		}
	}

	// (d) Nothing to go on: scan every section of this kind.
	return wholeSections(mine)
}

func wholeSections(secs []plex.Library) Resolution {
	r := Resolution{How: HowSection}
	for _, s := range secs {
		r.SectionKeys = append(r.SectionKeys, s.Key)
		r.Sections = append(r.Sections, s.Title)
	}
	return r
}

// locate finds the section whose folder p lies under, preferring the deepest folder, and
// returns p spelled the way Plex spells that folder.
func locate(p string, secs []plex.Library) (plex.Library, string, bool) {
	var best plex.Library
	bestLoc, bestRel, found := "", "", false
	for _, sec := range secs {
		for _, loc := range sec.Locations {
			rel, ok := under(p, loc)
			if !ok {
				continue
			}
			if !found || len(canon(loc)) > len(canon(bestLoc)) {
				best, bestLoc, bestRel, found = sec, loc, rel, true
			}
		}
	}
	if !found {
		return plex.Library{}, "", false
	}
	return best, join(bestLoc, bestRel), true
}

func kindNoun(kind string) string {
	if kind == KindShow {
		return "TV"
	}
	return "movie"
}

// Paths come from two machines that may not agree on anything: Arrmada runs in a Linux
// container, Plex may be on Windows (D:\Media\Movies, \\nas\media). They're compared in
// one canonical spelling — forward slashes, cleaned, no trailing slash — and Windows
// paths compare without regard to case, the way Windows treats them. Output keeps the
// spelling of the side it belongs to.

var reDrive = regexp.MustCompile(`^[A-Za-z]:`)

// windowsStyle reports a Windows path: a drive letter, a UNC share, or backslashes only.
func windowsStyle(p string) bool {
	p = strings.TrimSpace(p)
	return reDrive.MatchString(p) || strings.HasPrefix(p, `\\`) || (strings.Contains(p, `\`) && !strings.Contains(p, "/"))
}

// canon is p in the canonical comparison spelling ("" stays "").
func canon(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	// A backslash is a separator only on Windows; in a Linux path it's part of a name.
	win := windowsStyle(p)
	unc := strings.HasPrefix(p, `\\`)
	if win {
		p = strings.ReplaceAll(p, `\`, "/")
	}
	p = path.Clean(p)
	if unc {
		p = "/" + p // Clean folds the leading "//" of a share
	}
	if reDrive.MatchString(p) && len(p) == 2 {
		p += "/" // "D:" alone is the drive's root
	}
	return p
}

// under reports whether child is parent or inside it, and the part of child below it
// ("" when they're the same folder), in forward slashes. It matches whole folder names
// only: /movies2 is not under /movies.
func under(child, parent string) (string, bool) {
	c, p := canon(child), canon(parent)
	if c == "" || p == "" {
		return "", false
	}
	fold := windowsStyle(child) || windowsStyle(parent)
	eq := func(a, b string) bool {
		if fold {
			return strings.EqualFold(a, b)
		}
		return a == b
	}
	if eq(c, p) {
		return "", true
	}
	prefix := p
	if !strings.HasSuffix(prefix, "/") {
		prefix += "/"
	}
	if len(c) > len(prefix) && eq(c[:len(prefix)], prefix) {
		return c[len(prefix):], true
	}
	return "", false
}

// join appends rel (forward slashes) to base in base's own spelling: backslashes for a
// Windows base, forward slashes otherwise.
func join(base, rel string) string {
	base = strings.TrimSpace(base)
	if windowsStyle(base) {
		b := strings.ReplaceAll(canon(base), "/", `\`)
		if rel == "" {
			return b
		}
		return strings.TrimRight(b, `\`) + `\` + strings.ReplaceAll(rel, "/", `\`)
	}
	b := canon(base)
	if rel == "" {
		return b
	}
	return strings.TrimRight(b, "/") + "/" + rel
}

// lastSegment is a folder's own name ("movies" for /mnt/user/media/movies/).
func lastSegment(p string) string {
	c := canon(p)
	if i := strings.LastIndex(c, "/"); i >= 0 {
		c = c[i+1:]
	}
	if reDrive.MatchString(c) {
		return "" // a bare drive has no name to match
	}
	return c
}

// CleanMaps tidies mappings the owner entered: trims them, drops empty rows and repeats
// of the same From (the first wins), and orders them longest From first, which is the
// order Resolve tries them in anyway.
func CleanMaps(in []PathMap) []PathMap {
	seen := map[string]bool{}
	out := []PathMap{}
	for _, m := range in {
		m.From, m.To = strings.TrimSpace(m.From), strings.TrimSpace(m.To)
		if m.From == "" || m.To == "" {
			continue
		}
		k := canon(m.From)
		if windowsStyle(m.From) {
			k = strings.ToLower(k)
		}
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, m)
	}
	sort.SliceStable(out, func(i, j int) bool { return len(canon(out[i].From)) > len(canon(out[j].From)) })
	return out
}

// Absolute reports whether p is a full path on either kind of machine.
func Absolute(p string) bool {
	p = strings.TrimSpace(p)
	return strings.HasPrefix(p, "/") || strings.HasPrefix(p, `\\`) || (reDrive.MatchString(p) && len(p) >= 3 && (p[2] == '\\' || p[2] == '/'))
}
