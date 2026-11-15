package automation

import (
	"os"
	"path/filepath"
	"testing"
)

// #116: Match… on a library folder the scan held weighs every edition, not only the target.
// The folder holds the 9-track deluxe; the album's target is the 6-track standard. Matching
// it moves the album to the deluxe and records all nine files where they are.
func TestScanFolderMatchWeighsEveryEdition(t *testing.T) {
	all := append(append([]string(nil), okcTitles...), okcBonus...)
	files := trackFileNames(all)
	r := newMatchRig(t, false, nil)
	r.tagAll(files, all)
	dir := filepath.Join(r.h.c.imp.MusicDir(), "Radiohead", "OK Computer (Deluxe)")
	for _, f := range files {
		blobTrack(t, dir, f, 1)
	}
	std := r.editionID(t, "rel-std")
	dlx := r.editionID(t, "rel-dlx")
	if al, _ := r.svc.AlbumRecord(r.h.ctx, r.album.ID); al.TargetEditionID != std {
		t.Fatalf("target %d, want the standard %d to start with", al.TargetEditionID, std)
	}
	res, err := r.h.c.db.Exec(`INSERT INTO import_reviews (hash, name, content_path, media_type, expected_id, reason, reason_code)
		VALUES (?, ?, ?, 'music', 0, 'held', 'unmatched')`, scanReviewPrefix+"okc", "Radiohead/OK Computer (Deluxe)", dir)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	if err := r.h.c.ImportReview(r.h.ctx, id, r.album.ID, "music"); err != nil {
		t.Fatalf("match: %v", err)
	}
	al, _ := r.svc.AlbumRecord(r.h.ctx, r.album.ID)
	if al.TargetEditionID != dlx || al.HaveTracks != 9 {
		t.Errorf("target %d (want the deluxe %d), have %d (want 9)", al.TargetEditionID, dlx, al.HaveTracks)
	}
	for _, f := range files {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Errorf("%s moved: %v", f, err)
		}
	}
}

// Nothing recorded, nothing switched: files that fit no edition leave the album as it was.
func TestScanFolderMatchOfNothingKeepsTheEdition(t *testing.T) {
	r := newMatchRig(t, false, nil)
	dir := filepath.Join(r.h.c.imp.MusicDir(), "Radiohead", "Unrelated")
	blobTrack(t, dir, "zz - Totally Unrelated.flac", 1)
	blobTrack(t, dir, "zz - Also Unrelated.flac", 1)
	before, _ := r.svc.AlbumRecord(r.h.ctx, r.album.ID)
	res, err := r.h.c.db.Exec(`INSERT INTO import_reviews (hash, name, content_path, media_type, expected_id, reason, reason_code)
		VALUES (?, ?, ?, 'music', 0, 'held', 'unmatched')`, scanReviewPrefix+"x", "Radiohead/Unrelated", dir)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	if err := r.h.c.ImportReview(r.h.ctx, id, r.album.ID, "music"); err == nil {
		t.Fatal("matching files that fit nothing succeeded")
	}
	if after, _ := r.svc.AlbumRecord(r.h.ctx, r.album.ID); after.TargetEditionID != before.TargetEditionID {
		t.Errorf("the edition switched (%d → %d) though nothing was recorded", before.TargetEditionID, after.TargetEditionID)
	}
}

// An album that already holds files stays on its edition: moving it would leave those files
// on tracks the new edition doesn't list. Only a person's own choice of edition moves it.
func TestScanFolderMatchKeepsTheEditionOfAnAlbumWithFiles(t *testing.T) {
	r := newMatchRig(t, false, nil)
	std := r.editionID(t, "rel-std")
	stdFiles := trackFileNames(okcTitles)
	r.tagAll(stdFiles, okcTitles)
	held := filepath.Join(r.h.c.imp.MusicDir(), "Radiohead", "OK Computer")
	for _, f := range stdFiles {
		blobTrack(t, held, f, 1)
	}
	match := func(name, dir string) error {
		res, err := r.h.c.db.Exec(`INSERT INTO import_reviews (hash, name, content_path, media_type, expected_id, reason, reason_code)
			VALUES (?, ?, ?, 'music', 0, 'held', 'unmatched')`, scanReviewPrefix+name, name, dir)
		if err != nil {
			t.Fatal(err)
		}
		id, _ := res.LastInsertId()
		return r.h.c.ImportReview(r.h.ctx, id, r.album.ID, "music")
	}
	if err := match("std", held); err != nil {
		t.Fatalf("first match: %v", err)
	}
	if al, _ := r.svc.AlbumRecord(r.h.ctx, r.album.ID); al.TargetEditionID != std || al.HaveTracks != 6 {
		t.Fatalf("after the first folder: target %d (want %d), have %d (want 6)", al.TargetEditionID, std, al.HaveTracks)
	}

	all := append(append([]string(nil), okcTitles...), okcBonus...)
	dlxFiles := trackFileNames(all)
	r.tagAll(dlxFiles, all)
	second := filepath.Join(r.h.c.imp.MusicDir(), "Radiohead", "OK Computer (Deluxe)")
	for _, f := range dlxFiles {
		blobTrack(t, second, f, 2)
	}
	_ = match("dlx", second) // nothing new to record on the standard: refused, and that's fine
	if al, _ := r.svc.AlbumRecord(r.h.ctx, r.album.ID); al.TargetEditionID != std || al.HaveTracks != 6 {
		t.Errorf("target %d (want the standard %d still), have %d (want 6)", al.TargetEditionID, std, al.HaveTracks)
	}
}
