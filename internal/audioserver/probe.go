package audioserver

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/tristenlammi/arrmada/internal/library"
)

// AudioFile is one audio file of an item, with what ffprobe found in it.
type AudioFile struct {
	Index    int
	Ino      string
	Path     string
	Name     string
	Ext      string
	Size     int64
	MTimeMs  int64
	Duration float64
	Bitrate  int
	Codec    string
	Title    string
	Chapters []Chapter
}

// Chapter is a chapter marker.
type Chapter struct {
	ID    int     `json:"id"`
	Start float64 `json:"start"`
	End   float64 `json:"end"`
	Title string  `json:"title"`
}

type prober struct {
	db      *sql.DB
	ffprobe string
	cache   filesCache
}

// files lists an item's audio files in play order, probed (cached per file).
func (p *prober) files(ctx context.Context, path string, probe bool) ([]AudioFile, error) {
	now := time.Now()
	if !probe {
		if files, ok := p.cache.get(path, now); ok {
			return files, nil
		}
	}
	gen := p.cache.generation()
	out, err := p.list(ctx, path, probe)
	if err == nil {
		p.cache.put(path, out, now, gen)
	}
	return out, err
}

func (p *prober) list(ctx context.Context, path string, probe bool) ([]AudioFile, error) {
	st, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	var paths []string
	if st.IsDir() {
		for _, f := range library.FindBookFiles(path) {
			if library.IsAudiobookFile(f.Path) {
				paths = append(paths, f.Path)
			}
		}
		sort.SliceStable(paths, func(i, j int) bool { return naturalLess(relTo(path, paths[i]), relTo(path, paths[j])) })
	} else {
		paths = []string{path}
	}
	if len(paths) == 0 {
		return nil, errors.New("no audio files")
	}
	base := path
	if !st.IsDir() {
		base = filepath.Dir(path)
	}
	out := make([]AudioFile, 0, len(paths))
	for i, fp := range paths {
		fi, err := os.Stat(fp)
		if err != nil {
			continue
		}
		af := AudioFile{Index: i + 1, Ino: inoFor(relTo(base, fp)), Path: fp, Name: filepath.Base(fp),
			Ext: strings.ToLower(filepath.Ext(fp)), Size: fi.Size(), MTimeMs: fi.ModTime().UnixMilli()}
		if probe {
			p.meta(ctx, &af)
		} else {
			p.cached(ctx, &af)
		}
		out = append(out, af)
	}
	return out, nil
}

// cached fills in probed details only if they are already known.
func (p *prober) cached(ctx context.Context, af *AudioFile) bool {
	var size, mtime int64
	var chapters string
	err := p.db.QueryRowContext(ctx,
		`SELECT size, mtime, duration, bitrate, codec, title, chapters FROM audio_file_meta WHERE path = ?`, af.Path).
		Scan(&size, &mtime, &af.Duration, &af.Bitrate, &af.Codec, &af.Title, &chapters)
	if err != nil || size != af.Size || mtime != af.MTimeMs {
		af.Duration, af.Bitrate, af.Codec, af.Title = 0, 0, "", ""
		return false
	}
	_ = json.Unmarshal([]byte(chapters), &af.Chapters)
	return true
}

// meta fills in probed details, probing and caching when not known yet.
func (p *prober) meta(ctx context.Context, af *AudioFile) {
	if p.cached(ctx, af) {
		return
	}
	pctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	out, err := exec.CommandContext(pctx, p.ffprobe, "-v", "error", "-print_format", "json",
		"-show_format", "-show_chapters", "-show_streams", "-select_streams", "a:0", af.Path).Output()
	if err != nil {
		return
	}
	if !parseProbe(out, af) {
		return
	}
	ch, _ := json.Marshal(af.Chapters)
	_, _ = p.db.ExecContext(ctx,
		`INSERT INTO audio_file_meta (path, size, mtime, duration, bitrate, codec, title, chapters) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(path) DO UPDATE SET size = excluded.size, mtime = excluded.mtime, duration = excluded.duration,
		   bitrate = excluded.bitrate, codec = excluded.codec, title = excluded.title, chapters = excluded.chapters`,
		af.Path, af.Size, af.MTimeMs, af.Duration, af.Bitrate, af.Codec, af.Title, string(ch))
}

