// Package buildinfo carries version metadata stamped into the binary at build
// time via -ldflags. The zero values are the sane defaults for a local dev build.
package buildinfo

// These are overridden at release time, e.g.:
//
//	go build -ldflags "-X github.com/tristenlammi/arrmada/internal/buildinfo.Version=0.1.0 \
//	                   -X github.com/tristenlammi/arrmada/internal/buildinfo.Commit=$(git rev-parse --short HEAD)"
var (
	// Version is the semantic version of this build.
	Version = "0.0.0-dev"
	// Commit is the short git SHA this build was cut from.
	Commit = "unknown"
)
