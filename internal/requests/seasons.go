package requests

import (
	"context"
	"fmt"
	"strings"

	"github.com/tristenlammi/arrmada/internal/series"
)

// requesterName is who a request is for, in History lines.
func requesterName(req Request) string {
	if n := strings.TrimSpace(req.RequestedByName); n != "" {
		return n
	}
	return "a requester"
}

// monitorExistingSeries makes a show that is already in the library fetch what an
// approved request asked for: its seasons, or the whole show. wants is false when every
// aired episode asked for is already on disk: nothing is changed then, and the ready
// path stamps the request.
func (s *Service) monitorExistingSeries(ctx context.Context, req Request) (sr series.Series, wants bool, err error) {
	sr, err = s.series.GetByTMDB(ctx, req.TMDBID)
	if err != nil {
		return series.Series{}, false, fmt.Errorf("find the show in the library: %w", err)
	}
	var seasons []int // nil: the whole show
	if len(req.Seasons) > 0 {
		seasons = req.Seasons
	}
	prog, err := s.series.SeasonProgress(ctx, sr.ID)
	if err != nil {
		return sr, false, err
	}
	if seasonsOnDisk(prog, seasons) {
		return sr, false, nil
	}
	if err := s.series.EnsureMonitored(ctx, sr.ID, seasons, requesterName(req)); err != nil {
		return sr, false, err
	}
	return sr, true, nil
}

// seasonsOnDisk reports whether every aired episode of the given seasons (nil = every
// season of the show) is on disk. A listed season the library doesn't have isn't; for
// the whole show, seasons with nothing aired yet don't count, and something must be on
// disk at all.
func seasonsOnDisk(prog map[int]series.SeasonProgress, seasons []int) bool {
	if seasons == nil {
		some := false
		for _, p := range prog {
			if p.Aired == 0 {
				continue
			}
			if !p.OnDisk() {
				return false
			}
			some = true
		}
		return some
	}
	for _, n := range seasons {
		p, ok := prog[n]
		if !ok || !p.OnDisk() {
			return false
		}
	}
	return len(seasons) > 0
}
