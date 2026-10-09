package subtitles

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// Pairing is by base name, for movies and TV alike: a subtitle named for another file in
// the folder is never this file's coverage.
func TestScanSidecarsPairsByBase(t *testing.T) {
	_, video := mkFiles(t, "Movie (2004) Bluray-2160p.mkv", "Movie (2004) Bluray-2160p.en.srt",
		"Movie (2004) Bluray-2160p.es.forced.srt", "Movie (2004).fr.srt")
	sc := scanSidecars(video, []string{"en", "es", "fr"}, "movie")
	if !reflect.DeepEqual(sc.Present, []string{"en"}) {
		t.Errorf("present = %v, want [en]", sc.Present)
	}
	if len(sc.Variants) != 2 {
		t.Errorf("variants = %+v, want the two paired sidecars", sc.Variants)
	}
	if len(sc.Orphans) != 1 || sc.Orphans[0].Name != "Movie (2004).fr.srt" || sc.Orphans[0].Lang != "fr" {
		t.Errorf("orphans = %+v, want the fr subtitle named for no video", sc.Orphans)
	}
	// TV never reports orphans: a season folder is full of other episodes' sidecars.
	_, ep := mkFiles(t, "Show S01E01.mkv", "Show S01E02.en.srt", "Show.en.srt")
	if sc := scanSidecars(ep, []string{"en"}, "episode"); len(sc.Orphans) != 0 || len(sc.Present) != 0 {
		t.Errorf("episode scan = %+v, want nothing", sc)
	}
}

// "Movie.Proper.en.srt" belongs to "Movie.Proper.mkv", not to "Movie.mkv" beside it.
func TestScanSidecarsLongerNamedSibling(t *testing.T) {
	_, video := mkFiles(t, "Movie.mkv", "Movie.Proper.mkv", "Movie.Proper.en.srt")
	if sc := scanSidecars(video, []string{"en"}, "movie"); len(sc.Present) != 0 || len(sc.Orphans) != 0 {
		t.Errorf("scan = %+v, want en missing and no orphans", sc)
	}
}

func TestOrphanSidecars(t *testing.T) {
	// The acceptance case: an Anchorman sidecar under an old name.
	_, video := mkFiles(t, "Anchorman (2004) Bluray-2160p.mkv", "Anchorman.eng.srt")
	sc := scanSidecars(video, []string{"en"}, "movie")
	if len(sc.Present) != 0 || len(sc.Orphans) != 1 || sc.Orphans[0].Lang != "eng" {
		t.Fatalf("scan = %+v, want en missing and one eng orphan", sc)
	}
	if got := orphanCovered([]string{"en"}, sc.Present, sc.Orphans); !got["en"] {
		t.Errorf("orphanCovered = %v, want en", got)
	}

	// A multi-version folder: each version's sidecars belong to that version only, and
	// none of them is an orphan.
	dir := t.TempDir()
	for _, n := range []string{"Dune (2021) - Bluray-1080p.mkv", "Dune (2021) - Bluray-1080p.en.srt",
		"Dune (2021) - Bluray-2160p.mkv", "Dune (2021) - Bluray-2160p.es.srt"} {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	hd := scanSidecars(filepath.Join(dir, "Dune (2021) - Bluray-1080p.mkv"), []string{"en", "es"}, "movie")
	uhd := scanSidecars(filepath.Join(dir, "Dune (2021) - Bluray-2160p.mkv"), []string{"en", "es"}, "movie")
	if !reflect.DeepEqual(hd.Present, []string{"en"}) || !reflect.DeepEqual(uhd.Present, []string{"es"}) {
		t.Errorf("1080p present %v, 2160p present %v; each version must count only its own", hd.Present, uhd.Present)
	}
	if len(hd.Orphans)+len(uhd.Orphans) != 0 {
		t.Errorf("orphans %v / %v; another version's sidecar is not an orphan", hd.Orphans, uhd.Orphans)
	}

	// An untagged orphan covers the first kept language; a forced one covers nothing.
	_, v2 := mkFiles(t, "Heat (1995).mkv", "Heat.srt", "Heat.es.forced.srt")
	sc = scanSidecars(v2, []string{"en", "es"}, "movie")
	got := orphanCovered([]string{"en", "es"}, sc.Present, sc.Orphans)
	if !got["en"] || got["es"] {
		t.Errorf("orphanCovered = %v, want en only", got)
	}
}

// The six-hourly sweep leaves a movie alone when its only gap is a language an orphan
// covers, but still queues one with a real gap.
func TestSweepSkipsOrphanedLanguage(t *testing.T) {
	_, onlyOrphan := mkFiles(t, "Anchorman (2004) Bluray-2160p.mkv", "Anchorman.eng.srt")
	if movieNeedsSweep(onlyOrphan, []string{"en"}) {
		t.Error("sweep would queue a movie whose only gap is covered by an orphan")
	}
	if !movieNeedsSweep(onlyOrphan, []string{"en", "es"}) {
		t.Error("sweep skipped a movie still missing es")
	}
	_, none := mkFiles(t, "Heat (1995).mkv")
	if !movieNeedsSweep(none, []string{"en"}) {
		t.Error("sweep skipped a movie with no subtitles at all")
	}
}

// A sweep job leaves the orphan-covered language alone, while Ensure (manual) and the
// import hook make a real, paired subtitle.
func TestManualEnsureIgnoresOrphan(t *testing.T) {
	ai := &fakeAI{avail: true, transcribe: true}
	s, video := ladderFixture(t, "en", englishAudio(), nil, ai)
	if err := os.WriteFile(filepath.Join(filepath.Dir(video), "Old Name.en.srt"), []byte(fakeSRT), 0o644); err != nil {
		t.Fatal(err)
	}
	sweep := s.enqueue(&Job{Kind: "movie", MovieID: 1, Title: "Movie", Priority: PrioSweep})
	s.pop()
	s.process(context.Background(), sweep)
	if sweep.State != StateSkipped || len(ai.calls) != 0 {
		t.Errorf("sweep job: state=%q note=%q ai=%v; want it to leave the orphaned language", sweep.State, sweep.Note, ai.calls)
	}
	job := runJob(s) // manual
	if job.State != StateDone || len(ai.calls) != 1 {
		t.Errorf("manual job: state=%q note=%q ai=%v; want a paired subtitle made", job.State, job.Note, ai.calls)
	}
	assertSidecar(t, video, "en")
}

// The Library chip marks a language an orphan covers.
func TestFillCoverageOrphan(t *testing.T) {
	s, video := ladderFixture(t, "en,es", englishAudio(), nil, &fakeAI{})
	if err := os.WriteFile(filepath.Join(filepath.Dir(video), "Old Name.en.srt"), []byte(fakeSRT), 0o644); err != nil {
		t.Fatal(err)
	}
	fs := FileSubs{Kind: "movie", Path: video}
	s.fillCoverage(context.Background(), &fs, []string{"en", "es"}, false)
	if fs.Missing != 2 || len(fs.Orphans) != 1 {
		t.Fatalf("missing=%d orphans=%+v, want both missing and one orphan", fs.Missing, fs.Orphans)
	}
	if !fs.Languages[0].Orphan || fs.Languages[1].Orphan {
		t.Errorf("languages = %+v, want en orphaned, es not", fs.Languages)
	}
}
