package subtitles

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"testing"
)

func TestPickFullTrack(t *testing.T) {
	forced := SubTrack{Index: 0, Codec: "subrip", Lang: "eng", Text: true, Forced: true, Title: "English (Forced)"}
	full := SubTrack{Index: 1, Codec: "subrip", Lang: "eng", Text: true, Title: "English"}
	sdh := SubTrack{Index: 2, Codec: "subrip", Lang: "eng", Text: true, SDH: true, Title: "English SDH"}
	signs := SubTrack{Index: 3, Codec: "ass", Lang: "eng", Text: true, Forced: true, Title: "Signs & Songs"}
	pgs := SubTrack{Index: 4, Codec: "hdmv_pgs_subtitle", Lang: "eng"}
	untagged := SubTrack{Index: 5, Codec: "subrip", Text: true}
	full2 := SubTrack{Index: 6, Codec: "subrip", Lang: "eng", Text: true, Default: true}
	sdhDefault := SubTrack{Index: 7, Codec: "subrip", Lang: "eng", Text: true, SDH: true, Default: true}

	cases := []struct {
		name string
		subs []SubTrack
		want int // -1 = none
	}{
		{"forced listed first is skipped", []SubTrack{forced, full, sdh}, 1},
		{"SDH only", []SubTrack{forced, sdh}, 2},
		{"title-only forced (Signs & Songs) is never picked", []SubTrack{signs}, -1},
		{"forced only", []SubTrack{forced, signs, pgs}, -1},
		{"image track is not extractable", []SubTrack{pgs}, -1},
		{"untagged track never matches", []SubTrack{untagged}, -1},
		{"default breaks a tie", []SubTrack{full, full2}, 6},
		{"plain beats a default SDH", []SubTrack{sdhDefault, full}, 1},
		{"default SDH beats plain SDH", []SubTrack{sdh, sdhDefault}, 7},
	}
	for _, c := range cases {
		got, ok := pickFullTrack(c.subs, "en")
		switch {
		case c.want < 0 && ok:
			t.Errorf("%s: picked %+v, want none", c.name, got)
		case c.want >= 0 && (!ok || got.Index != c.want):
			t.Errorf("%s: picked %+v (ok=%v), want index %d", c.name, got, ok, c.want)
		}
	}
	// A language whose only text track is forced isn't an extract job.
	if got := bestSource([]SubTrack{forced}, "en", true); got != "download" {
		t.Errorf("forced-only: source = %q, want download", got)
	}
}

