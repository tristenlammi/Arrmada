// Package videojoin joins the parts of one episode that a release split across files
// ("… Part 1", "… Part 2") back into a single file, without re-encoding.
//
// A join is a stream copy: every packet of every played track of every part, in order,
// into one container. That only gives a file that plays right through when the parts were
// encoded the same way, so Compatible checks them first, down to the codec's own setup
// data, and Join checks the result has the parts' tracks and is as long as the parts
// together before it is handed back. The parts are only ever read.
package videojoin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/tristenlammi/arrmada/internal/toolrun"
)

// Stream is one track of a file, with what must be the same in every part.
type Stream struct {
	Index      int    // its position in the file
	Type       string // video | audio | subtitle | attachment | data
	Codec      string
	Profile    string
	Width      int
	Height     int
	PixFmt     string
	SampleRate string
	SampleFmt  string
	Bits       int // bits per sample, where the codec says (FLAC, PCM)
	Channels   int
	Layout     string
	Language   string
	// Cover: a picture shown for the file (cover art), not a track that plays.
	Cover bool
	// Extradata is a hash of the codec's setup data (an H.264 stream's parameter sets, an
	// ASS subtitle's styles). A container such as Matroska keeps it once per track, so a
	// second part encoded with other settings would be decoded with the first part's.
	Extradata string
	// Disposition is the track's flags that are set: "default", "forced", ….
	Disposition []string
}

// Played says whether the track is joined: a picture that moves, sound or subtitles.
// Cover art and data tracks (a timecode, chapters as text) are left out of the joined
// file; fonts for subtitles are carried over from the first part.
func (s Stream) Played() bool {
	switch s.Type {
	case "video":
		return !s.Cover
	case "audio", "subtitle":
		return true
	}
	return false
}

// Chapter is a named point in a file, in seconds on that file's own clock.
type Chapter struct {
	Start, End float64
	Title      string
}

// Layout is one file's tracks, length and the rest a join carries over.
type Layout struct {
	Streams []Stream // every track, in the file's order
	// Duration and Start are the container's own figures, in seconds. Matroska counts its
	// duration from zero, so a file whose first frame is at 3 s plays for Duration-Start;
	// MP4 and the rest count it from the first frame. Length says how long it plays.
	Duration float64
	Start    float64
	Format   string // the container, as ffprobe names it ("matroska,webm", "mov,mp4,…")
	Title    string
	Chapters []Chapter
}

// Length is how long the file plays, in seconds.
func (l Layout) Length() float64 {
	if strings.Contains(l.Format, "matroska") && l.Start > 0 {
		return l.Duration - l.Start
	}
	return l.Duration
}

// played is the file's joined tracks, in order.
func (l Layout) played() []Stream {
	var out []Stream
	for _, s := range l.Streams {
		if s.Played() {
			out = append(out, s)
		}
	}
	return out
}

type probeOut struct {
	Streams []struct {
		Index         int               `json:"index"`
		CodecType     string            `json:"codec_type"`
		CodecName     string            `json:"codec_name"`
		Profile       string            `json:"profile"`
		Width         int               `json:"width"`
		Height        int               `json:"height"`
		PixFmt        string            `json:"pix_fmt"`
		SampleRate    string            `json:"sample_rate"`
		SampleFmt     string            `json:"sample_fmt"`
		BitsPerRaw    string            `json:"bits_per_raw_sample"`
		Channels      int               `json:"channels"`
		ChannelLayout string            `json:"channel_layout"`
		ExtradataHash string            `json:"extradata_hash"`
		Tags          map[string]string `json:"tags"`
		Disposition   map[string]int    `json:"disposition"`
	} `json:"streams"`
	Chapters []struct {
		StartTime string            `json:"start_time"`
		EndTime   string            `json:"end_time"`
		Tags      map[string]string `json:"tags"`
	} `json:"chapters"`
	Format struct {
		FormatName string            `json:"format_name"`
		Duration   string            `json:"duration"`
		StartTime  string            `json:"start_time"`
		Tags       map[string]string `json:"tags"`
	} `json:"format"`
}

