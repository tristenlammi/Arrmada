package httpapi

import (
	"context"
	"fmt"
	"testing"

	"github.com/tristenlammi/arrmada/internal/movies"
	"github.com/tristenlammi/arrmada/internal/quality"
)

// The detail page may only promise upgrade watching when the sweep will look: monitored,
// with a file, on a profile that upgrades. Every other combination is false.
func TestUpgradeWatched(t *testing.T) {
	for _, monitored := range []bool{false, true} {
		for _, hasFile := range []bool{false, true} {
			for _, allows := range []bool{false, true} {
				want := monitored && hasFile && allows
				if got := upgradeWatched(monitored, hasFile, allows); got != want {
					t.Errorf("upgradeWatched(%v, %v, %v) = %v, want %v", monitored, hasFile, allows, got, want)
				}
			}
		}
	}
}

// anyVersionUpgrades reads the profile the way the sweep does: a version counts only when
// it is monitored, has a file and its profile has upgrades on.
func TestAnyVersionUpgrades(t *testing.T) {
	s := newRouteServer(t, func(d *Deps) { d.Quality = quality.NewService(d.Store.DB()) })
	ctx := context.Background()
	mk := func(name string, upgrades bool) string {
		sp, err := s.deps.Quality.Create(ctx, quality.StoredProfile{MediaType: quality.MediaMovie, Name: name, UpgradesEnabled: upgrades})
		if err != nil {
			t.Fatal(err)
		}
		return fmt.Sprintf("custom:%d", sp.ID)
	}
	up, still := mk("Upgrades", true), mk("Stays", false)
	a := &api{deps: s.deps}

	cases := []struct {
		name string
		m    movies.Movie
		want bool
	}{
		{"default version on an upgrading profile", movies.Movie{Versions: []movies.Version{{Monitored: true, HasFile: true, QualityProfile: up}}}, true},
		{"profile with upgrades off", movies.Movie{Versions: []movies.Version{{Monitored: true, HasFile: true, QualityProfile: still}}}, false},
		{"unmonitored version", movies.Movie{Versions: []movies.Version{{Monitored: false, HasFile: true, QualityProfile: up}}}, false},
		{"version without a file", movies.Movie{Versions: []movies.Version{{Monitored: true, HasFile: false, QualityProfile: up}}}, false},
		{"one of two versions qualifies", movies.Movie{Versions: []movies.Version{
			{Monitored: true, HasFile: true, QualityProfile: still},
			{Monitored: true, HasFile: true, QualityProfile: up},
		}}, true},
		{"no versions read: the movie's own profile", movies.Movie{QualityProfile: up}, true},
	}
	for _, c := range cases {
		if got := a.anyVersionUpgrades(ctx, &c.m); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
	if (&api{}).anyVersionUpgrades(ctx, &movies.Movie{QualityProfile: up}) {
		t.Error("no quality service: want false")
	}
}
