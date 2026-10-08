package automation

import (
	"reflect"
	"testing"

	"github.com/tristenlammi/arrmada/internal/series"
)

func scopeRef(s, e int) series.EpisodeRef { return series.EpisodeRef{Season: s, Episode: e} }

// Covers is the line between "the user said so" and "the gate decides". Too permissive
// re-opens the library-wide overwrite; too strict blocks an import the user chose. Both
// directions are checked.
func TestGrabScopeCovers(t *testing.T) {
	cases := []struct {
		name  string
		scope GrabScope
		ref   series.EpisodeRef
		want  bool
	}{
		{"whole show covers any season", WholeShow, scopeRef(7, 3), true},
		{"whole show covers specials", WholeShow, scopeRef(0, 2), true},
		{"season covers its episodes", GrabScope{Season: 3}, scopeRef(3, 1), true},
		{"season covers its last episode", GrabScope{Season: 3}, scopeRef(3, 22), true},
		{"season excludes the season before", GrabScope{Season: 3}, scopeRef(2, 1), false},
		{"season excludes the season after", GrabScope{Season: 3}, scopeRef(4, 1), false},
		{"season 3 excludes specials", GrabScope{Season: 3}, scopeRef(0, 3), false},
		{"episode covers itself", GrabScope{Season: 3, Episode: 4}, scopeRef(3, 4), true},
		{"episode excludes its neighbour", GrabScope{Season: 3, Episode: 4}, scopeRef(3, 5), false},
		{"episode excludes the same number in another season", GrabScope{Season: 3, Episode: 4}, scopeRef(4, 4), false},
		{"special covers itself", GrabScope{Season: 0, Episode: 5}, scopeRef(0, 5), true},
		{"special excludes S01E05", GrabScope{Season: 0, Episode: 5}, scopeRef(1, 5), false},
		{"specials season covers specials", GrabScope{Season: 0}, scopeRef(0, 9), true},
		{"specials season excludes season 1", GrabScope{Season: 0}, scopeRef(1, 1), false},
	}
	for _, tc := range cases {
		if got := tc.scope.Covers(tc.ref); got != tc.want {
			t.Errorf("%s: %+v.Covers(%+v) = %v, want %v", tc.name, tc.scope, tc.ref, got, tc.want)
		}
	}
}

func TestGrabScopeStringRoundTrip(t *testing.T) {
	for _, tc := range []struct {
		scope GrabScope
		str   string
	}{
		{WholeShow, ""},
		{GrabScope{Season: 3}, "S03"},
		{GrabScope{Season: 3, Episode: 4}, "S03E04"},
		{GrabScope{Season: 0, Episode: 5}, "S00E05"},
		{GrabScope{Season: 0}, "S00"},
		{GrabScope{Season: 12, Episode: 104}, "S12E104"},
	} {
		if got := tc.scope.String(); got != tc.str {
			t.Errorf("%+v.String() = %q, want %q", tc.scope, got, tc.str)
		}
		back, ok := parseGrabScope(tc.str)
		if !ok || back != tc.scope {
			t.Errorf("parseGrabScope(%q) = %+v, %v; want %+v", tc.str, back, ok, tc.scope)
		}
	}
	// Anything String can't write is refused, so a corrupted column can't widen a scope.
	for _, bad := range []string{"S", "E04", "s03", "S03E", "S03E00", "Season 3", " S03", "S-1"} {
		if sc, ok := parseGrabScope(bad); ok {
			t.Errorf("parseGrabScope(%q) = %+v, want refused", bad, sc)
		}
	}
	// The API's (season, episode) pairs map the same way.
	if ScopeFor(-1, 0) != WholeShow || ScopeFor(3, 0) != (GrabScope{Season: 3}) || ScopeFor(0, 5) != (GrabScope{Season: 0, Episode: 5}) {
		t.Error("ScopeFor mapped a (season, episode) pair wrongly")
	}
}

func TestRefsToPlaceHonoursForceScope(t *testing.T) {
	// A pack spanning two seasons; the gate approves nothing (every episode already has a
	// better file), so whatever is placed got in on the force rule alone.
	refs := []series.EpisodeRef{scopeRef(1, 1), scopeRef(1, 2), scopeRef(2, 1), scopeRef(2, 2)}
	gateSaysNo := func(series.EpisodeRef) bool { return false }
	gateWantsS2E2 := func(r series.EpisodeRef) bool { return r == scopeRef(2, 2) }

	place, forced := refsToPlace(refs, forceRule{}, gateWantsS2E2)
	if !reflect.DeepEqual(place, []series.EpisodeRef{scopeRef(2, 2)}) || len(forced) != 0 {
		t.Errorf("force off: place=%v forced=%v, want only the gate's S02E02 and nothing forced", place, forced)
	}

	place, forced = refsToPlace(refs, forceRule{On: true, Scope: GrabScope{Season: 1}}, gateSaysNo)
	if want := []series.EpisodeRef{scopeRef(1, 1), scopeRef(1, 2)}; !reflect.DeepEqual(place, want) || !reflect.DeepEqual(forced, want) {
		t.Errorf("force S01: place=%v forced=%v, want season 1 forced and season 2 gated out", place, forced)
	}

	// Out-of-scope episodes still go in when the gate approves them on their own merit.
	place, forced = refsToPlace(refs, forceRule{On: true, Scope: GrabScope{Season: 1, Episode: 2}}, gateWantsS2E2)
	if !reflect.DeepEqual(place, []series.EpisodeRef{scopeRef(1, 2), scopeRef(2, 2)}) || !reflect.DeepEqual(forced, []series.EpisodeRef{scopeRef(1, 2)}) {
		t.Errorf("force S01E02: place=%v forced=%v", place, forced)
	}

	place, forced = refsToPlace(refs, forceAll, gateSaysNo)
	if !reflect.DeepEqual(place, refs) || !reflect.DeepEqual(forced, refs) {
		t.Errorf("force all: place=%v forced=%v, want every ref", place, forced)
	}

	// A rule that isn't On forces nothing, whatever scope it carries.
	if _, forced := refsToPlace(refs, forceRule{Scope: WholeShow}, gateSaysNo); len(forced) != 0 {
		t.Errorf("an off rule forced %v", forced)
	}
}
