package subtitles

import (
	"context"
	"io"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/tristenlammi/arrmada/internal/store"
)

// A synthetic ffprobe answer: cover art, the real video, one audio track and five
// subtitle tracks flagged every way the release groups do it.
const probeFixture = `{
 "streams": [
  {"codec_type":"video","codec_name":"mjpeg","r_frame_rate":"90000/1","disposition":{"attached_pic":1}},
  {"codec_type":"video","codec_name":"hevc","r_frame_rate":"0/0","avg_frame_rate":"24000/1001"},
  {"codec_type":"audio","codec_name":"eac3","disposition":{"default":1},"tags":{"language":"eng"}},
  {"codec_type":"subtitle","codec_name":"subrip","disposition":{"forced":0},"tags":{"language":"eng","title":"English (Forced)"}},
  {"codec_type":"subtitle","codec_name":"subrip","disposition":{"default":1},"tags":{"language":"eng","title":"English"}},
  {"codec_type":"subtitle","codec_name":"subrip","tags":{"language":"eng","title":"English SDH"}},
  {"codec_type":"subtitle","codec_name":"ass","tags":{"language":"eng","title":"Signs & Songs"}},
  {"codec_type":"subtitle","codec_name":"hdmv_pgs_subtitle","disposition":{"forced":1,"hearing_impaired":1},"tags":{"language":"spa"}}
 ],
 "format": {"duration":"5400.5"}
}`

func TestProbeParsesSubtitleTitlesDispositionsAndFPS(t *testing.T) {
	mi, err := parseProbe([]byte(probeFixture))
	if err != nil {
		t.Fatal(err)
	}
	if mi.ProbeVersion != probeVersion {
		t.Errorf("probe version = %d, want %d", mi.ProbeVersion, probeVersion)
	}
	if math.Abs(mi.FPS-23.976) > 0.001 {
		t.Errorf("fps = %v, want 23.976 (avg_frame_rate of the real video, not the cover art)", mi.FPS)
	}
	if len(mi.Subs) != 5 {
		t.Fatalf("subs = %d, want 5", len(mi.Subs))
	}
	want := []struct {
		title              string
		forced, sdh, deflt bool
	}{
		{"English (Forced)", true, false, false},
		{"English", false, false, true},
		{"English SDH", false, true, false},
		{"Signs & Songs", true, false, false},
		{"", true, true, false},
	}
	for i, w := range want {
		got := mi.Subs[i]
		if got.Index != i || got.Title != w.title || got.Forced != w.forced || got.SDH != w.sdh || got.Default != w.deflt {
			t.Errorf("sub %d = %+v, want title=%q forced=%v sdh=%v default=%v", i, got, w.title, w.forced, w.sdh, w.deflt)
		}
	}
}

func TestTitleFlagsAreWordBounded(t *testing.T) {
	for title, forced := range map[string]bool{
		"Forced": true, "English [Forced]": true, "Signs and Songs": true, "Foreign Parts": true,
		"Signs": true, "Forcedly": false, "Unforced": false, "English": false,
	} {
		if got := forcedTitleRe.MatchString(title); got != forced {
			t.Errorf("forced(%q) = %v, want %v", title, got, forced)
		}
	}
	for title, sdh := range map[string]bool{
		"SDH": true, "English (CC)": true, "Hearing Impaired": true, "hearing-impaired": true,
		"Accent": false, "English": false,
	} {
		if got := sdhTitleRe.MatchString(title); got != sdh {
			t.Errorf("sdh(%q) = %v, want %v", title, got, sdh)
		}
	}
}

// A cache row written by an older probe (no version) is probed again once; the fresh row
// is then served from the cache.
func TestProbeCachedReprobesOldVersion(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	video := filepath.Join(t.TempDir(), "Movie.mkv")
	if err := os.WriteFile(video, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	fi, _ := os.Stat(video)
	s := &Service{cache: &probeCache{db: st.DB()}, log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	ctx := context.Background()
	// What an old build left behind: audio known, no version, no subtitle flags.
	s.cache.put(ctx, video, fi.Size(), fi.ModTime().Unix(), &mediaInfo{
		AudioLangs: []string{"eng"}, Audio: []AudioTrack{{Index: 0, Lang: "eng"}},
		Subs: []SubTrack{{Index: 0, Codec: "subrip", Lang: "eng", Text: true}},
	})
	runs := 0
	s.ffprobeRun = func(context.Context, string, string) (*mediaInfo, error) {
		runs++
		return parseProbe([]byte(probeFixture))
	}
	mi, err := s.probeCached(ctx, video)
	if err != nil {
		t.Fatal(err)
	}
	if runs != 1 || mi.ProbeVersion != probeVersion || !mi.Subs[0].Forced {
		t.Fatalf("runs=%d version=%d first sub=%+v; want one fresh probe", runs, mi.ProbeVersion, mi.Subs[0])
	}
	if _, err := s.probeCached(ctx, video); err != nil || runs != 1 {
		t.Errorf("second call re-probed (runs=%d, err=%v); want the cached row", runs, err)
	}
}
