// Package torrentmeta previews an uploaded .torrent file — bencode-decoding it to read
// its name, total size, and file list — so a user can confirm what they're about to
// grab before it's handed to the download client. Uploading the file (rather than a
// URL) means it works for private trackers, where the download link needs a login.
package torrentmeta

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// Info is what a previewed .torrent resolves to.
type Info struct {
	Name      string `json:"name"`
	SizeBytes int64  `json:"size_bytes"`
	Files     []File `json:"files,omitempty"`
}

// File is one entry inside a multi-file torrent.
type File struct {
	Path      string `json:"path"`
	SizeBytes int64  `json:"size_bytes"`
}

// ParseTorrent bencode-decodes a .torrent file into its name, total size, and files.
func ParseTorrent(data []byte) (*Info, error) {
	if t := bytes.TrimSpace(data); len(t) > 0 && t[0] == '<' {
		return nil, errors.New("that's a web page, not a .torrent — on a private tracker, download the .torrent from your browser while logged in, then upload that file")
	}
	v, _, err := decode(data, 0)
	if err != nil {
		return nil, fmt.Errorf("not a valid .torrent: %w", err)
	}
	top, ok := v.(map[string]any)
	if !ok {
		return nil, errors.New("not a valid .torrent")
	}
	info, ok := top["info"].(map[string]any)
	if !ok {
		return nil, errors.New(".torrent has no info dictionary")
	}
	out := &Info{}
	if name, ok := info["name"].(string); ok {
		out.Name = name
	}
	if length, ok := info["length"].(int64); ok {
		out.SizeBytes = length
	} else if files, ok := info["files"].([]any); ok {
		for _, f := range files {
			fm, ok := f.(map[string]any)
			if !ok {
				continue
			}
			l, _ := fm["length"].(int64)
			out.SizeBytes += l
			var parts []string
			if pl, ok := fm["path"].([]any); ok {
				for _, p := range pl {
					if ps, ok := p.(string); ok {
						parts = append(parts, ps)
					}
				}
			}
			out.Files = append(out.Files, File{Path: strings.Join(parts, "/"), SizeBytes: l})
		}
	}
	if out.Name == "" {
		return nil, errors.New(".torrent has no name")
	}
	return out, nil
}

// decode is a minimal bencode decoder returning string / int64 / []any / map[string]any.
func decode(b []byte, i int) (any, int, error) {
	if i >= len(b) {
		return nil, i, io.ErrUnexpectedEOF
	}
	switch {
	case b[i] == 'i': // integer: i<n>e
		j := i + 1
		for j < len(b) && b[j] != 'e' {
			j++
		}
		if j >= len(b) {
			return nil, j, io.ErrUnexpectedEOF
		}
		n, err := strconv.ParseInt(string(b[i+1:j]), 10, 64)
		return n, j + 1, err
	case b[i] == 'l': // list: l...e
		i++
		var list []any
		for i < len(b) && b[i] != 'e' {
			v, ni, err := decode(b, i)
			if err != nil {
				return nil, ni, err
			}
			list = append(list, v)
			i = ni
		}
		return list, i + 1, nil
	case b[i] == 'd': // dict: d<key><val>...e
		i++
		m := map[string]any{}
		for i < len(b) && b[i] != 'e' {
			k, ni, err := decode(b, i)
			if err != nil {
				return nil, ni, err
			}
			ks, ok := k.(string)
			if !ok {
				return nil, ni, errors.New("bencode: non-string dict key")
			}
			v, ni2, err := decode(b, ni)
			if err != nil {
				return nil, ni2, err
			}
			m[ks] = v
			i = ni2
		}
		return m, i + 1, nil
	case b[i] >= '0' && b[i] <= '9': // string: <len>:<bytes>
		j := i
		for j < len(b) && b[j] != ':' {
			j++
		}
		n, err := strconv.Atoi(string(b[i:j]))
		if err != nil {
			return nil, j, err
		}
		start := j + 1
		if start+n > len(b) {
			return nil, start, io.ErrUnexpectedEOF
		}
		return string(b[start : start+n]), start + n, nil
	}
	return nil, i, errors.New("bencode: unexpected byte")
}
