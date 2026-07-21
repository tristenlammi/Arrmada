package mediainfo

import "testing"

// Cover art is a video stream too (disposition attached_pic) and often comes FIRST in the
// file — picking it reported movies as 600×600 MJPEG. The parser must skip it.
func TestParseSkipsAttachedPicture(t *testing.T) {
	probe := []byte(`{
		"streams": [
			{"codec_type": "video", "codec_name": "mjpeg", "width": 600, "height": 600,
			 "disposition": {"attached_pic": 1}},
			{"codec_type": "video", "codec_name": "hevc", "width": 3840, "height": 2160,
			 "color_transfer": "smpte2084",
			 "disposition": {"attached_pic": 0},
			 "side_data_list": [{"side_data_type": "DOVI configuration record"}]},
			{"codec_type": "audio", "codec_name": "truehd", "channels": 8}
		],
		"format": {"duration": "5400.5"}
	}`)
	info, err := parse(probe)
	if err != nil {
		t.Fatal(err)
	}
	if info.VideoCodec != "x265" || info.Width != 3840 {
		t.Errorf("picked the wrong video stream: %+v", info)
	}
	if info.Resolution != "2160p" {
		t.Errorf("resolution = %q, want 2160p", info.Resolution)
	}
	// The stream-level DV name is "DOVI configuration record" — it must still detect as DV.
	foundDV, foundHDR10 := false, false
	for _, h := range info.HDR {
		if h == "DV" {
			foundDV = true
		}
		if h == "HDR10" {
			foundHDR10 = true
		}
	}
	if !foundDV || !foundHDR10 {
		t.Errorf("HDR detection = %v, want both HDR10 and DV", info.HDR)
	}
}
