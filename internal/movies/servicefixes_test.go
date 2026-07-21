package movies

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/tristenlammi/arrmada/internal/store"
)

func testService(t *testing.T) (*Service, context.Context) {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return &Service{repo: NewRepo(st.DB()), log: slog.Default()}, context.Background()
}

// Match and Matcher must key titles the same way the search side does
// (parser.TitleKey): accents folded and "&" == "and". Otherwise the searcher
// grabs a release the importer can't re-match, the grab stays pending, and the
// next sweep re-grabs a duplicate.
func TestMatchSharedNormalizer(t *testing.T) {
	svc, ctx := testService(t)
	if _, err := svc.repo.Create(ctx, Movie{TMDBID: 1, Title: "Pokémon Heroes", Year: 2002}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.repo.Create(ctx, Movie{TMDBID: 2, Title: "Fast & Furious", Year: 2009}); err != nil {
		t.Fatal(err)
	}

	if m, ok := svc.Match(ctx, "Pokemon Heroes", 2002); !ok || m.TMDBID != 1 {
		t.Errorf("ASCII release title must match accented library title, got %+v/%v", m, ok)
	}
	if m, ok := svc.Match(ctx, "Fast and Furious", 2009); !ok || m.TMDBID != 2 {
		t.Errorf("\"and\" release title must match \"&\" library title, got %+v/%v", m, ok)
	}

	// Matcher (the indexed variant) uses the same key.
	all, _ := svc.repo.List(ctx)
	match := svc.Matcher(all)
	if m, ok := match("Pokemon Heroes", 2002); !ok || m.TMDBID != 1 {
		t.Errorf("Matcher: accent folding, got %+v/%v", m, ok)
	}
	if m, ok := match("Fast and Furious", 2009); !ok || m.TMDBID != 2 {
		t.Errorf("Matcher: &/and, got %+v/%v", m, ok)
	}
}

// A release with no parseable year must NOT attach when more than one library
// movie shares the title key — a "Cinderella.1080p" release must not land on
// whichever Cinderella was added last.
func TestMatchYearlessAmbiguity(t *testing.T) {
	svc, ctx := testService(t)
	if _, err := svc.repo.Create(ctx, Movie{TMDBID: 11, Title: "Cinderella", Year: 1950}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.repo.Create(ctx, Movie{TMDBID: 12, Title: "Cinderella", Year: 2015}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.repo.Create(ctx, Movie{TMDBID: 13, Title: "Dune", Year: 2021}); err != nil {
		t.Fatal(err)
	}

	if m, ok := svc.Match(ctx, "Cinderella", 0); ok {
		t.Errorf("year-less title shared by two movies must not match, got %+v", m)
	}
	if m, ok := svc.Match(ctx, "Cinderella", 2015); !ok || m.TMDBID != 12 {
		t.Errorf("with a year the right Cinderella must match, got %+v/%v", m, ok)
	}
	if m, ok := svc.Match(ctx, "Cinderella", 1950); !ok || m.TMDBID != 11 {
		t.Errorf("with a year the other Cinderella must match, got %+v/%v", m, ok)
	}
	if m, ok := svc.Match(ctx, "Dune", 0); !ok || m.TMDBID != 13 {
		t.Errorf("year-less title with exactly one candidate must match, got %+v/%v", m, ok)
	}
}

// MarkImported must refuse to replace an existing file with a lower-resolution
// one (ErrWorseQuality, existing file kept); MarkImportedManual is the user's
// deliberate override and always replaces.
func TestMarkImportedQualityGate(t *testing.T) {
	svc, ctx := testService(t)
	m, err := svc.repo.Create(ctx, Movie{TMDBID: 21, Title: "Dune", Year: 2021, Monitored: true})
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	mk := func(name string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(name), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}

	// First file: no existing file, imports unconditionally.
	f2160 := mk("Dune.2021.2160p.BluRay.x265-GRP.mkv")
	if err := svc.MarkImported(ctx, m.ID, f2160, "Dune.2021.2160p.BluRay.x265-GRP"); err != nil {
		t.Fatal(err)
	}

	// Lower resolution: refused, existing kept.
	f1080 := mk("Dune.2021.1080p.WEB.x264-GRP.mkv")
	err = svc.MarkImported(ctx, m.ID, f1080, "Dune.2021.1080p.WEB.x264-GRP")
	if !errors.Is(err, ErrWorseQuality) {
		t.Fatalf("lower-resolution import must return ErrWorseQuality, got %v", err)
	}
	if _, err := os.Stat(f2160); err != nil {
		t.Error("existing higher-quality file must not be deleted")
	}
	if got, _ := svc.repo.Get(ctx, m.ID); got.MovieFilePath != f2160 {
		t.Errorf("db must still point at the 2160p file, got %q", got.MovieFilePath)
	}

	// Manual import: the user's explicit pick replaces regardless.
	if err := svc.MarkImportedManual(ctx, m.ID, f1080, "Dune.2021.1080p.WEB.x264-GRP"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(f2160); !os.IsNotExist(err) {
		t.Error("manual replacement should have removed the old file")
	}
	if got, _ := svc.repo.Get(ctx, m.ID); got.MovieFilePath != f1080 {
		t.Errorf("db must point at the manually imported file, got %q", got.MovieFilePath)
	}

	// Equal-or-higher resolution: replaces as before.
	f2160b := mk("Dune.2021.2160p.WEB-DL.x265-GRP.mkv")
	if err := svc.MarkImported(ctx, m.ID, f2160b, "Dune.2021.2160p.WEB-DL.x265-GRP"); err != nil {
		t.Fatal(err)
	}
	if got, _ := svc.repo.Get(ctx, m.ID); got.MovieFilePath != f2160b {
		t.Errorf("higher-resolution import must replace, got %q", got.MovieFilePath)
	}
}

// fakeResolver maps profile refs to allowed resolutions for routeVersion tests.
type fakeResolver struct{ allowed map[string][]string }

func (f fakeResolver) AllowedResolutions(_ context.Context, ref string) []string {
	return f.allowed[ref]
}

// routeVersion: a known-resolution file goes to the track that accepts it, an
// unknown-resolution file always goes to the default track, and a resolution
// every track forbids falls back to the default track.
func TestRouteVersion(t *testing.T) {
	ctx := context.Background()
	svc := &Service{log: slog.Default(), resolver: fakeResolver{allowed: map[string][]string{
		"hd1080": {"1080p", "720p"},
		"uhd":    {"2160p"},
		"any":    nil,
	}}}

	defHD := Version{IsDefault: true, Label: "Default", QualityProfile: "hd1080"}
	v4k := Version{ID: 1, Label: "4K", QualityProfile: "uhd"}

	// 2160p file with 1080p-default + 4K-secondary → routes to the 4K track,
	// never to a track whose profile forbids the resolution.
	if got := svc.routeVersion(ctx, []Version{defHD, v4k}, "/lib/Dune.2021.2160p.BluRay.mkv"); got.Label != "4K" {
		t.Errorf("2160p file routed to %q, want 4K", got.Label)
	}

	// 1080p file stays on the default track.
	if got := svc.routeVersion(ctx, []Version{defHD, v4k}, "/lib/Dune.2021.1080p.WEB.mkv"); !got.IsDefault {
		t.Errorf("1080p file routed to %q, want Default", got.Label)
	}

	// Unknown-resolution file routes to the DEFAULT track — even when the default
	// profile is specific (would score -1) and another track accepts anything
	// (the old code sent it to the accept-any secondary).
	vAny := Version{ID: 2, Label: "Anything", QualityProfile: "any"}
	if got := svc.routeVersion(ctx, []Version{defHD, vAny}, "/lib/Dune.mkv"); !got.IsDefault {
		t.Errorf("unknown-resolution file routed to %q, want Default", got.Label)
	}

	// A resolution every track forbids falls back to the default track.
	def4K := Version{IsDefault: true, Label: "Default", QualityProfile: "uhd"}
	vHD := Version{ID: 3, Label: "HD", QualityProfile: "hd1080"}
	if got := svc.routeVersion(ctx, []Version{def4K, vHD}, "/lib/Dune.2021.480p.DVD.mkv"); !got.IsDefault {
		t.Errorf("all-forbidden resolution routed to %q, want Default", got.Label)
	}
}