// Probe reads a file's tracks and length with ffprobe.
func Probe(ctx context.Context, path string) (Layout, error) {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	out, err := toolrun.Command(ctx, toolrun.Normal, "ffprobe", "-v", "error", "-print_format", "json",
		"-show_format", "-show_streams", "-show_chapters", "-show_data_hash", "MD5", path).Output()
	if err != nil {
		return Layout{}, fmt.Errorf("ffprobe: %w", err)
	}
	return parseProbe(out)
}

// tag reads a tag whatever its case (Matroska's are upper case, MP4's lower).
func tag(tags map[string]string, name string) string {
	for k, v := range tags {
		if strings.EqualFold(k, name) {
			return v
		}
	}
	return ""
}

// dispositions are the track flags a join carries over: every one ffmpeg 6 and later
// (and jellyfin-ffmpeg) can set, but cover art's own.
var dispositions = []string{"default", "dub", "original", "comment", "lyrics", "karaoke", "forced",
	"hearing_impaired", "visual_impaired", "clean_effects", "captions", "descriptions", "dependent", "metadata"}

func parseProbe(out []byte) (Layout, error) {
	var p probeOut
	if err := json.Unmarshal(out, &p); err != nil {
		return Layout{}, fmt.Errorf("ffprobe: %w", err)
	}
	var l Layout
	for _, s := range p.Streams {
		st := Stream{
			Index: s.Index, Type: s.CodecType, Codec: s.CodecName, Profile: s.Profile, Width: s.Width, Height: s.Height,
			PixFmt: s.PixFmt, SampleRate: s.SampleRate, SampleFmt: s.SampleFmt, Channels: s.Channels, Layout: s.ChannelLayout,
			Language: strings.ToLower(tag(s.Tags, "language")), Extradata: s.ExtradataHash,
			Cover: s.Disposition["attached_pic"] == 1,
		}
		st.Bits, _ = strconv.Atoi(s.BitsPerRaw)
		for _, d := range dispositions {
			if s.Disposition[d] == 1 {
				st.Disposition = append(st.Disposition, d)
			}
		}
		l.Streams = append(l.Streams, st)
	}
	for _, c := range p.Chapters {
		start, err1 := strconv.ParseFloat(c.StartTime, 64)
		end, err2 := strconv.ParseFloat(c.EndTime, 64)
		if err1 == nil && err2 == nil {
			l.Chapters = append(l.Chapters, Chapter{Start: start, End: end, Title: tag(c.Tags, "title")})
		}
	}
	l.Duration, _ = strconv.ParseFloat(p.Format.Duration, 64)
	l.Start, _ = strconv.ParseFloat(p.Format.StartTime, 64)
	l.Format = p.Format.FormatName
	l.Title = tag(p.Format.Tags, "title")
	if len(l.played()) == 0 || l.Length() <= 0 {
		return Layout{}, errors.New("ffprobe: no tracks or no length")
	}
	return l, nil
}

// maxStartOffset is how late a part's first frame may be. A join puts each part straight
// after the one before by the container's length, and Matroska counts that from zero: a
// part starting late would leave a gap of that much at the seam.
const maxStartOffset = 0.5

// extradataPerFile are codecs whose setup data differs from file to file however alike
// they were encoded: FLAC's holds the file's own length and checksum.
var extradataPerFile = map[string]bool{"flac": true}

