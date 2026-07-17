// Package torrentmeta previews a pasted torrent link — a magnet or a .torrent URL —
// so a user can confirm what they're about to grab before handing it to the download
// client. It parses the magnet's display name, or fetches and bencode-decodes a
// .torrent to read its name, total size, and file list.
package torrentmeta

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// Info is what a previewed link resolves to.
type Info struct {
	Name      string `json:"name"`
	SizeBytes int64  `json:"size_bytes"`
	Hash      string `json:"hash,omitempty"`
	Magnet    bool   `json:"magnet"`
	Files     []File `json:"files,omitempty"`
}

// File is one entry inside a multi-file torrent.
type File struct {
	Path      string `json:"path"`
	SizeBytes int64  `json:"size_bytes"`
}

const browserUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36"

// Preview resolves a magnet or .torrent URL to its Info.
func Preview(ctx context.Context, hc *http.Client, link string) (*Info, error) {
	link = strings.TrimSpace(link)
	if link == "" {
		return nil, errors.New("paste a magnet link or a .torrent URL")
	}
	if info, ok := ParseMagnet(link); ok {
		if info.Name == "" {
			info.Name = "Magnet (" + shortHash(info.Hash) + ")"
		}
		return info, nil
	}
	if strings.HasPrefix(link, "http://") || strings.HasPrefix(link, "https://") {
		body, err := fetchTorrent(ctx, hc, link)
		if err != nil {
			return nil, err
		}
		info, err := ParseTorrent(body)
		if err != nil {
			return nil, err
		}
		return info, nil
	}
	return nil, errors.New("that doesn't look like a magnet link or a .torrent URL")
}

// ParseMagnet extracts the name (dn) and info hash from a magnet link.
func ParseMagnet(link string) (*Info, bool) {
	if !strings.HasPrefix(link, "magnet:") {
		return nil, false
	}
	u, err := url.Parse(link)
	if err != nil {
		return nil, false
	}
	q := u.Query()
	info := &Info{Name: q.Get("dn"), Magnet: true}
	for _, xt := range q["xt"] {
		if strings.HasPrefix(xt, "urn:btih:") {
			info.Hash = strings.ToLower(strings.TrimPrefix(xt, "urn:btih:"))
		}
	}
	return info, true
}

// ParseTorrent bencode-decodes a .torrent file into its name, total size, and files.
func ParseTorrent(data []byte) (*Info, error) {
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

func fetchTorrent(ctx context.Context, hc *http.Client, link string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, link, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", browserUA)
	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetching the link returned HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 16<<20))
}

func shortHash(h string) string {
	if len(h) > 8 {
		return h[:8]
	}
	return h
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
