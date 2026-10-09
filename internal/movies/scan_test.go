package movies

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/tristenlammi/arrmada/internal/metadata"
	"github.com/tristenlammi/arrmada/internal/outbox"
	"github.com/tristenlammi/arrmada/internal/store"
)

// fakeMeta answers TMDB lookups from a fixed list of films.
type fakeMeta struct{ films []metadata.MovieResult }

func (f fakeMeta) Available() bool { return true }
func (f fakeMeta) SearchMovie(_ context.Context, q string) ([]metadata.MovieResult, error) {
	var out []metadata.MovieResult
	for _, m := range f.films {
		if normalizeTitle(m.Title) == normalizeTitle(q) {
			out = append(out, m)
		}
	}
	return out, nil
}
func (f fakeMeta) GetMovie(_ context.Context, id int) (*metadata.MovieDetails, error) {
	for _, m := range f.films {
		if m.TMDBID == id {
			return &metadata.MovieDetails{MovieResult: m, Status: "Released"}, nil
		}
	}
	return nil, errors.New("not found")
}
func (f fakeMeta) GetCollection(context.Context, int) (*metadata.Collection, error) {
	return nil, errors.New("no collections here")
}

// recordingOutbox keeps what was enqueued.
type recordingOutbox struct{ topics []string }

func (o *recordingOutbox) Enqueue(_ context.Context, _ store.Execer, topic string, _ any, _ string) error {
	o.topics = append(o.topics, topic)
	return nil
}

