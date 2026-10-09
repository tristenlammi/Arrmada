package convert

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
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
// film is sampled densely across its whole runtime (about every 25 seconds) and only what is
// black in EVERY sampled frame goes: the union of the picture areas found. And a film whose
// picture changes shape is not cropped at all: if any sample shows picture beyond the
// film's usual frame, other wider shots may sit between the samples, so the union can't be
// trusted to cover them.

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
	cropEvery      = 25.0 // seconds of film per sample point
	cropMinSamples = 40   // points across even a short film, three keyframes each
	cropMaxSamples = 400  // a long film's cap: each point is a seek and three keyframe decodes
	cropMinFrames  = 12   // usable frames needed before trusting the result
	// cropShapeTolPct is how far (percent of the frame) a sample's picture edge may sit
	// outside the film's usual frame before the film counts as changing shape. Covers the
	// detector's jitter of a few pixels, nothing more.
	cropShapeTolPct = 2
)

// cropSampleCount is how many points across a runtime are sampled: one every cropEvery
// seconds, within cropMinSamples..cropMaxSamples (288 for a two-hour film).
func cropSampleCount(durationSec float64) int {
	return min(max(int(durationSec/cropEvery), cropMinSamples), cropMaxSamples)
}

var cropLine = regexp.MustCompile(`crop=(-?\d+):(-?\d+):(-?\d+):(-?\d+)`)

// detectCrop finds the black bars a file carries, or returns nil when there are none worth
// removing (or the film can't be judged confidently, or its picture changes shape). Decodes
// only keyframes, three per sample point.
func (s *Service) detectCrop(ctx context.Context, src string, mi *MediaInfo) *Crop {
	if mi.Width <= 0 || mi.Height <= 0 || mi.DurationSec < 60 {
		return nil
	}
	var frames []Crop
	samples := cropSampleCount(mi.DurationSec)
	for i := 0; i < samples; i++ {
		at := mi.DurationSec * (float64(i) + 0.5) / float64(samples)
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
	if u := usableFrames(frames, mi.Width, mi.Height); len(u) >= cropMinFrames && shapeChanges(u, mi.Width, mi.Height) {
		s.event("info", fmt.Sprintf("%s: the picture changes shape (IMAX or open-matte scenes, or something in the bars) — keeping the full frame",
			filepath.Base(src)))
		return nil
	}
	return unionCrop(frames, mi.Width, mi.Height)
}

// unionCrop combines per-frame detections into the area that holds picture in any of
// them. Frames that are black or nearly so (a fade, a night sky) report a nonsense or tiny
// area and are ignored — they say nothing about where the picture's edges are. A film whose
// picture changes shape (see shapeChanges) gets no crop at all.
func unionCrop(frames []Crop, w, h int) *Crop {
	usable := usableFrames(frames, w, h)
	if len(usable) < cropMinFrames || shapeChanges(usable, w, h) {
		return nil
	}
	x0, y0, x1, y1 := w, h, 0, 0
	for _, c := range usable {
		x0, y0 = min(x0, c.X), min(y0, c.Y)
		x1, y1 = max(x1, c.X+c.W), max(y1, c.Y+c.H)
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

// usableFrames drops the detections that say nothing about the picture's edges: black or
// near-black frames, which report a nonsense or tiny area.
func usableFrames(frames []Crop, w, h int) []Crop {
	var out []Crop
	for _, c := range frames {
		if c.W < w/4 || c.H < h/4 || c.X < 0 || c.Y < 0 || c.X+c.W > w || c.Y+c.H > h {
			continue
		}
		out = append(out, c)
	}
	return out
}

// shapeChanges reports whether the sampled picture changes shape across the film, which
// means it mustn't be cropped. Each edge's usual position is the one most samples agree on
// (within cropShapeTolPct). The film changes shape when:
//
//   - any sample shows picture beyond the usual frame on some edge — an IMAX or open-matte
//     shot, or something drawn in the bars. Where one was sampled, others may sit between
//     the samples, so even the union of every sample can't be trusted; or
//   - fewer than half the samples show the usual frame at all, so there isn't one.
//
// A sample whose picture sits INSIDE the usual frame is a dark scene (black at its own
// edges reads as bar to the detector), not a different shape — those are left to the union.
func shapeChanges(frames []Crop, w, h int) bool {
	if len(frames) == 0 {
		return false
	}
	tolX, tolY := max(w*cropShapeTolPct/100, 2), max(h*cropShapeTolPct/100, 2)
	edges := func(c Crop) [4]int { return [4]int{c.X, c.Y, c.X + c.W, c.Y + c.H} }
	tol := [4]int{tolX, tolY, tolX, tolY}
	var usual [4]int
	for e := 0; e < 4; e++ {
		best := -1
		for _, a := range frames {
			n := 0
			for _, b := range frames {
				if abs(edges(a)[e]-edges(b)[e]) <= tol[e] {
					n++
				}
			}
			// On a tie, the outer position: darkness only ever pulls an edge inwards.
			outer := (e < 2 && edges(a)[e] < usual[e]) || (e >= 2 && edges(a)[e] > usual[e])
			if n > best || (n == best && outer) {
				best, usual[e] = n, edges(a)[e]
			}
		}
	}
	agree := 0
	for _, c := range frames {
		ed := edges(c)
		// Beyond the usual frame: further left/up on the near edges, right/down on the far.
		if ed[0] < usual[0]-tolX || ed[1] < usual[1]-tolY || ed[2] > usual[2]+tolX || ed[3] > usual[3]+tolY {
			return true
		}
		if abs(ed[0]-usual[0]) <= tolX && abs(ed[1]-usual[1]) <= tolY && abs(ed[2]-usual[2]) <= tolX && abs(ed[3]-usual[3]) <= tolY {
			agree++
		}
	}
	return agree*2 < len(frames)
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
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
