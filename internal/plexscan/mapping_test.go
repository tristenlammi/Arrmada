package plexscan

import (
	"reflect"
	"testing"

	"github.com/tristenlammi/arrmada/internal/plex"
)

func TestResolve(t *testing.T) {
	movies := plex.Library{Key: "1", Title: "Movies", Type: "movie", Locations: []string{"/data/media/movies"}}
	movies4k := plex.Library{Key: "4", Title: "Movies 4K", Type: "movie", Locations: []string{"/data/media/movies/4k"}}
	tv := plex.Library{Key: "2", Title: "TV Shows", Type: "show", Locations: []string{"/data/media/tv"}}
	winMovies := plex.Library{Key: "5", Title: "Films", Type: "movie", Locations: []string{`D:\Media\Movies`}}
	music := plex.Library{Key: "3", Title: "Music", Type: "artist", Locations: []string{"/movies"}}

	cases := []struct {
		name     string
		kind     string
		dir      string
		root     string
		secs     []plex.Library
		maps     []PathMap
		wantKeys []string
		wantPath string
		wantHow  string
	}{
		{
			name: "same path on both sides", kind: KindMovie, dir: "/data/media/movies/Heat (1995)", root: "/data/media/movies",
			secs: []plex.Library{movies, tv}, wantKeys: []string{"1"}, wantPath: "/data/media/movies/Heat (1995)", wantHow: HowDirect,
		},
		{
			name: "deepest Plex folder wins", kind: KindMovie, dir: "/data/media/movies/4k/Dune (2021)", root: "/data/media/movies",
			secs: []plex.Library{movies, movies4k}, wantKeys: []string{"4"}, wantPath: "/data/media/movies/4k/Dune (2021)", wantHow: HowDirect,
		},
		{
			name: "user mapping", kind: KindMovie, dir: "/films/Heat (1995)", root: "/films",
			secs: []plex.Library{movies, tv}, maps: []PathMap{{From: "/films", To: "/data/media/movies"}},
			wantKeys: []string{"1"}, wantPath: "/data/media/movies/Heat (1995)", wantHow: HowMapped,
		},
		{
			name: "longest mapping wins", kind: KindMovie, dir: "/media/movies/4k/Dune (2021)", root: "/media/movies",
			secs:     []plex.Library{movies, movies4k},
			maps:     []PathMap{{From: "/media", To: "/elsewhere"}, {From: "/media/movies", To: "/data/media/movies"}},
			wantKeys: []string{"4"}, wantPath: "/data/media/movies/4k/Dune (2021)", wantHow: HowMapped,
		},
		{
			name: "mapping to a Windows Plex", kind: KindMovie, dir: "/movies/Heat (1995)", root: "/movies",
			secs: []plex.Library{winMovies}, maps: []PathMap{{From: "/movies", To: `d:\media\movies\`}},
			wantKeys: []string{"5"}, wantPath: `D:\Media\Movies\Heat (1995)`, wantHow: HowMapped,
		},
		{
			name: "folder-name guess", kind: KindMovie, dir: "/movies/Heat (1995)", root: "/movies",
			secs: []plex.Library{music, movies, tv}, wantKeys: []string{"1"}, wantPath: "/data/media/movies/Heat (1995)", wantHow: HowGuessed,
		},
		{
			name: "folder-name guess onto Windows", kind: KindMovie, dir: "/movies/Heat (1995)/extras", root: "/movies/",
			secs: []plex.Library{winMovies}, wantKeys: []string{"5"}, wantPath: `D:\Media\Movies\Heat (1995)\extras`, wantHow: HowGuessed,
		},
		{
			name: "guess is by whole folder name", kind: KindMovie, dir: "/movies2/Heat (1995)", root: "/movies2",
			secs: []plex.Library{movies}, wantKeys: []string{"1"}, wantHow: HowSection,
		},
		{
			name: "unrelated layout scans the whole section", kind: KindMovie, dir: "/srv/films/Heat (1995)", root: "/srv/films",
			secs: []plex.Library{movies, movies4k, tv}, wantKeys: []string{"1", "4"}, wantHow: HowSection,
		},
		{
			name: "types never cross", kind: KindShow, dir: "/data/media/movies/Odd Show", root: "/data/media/movies",
			secs: []plex.Library{movies, tv}, wantKeys: []string{"2"}, wantHow: HowSection,
		},
		{
			name: "show by guess", kind: KindShow, dir: "/tv/Law & Order (1990)/Season 01", root: "/tv",
			secs: []plex.Library{movies, tv}, wantKeys: []string{"2"}, wantPath: "/data/media/tv/Law & Order (1990)/Season 01", wantHow: HowGuessed,
		},
		{
			name: "mapping outside every library falls back", kind: KindMovie, dir: "/films/Heat (1995)", root: "/films",
			secs: []plex.Library{movies}, maps: []PathMap{{From: "/films", To: "/nowhere"}},
			wantKeys: []string{"1"}, wantHow: HowSection,
		},
		{
			name: "no library of the kind", kind: KindShow, dir: "/tv/x", root: "/tv",
			secs: []plex.Library{movies}, wantHow: HowNone,
		},
		{
			name: "root itself", kind: KindMovie, dir: "/movies", root: "/movies",
			secs: []plex.Library{movies}, wantKeys: []string{"1"}, wantPath: "/data/media/movies", wantHow: HowGuessed,
		},
		{
			name: "Linux names keep their backslashes", kind: KindMovie, dir: `/data/media/movies/A\B (2001)`, root: "/data/media/movies",
			secs: []plex.Library{movies}, wantKeys: []string{"1"}, wantPath: `/data/media/movies/A\B (2001)`, wantHow: HowDirect,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := Resolve(c.kind, c.dir, c.root, c.secs, c.maps)
			if !reflect.DeepEqual(r.SectionKeys, c.wantKeys) || r.PlexPath != c.wantPath || r.How != c.wantHow {
				t.Fatalf("Resolve = keys %v path %q how %s (note %q)\nwant keys %v path %q how %s",
					r.SectionKeys, r.PlexPath, r.How, r.Note, c.wantKeys, c.wantPath, c.wantHow)
			}
			if len(r.Sections) != len(r.SectionKeys) {
				t.Errorf("titles %v don't line up with keys %v", r.Sections, r.SectionKeys)
			}
		})
	}
}

func TestUnderAndJoin(t *testing.T) {
	for _, c := range []struct {
		child, parent, rel string
		ok                 bool
	}{
		{"/movies/a", "/movies", "a", true},
		{"/movies", "/movies/", "", true},
		{"/movies2/a", "/movies", "", false},
		{"/Movies/a", "/movies", "", false}, // Linux is case-sensitive
		{`D:\Media\Movies\a b`, `d:/media/movies`, "a b", true},
		{`\\nas\media\tv\Show`, `\\NAS\media\tv`, "Show", true},
		{"/anything/at/all", "/", "anything/at/all", true},
		{"", "/movies", "", false},
	} {
		rel, ok := under(c.child, c.parent)
		if rel != c.rel || ok != c.ok {
			t.Errorf("under(%q, %q) = %q, %v; want %q, %v", c.child, c.parent, rel, ok, c.rel, c.ok)
		}
	}
	if got := join(`\\nas\media\tv`, "Show/Season 01"); got != `\\nas\media\tv\Show\Season 01` {
		t.Errorf("UNC join = %q", got)
	}
	if got := join(`D:\`, "Movies"); got != `D:\Movies` {
		t.Errorf("drive-root join = %q", got)
	}
}

func TestCleanMaps(t *testing.T) {
	got := CleanMaps([]PathMap{
		{From: " /movies ", To: "/data/movies"},
		{From: "", To: "/x"},
		{From: "/movies/", To: "/dup"},
		{From: "/movies/4k", To: "/data/4k"},
	})
	want := []PathMap{{From: "/movies/4k", To: "/data/4k"}, {From: "/movies", To: "/data/movies"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("CleanMaps = %+v, want %+v", got, want)
	}
	for p, want := range map[string]bool{"/movies": true, `D:\Media`: true, `\\nas\x`: true, "movies": false, "D:": false, "": false} {
		if Absolute(p) != want {
			t.Errorf("Absolute(%q) = %v", p, !want)
		}
	}
}
