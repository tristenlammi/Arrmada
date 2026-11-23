// Package videojoin joins the parts of one episode that a release split across files
// ("… Part 1", "… Part 2") back into a single file, without re-encoding.
//
// A join is a stream copy: every packet of every part, in order, into one container. That
// only gives a file that plays right through when the parts were encoded the same way, so
// Compatible checks them first, down to the codec's own setup data, and Join checks the
// result is as long as the parts together before it is handed back. The parts are only
// ever read.
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

// Stream is what must be the same in every part for one track.
type Stream struct {
	Type       string // video | audio | subtitle | attachment | data
	Codec      string
	Profile    string
	Width      int
	Height     int
	PixFmt     string
	SampleRate string
	Channels   int
	Layout     string
	Language   string
	// Extradata is a hash of the codec's setup data (an H.264 stream's parameter sets). A
	// container such as Matroska keeps it once per track, so a second part encoded with
	// other settings would be decoded with the first part's and come out as garbage.
	Extradata string
}

// Layout is one file's tracks and length.
type Layout struct {
	Streams  []Stream
	Duration float64 // seconds
}

type probeOut struct {
	Streams []struct {
		CodecType     string            `json:"codec_type"`
		CodecName     string            `json:"codec_name"`
		Profile       string            `json:"profile"`
		Width         int               `json:"width"`
		Height        int               `json:"height"`
		PixFmt        string            `json:"pix_fmt"`
		SampleRate    string            `json:"sample_rate"`
		Channels      int               `json:"channels"`
		ChannelLayout string            `json:"channel_layout"`
		ExtradataHash string            `json:"extradata_hash"`
		Tags          map[string]string `json:"tags"`
		Disposition   map[string]int    `json:"disposition"`
	} `json:"streams"`
	Format struct {
		Duration string `json:"duration"`
	} `json:"format"`
}

// Probe reads a file's tracks and length with ffprobe.
func Probe(ctx context.Context, path string) (Layout, error) {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	out, err := toolrun.Command(ctx, toolrun.Normal, "ffprobe", "-v", "error", "-print_format", "json",
		"-show_format", "-show_streams", "-show_data_hash", "MD5", path).Output()
	if err != nil {
		return Layout{}, fmt.Errorf("ffprobe: %w", err)
	}
	return parseProbe(out)
}

func parseProbe(out []byte) (Layout, error) {
	var p probeOut
	if err := json.Unmarshal(out, &p); err != nil {
		return Layout{}, fmt.Errorf("ffprobe: %w", err)
	}
	var l Layout
	for _, s := range p.Streams {
		if s.CodecType == "video" && s.Disposition["attached_pic"] == 1 {
			continue // cover art: not a track that plays
		}
		lang := ""
		for k, v := range s.Tags {
			if strings.EqualFold(k, "language") {
				lang = strings.ToLower(v)
			}
		}
		l.Streams = append(l.Streams, Stream{
			Type: s.CodecType, Codec: s.CodecName, Profile: s.Profile, Width: s.Width, Height: s.Height,
			PixFmt: s.PixFmt, SampleRate: s.SampleRate, Channels: s.Channels, Layout: s.ChannelLayout,
			Language: lang, Extradata: s.ExtradataHash,
		})
	}
	l.Duration, _ = strconv.ParseFloat(p.Format.Duration, 64)
	if len(l.Streams) == 0 || l.Duration <= 0 {
		return Layout{}, errors.New("ffprobe: no tracks or no length")
	}
	return l, nil
}

// Compatible says why two parts can't be joined by copying ("" when they can): they must
// have the same tracks in the same order, encoded the same way.
func Compatible(a, b Layout) string {
	if len(a.Streams) != len(b.Streams) {
		return fmt.Sprintf("one part has %d tracks and another %d", len(a.Streams), len(b.Streams))
	}
	video := 0
	for i, x := range a.Streams {
		y := b.Streams[i]
		what := fmt.Sprintf("track %d (%s)", i+1, x.Type)
		switch {
		case x.Type != y.Type:
			return fmt.Sprintf("track %d is %s in one part and %s in another", i+1, x.Type, y.Type)
		case x.Codec != y.Codec:
			return fmt.Sprintf("%s is %s in one part and %s in another", what, x.Codec, y.Codec)
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
			if x.SampleRate != y.SampleRate || x.Channels != y.Channels || x.Layout != y.Layout || x.Profile != y.Profile {
				return what + " has a different sound format in each part"
			}
		}
		if (x.Type == "video" || x.Type == "audio") && x.Extradata != y.Extradata {
			return what + " was encoded with different settings in each part"
		}
	}
	if video != 1 {
		return fmt.Sprintf("the parts have %d video tracks", video)
	}
	return ""
}

// lengthSlack is how far the joined file's length may be from the parts' together: a
// container rounds each part's end to a frame or an audio packet.
func lengthSlack(total float64) float64 { return 1.5 + total*0.001 }

// Join copies parts, in order, into one file at out (which must not exist), and checks it:
// the same tracks as the parts and as long as them together. On any failure out is removed
// and nothing is left behind. want is the parts' layouts from Probe, in the same order.
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
		total += want[i].Duration
	}
	if err := list.Close(); err != nil {
		return err
	}
	ok := false
	defer func() {
		if !ok {
			_ = os.Remove(out)
		}
	}()
	cmd := toolrun.Command(ctx, toolrun.Normal, "ffmpeg", "-nostdin", "-v", "error", "-n",
		"-f", "concat", "-safe", "0", "-i", list.Name(),
		"-map", "0", "-c", "copy", "-map_metadata", "0", "-max_interleave_delta", "0", out)
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
	if len(got.Streams) != len(want[0].Streams) {
		return fmt.Errorf("the joined file has %d tracks, the parts %d", len(got.Streams), len(want[0].Streams))
	}
	if d := got.Duration - total; d > lengthSlack(total) || d < -lengthSlack(total) {
		return fmt.Errorf("the joined file is %.1f s long, the parts together %.1f s", got.Duration, total)
	}
	ok = true
	return nil
}
