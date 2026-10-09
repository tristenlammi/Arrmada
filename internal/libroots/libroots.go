// Package libroots holds the rules every library or downloads folder must follow, in
// one place, so the folder settings, the folder picker, the health panel and manual
// import all draw the same line.
//
// The first rule is the standing one: media never lives in Arrmada's data folder (the
// database, logs and backups). An import or a Convert write into it would mix media with
// app state, and the hourly housekeeping and the backups would then treat media as
// theirs.
package libroots

import (
	"path/filepath"

	"github.com/tristenlammi/arrmada/internal/pathguard"
)

// containerDataDir is where the Docker image keeps the database. It is refused even when
// the running data dir is elsewhere (a native dev run), because a compose file that maps
// media there is exactly the mistake this rule exists for.
const containerDataDir = "/data"

// InDataDir reports whether p is the data dir or inside it. It is the check for reading
// (manual import): a walk that starts above the data dir is caught by the root check
// instead, and says so in its own words.
func InDataDir(p, dataDir string) bool {
	for _, d := range dataDirs(dataDir) {
		if pathguard.Under(p, d) {
			return true
		}
	}
	return false
}

// UnderDataDir reports whether p can't be a media folder because of the data dir: it is
// the data dir, lies inside it, or contains it ("/" or the parent of the data dir), which
// would put the database inside a library that scans, imports and converts walk. Both
// sides are fully resolved, so ".." and symlinks can't hide either case.
func UnderDataDir(p, dataDir string) bool {
	if isRoot(p) {
		return true
	}
	if InDataDir(p, dataDir) {
		return true
	}
	for _, d := range dataDirs(dataDir) {
		if pathguard.Under(d, p) {
			return true
		}
	}
	return false
}

// dataDirs lists the folders that count as Arrmada's data folder.
func dataDirs(dataDir string) []string {
	out := []string{containerDataDir}
	if dataDir != "" {
		out = append(out, dataDir)
	}
	return out
}

// isRoot reports whether p resolves to a filesystem root ("/" or a Windows volume).
func isRoot(p string) bool {
	r, err := pathguard.Resolve(p)
	if err != nil {
		return false
	}
	return filepath.Dir(r) == r
}