// scanFixture is a temp library with one folder per film and a service whose TMDB knows
// those films. Nothing here touches a real library.
func scanFixture(t *testing.T, folders ...string) (*Service, *recordingOutbox, string) {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	root := t.TempDir()
	meta := fakeMeta{films: []metadata.MovieResult{{TMDBID: 10, Title: "Movie", Year: 2010}, {TMDBID: 20, Title: "Other", Year: 2020}}}
	svc := NewService(st.DB(), meta, nil, root, "", nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	var fc fileCounter
	fc.install(svc)
	ob := &recordingOutbox{}
	svc.SetOutbox(ob)
	for _, f := range folders {
		videoFixture(t, filepath.Join(root, f))
	}
	return svc, ob, root
}

// videoFixture writes a (sparse) file big enough that a library scan takes it for a film
// rather than a sample.
func videoFixture(t *testing.T, p string) string {
	t.Helper()
	writeFixture(t, p)
	if err := os.Truncate(p, 51<<20); err != nil {
		t.Fatal(err)
	}
	return p
}

// A film already in the library without a file (added by a request, say) gets the file a
// scan finds: it reads as downloaded, the event says so, and the import is announced so
// requesters hear about it — instead of the sweeps downloading it again.
func TestScanAttachesToExistingMovieWithoutFile(t *testing.T) {
	svc, ob, root := scanFixture(t, "Movie (2010)/Movie.2010.1080p.mkv")
	ctx := context.Background()
	m, err := svc.repo.Create(ctx, Movie{TMDBID: 10, Title: "Movie", Year: 2010, Monitored: true, QualityProfile: "hd"})
	if err != nil {
		t.Fatal(err)
	}
	res, err := svc.ScanLibrary(ctx, "", ScanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Attached != 1 || res.Imported != 0 || len(res.Duplicates) != 0 {
		t.Fatalf("result = %+v, want 1 attached", res)
	}
	got, _ := svc.repo.Get(ctx, m.ID)
	want := filepath.Join(root, "Movie (2010)", "Movie.2010.1080p.mkv")
	if !got.HasFile || got.MovieFilePath != want || got.File == nil {
		t.Fatalf("movie after scan = %+v", got)
	}
	if !got.Monitored || got.QualityProfile != "hd" {
		t.Errorf("attaching must not change monitoring or the profile: %+v", got)
	}
	assertLastEvent(t, svc, m.ID, "detected")
	if len(ob.topics) != 1 || ob.topics[0] != outbox.TopicMovieImported {
		t.Errorf("outbox = %v, want one movie.imported", ob.topics)
	}

	// Scanning again changes nothing.
	res, _ = svc.ScanLibrary(ctx, "", ScanOptions{})
	if res.Attached != 0 || res.Skipped != 1 {
		t.Errorf("second scan = %+v, want it skipped", res)
	}
}

// A folder for a film that already has a different file is reported and left alone.
func TestScanReportsDuplicateForMovieWithOtherFile(t *testing.T) {
	svc, ob, root := scanFixture(t, "Movie (2010)/Movie.2010.1080p.mkv")
	ctx := context.Background()
	m, _ := svc.repo.Create(ctx, Movie{TMDBID: 10, Title: "Movie", Year: 2010, Monitored: true})
	other := writeFixture(t, filepath.Join(root, "Elsewhere", "Movie.2010.2160p.mkv"))
	if err := svc.repo.SetFile(ctx, m.ID, other); err != nil {
		t.Fatal(err)
	}
	res, err := svc.ScanLibrary(ctx, "", ScanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Attached != 0 || len(res.Duplicates) != 1 || res.Duplicates[0].MovieID != m.ID || res.Duplicates[0].Folder != "Movie (2010)" {
		t.Fatalf("result = %+v, want one duplicate", res)
	}
	if got, _ := svc.repo.Get(ctx, m.ID); got.MovieFilePath != other {
		t.Errorf("the film's file changed to %q", got.MovieFilePath)
	}
	if len(ob.topics) != 0 {
		t.Errorf("a duplicate announced %v", ob.topics)
	}
}

// The scan's options decide how new films are added; the default stays unmonitored 'n/a'.
func TestScanOptionsMonitorAndProfile(t *testing.T) {
	svc, _, _ := scanFixture(t, "Movie (2010)/Movie.2010.1080p.mkv", "Other (2020)/Other.2020.720p.mkv")
	ctx := context.Background()
	res, err := svc.ScanLibrary(ctx, "", ScanOptions{Monitor: true, QualityProfile: "uhd"})
	if err != nil || res.Imported != 2 {
		t.Fatalf("scan: %+v %v", res, err)
	}
	all, _ := svc.repo.List(ctx)
	for _, m := range all {
		if !m.Monitored || m.QualityProfile != "uhd" || !m.HasFile {
			t.Errorf("%s: monitored=%v profile=%q has_file=%v, want monitored on uhd", m.Title, m.Monitored, m.QualityProfile, m.HasFile)
		}
	}

	svc2, _, _ := scanFixture(t, "Movie (2010)/Movie.2010.1080p.mkv")
	if _, err := svc2.ScanLibrary(ctx, "", ScanOptions{}); err != nil {
		t.Fatal(err)
	}
	all, _ = svc2.repo.List(ctx)
	if len(all) != 1 || all[0].Monitored || all[0].QualityProfile != "n/a" {
		t.Errorf("default scan = %+v, want unmonitored n/a", all)
	}
}

// 'Import as' on a folder whose film exists without a file attaches it; with a file it
// says which, and changes nothing.
func TestImportFolderAsAttachesToExisting(t *testing.T) {
	svc, _, root := scanFixture(t, "Movie (2010)/Movie.2010.1080p.mkv", "Other (2020)/Other.2020.720p.mkv")
	ctx := context.Background()
	m, _ := svc.repo.Create(ctx, Movie{TMDBID: 10, Title: "Movie", Year: 2010, Monitored: true})
	if err := svc.ImportFolderAs(ctx, "", "Movie (2010)", 10); err != nil {
		t.Fatal(err)
	}
	if got, _ := svc.repo.Get(ctx, m.ID); !got.HasFile || got.MovieFilePath != filepath.Join(root, "Movie (2010)", "Movie.2010.1080p.mkv") {
		t.Fatalf("not attached: %+v", got)
	}
	// The same film again from another folder: it has a file now.
	if err := os.Rename(filepath.Join(root, "Other (2020)"), filepath.Join(root, "Movie copy")); err != nil {
		t.Fatal(err)
	}
	err := svc.ImportFolderAs(ctx, "", "Movie copy", 10)
	if !errors.Is(err, ErrExists) {
		t.Fatalf("err = %v, want ErrExists naming the file", err)
	}
	if got, _ := svc.repo.Get(ctx, m.ID); filepath.Base(got.MovieFilePath) != "Movie.2010.1080p.mkv" {
		t.Errorf("the film's file changed to %q", got.MovieFilePath)
	}
}
