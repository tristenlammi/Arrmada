package convert

import (
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
)

// Black bars. A 2.39:1 film on a 4K disc is 3840×2160 with ~276 rows of black above and
// below the picture. The file carries the picture alone, and the TV letterboxes it exactly
// as before: only black is removed — the picture and its shape are untouched (the pixels
// stay square).
//
// What it buys, measured on that disc: about 2% smaller, and an honest quality check —
// with the bars in, a quarter of every compared frame was perfect black, which lifted the
// score (0.9965 with bars, 0.9947 on the picture alone, same encode settings). It does NOT
// make encoding noticeably faster: x265 skips flat black blocks almost for free.
//
// The hard part is never removing picture. A dark scene looks like a black bar to a
// detector, and some films change shape (IMAX sequences open up to the full frame). So the
// film is sampled across its whole runtime and only what is black in EVERY sampled frame
// goes: the union of the picture areas found. One full-frame IMAX shot among the samples
// and nothing is cropped from that edge.

// Crop is the area of the frame a conversion keeps, in pixels.
type Crop struct{ W, H, X, Y int }

// filter is the crop as an ffmpeg filter, or "" for none.
func (c *Crop) filter() string {
	if c == nil {
		return ""
	}
	return fmt.Sprintf("crop=%d:%d:%d:%d", c.W, c.H, c.X, c.Y)
}

const (
	cropSamples   = 40 // points across the runtime, three keyframes each
	cropMinFrames = 12 // usable frames needed before trusting the result
)

var cropLine = regexp.MustCompile(`crop=(-?\d+):(-?\d+):(-?\d+):(-?\d+)`)

// detectCrop finds the black bars a file carries, or returns nil when there are none worth
// removing (or the film can't be judged confidently). Decodes only keyframes, so even a
// 4K remux takes seconds.
func (s *Service) detectCrop(ctx context.Context, src string, mi *MediaInfo) *Crop {
	if mi.Width <= 0 || mi.Height <= 0 || mi.DurationSec < 60 {
		return nil
	}
	var frames []Crop
	for i := 0; i < cropSamples; i++ {
		at := mi.DurationSec * (float64(i) + 0.5) / cropSamples
		out, _ := exec.CommandContext(ctx, s.ffmpeg, "-hide_banner", "-nostdin", "-loglevel", "info",
			"-skip_frame", "nokey", "-ss", strconv.FormatFloat(at, 'f', 2, 64), "-i", src,
			"-map", fmt.Sprintf("0:v:%d", mi.VideoIndex), "-an", "-sn",
			"-vf", "cropdetect=round=2:reset=1:skip=0", "-frames:v", "3", "-f", "null", "-").CombinedOutput()
		if ctx.Err() != nil {
			return nil
		}
		for _, m := range cropLine.FindAllStringSubmatch(string(out), -1) {
			var c Crop
			c.W, _ = strconv.Atoi(m[1])
			c.H, _ = strconv.Atoi(m[2])
			c.X, _ = strconv.Atoi(m[3])
			c.Y, _ = strconv.Atoi(m[4])
			frames = append(frames, c)
		}
	}
	return unionCrop(frames, mi.Width, mi.Height)
}

// unionCrop combines per-frame detections into the area that holds picture in any of
// them. Frames that are black or nearly so (a fade, a night sky) report a nonsense or tiny
// area and are ignored — they say nothing about where the picture's edges are.
func unionCrop(frames []Crop, w, h int) *Crop {
	x0, y0, x1, y1 := w, h, 0, 0
	n := 0
	for _, c := range frames {
		if c.W < w/4 || c.H < h/4 || c.X < 0 || c.Y < 0 || c.X+c.W > w || c.Y+c.H > h {
			continue
		}
		n++
		x0, y0 = min(x0, c.X), min(y0, c.Y)
		x1, y1 = max(x1, c.X+c.W), max(y1, c.Y+c.H)
	}
	if n < cropMinFrames {
		return nil
	}
	// Bars under 1% of an axis are left: a few rows of black aren't worth an odd size.
	if (w-(x1-x0))*100 < w {
		x0, x1 = 0, w
	}
	if (h-(y1-y0))*100 < h {
		y0, y1 = 0, h
	}
	// 4:2:0 chroma covers 2×2 pixels: the edges must fall on even rows and columns, or the
	// colour shifts by half a pixel (and interlaced fields swap).
	x0, y0 = x0&^1, y0&^1
	x1, y1 = min((x1+1)&^1, w), min((y1+1)&^1, h)
	if x0 == 0 && y0 == 0 && x1 == w && y1 == h {
		return nil
	}
	return &Crop{W: x1 - x0, H: y1 - y0, X: x0, Y: y0}
}

// keepsImageSubs reports whether the plan leaves image subtitles in the file. Those are
// drawn on a canvas the size of the original frame, and players stretch that canvas over
// the cropped picture — so a file that keeps them keeps its full frame too.
func keepsImageSubs(mi *MediaInfo, plan Plan) bool {
	for _, sub := range keptSubs(mi, plan) {
		if !sub.Text {
			return true
		}
	}
	return false
}

// withCrop sets the plan's crop when black bars should be removed: the setting is on, the
// picture is being re-encoded anyway (a crop is never a reason to re-encode), and no image
// subtitles stay behind.
func (s *Service) withCrop(ctx context.Context, src string, mi *MediaInfo, plan Plan, p prefs) Plan {
	plan.Crop = nil
	if !p.crop || plan.VideoCodec == "" || keepsImageSubs(mi, plan) {
		return plan
	}
	plan.Crop = s.detectCrop(ctx, src, mi)
	return plan
}