// Search asks the API to leave out forced and AI-translated uploads, and drops any that
// come back anyway.
func TestSearchExcludesForeignPartsAndAI(t *testing.T) {
	var got url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.Query()
		_, _ = io.WriteString(w, `{"data":[
		 {"attributes":{"language":"en","release":"forced","foreign_parts_only":true,"files":[{"file_id":1}]}},
		 {"attributes":{"language":"en","release":"ai","ai_translated":true,"files":[{"file_id":2}]}},
		 {"attributes":{"language":"en","release":"mt","machine_translated":true,"files":[{"file_id":3}]}},
		 {"attributes":{"language":"en","release":"good","files":[{"file_id":4}]}}
		]}`)
	}))
	defer srv.Close()
	o := NewOpenSubtitles("key", "", "")
	o.baseURL = srv.URL
	res, err := o.Search(context.Background(), SearchRequest{IMDBID: "tt1", Language: "en"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Get("foreign_parts_only") != "exclude" || got.Get("ai_translated") != "exclude" {
		t.Errorf("query = %v, want foreign_parts_only=exclude and ai_translated=exclude", got)
	}
	if len(res) != 1 || res[0].FileID != "4" {
		t.Errorf("results = %+v, want only the clean upload", res)
	}
	// Asking for forced subtitles flips the filter.
	res, _ = o.Search(context.Background(), SearchRequest{IMDBID: "tt1", Language: "en", ForeignParts: "only"})
	if got.Get("foreign_parts_only") != "only" || len(res) != 1 || res[0].FileID != "1" {
		t.Errorf("only-forced search: query=%v results=%+v", got, res)
	}
}

// The tracks [eng forced, eng full, eng SDH] give <base>.en.srt from the full track, and
// the forced track rides along in the same ffmpeg pass as <base>.en.forced.srt.
func TestExtractAlsoWritesForced(t *testing.T) {
	mi := englishAudio()
	mi.Subs = []SubTrack{
		{Index: 0, Codec: "subrip", Lang: "eng", Text: true, Forced: true, Title: "English (Forced)"},
		{Index: 1, Codec: "subrip", Lang: "eng", Text: true, Title: "English"},
		{Index: 2, Codec: "subrip", Lang: "eng", Text: true, SDH: true, Title: "English SDH"},
	}
	s, video := ladderFixture(t, "en", mi, nil, &fakeAI{})
	var picked []extractPick
	s.extract = func(_ context.Context, _ string, picks []extractPick) error {
		picked = picks
		for _, p := range picks {
			if err := os.WriteFile(p.Path, []byte(fakeSRT), 0o644); err != nil {
				return err
			}
		}
		return nil
	}
	job := runJob(s)
	if len(picked) != 2 {
		t.Fatalf("picks = %+v, want the full track and the forced one", picked)
	}
	if picked[0].Index != 1 || picked[0].Path != sidecarPath(video, "en") {
		t.Errorf("full pick = %+v, want stream 1 → <base>.en.srt", picked[0])
	}
	if picked[1].Index != 0 || picked[1].Path != sidecarPathV(video, "en", VariantForced) {
		t.Errorf("forced pick = %+v, want stream 0 → <base>.en.forced.srt", picked[1])
	}
	if job.State != StateDone || job.Note != "en: extracted" {
		t.Errorf("state=%q note=%q (the forced extra isn't the language's outcome)", job.State, job.Note)
	}
}

// When the only full English text track is SDH it is written as <base>.en.sdh.srt, which
// counts as English coverage.
func TestExtractSDHOnlyWritesSDHSidecar(t *testing.T) {
	mi := englishAudio()
	mi.Subs = []SubTrack{{Index: 0, Codec: "subrip", Lang: "eng", Text: true, SDH: true}}
	s, video := ladderFixture(t, "en", mi, nil, &fakeAI{})
	s.extract = func(_ context.Context, _ string, picks []extractPick) error {
		for _, p := range picks {
			if err := os.WriteFile(p.Path, []byte(fakeSRT), 0o644); err != nil {
				return err
			}
		}
		return nil
	}
	runJob(s)
	if _, err := os.Stat(sidecarPathV(video, "en", VariantSDH)); err != nil {
		t.Fatalf("no .en.sdh.srt: %v", err)
	}
	if got := presentLanguages(video, []string{"en"}, true); len(got) != 1 {
		t.Errorf("present = %v, want en covered by the SDH sidecar", got)
	}
}

// With only a forced English track and nothing to download, English isn't extracted at all:
// it falls to the AI rung, and no extraction runs just for the forced extra.
func TestForcedOnlyFallsThrough(t *testing.T) {
	mi := englishAudio()
	mi.Subs = []SubTrack{{Index: 0, Codec: "subrip", Lang: "eng", Text: true, Forced: true}}
	ai := &fakeAI{avail: true, transcribe: true}
	s, video := ladderFixture(t, "en", mi, nil, ai) // the fixture's extract fails the test if called
	job := runJob(s)
	assertSidecar(t, video, "en")
	if job.Note != "en: AI-generated" {
		t.Errorf("note = %q", job.Note)
	}
}

// A downloaded hearing-impaired subtitle is written with the .sdh qualifier.
func TestDownloadedHearingImpairedIsSDH(t *testing.T) {
	prov := &fakeProvider{canDownload: true, results: map[string][]SubtitleResult{"en": {{FileID: "1", HearingImpaired: true}}}}
	s, video := ladderFixture(t, "en", englishAudio(), prov, &fakeAI{})
	job := runJob(s)
	if _, err := os.Stat(sidecarPathV(video, "en", VariantSDH)); err != nil || job.Note != "en: downloaded" {
		t.Errorf("sdh sidecar err=%v note=%q", err, job.Note)
	}
}
