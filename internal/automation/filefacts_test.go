package automation

import (
	"context"
	"strings"
	"testing"

	"github.com/tristenlammi/arrmada/internal/movies"
	"github.com/tristenlammi/arrmada/internal/parser"
	"github.com/tristenlammi/arrmada/internal/quality"
)

// fakeFacts is a FileFactsSource over a fixed map, gated on size like the real one.
type fakeFacts map[string]struct {
	size  int64
	facts quality.FileFacts
}

func (f fakeFacts) FactsForPath(_ context.Context, path string, size int64) (quality.FileFacts, bool) {
	e, ok := f[path]
	if !ok || e.size != size {
		return quality.FileFacts{}, false
	}
	return e.facts, true
}

// A library-scanned file has no recorded release, so its baseline is built from the
// cached media info — HDR, Atmos and lossless audio included, or it never meets an HDR
// or Atmos target and is searched for an upgrade every sweep.
func TestUpgradeBaselineCarriesProbedFormats(t *testing.T) {
	m := movies.Movie{Title: "Bambi", Year: 1942}
	v := movies.Version{IsDefault: true, HasFile: true, FilePath: "/m/Bambi (1942)/Bambi.mkv", File: &movies.MovieFile{
		Quality: "2160p BluRay", Codec: "hevc", HDR: []string{"DV", "HDR10"}, Atmos: true,
		Audio: []string{"truehd 7.1"},
	}}
	got := upgradeBaseline(m, v)
	for _, want := range []string{"Bambi 1942 2160p BluRay hevc", "DV", "HDR10", "Atmos", "TrueHD"} {
		if !strings.Contains(got, want) {
			t.Errorf("baseline %q is missing %q", got, want)
		}
	}
	f := quality.ReleaseFacts(parser.Parse(got), 0)
	if f.HDR != "HDR10" || !f.DolbyVision || !f.Atmos || !f.Lossless || f.Codec != "hevc" || f.Resolution != "2160p" {
		t.Errorf("the baseline doesn't read back as the file: %+v", f)
	}
	// A lossy track adds nothing.
	v.File.Audio, v.File.Atmos, v.File.HDR = []string{"eac3 5.1"}, false, nil
	if got := upgradeBaseline(m, v); got != "Bambi 1942 2160p BluRay hevc" {
		t.Errorf("baseline = %q", got)
	}
	// A recorded release always wins.
	v.SourceRelease = "Bambi.1942.2160p.BluRay.x265-GRP"
	if got := upgradeBaseline(m, v); got != v.SourceRelease {
		t.Errorf("baseline = %q, want the recorded release", got)
	}
}

// The movie sweep reads Convert's facts for a version's file — only while they describe
// the size the database has for it — and nothing at all without a source.
func TestCurrentMovieFileFacts(t *testing.T) {
	m := movies.Movie{Title: "Film", Year: 2021, Runtime: 120}
	v := movies.Version{IsDefault: true, HasFile: true, FilePath: "/m/Film.mkv", SourceRelease: "Film.2021.1080p.BluRay.x264-GRP",
		File: &movies.MovieFile{Path: "/m/Film.mkv", SizeBytes: 5 << 30}}
	c := &Coordinator{}
	if cur := c.currentMovieFile(context.Background(), m, v); cur.Facts != nil || cur.Release != v.SourceRelease || cur.RuntimeMin != 120 || cur.SizeGB != 5 {
		t.Fatalf("without a source: %+v", cur)
	}
	c.SetFileFacts(fakeFacts{"/m/Film.mkv": {5 << 30, quality.FileFacts{Resolution: "1080p", Codec: "av1"}}})
	if cur := c.currentMovieFile(context.Background(), m, v); cur.Facts == nil || cur.Facts.Codec != "av1" {
		t.Fatalf("with a matching analysis: %+v", cur)
	}
	if cur := c.CurrentMovieFile(context.Background(), movies.Movie{Title: "Film", HasFile: true, MovieFilePath: "/m/Film.mkv",
		SourceRelease: v.SourceRelease, File: v.File}); cur.Facts == nil {
		t.Error("the profile-change prompt must see the same facts as the sweep")
	}
	// The database says a different size: the analysis is of some other file.
	v.File.SizeBytes = 6 << 30
	if cur := c.currentMovieFile(context.Background(), m, v); cur.Facts != nil {
		t.Errorf("a size mismatch must fall back to the release name: %+v", cur.Facts)
	}
}