// Compatible says why two parts can't be joined by copying ("" when they can): they must
// have the same tracks in the same order, encoded the same way, and start on time.
func Compatible(a, b Layout) string {
	for _, l := range []Layout{a, b} {
		if l.Start > maxStartOffset {
			return fmt.Sprintf("a part starts %.1f s in", l.Start)
		}
	}
	// A join matches tracks by their position, so every track counts but fonts after the
	// last of the rest (they're carried over from the first part).
	as, bs := trimFonts(a.Streams), trimFonts(b.Streams)
	if len(as) != len(bs) {
		return fmt.Sprintf("one part has %d tracks and another %d", len(as), len(bs))
	}
	video := 0
	for i, x := range as {
		y := bs[i]
		what := fmt.Sprintf("track %d (%s)", i+1, x.Type)
		switch {
		case x.Type != y.Type:
			return fmt.Sprintf("track %d is %s in one part and %s in another", i+1, x.Type, y.Type)
		case x.Cover != y.Cover:
			return fmt.Sprintf("track %d is cover art in one part and not in another", i+1)
		case x.Type == "attachment":
			continue // a font among the tracks: carried over, never played
		case x.Codec != y.Codec:
			return fmt.Sprintf("%s is %s in one part and %s in another", what, x.Codec, y.Codec)
		case !x.Played():
			continue // cover art and data: left out of the joined file
		case x.Language != y.Language:
			return fmt.Sprintf("%s is in a different language in each part", what)
		}
		switch x.Type {
		case "video":
			video++
			if x.Width != y.Width || x.Height != y.Height || x.PixFmt != y.PixFmt || x.Profile != y.Profile {
				return what + " has a different picture size or format in each part"
			}
		case "audio":
			if x.SampleRate != y.SampleRate || x.Channels != y.Channels || x.Layout != y.Layout || x.Profile != y.Profile ||
				x.SampleFmt != y.SampleFmt || x.Bits != y.Bits {
				return what + " has a different sound format in each part"
			}
		}
		if x.Extradata != y.Extradata && !extradataPerFile[x.Codec] {
			if x.Type == "subtitle" {
				return what + " is styled differently in each part"
			}
			return what + " was encoded with different settings in each part"
		}
	}
	if video != 1 {
		return fmt.Sprintf("the parts have %d video tracks", video)
	}
	return ""
}

// trimFonts drops the attachments that end a file's tracks.
func trimFonts(s []Stream) []Stream {
	n := len(s)
	for n > 0 && s[n-1].Type == "attachment" {
		n--
	}
	return s[:n]
}

// lengthSlack is how far the joined file's length may be from the parts' together: a
// container rounds each part's end to a frame or an audio packet.
func lengthSlack(total float64) float64 { return 1.5 + total*0.001 }

