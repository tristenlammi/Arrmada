package download

import (
	"crypto/sha1"
	"encoding/base32"
	"encoding/hex"
	"errors"
	"net/url"
	"strconv"
	"strings"
)

// The info hash is a torrent's real identity. Names are not: an indexer's listing title
// is frequently a prettified version of the actual torrent — "EAC3" written as "DD+",
// "10bit" dropped, episode titles omitted — so a grab recorded under the listing title
// can never be matched back to what the download client is actually holding. The hash is
// derived from the torrent itself and both sides agree on it by definition.

// InfoHashFromFile computes the BitTorrent v1 info hash of a .torrent file: the SHA-1 of
// its bencoded "info" dictionary, byte for byte as it appears in the file.
//
// The dictionary must be hashed exactly as encoded, so this locates the value's byte
// range rather than decoding and re-encoding it — a round trip would reorder keys or
// normalize integers and produce a hash no tracker would recognize.
func InfoHashFromFile(b []byte) (string, error) {
	if len(b) == 0 || b[0] != 'd' {
		return "", errors.New("not a bencoded dictionary")
	}
	p := 1 // inside the top-level dict
	for p < len(b) && b[p] != 'e' {
		key, next, err := bencodeString(b, p)
		if err != nil {
			return "", err
		}
		start := next
		end, err := bencodeSkip(b, start)
		if err != nil {
			return "", err
		}
		if key == "info" {
			sum := sha1.Sum(b[start:end])
			return hex.EncodeToString(sum[:]), nil
		}
		p = end
	}
	return "", errors.New("no info dictionary")
}

// InfoHashFromMagnet extracts the info hash from a magnet URI's xt=urn:btih parameter,
// accepting both the 40-char hex and 32-char base32 forms trackers use.
func InfoHashFromMagnet(raw string) (string, error) {
	if !strings.HasPrefix(strings.ToLower(raw), "magnet:") {
		return "", errors.New("not a magnet URI")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	for _, xt := range u.Query()["xt"] {
		const prefix = "urn:btih:"
		if !strings.HasPrefix(strings.ToLower(xt), prefix) {
			continue
		}
		v := xt[len(prefix):]
		switch len(v) {
		case 40:
			if _, err := hex.DecodeString(v); err != nil {
				return "", err
			}
			return strings.ToLower(v), nil
		case 32:
			b, err := base32.StdEncoding.DecodeString(strings.ToUpper(v))
			if err != nil {
				return "", err
			}
			return hex.EncodeToString(b), nil
		}
	}
	return "", errors.New("no btih in magnet")
}

// bencodeString reads a bencoded string at p, returning it and the offset just past it.
func bencodeString(b []byte, p int) (string, int, error) {
	colon := -1
	for i := p; i < len(b) && i < p+20; i++ {
		if b[i] == ':' {
			colon = i
			break
		}
	}
	if colon < 0 {
		return "", 0, errors.New("malformed bencoded string")
	}
	n, err := strconv.Atoi(string(b[p:colon]))
	if err != nil || n < 0 || colon+1+n > len(b) {
		return "", 0, errors.New("bad string length")
	}
	return string(b[colon+1 : colon+1+n]), colon + 1 + n, nil
}

// bencodeSkip returns the offset just past the value starting at p.
func bencodeSkip(b []byte, p int) (int, error) {
	if p >= len(b) {
		return 0, errors.New("truncated")
	}
	switch c := b[p]; {
	case c == 'd', c == 'l':
		p++
		for p < len(b) && b[p] != 'e' {
			next, err := bencodeSkip(b, p)
			if err != nil {
				return 0, err
			}
			p = next
		}
		if p >= len(b) {
			return 0, errors.New("unterminated container")
		}
		return p + 1, nil // past the 'e'
	case c == 'i':
		for i := p + 1; i < len(b); i++ {
			if b[i] == 'e' {
				return i + 1, nil
			}
		}
		return 0, errors.New("unterminated integer")
	case c >= '0' && c <= '9':
		_, next, err := bencodeString(b, p)
		return next, err
	}
	return 0, errors.New("unknown bencode type")
}
