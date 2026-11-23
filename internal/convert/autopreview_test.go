package convert

import (
	"context"
	"fmt"
	"testing"
)

func indexEpisode(t *testing.T, s *Service, seriesID int64, season, episode int, mi *MediaInfo) {
	t.Helper()
	mi.SizeBytes = int64(mi.BitrateKbps) * 1000 / 8 * int64(mi.DurationSec)
	err := s.index.upsert(context.Background(), indexRow{
		Path:      fmt.Sprintf("/tv/Show %d/S%02dE%02d.mkv", seriesID, season, episode),
		MediaType: "episode", SeriesID: seriesID, Season: season, Episode: episode,
		Title:     fmt.Sprintf("Show - S%02dE%02d", season, episode),
		SizeBytes: mi.SizeBytes, Codec: mi.VideoCodec, Info: mi,
	})
	if err != nil {
		t.Fatal(err)
	}
}

func dvP8(kbps int) *MediaInfo {
	mi := film("hevc", 3840, 2160, kbps, aud("truehd", "eng", 8))
	mi.HDR, mi.DVProfile, mi.DVBase, mi.TenBit, mi.Container = "Dolby Vision", 8, "HDR10", true, "MKV"
	return mi
}

// The Switch-on summary counts what converting the library would cost, in this library's
// own numbers, and narrows to one show or season for a bulk Convert-now.
func TestAutoPreviewCounts(t *testing.T) {
	ctx := context.Background()
	s := newTestService(t)
	set(t, s, map[string]string{keyDropCommentary: "true", keyDolbyVision: DVDrop})
	comm := func() AudioStream { a := aud("ac3", "eng", 2); a.Commentary = true; return a }

	indexMovie(t, s, 1, "DV One", dvP8(60000))
	indexMovie(t, s, 2, "DV Two", dvP8(60000))
	mp4 := film("h264", 1920, 1080, 12000, aud("ac3", "eng", 6))
	mp4.Container = "MP4"
	indexMovie(t, s, 3, "Old MP4", mp4)
	indexMovie(t, s, 4, "Lean", film("h264", 1920, 1080, 2000, aud("aac", "eng", 2))) // nothing to do
	failed := film("h264", 1920, 1080, 12000, aud("ac3", "eng", 6))
	indexMovie(t, s, 5, "Fails", failed)
	for i := 0; i < maxFailures; i++ {
		s.failures.recordFailure(ctx, movieKey(5), "boom")
	}

	indexEpisode(t, s, 10, 1, 1, dvP8(60000))
	cc := film("h264", 1920, 1080, 12000, aud("ac3", "eng", 6), comm())
	cc.HasCC = true
	indexEpisode(t, s, 10, 1, 2, cc)
	indexEpisode(t, s, 10, 2, 1, film("h264", 1920, 1080, 12000, aud("ac3", "eng", 6), comm()))
	indexEpisode(t, s, 11, 1, 1, film("h264", 1920, 1080, 12000, aud("ac3", "eng", 6)))

	all := s.AutoPreview(ctx, PreviewFilter{Season: -1})
	want := AutoPreview{Files: 7, Reencode: 7, DVFiles: 3, CCFiles: 1, AudioDrops: 2, Containers: 1}
	if all.Files != want.Files || all.Reencode != want.Reencode || all.DVFiles != want.DVFiles || all.CCFiles != want.CCFiles ||
		all.AudioDrops != want.AudioDrops || all.Containers != want.Containers || all.ImageSubs != 0 || all.ForcedLost != 0 {
		t.Errorf("library: %+v, want %+v", all, want)
	}
	if all.Bytes <= 0 || all.EstBytes <= 0 || all.EstBytes >= all.Bytes {
		t.Errorf("sizes %d → %d: want a smaller total after", all.Bytes, all.EstBytes)
	}

	show := s.AutoPreview(ctx, PreviewFilter{SeriesID: 10, Season: -1})
	if show.Files != 3 || show.DVFiles != 1 || show.CCFiles != 1 || show.AudioDrops != 2 || show.Containers != 0 {
		t.Errorf("show 10: %+v", show)
	}
	season := s.AutoPreview(ctx, PreviewFilter{SeriesID: 10, Season: 2})
	if season.Files != 1 || season.DVFiles != 0 || season.CCFiles != 0 || season.AudioDrops != 1 {
		t.Errorf("show 10 season 2: %+v", season)
	}
	if none := s.AutoPreview(ctx, PreviewFilter{SeriesID: 99, Season: -1}); none.Files != 0 {
		t.Errorf("unknown show: %+v", none)
	}

	// Left alone (a new install's default), the Dolby Vision files drop out of the summary
	// and it says how many there are and why.
	set(t, s, map[string]string{keyDolbyVision: DVSkip})
	s.invalidateLibraryCache()
	if p := s.AutoPreview(ctx, PreviewFilter{Season: -1}); p.Files != 4 || p.DVFiles != 0 || p.DVLeftAlone != 3 {
		t.Errorf("left alone: %+v, want 4 files, 3 Dolby Vision files left alone", p)
	}
	if p := s.AutoPreview(ctx, PreviewFilter{SeriesID: 10, Season: 2}); p.DVLeftAlone != 0 {
		t.Errorf("season 2 has no Dolby Vision: %+v", p)
	}
	if p := s.AutoPreview(ctx, PreviewFilter{SeriesID: 10, Season: -1}); p.DVLeftAlone != 1 {
		t.Errorf("show 10: %+v, want 1 left alone", p)
	}
}

// Image subtitles and forced tracks the plan removes are counted too.
func TestAutoPreviewCountsSubtitles(t *testing.T) {
	ctx := context.Background()
	s := newTestService(t)
	set(t, s, map[string]string{keyImageSubs: ImageSubsRemove})
	mi := withSubs(film("h264", 1920, 1080, 12000, aud("ac3", "eng", 6)), pgs("eng"), forcedPGS("eng"), srt("eng"))
	indexMovie(t, s, 1, "Subs", mi)
	p := s.AutoPreview(ctx, PreviewFilter{Season: -1})
	if p.Files != 1 || p.ImageSubs != 2 || p.ForcedLost != 1 {
		t.Errorf("preview = %+v, want 2 image tracks removed, 1 of them forced", p)
	}
}