// Join copies parts, in order, into one file at out (which must not exist), and checks it:
// the same tracks as the parts and as long as them together. On any failure out is removed
// and nothing is left behind. want is the parts' layouts from Probe, in the same order,
// already found Compatible.
//
// The joined file has the first part's played tracks, in its order, with their flags
// ("default", "forced") and fonts; every part's chapters, each at its place in the whole;
// and the first part's title. Cover art and data tracks are left out.
func Join(ctx context.Context, parts []string, want []Layout, out string) error {
	if len(parts) < 2 || len(parts) != len(want) {
		return errors.New("join: need two or more probed parts")
	}
	if _, err := os.Lstat(out); err == nil {
		return fmt.Errorf("join: %s is already there", filepath.Base(out))
	}
	list, err := os.CreateTemp(filepath.Dir(out), "join-*.txt")
	if err != nil {
		return err
	}
	defer os.Remove(list.Name())
	var total float64
	for i, p := range parts {
		abs, err := filepath.Abs(p)
		if err != nil {
			_ = list.Close()
			return err
		}
		// The concat list's own quoting: a single quote ends the string, so it is written
		// as '\'' ; nothing else is special inside single quotes.
		fmt.Fprintf(list, "file '%s'\n", strings.ReplaceAll(filepath.ToSlash(abs), "'", `'\''`))
		total += want[i].Length()
	}
	if err := list.Close(); err != nil {
		return err
	}
	meta, err := os.CreateTemp(filepath.Dir(out), "join-*.meta")
	if err != nil {
		return err
	}
	defer os.Remove(meta.Name())
	if _, err := meta.WriteString(ffmetadata(want)); err != nil {
		_ = meta.Close()
		return err
	}
	if err := meta.Close(); err != nil {
		return err
	}
	ok := false
	defer func() {
		if !ok {
			_ = os.Remove(out)
		}
	}()
	args := []string{"-nostdin", "-v", "error", "-n",
		"-f", "concat", "-safe", "0", "-i", list.Name(),
		"-f", "ffmetadata", "-i", meta.Name()}
	// Each track by its position: the concat input has the first part's tracks.
	var flags []string
	n := 0
	for _, s := range want[0].Streams {
		if !s.Played() && s.Type != "attachment" {
			continue
		}
		args = append(args, "-map", "0:"+strconv.Itoa(s.Index))
		d := "0" // none: without this ffmpeg makes the first track of each kind the default
		if len(s.Disposition) > 0 {
			d = strings.Join(s.Disposition, "+")
		}
		flags = append(flags, "-disposition:"+strconv.Itoa(n), d)
		n++
	}
	args = append(args, "-map_metadata", "1", "-map_chapters", "1", "-c", "copy")
	args = append(args, flags...)
	args = append(args, out)
	// Bulk work: a lower CPU and disk priority, so playback comes first.
	cmd := toolrun.Command(ctx, toolrun.Background, "ffmpeg", args...)
	if b, err := cmd.CombinedOutput(); err != nil {
		msg := strings.TrimSpace(string(b))
		if i := strings.IndexByte(msg, '\n'); i >= 0 {
			msg = msg[:i]
		}
		if len(msg) > 200 {
			msg = msg[:200]
		}
		return fmt.Errorf("ffmpeg: %w: %s", err, msg)
	}
	got, err := Probe(ctx, out)
	if err != nil {
		return fmt.Errorf("the joined file can't be read: %w", err)
	}
	gp, wp := got.played(), want[0].played()
	if len(gp) != len(wp) {
		return fmt.Errorf("the joined file has %d tracks, the parts %d", len(gp), len(wp))
	}
	for i := range gp {
		if gp[i].Type != wp[i].Type || gp[i].Codec != wp[i].Codec {
			return fmt.Errorf("the joined file's track %d is %s %s, the parts' %s %s", i+1, gp[i].Type, gp[i].Codec, wp[i].Type, wp[i].Codec)
		}
	}
	if d := got.Length() - total; d > lengthSlack(total) || d < -lengthSlack(total) {
		return fmt.Errorf("the joined file is %.1f s long, the parts together %.1f s", got.Length(), total)
	}
	ok = true
	return nil
}

// ffmetadata is the joined file's title and chapters, in ffmpeg's metadata file format.
// A part's chapters move to where the part lands: the join puts each part straight after
// the one before by the container's length, its first frame at that point.
func ffmetadata(parts []Layout) string {
	var b strings.Builder
	b.WriteString(";FFMETADATA1\n")
	if t := parts[0].Title; t != "" {
		b.WriteString("title=" + metaEscape(t) + "\n")
	}
	offset := 0.0
	for _, p := range parts {
		for _, c := range p.Chapters {
			start := max(0, c.Start-p.Start+offset)
			end := max(start, c.End-p.Start+offset)
			fmt.Fprintf(&b, "\n[CHAPTER]\nTIMEBASE=1/1000\nSTART=%d\nEND=%d\n", int64(start*1000+0.5), int64(end*1000+0.5))
			if c.Title != "" {
				b.WriteString("title=" + metaEscape(c.Title) + "\n")
			}
		}
		offset += p.Duration // as the join counts it, whatever the container means by it
	}
	return b.String()
}

// metaEscape escapes what ffmpeg's metadata file format treats as special.
func metaEscape(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch r {
		case '=', ';', '#', '\\', '\n':
			b.WriteByte('\\')
		case '\r':
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}
