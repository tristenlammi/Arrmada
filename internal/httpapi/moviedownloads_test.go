package httpapi

import (
	"testing"

	"github.com/tristenlammi/arrmada/internal/automation"
	"github.com/tristenlammi/arrmada/internal/download"
	"github.com/tristenlammi/arrmada/internal/movies"
)

// The list's download join: by info hash, so a film that has its file still shows an
// upgrade's progress (kind upgrade) and an extra version's (kind version, with its
// label); a legacy grab with no hash falls back to its release name; held and finished
// torrents show nothing; the film's own missing file wins the one indicator.
func TestMovieDownloadsJoinByHash(t *testing.T) {
	queue := []download.Item{
		{Hash: "AAAA", Name: "Pretty.Tracker.Title.2160p", State: "downloading", Progress: 0.4},
		{Hash: "bbbb", Name: "Film.2020.Directors.Cut.1080p", State: "downloading", Progress: 0.2},
		{Hash: "cccc", Name: "Film.2020.1080p.WEB-DL", State: "stalledDL", Progress: 0.7},
		{Hash: "dddd", Name: "Film.2020.Done.1080p", State: "uploading", Progress: 1},
	}
	qi := newQueueIndex(queue)
	label := func(vid int64) string {
		if vid == 7 {
			return "Director's Cut"
		}
		return ""
	}

	upgrade := []automation.Acquisition{{InfoHash: "aaaa", Title: "Film 2020 2160p UHD", Status: "grabbed", Replaces: true}}
	if d := movieDownloadOf(upgrade, qi, label); d == nil || d.Kind != movies.DownloadUpgrade || d.Progress != 0.4 {
		t.Fatalf("upgrade = %+v, want kind upgrade at 40%%", d)
	}
	version := []automation.Acquisition{{InfoHash: "bbbb", VersionID: 7, Status: "grabbed"}}
	if d := movieDownloadOf(version, qi, label); d == nil || d.Kind != movies.DownloadVersion || d.VersionLabel != "Director's Cut" || d.VersionID != 7 {
		t.Fatalf("version = %+v", d)
	}
	legacy := []automation.Acquisition{{Title: "Film.2020.1080p.WEB-DL", Status: "grabbed"}}
	if d := movieDownloadOf(legacy, qi, label); d == nil || d.Kind != movies.DownloadMissing || d.State != "stalledDL" {
		t.Fatalf("legacy by name = %+v", d)
	}
	held := []automation.Acquisition{{InfoHash: "cccc", Status: "held"}}
	if d := movieDownloadOf(held, qi, label); d != nil {
		t.Fatalf("held grab shows %+v", d)
	}
	done := []automation.Acquisition{{InfoHash: "dddd", Status: "grabbed"}}
	if d := movieDownloadOf(done, qi, label); d != nil {
		t.Fatalf("finished torrent shows %+v", d)
	}
	both := append(append([]automation.Acquisition{}, upgrade...), legacy...)
	if d := movieDownloadOf(both, qi, label); d == nil || d.Kind != movies.DownloadMissing {
		t.Fatalf("both = %+v, want the missing file's download first", d)
	}
}