// bookChapters builds the chapter list for a whole item: embedded chapters of a single
// file, otherwise one chapter per file.
func bookChapters(files []AudioFile) []Chapter {
	if len(files) == 1 && len(files[0].Chapters) > 0 {
		return files[0].Chapters
	}
	var out []Chapter
	offset := 0.0
	for i, f := range files {
		if len(files) == 1 {
			return []Chapter{{ID: 0, Start: 0, End: f.Duration, Title: firstNonEmpty(f.Title, strings.TrimSuffix(f.Name, f.Ext))}}
		}
		title := f.Title
		if title == "" {
			title = strings.TrimSuffix(f.Name, f.Ext)
		}
		out = append(out, Chapter{ID: i, Start: offset, End: offset + f.Duration, Title: title})
		offset += f.Duration
	}
	return out
}

func totalDuration(files []AudioFile) float64 {
	t := 0.0
	for _, f := range files {
		t += f.Duration
	}
	return t
}

func totalSize(files []AudioFile) int64 {
	var t int64
	for _, f := range files {
		t += f.Size
	}
	return t
}

func tagValue(tags map[string]string, key string) string {
	for k, v := range tags {
		if strings.EqualFold(k, key) {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func relTo(base, p string) string {
	if r, err := filepath.Rel(base, p); err == nil {
		return filepath.ToSlash(r)
	}
	return filepath.Base(p)
}

// inoFor makes a stable numeric-string id for a file from its path within the item —
// Audiobookshelf clients treat "ino" as an opaque string, and a number is the safe shape.
func inoFor(rel string) string {
	var h uint64 = 1469598103934665603
	for i := 0; i < len(rel); i++ {
		h ^= uint64(rel[i])
		h *= 1099511628211
	}
	return strconv.FormatUint(h>>11, 10) // keep it within 53 bits: safe in JSON numbers too
}

func mimeFor(ext string) string {
	switch strings.ToLower(ext) {
	case ".m4b", ".m4a", ".mp4":
		return "audio/mp4"
	case ".mp3":
		return "audio/mpeg"
	case ".flac":
		return "audio/flac"
	case ".ogg", ".oga":
		return "audio/ogg"
	case ".opus":
		return "audio/ogg"
	case ".aac":
		return "audio/aac"
	case ".wav":
		return "audio/wav"
	case ".wma":
		return "audio/x-ms-wma"
	}
	return "application/octet-stream"
}

// naturalLess orders "Chapter 2" before "Chapter 10".
func naturalLess(a, b string) bool {
	a, b = strings.ToLower(a), strings.ToLower(b)
	for a != "" && b != "" {
		ra, rb := rune(a[0]), rune(b[0])
		if unicode.IsDigit(ra) && unicode.IsDigit(rb) {
			na, restA := leadingNumber(a)
			nb, restB := leadingNumber(b)
			if na != nb {
				return na < nb
			}
			a, b = restA, restB
			continue
		}
		if ra != rb {
			return ra < rb
		}
		a, b = a[1:], b[1:]
	}
	return len(a) < len(b)
}

func leadingNumber(s string) (int64, string) {
	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	n, _ := strconv.ParseInt(s[:i], 10, 64)
	return n, s[i:]
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if strings.TrimSpace(s) != "" {
			return s
		}
	}
	return ""
}

// parseProbe reads ffprobe's JSON (format, first audio stream, chapters) into af.
func parseProbe(out []byte, af *AudioFile) bool {
	var r struct {
		Format struct {
			Duration string            `json:"duration"`
			BitRate  string            `json:"bit_rate"`
			Tags     map[string]string `json:"tags"`
		} `json:"format"`
		Streams []struct {
			CodecName string `json:"codec_name"`
		} `json:"streams"`
		Chapters []struct {
			StartTime string            `json:"start_time"`
			EndTime   string            `json:"end_time"`
			Tags      map[string]string `json:"tags"`
		} `json:"chapters"`
	}
	if json.Unmarshal(out, &r) != nil {
		return false
	}
	af.Duration, _ = strconv.ParseFloat(r.Format.Duration, 64)
	af.Bitrate, _ = strconv.Atoi(r.Format.BitRate)
	if len(r.Streams) > 0 {
		af.Codec = r.Streams[0].CodecName
	}
	af.Title = tagValue(r.Format.Tags, "title")
	af.Chapters = nil
	for i, c := range r.Chapters {
		s, _ := strconv.ParseFloat(c.StartTime, 64)
		e, _ := strconv.ParseFloat(c.EndTime, 64)
		title := tagValue(c.Tags, "title")
		if title == "" {
			title = "Chapter " + strconv.Itoa(i+1)
		}
		af.Chapters = append(af.Chapters, Chapter{ID: i, Start: s, End: e, Title: title})
	}
	return true
}
