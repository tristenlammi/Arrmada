package torrentmeta

import "testing"

func TestMagnet(t *testing.T) {
	info, ok := ParseMagnet("magnet:?xt=urn:btih:ABCDEF0123&dn=Elementary+S01-07+Complete+1080p&tr=udp://x")
	if !ok || info.Name != "Elementary S01-07 Complete 1080p" || info.Hash != "abcdef0123" {
		t.Fatalf("magnet = %+v ok=%v", info, ok)
	}
}

func TestTorrentBencode(t *testing.T) {
	// Multi-file: info{files:[{100,a.mkv},{250,b.mkv}], name:Boxset, pieces:""}
	tor := "d4:infod5:filesld6:lengthi100e4:pathl5:a.mkveed6:lengthi250e4:pathl5:b.mkveee4:name6:Boxset6:pieces0:ee"
	info, err := ParseTorrent([]byte(tor))
	if err != nil {
		t.Fatal(err)
	}
	if info.Name != "Boxset" || info.SizeBytes != 350 || len(info.Files) != 2 {
		t.Fatalf("torrent = %+v", info)
	}
	// Single-file: info{length:999, name:move}
	single := "d4:infod6:lengthi999e4:name4:move6:pieces0:ee"
	si, err := ParseTorrent([]byte(single))
	if err != nil || si.Name != "move" || si.SizeBytes != 999 {
		t.Fatalf("single = %+v err=%v", si, err)
	}
}
