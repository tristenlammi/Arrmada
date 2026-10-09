package quality

import (
	"context"
	"fmt"

	"github.com/tristenlammi/arrmada/internal/parser"
)

// ExceedsCeiling reports whether a release of sizeGB covering runtimeMin minutes is above
// the profile's bitrate ceiling for its resolution, and says so in the words Evaluate uses
// ("Over your 15 Mbps ceiling (25.4 Mbps)"). It's the raw bitrate, like Evaluate's
// ceiling: a ceiling bounds file size and bandwidth, and for that a bit is a bit.
//
// The import gate needs this on its own. The searcher already refuses an over-ceiling
// release, but a grab made before series candidates carried a runtime — or one from
// outside the planner — would otherwise still replace a file that fits. Unknown runtime or
// no ceiling: false, since nothing can be judged.
func (s *Service) ExceedsCeiling(ctx context.Context, ref, release string, sizeGB float64, runtimeMin int) (bool, string) {
	p, _ := s.Resolve(ctx, ref)
	limit := p.capFor(parser.Parse(release).Resolution)
	br := BitrateMbps(sizeGB, runtimeMin)
	if limit <= 0 || br <= limit {
		return false, ""
	}
	return true, fmt.Sprintf("Over your %.0f Mbps ceiling (%.1f Mbps)", limit, br)
}
