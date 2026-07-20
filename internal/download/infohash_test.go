package download

import (
	"crypto/sha1"
	"encoding/hex"
	"testing"
)

// The info dict must be hashed exactly as it appears in the file. Decoding and
// re-encoding would reorder keys or normalize integers and produce a hash no tracker
// would recognize, so this checks the bytes actually hashed are the original ones.
func TestInfoHashFromFile(t *testing.T) {
	info := "d6:lengthi1234e4:name9:Some.File12:piece lengthi16384ee"
	torrent := "d8:announce19:http://tracker/annc4:info" + info + "e"

	want := sha1.Sum([]byte(info))
	got, err := InfoHashFromFile([]byte(torrent))
	if err != nil {
		t.Fatalf("InfoHashFromFile: %v", err)
	}
	if got != hex.EncodeToString(want[:]) {
		t.Errorf("hash = %s, want %s", got, hex.EncodeToString(want[:]))
	}
}

// The info dict is rarely the first key, and anything before it — nested lists of
// announce URLs especially — has to be skipped structurally, not by scanning for "info".
func TestInfoHashSkipsEarlierKeys(t *testing.T) {
	info := "d6:lengthi42e4:name3:abce"
	torrent := "d" +
		"13:announce-listll17:http://a/announce17:http://b/announceee" + // nested lists
		"13:creation datei1700000000e" + // integer
		"7:comment16:info is not here" + // a string containing "info"
		"4:info" + info +
		"e"

	want := sha1.Sum([]byte(info))
	got, err := InfoHashFromFile([]byte(torrent))
	if err != nil {
		t.Fatalf("InfoHashFromFile: %v", err)
	}
	if got != hex.EncodeToString(want[:]) {
		t.Errorf("hash = %s, want %s — earlier keys were not skipped correctly", got, hex.EncodeToString(want[:]))
	}
}

// Malformed input must return an error rather than panic on a slice bound — these bytes
// come off the network from an indexer we don't control.
func TestInfoHashRejectsGarbage(t *testing.T) {
	for _, bad := range []string{
		"", "not a torrent", "d", "de", "d4:infoe",
		"d4:info", "d3:abc", "d4:infod", "d99999:x", "d4:infoi123",
	} {
		if _, err := InfoHashFromFile([]byte(bad)); err == nil {
			t.Errorf("InfoHashFromFile(%q) succeeded, want an error", bad)
		}
	}
}

func TestInfoHashFromMagnet(t *testing.T) {
	const hexHash = "c12fe1c06bba254a9dc9f519b335aa7c1367a88a"

	got, err := InfoHashFromMagnet("magnet:?xt=urn:urn:btih:nope&xt=urn:btih:" + hexHash + "&dn=Some.Release")
	if err != nil {
		t.Fatalf("hex magnet: %v", err)
	}
	if got != hexHash {
		t.Errorf("hash = %s, want %s", got, hexHash)
	}

	// Base32 is the other form trackers use; it must resolve to the same hex hash.
	b32, err := InfoHashFromMagnet("magnet:?xt=urn:btih:YEX6DQDLXISUVHOJ6UM3GNNKPQJWPKEK")
	if err != nil {
		t.Fatalf("base32 magnet: %v", err)
	}
	if b32 != hexHash {
		t.Errorf("base32 hash = %s, want %s", b32, hexHash)
	}

	// Uppercase hex is equivalent — the client reports lowercase, so normalize.
	up, err := InfoHashFromMagnet("magnet:?xt=urn:btih:C12FE1C06BBA254A9DC9F519B335AA7C1367A88A")
	if err != nil || up != hexHash {
		t.Errorf("uppercase hex = %q (err %v), want %s", up, err, hexHash)
	}

	for _, bad := range []string{"", "http://example/x.torrent", "magnet:?dn=NoHash", "magnet:?xt=urn:btih:short"} {
		if _, err := InfoHashFromMagnet(bad); err == nil {
			t.Errorf("InfoHashFromMagnet(%q) succeeded, want an error", bad)
		}
	}
}
